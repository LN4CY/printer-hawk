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

	"github.com/LN4CY/printer-hawk/internal/config"
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
