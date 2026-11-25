package config

import (
	"testing"
)

func TestFromManifestAndMergeSecrets(t *testing.T) {
	ms := DefaultManifestSettings()
	ms.Stage1Provider = ProviderOllama
	ms.Stage1BaseURL = "http://192.168.50.201:11434/"
	ms.Stage1Model = "gemma4:26b"
	ms.Stage1FallbackProvider = ProviderGemini
	ms.Stage1FallbackModel = "gemini-3.8-flash"
	ms.Stage1FallbackAPIKey = "secret-gem-123"
	ms.LiveFrameURL = "http://127.0.0.1:1984/api/frame.jpeg?src=coreone"

	cfg := FromManifest(ms)
	if cfg.Stage1Primary.BaseURL != "http://192.168.50.201:11434" {
		t.Fatalf("unexpected BaseURL trimming: %q", cfg.Stage1Primary.BaseURL)
	}
	if cfg.DefaultPrinter.CameraMode != CameraModeURL {
		t.Fatalf("expected CameraModeURL when LiveFrameURL set, got %q", cfg.DefaultPrinter.CameraMode)
	}

	redacted := cfg.Redact()
	if redacted.Stage1Fallback.APIKey != SecretMask {
		t.Fatalf("expected secret mask on redacted config, got %q", redacted.Stage1Fallback.APIKey)
	}

	// Merging back masked secret keeps original key
	updated := redacted
	updated.PageSetup = false // PageSetup cannot be toggled via web UI
	updated.Stage1Primary.Model = "qwen3-vl:8b"
	merged := MergeUpdate(cfg, updated)
	if merged.Stage1Fallback.APIKey != "secret-gem-123" {
		t.Fatalf("expected preserved secret, got %q", merged.Stage1Fallback.APIKey)
	}
	if !merged.PageSetup {
		t.Fatal("expected PageSetup to remain controlled by manifest")
	}

	// Explicitly clearing key removes it
	cleared := merged
	cleared.Stage1Fallback.APIKey = ""
	merged2 := MergeUpdate(merged, cleared)
	if merged2.Stage1Fallback.APIKey != "" {
		t.Fatalf("expected empty cleared key, got %q", merged2.Stage1Fallback.APIKey)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ms := DefaultManifestSettings()
	ms.Stage1Provider = ProviderGemini
	ms.Stage1Model = "gemini-3.8-flash"
	ms.Stage1APIKey = "gem-key"

	cfg := FromManifest(ms)
	cfg.Printers["printer-1"] = PrinterConfig{
		Enabled: true,
		Action:  ActionPause,
	}
	if err := SaveToDisk(dir, cfg); err != nil {
		t.Fatalf("SaveToDisk failed: %v", err)
	}

	loaded, err := LoadFromDisk(dir, ms)
	if err != nil {
		t.Fatalf("LoadFromDisk failed: %v", err)
	}
	if !loaded.Printers["printer-1"].Enabled || loaded.Printers["printer-1"].Action != ActionPause {
		t.Fatalf("printer override lost: %+v", loaded.Printers["printer-1"])
	}
	if loaded.Printers["printer-1"].FallbackMaxWaitSec != 60 {
		t.Fatalf("expected normalized default FallbackMaxWaitSec=60, got %v", loaded.Printers["printer-1"].FallbackMaxWaitSec)
	}
}
