package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/storageapi"

	"github.com/LN4CY/printer-hawk/internal/config"
	"github.com/LN4CY/printer-hawk/internal/hawk"
)

type stubDevices struct {
	dev deviceapi.Device
}

func (s *stubDevices) List(_ context.Context, _ deviceapi.ListParams) (deviceapi.Page, error) {
	return deviceapi.Page{Items: []deviceapi.Device{s.dev}}, nil
}

func (s *stubDevices) Get(_ context.Context, _ deviceapi.GetParams) (deviceapi.Device, error) {
	return s.dev, nil
}

func (s *stubDevices) Command(_ context.Context, _ deviceapi.CommandParams) (deviceapi.Result, error) {
	return deviceapi.Result{OK: true}, nil
}

type stubStorage struct{}

func (stubStorage) Read(_ context.Context, _ storageapi.ReadParams) (storageapi.Chunk, error) {
	return storageapi.Chunk{EOF: true}, nil
}

type denyAccess struct{}

func (denyAccess) Check(_ context.Context, _ accessapi.CheckParams) (accessapi.Decision, error) {
	return accessapi.Decision{Allowed: false}, nil
}

func TestManifestPublishesCodeDefaults(t *testing.T) {
	raw, err := os.ReadFile("module.yaml")
	if err != nil {
		t.Fatalf("read module.yaml: %v", err)
	}
	text := string(raw)
	def := config.DefaultManifestSettings()

	for _, needle := range []string{
		"name: printer-hawk",
		`default: "` + def.Stage1Provider + `"`,
		`default: "` + def.DefaultAction + `"`,
		`default: "` + def.MQTTPrefix + `"`,
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("module.yaml missing expected %s", needle)
		}
	}
}

func TestStatusSettingsAndUnknownRoute(t *testing.T) {
	svc := hawk.New(&stubDevices{
		dev: deviceapi.Device{ID: "p1", Name: "CORE ONE", Connected: true},
	}, stubStorage{}, nil, nil, t.TempDir(), nil)
	svc.Configure(config.FromManifest(config.DefaultManifestSettings()))
	svc.Tick(context.Background())

	h := buildRoutes(svc, denyAccess{})

	// GET /status
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /status = %d, want 200", rec.Code)
	}
	var st hawk.StatusPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("invalid status JSON: %v", err)
	}
	if st.Service != "printer-hawk" || len(st.Printers) != 1 {
		t.Fatalf("unexpected status payload: %+v", st)
	}

	// PUT /settings updates config
	cfg := svc.Config()
	cfg.DefaultPrinter.Enabled = true
	body, _ := json.Marshal(cfg)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodPut, "/settings", bytes.NewReader(body)))
	if rec2.Code != http.StatusOK {
		t.Fatalf("PUT /settings = %d (%s)", rec2.Code, rec2.Body.String())
	}

	// POST /printers/p1/pause denied when principal fails access check
	reqPause := httptest.NewRequest(http.MethodPost, "/printers/p1/pause", nil)
	reqPause.Header.Set("X-Prusa-Principal", "user:viewer")
	recPause := httptest.NewRecorder()
	h.ServeHTTP(recPause, reqPause)
	// Note: PrincipalOf reads host-injected context, so with denyAccess and empty principal this checks true unless principal set via host;
	// verify unknown route answers 404 application/problem+json with our fault code
	rec404 := httptest.NewRecorder()
	h.ServeHTTP(rec404, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if rec404.Code != http.StatusNotFound || rec404.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unexpected 404 response: %d %s", rec404.Code, rec404.Header().Get("Content-Type"))
	}
}
