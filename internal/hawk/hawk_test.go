package hawk

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/storageapi"

	"github.com/ln4cy/printer-hawk/internal/config"
)

func testJPEG(r, g, b uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 120, 90))
	for y := range 90 {
		for x := range 120 {
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	return buf.Bytes()
}

type fakeDevices struct {
	dev      deviceapi.Device
	commands []string
}

func (f *fakeDevices) List(_ context.Context, _ deviceapi.ListParams) (deviceapi.Page, error) {
	return deviceapi.Page{Items: []deviceapi.Device{f.dev}}, nil
}

func (f *fakeDevices) Get(_ context.Context, _ deviceapi.GetParams) (deviceapi.Device, error) {
	return f.dev, nil
}

func (f *fakeDevices) Command(_ context.Context, params deviceapi.CommandParams) (deviceapi.Result, error) {
	f.commands = append(f.commands, params.Verb)
	return deviceapi.Result{OK: true}, nil
}

type fakeStorage struct{ data []byte }

func (fs *fakeStorage) Read(_ context.Context, params storageapi.ReadParams) (storageapi.Chunk, error) {
	start := int(params.Offset)
	if start >= len(fs.data) {
		return storageapi.Chunk{EOF: true}, nil
	}
	return storageapi.Chunk{Data: fs.data[start:], EOF: true}, nil
}

type fakeEmitter struct{ emitted int }

func (fe *fakeEmitter) Emit(_ context.Context, _ string, _ string, _ any) error {
	fe.emitted++
	return nil
}

func TestParseTelemetryOfflineZeros(t *testing.T) {
	dev := deviceapi.Device{
		ID:        "p1",
		Name:      "CORE ONE",
		Connected: false,
		State: map[string]json.RawMessage{
			"/state":           json.RawMessage(`"ERROR"`),
			"/temperature/bed": json.RawMessage(`60.0`),
			"/axis/z":          json.RawMessage(`12.4`),
			"/job/progress":    json.RawMessage(`55.0`),
		},
	}
	tele := ParseTelemetry(dev)
	if tele.State != "OFFLINE" || tele.BedTemp == nil || *tele.BedTemp != 0 || tele.Z != nil {
		t.Fatalf("unexpected offline telemetry normalization: %+v", tele)
	}
}

func TestInspectFalseAlarmPauseAndFrameRetention(t *testing.T) {
	var stage1Status = `{"status":"failure","confidence":0.95,"defect":"spaghetti","reason":"Loose strings."}`
	var confirmPause = true

	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		think, _ := body["think"].(bool)
		if !think {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]any{"content": stage1Status},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{
				"content": mustStr(map[string]any{
					"confirm_pause": confirmPause,
					"status":        "failure",
					"confidence":    0.92,
					"defect":        "spaghetti",
					"reason":        "Confirmed spaghetti.",
				}),
			},
		})
	}))
	defer ollamaSrv.Close()

	camSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(testJPEG(200, 120, 40))
	}))
	defer camSrv.Close()

	devs := &fakeDevices{
		dev: deviceapi.Device{
			ID:        "p1",
			Name:      "CORE One INDX",
			Model:     "COREONEINDX",
			Connected: true,
			State: map[string]json.RawMessage{
				"/state":        json.RawMessage(`"PRINTING"`),
				"/axis/z":       json.RawMessage(`0.20`),
				"/job/progress": json.RawMessage(`12.0`),
			},
		},
	}
	store := &fakeStorage{data: testJPEG(10, 20, 30)}
	emit := &fakeEmitter{}
	dataDir := t.TempDir()

	svc := New(devs, store, emit, nil, dataDir, nil)
	svc.now = func() time.Time { return time.Unix(1700000000, 0).UTC() }

	mono := 0.0
	svc.clockMono = func() float64 { return mono }

	cfg := config.RuntimeConfig{
		PageSetup:        true,
		Stage1Primary:    config.ProviderSlot{Provider: config.ProviderOllama, BaseURL: ollamaSrv.URL, Model: "gemma4:26b"},
		Stage2Primary:    config.ProviderSlot{Provider: config.ProviderOllama, BaseURL: ollamaSrv.URL, Model: "gemma4:26b", Think: true},
		MaxImageWidth:    1024,
		JPEGQuality:      82,
		MaxHistoryFrames: 2,
		DefaultPrinter: config.PrinterConfig{
			Enabled:                 true,
			Action:                  config.ActionPause,
			CameraMode:              config.CameraModeURL,
			LiveFrameURL:            camSrv.URL,
			WarmupSeconds:           1,
			ZMinStepMM:              0.04,
			ZMaxStepMM:              1.20,
			ZSettleSeconds:          1,
			MinCheckIntervalSec:     1,
			FallbackMaxWaitSec:      60,
			Stage1FailThreshold:     0.65,
			Stage1WarnEscalateCount: 2,
			Stage2PauseThreshold:    0.75,
		},
	}
	svc.Configure(cfg)

	// Dry-run manual inspect runs Stage 1 + Stage 2 when verify=true, does not pause
	rec, err := svc.Inspect(context.Background(), "p1", true)
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	if rec.Action != "dry_run_would_pause" || rec.ImageSource != "live_url" || len(devs.commands) != 0 {
		t.Fatalf("unexpected dry-run result: %+v cmds=%v", rec, devs.commands)
	}

	// Automated tick triggers warmup -> baseline -> auto-pause + event emission
	svc.Tick(context.Background())
	mono = 2.0
	svc.Tick(context.Background())
	mono = 4.0
	svc.Tick(context.Background())

	if len(devs.commands) != 1 || devs.commands[0] != verbPause || emit.emitted != 1 {
		t.Fatalf("expected auto pause + emitted event, cmds=%v emitted=%d", devs.commands, emit.emitted)
	}

	// FalseAlarm clears alert/latch and promotes lastAnalyzedJPG as baseline
	ok, err := svc.FalseAlarm("p1")
	if err != nil || !ok {
		t.Fatalf("FalseAlarm failed: %v", err)
	}
	st := svc.Status("p1")
	if len(st.Printers) != 1 || st.Printers[0].AlertActive || st.Printers[0].PausedForJob {
		t.Fatalf("expected cleared alert/latch after FalseAlarm: %+v", st.Printers)
	}

	// Trigger a 3rd inspection and verify MaxHistoryFrames=2 prunes older JPGs
	svc.now = func() time.Time { return time.Unix(1700000100, 0).UTC() }
	_, _ = svc.Inspect(context.Background(), "p1", false)
	entries, err := os.ReadDir(filepath.Join(dataDir, "frames", "p1"))
	if err != nil || len(entries) > 2 {
		t.Fatalf("expected <= 2 retained frames, got %d (err=%v)", len(entries), err)
	}
}

func mustStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestStartInspectAsync(t *testing.T) {
	imgBytes := testJPEG(200, 120, 40)
	camSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(imgBytes)
	}))
	defer camSrv.Close()

	aiStarted := make(chan struct{})
	aiRelease := make(chan struct{})
	aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(aiStarted)
		<-aiRelease
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"{\"status\":\"ok\",\"confidence\":0.98,\"defect\":\"none\",\"reason\":\"Bed clean\"}"}}`))
	}))
	defer aiSrv.Close()

	devs := &fakeDevices{dev: deviceapi.Device{
		ID:        "p1",
		Name:      "CORE ONE",
		Connected: true,
		State:     map[string]json.RawMessage{"/state": json.RawMessage(`"IDLE"`)},
	}}
	svc := New(devs, &fakeStorage{}, nil, nil, t.TempDir(), nil)
	cfg := config.FromManifest(config.DefaultManifestSettings())
	cfg.Stage1Primary = config.ProviderSlot{
		Provider:          config.ProviderOllama,
		BaseURL:           aiSrv.URL,
		Model:             "gemma4:26b",
		ConnectTimeoutSec: 2,
		ReadTimeoutSec:    10,
	}
	cfg.DefaultPrinter.CameraMode = config.CameraModeURL
	cfg.DefaultPrinter.LiveFrameURL = camSrv.URL
	svc.Configure(cfg)
	svc.Tick(context.Background())

	if _, err := svc.StartInspect("missing", false); err == nil {
		t.Fatal("expected error for unknown printer")
	}

	started, err := svc.StartInspect("p1", false)
	if err != nil || !started {
		t.Fatalf("expected StartInspect started=true, got started=%v err=%v", started, err)
	}
	<-aiStarted

	startedAgain, err := svc.StartInspect("p1", false)
	if err != nil || startedAgain {
		t.Fatalf("expected duplicate StartInspect to return started=false, got %v err=%v", startedAgain, err)
	}
	if st := svc.Status("p1"); len(st.Printers) != 1 || !st.Printers[0].Inspecting {
		t.Fatalf("expected Inspecting=true while AI call in flight: %+v", st.Printers)
	}

	close(aiRelease)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := svc.Status("p1")
		if len(st.Printers) == 1 && !st.Printers[0].Inspecting && st.Printers[0].LastCheck != nil {
			if st.Printers[0].LastCheck.Stage1.Status != "ok" {
				t.Fatalf("unexpected stage1 status: %+v", st.Printers[0].LastCheck)
			}
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("timed out waiting for async StartInspect completion")
}

func TestWholePrintTimelapseLifecycle(t *testing.T) {
	imgBytes := testJPEG(90, 140, 210)
	camSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(imgBytes)
	}))
	defer camSrv.Close()

	devs := &fakeDevices{dev: deviceapi.Device{
		ID:        "p1",
		Name:      "CORE One",
		Connected: true,
		State: map[string]json.RawMessage{
			"/state":    json.RawMessage(`"PRINTING"`),
			"/axis/z":   json.RawMessage(`0.20`),
			"/job/path": json.RawMessage(`"/usb/benchy.bgcode"`),
		},
	}}
	svc := New(devs, &fakeStorage{}, nil, nil, t.TempDir(), nil)
	svc.now = func() time.Time { return time.Date(2026, 11, 25, 19, 0, 0, 0, time.UTC) }
	mono := 0.0
	svc.clockMono = func() float64 { return mono }

	cfg := config.FromManifest(config.DefaultManifestSettings())
	cfg.DefaultPrinter.Enabled = false // Timelapse must work even when AI guard is disabled
	cfg.DefaultPrinter.CameraMode = config.CameraModeURL
	cfg.DefaultPrinter.LiveFrameURL = camSrv.URL
	cfg.DefaultPrinter.WarmupSeconds = 5
	cfg.DefaultPrinter.ZSettleSeconds = 1
	cfg.DefaultPrinter.TimelapseMode = config.TimelapseLayer
	cfg.DefaultPrinter.TimelapseFPS = 24
	svc.Configure(cfg)

	// Tick 1: starts session + captures initial frame (frame 1)
	svc.Tick(context.Background())
	// Warmup + settle Layer 1 baseline (frame 2)
	mono = 6
	svc.Tick(context.Background())
	mono = 8
	svc.Tick(context.Background())

	// Layer 2 at Z=0.40 settles (frame 3)
	devs.dev.State["/axis/z"] = json.RawMessage(`0.40`)
	mono = 11
	svc.Tick(context.Background())
	mono = 13
	svc.Tick(context.Background())

	sessions := svc.ListTimelapses("p1")
	if len(sessions) != 1 || sessions[0].Status != "recording" || sessions[0].FrameCount != 3 {
		t.Fatalf("expected active session with 3 frames, got %+v", sessions)
	}

	// Print finishes -> captures final frame (frame 4) and marks completed
	devs.dev.State["/state"] = json.RawMessage(`"FINISHED"`)
	devs.dev.State["/job/progress"] = json.RawMessage(`100.0`)
	mono = 15
	svc.Tick(context.Background())

	sessions = svc.ListTimelapses("p1")
	if len(sessions) != 1 || sessions[0].Status != "completed" || sessions[0].FrameCount != 4 || sessions[0].JobName != "benchy" {
		t.Fatalf("expected completed benchy session with 4 frames, got %+v", sessions)
	}
	raw, err := svc.TimelapseFrame("p1", sessions[0].ID, "000004.jpg")
	if err != nil || len(raw) == 0 {
		t.Fatalf("TimelapseFrame failed: %v", err)
	}
	if err := svc.DeleteTimelapse("p1", sessions[0].ID); err != nil {
		t.Fatalf("DeleteTimelapse failed: %v", err)
	}
	if got := svc.ListTimelapses("p1"); len(got) != 0 {
		t.Fatalf("expected empty list after delete, got %+v", got)
	}
}
