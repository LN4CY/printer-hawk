// Command printer-hawk runs as a native Prusa Connect Local extension (`kind: sink`).
// It embeds module.yaml and openapi.json, subscribes to camera/device events,
// tracks Z layers, runs pluggable dual-stage VLM failure checks, and serves UI/API routes.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/busapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/fault"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/storageapi"
	"github.com/prusa3d/prusa-connect/sdk/wire"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"

	"github.com/ln4cy/printer-hawk/internal/config"
	"github.com/ln4cy/printer-hawk/internal/hawk"
	"github.com/ln4cy/printer-hawk/internal/vision"
)

//go:embed module.yaml
var manifestYAML []byte

//go:embed openapi.json
var openAPI []byte

//nolint:gochecknoglobals // read once from embedded manifestYAML.
var (
	name    = manifestScalar(manifestYAML, "name")
	release = extension.Version(manifestScalar(manifestYAML, "version"))
)

func manifestScalar(document []byte, key string) string {
	for _, line := range strings.Split(string(document), "\n") {
		value, found := strings.CutPrefix(line, key+":")
		if !found {
			continue
		}
		if comment := strings.Index(value, " #"); comment >= 0 {
			value = value[:comment]
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

// AccessChecker checks whether a caller principal holds an action on a printer object.
type AccessChecker interface {
	Check(ctx context.Context, params accessapi.CheckParams) (accessapi.Decision, error)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	line, err := wire.Handshake{
		Protocol: wire.ProtocolMajor,
		MinMinor: wire.MinMinor,
		MaxMinor: wire.MaxMinor,
		Address:  wire.StdioAddress,
	}.Line()
	if err != nil {
		return fmt.Errorf("render handshake: %w", err)
	}
	if _, err = os.Stdout.WriteString(line); err != nil {
		return fmt.Errorf("write handshake: %w", err)
	}

	serves := wirerpc.Methods{}
	conn, err := wirerpc.NewConn(stdio{}, wirerpc.Options{
		Role:    wirerpc.RoleChild,
		Methods: serves,
	})
	if err != nil {
		return fmt.Errorf("build wirerpc session: %w", err)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m := &module{
		conn:   conn,
		access: accessapi.NewClient(conn),
		log:    log,
	}
	defer m.stop()

	maps.Copy(serves, m.methods(ctx))
	fmt.Fprintf(os.Stderr, "%s %s serving on stdio as pid %d\n", name, release, os.Getpid())

	if err = conn.Serve(ctx); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

type module struct {
	conn   *wirerpc.Conn
	access AccessChecker
	log    *slog.Logger

	mu      sync.Mutex
	svc     *hawk.Service
	started bool
	cancel  context.CancelFunc
}

func (m *module) methods(ctx context.Context) wirerpc.Methods {
	return wirerpc.Methods{
		wirerpc.MethodInfo: func(context.Context, wirerpc.Call) (any, error) {
			return routesapi.Describe(wirerpc.Info{
				Name:     name,
				Version:  release,
				Contract: wirerpc.Contract,
				Serves:   []wirerpc.Method{wirerpc.MethodConfigure},
			}, openAPI)
		},
		wirerpc.MethodConfigure: wirerpc.ConfigureGranted(func(_ context.Context, sent config.ManifestSettings, grants wirerpc.Grants) error {
			return m.configure(ctx, sent, grants)
		}),
		wirerpc.MethodShutdown: func(context.Context, wirerpc.Call) (any, error) {
			fmt.Fprintf(os.Stderr, "%s stopping\n", name)
			m.stop()
			return nil, nil
		},
		routesapi.MethodServe: routesapi.Serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.routes().ServeHTTP(w, r)
		})),
	}
}

func (m *module) configure(ctx context.Context, sent config.ManifestSettings, grants wirerpc.Grants) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.svc == nil {
		devices := deviceapi.NewClient(m.conn)
		storage := storageapi.NewClient(m.conn)
		emitter := busapi.NewClient(m.conn)
		sub := func(c context.Context, p busapi.SubscribeParams, onWin func(busapi.Window) error) error {
			return busapi.Subscribe(c, m.conn, p, onWin)
		}
		m.svc = hawk.New(devices, storage, emitter, sub, grants.Data, m.log)
	}

	loaded, err := config.LoadFromDisk(grants.Data, sent)
	if err != nil {
		m.log.Warn("failed loading saved settings; falling back to manifest defaults", "error", err)
		loaded = config.FromManifest(sent)
	}
	m.svc.Configure(loaded)

	if !m.started {
		m.started = true
		runCtx, cancel := context.WithCancel(ctx)
		m.cancel = cancel
		go m.svc.Run(runCtx)
	}
	return nil
}

func (m *module) stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *module) routes() http.Handler {
	m.mu.Lock()
	svc := m.svc
	access := m.access
	m.mu.Unlock()
	return buildRoutes(svc, access)
}

func buildRoutes(svc *hawk.Service, access AccessChecker) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			answerJSON(w, http.StatusOK, config.DefaultManifestSettings())
			return
		}
		printerFilter := strings.TrimSpace(r.URL.Query().Get("printer"))
		answerJSON(w, http.StatusOK, svc.Status(printerFilter))
	})

	mux.HandleFunc("GET /settings", func(w http.ResponseWriter, _ *http.Request) {
		if svc == nil {
			answerJSON(w, http.StatusOK, config.FromManifest(config.DefaultManifestSettings()))
			return
		}
		answerJSON(w, http.StatusOK, svc.Config())
	})

	mux.HandleFunc("PUT /settings", func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			refuse(w, r, InvalidPayload.New("service not initialized").With(fault.StringParam("reason", "not configured")))
			return
		}
		if !svc.RawConfig().PageSetup {
			refuse(w, r, PermissionDenied.New("web UI configuration is disabled by page_setup=false").
				With(fault.StringParam("resource", "settings")))
			return
		}
		var incoming config.RuntimeConfig
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&incoming); err != nil {
			refuse(w, r, InvalidPayload.New("invalid settings JSON").With(fault.StringParam("reason", err.Error())))
			return
		}
		updated, err := svc.UpdateConfig(incoming)
		if err != nil {
			refuse(w, r, InvalidPayload.New("could not save settings").With(fault.StringParam("reason", err.Error())))
			return
		}
		answerJSON(w, http.StatusOK, updated)
	})

	mux.HandleFunc("POST /providers/test", func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			refuse(w, r, CommandRejected.New("service not initialized").With(fault.StringParam("reason", "not configured")))
			return
		}
		var req struct {
			Slot   string              `json:"slot"`
			Config config.ProviderSlot `json:"config"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
			refuse(w, r, InvalidPayload.New("invalid provider test JSON").With(fault.StringParam("reason", err.Error())))
			return
		}
		slot := config.ResolveTestSlot(req.Slot, req.Config, svc.RawConfig())
		res, err := vision.TestProvider(r.Context(), slot)
		if err != nil {
			answerJSON(w, http.StatusOK, map[string]any{
				"ok":         false,
				"latency_ms": 0,
				"detail":     err.Error(),
			})
			return
		}
		answerJSON(w, http.StatusOK, res)
	})

	mux.HandleFunc("GET /printers/{printer}/live.jpg", func(w http.ResponseWriter, r *http.Request) {
		printerID := r.PathValue("printer")
		jpg, source, err := svc.LiveJPEG(r.Context(), printerID)
		if err != nil {
			refuse(w, r, FrameNotFound.New(err.Error()).With(fault.StringParam("printer", printerID)))
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-store")
		if source != "" {
			w.Header().Set("X-Live-Source", source)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(jpg)
	})

	mux.HandleFunc("GET /printers/{printer}/frames/{frameId}", func(w http.ResponseWriter, r *http.Request) {
		printerID := r.PathValue("printer")
		rawID := strings.TrimSuffix(r.PathValue("frameId"), ".jpg")
		jpg, err := svc.StoredFrame(printerID, rawID)
		if err != nil {
			refuse(w, r, FrameNotFound.New("stored frame not found").With(fault.StringParam("printer", printerID)))
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(jpg)
	})

	mux.HandleFunc("POST /printers/{printer}/inspect", func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			refuse(w, r, CommandRejected.New("service not initialized").With(fault.StringParam("reason", "not configured")))
			return
		}
		printerID := r.PathValue("printer")
		v := strings.ToLower(r.URL.Query().Get("verify"))
		verify := v == "1" || v == "true" || v == "yes"
		started, err := svc.StartInspect(printerID, verify)
		if err != nil {
			refuse(w, r, PrinterNotFound.New(err.Error()).With(fault.StringParam("printer", printerID)))
			return
		}
		detail := "Inspection already in progress"
		if started {
			detail = "Inspection started"
		}
		answerJSON(w, http.StatusOK, map[string]any{"ok": true, "printer": printerID, "detail": detail})
	})

	mux.HandleFunc("POST /printers/{printer}/false-alarm", func(w http.ResponseWriter, r *http.Request) {
		printerID := r.PathValue("printer")
		if _, err := svc.FalseAlarm(printerID); err != nil {
			refuse(w, r, PrinterNotFound.New(err.Error()).With(fault.StringParam("printer", printerID)))
			return
		}
		answerJSON(w, http.StatusOK, map[string]any{"ok": true, "printer": printerID, "detail": "Baseline updated from latest frame"})
	})

	mux.HandleFunc("POST /printers/{printer}/clear", func(w http.ResponseWriter, r *http.Request) {
		printerID := r.PathValue("printer")
		if err := svc.ClearAlert(printerID); err != nil {
			refuse(w, r, PrinterNotFound.New(err.Error()).With(fault.StringParam("printer", printerID)))
			return
		}
		answerJSON(w, http.StatusOK, map[string]any{"ok": true, "printer": printerID, "detail": "Alert cleared"})
	})

	mux.HandleFunc("POST /printers/{printer}/pause", func(w http.ResponseWriter, r *http.Request) {
		printerID := r.PathValue("printer")
		if !checkDeviceOperate(r.Context(), access, routesapi.PrincipalOf(r), printerID) {
			refuse(w, r, PermissionDenied.New("caller lacks operate permission on printer").
				With(fault.StringParam("resource", printerID)))
			return
		}
		if _, err := svc.Pause(r.Context(), printerID); err != nil {
			refuse(w, r, CommandRejected.New(err.Error()).With(fault.StringParam("reason", err.Error())))
			return
		}
		answerJSON(w, http.StatusOK, map[string]any{"ok": true, "printer": printerID, "detail": "Pause command sent"})
	})

	mux.HandleFunc("POST /printers/{printer}/stop", func(w http.ResponseWriter, r *http.Request) {
		printerID := r.PathValue("printer")
		if !checkDeviceOperate(r.Context(), access, routesapi.PrincipalOf(r), printerID) {
			refuse(w, r, PermissionDenied.New("caller lacks operate permission on printer").
				With(fault.StringParam("resource", printerID)))
			return
		}
		if _, err := svc.Stop(r.Context(), printerID); err != nil {
			refuse(w, r, CommandRejected.New(err.Error()).With(fault.StringParam("reason", err.Error())))
			return
		}
		answerJSON(w, http.StatusOK, map[string]any{"ok": true, "printer": printerID, "detail": "Stop command sent"})
	})

	mux.HandleFunc("GET /timelapses", func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			answerJSON(w, http.StatusOK, map[string]any{"sessions": []any{}})
			return
		}
		printerFilter := strings.TrimSpace(r.URL.Query().Get("printer"))
		answerJSON(w, http.StatusOK, map[string]any{"sessions": svc.ListTimelapses(printerFilter)})
	})

	mux.HandleFunc("GET /timelapses/{printer}/{session}/frames/{frame}", func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			refuse(w, r, FrameNotFound.New("service not initialized"))
			return
		}
		printerID := r.PathValue("printer")
		sessionID := r.PathValue("session")
		frameName := r.PathValue("frame")
		jpg, err := svc.TimelapseFrame(printerID, sessionID, frameName)
		if err != nil {
			refuse(w, r, FrameNotFound.New("timelapse frame not found").With(fault.StringParam("printer", printerID)))
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(jpg)
	})

	mux.HandleFunc("DELETE /timelapses/{printer}/{session}", func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			refuse(w, r, CommandRejected.New("service not initialized").With(fault.StringParam("reason", "not configured")))
			return
		}
		printerID := r.PathValue("printer")
		sessionID := r.PathValue("session")
		if !checkDeviceOperate(r.Context(), access, routesapi.PrincipalOf(r), printerID) {
			refuse(w, r, PermissionDenied.New("caller lacks operate permission on printer").
				With(fault.StringParam("resource", printerID)))
			return
		}
		if err := svc.DeleteTimelapse(printerID, sessionID); err != nil {
			refuse(w, r, FrameNotFound.New("timelapse session not found").With(fault.StringParam("printer", printerID)))
			return
		}
		answerJSON(w, http.StatusOK, map[string]any{"ok": true, "printer": printerID, "detail": "Timelapse deleted"})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		refuse(w, r, RouteNotFound.
			New("this module serves no route at "+r.URL.Path).
			With(fault.StringParam("path", r.URL.Path)))
	})

	return mux
}

func checkDeviceOperate(ctx context.Context, access AccessChecker, principal, printerID string) bool {
	if access == nil || principal == "" {
		return true
	}
	decision, err := access.Check(ctx, accessapi.CheckParams{
		Subject:    principal,
		Action:     "operate",
		ObjectType: "device",
		ObjectID:   printerID,
	})
	if err != nil {
		return false
	}
	return decision.Allowed
}

func answerJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error                { return os.Stdin.Close() }
