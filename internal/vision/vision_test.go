package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ln4cy/printer-hawk/internal/config"
)

func sampleJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	return buf.Bytes()
}

func TestResizeJPEGDownscalesCleanly(t *testing.T) {
	raw := sampleJPEG(1600, 900)
	resized, err := ResizeJPEG(raw, 800, 80)
	if err != nil {
		t.Fatalf("ResizeJPEG failed: %v", err)
	}
	dec, err := jpeg.Decode(bytes.NewReader(resized))
	if err != nil {
		t.Fatalf("decode resized: %v", err)
	}
	if dec.Bounds().Dx() != 800 || dec.Bounds().Dy() != 450 {
		t.Fatalf("unexpected dimensions %dx%d, want 800x450", dec.Bounds().Dx(), dec.Bounds().Dy())
	}
}

func TestParseJSONObjectRawAndFenced(t *testing.T) {
	raw := `{"status":"ok","confidence":0.95,"defect":"none","reason":"Clean bed."}`
	obj, err := ParseJSONObject(raw)
	if err != nil || obj["status"] != "ok" {
		t.Fatalf("raw parse failed: obj=%v err=%v", obj, err)
	}
	fenced := "Result:\n```json\n{\"confirm_pause\":true,\"status\":\"failure\",\"confidence\":0.9,\"defect\":\"spaghetti\",\"reason\":\"Loose strands.\"}\n```"
	obj2, err := ParseJSONObject(fenced)
	if err != nil || obj2["defect"] != "spaghetti" {
		t.Fatalf("fenced parse failed: obj=%v err=%v", obj2, err)
	}
}

func TestStage1AndStage2Failover(t *testing.T) {
	failingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "out of vram", http.StatusInternalServerError)
	}))
	defer failingSrv.Close()

	gemSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{"text": `{"status":"failure","confidence":0.88,"defect":"spaghetti","reason":"Loose strands."}`},
						},
					},
				},
			},
		})
	}))
	defer gemSrv.Close()

	ollamaOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{
				"content": `{"confirm_pause":true,"status":"failure","confidence":0.92,"defect":"spaghetti","reason":"Confirmed spaghetti."}`,
			},
		})
	}))
	defer ollamaOK.Close()

	jpg := sampleJPEG(100, 100)
	ctx := context.Background()

	s1Primary := config.ProviderSlot{Provider: config.ProviderOllama, BaseURL: failingSrv.URL, Model: "gemma4:26b"}
	s1Fallback := config.ProviderSlot{Provider: config.ProviderGemini, BaseURL: gemSrv.URL, Model: "gemini-3.8-flash", APIKey: "k"}
	res1, err := RunStage1(ctx, s1Primary, s1Fallback, jpg, nil, "ctx")
	if err != nil {
		t.Fatalf("RunStage1 failed: %v", err)
	}
	if res1.Provider != config.ProviderGemini || res1.FallbackFrom != config.ProviderOllama {
		t.Fatalf("expected gemini fallback from ollama, got %+v", res1)
	}

	s2Primary := config.ProviderSlot{Provider: config.ProviderAnthropic, BaseURL: failingSrv.URL, Model: "claude-sonnet-5-5", APIKey: "k"}
	s2Fallback := config.ProviderSlot{Provider: config.ProviderOllama, BaseURL: ollamaOK.URL, Model: "gemma4:26b", Think: true}
	res2, err := RunStage2(ctx, s2Primary, s2Fallback, jpg, nil, nil, "ctx", res1)
	if err != nil {
		t.Fatalf("RunStage2 failed: %v", err)
	}
	if res2.Provider != config.ProviderOllama || res2.FallbackFrom != config.ProviderAnthropic || res2.ConfirmPause == nil || !*res2.ConfirmPause {
		t.Fatalf("expected confirmed ollama fallback from anthropic, got %+v", res2)
	}
}
