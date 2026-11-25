// Package config manages manifest defaults, persisted operator overrides, and secret masking.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Provider identifier constants.
const (
	ProviderNone         = "none"
	ProviderOllama       = "ollama"
	ProviderGemini       = "gemini"
	ProviderAnthropic    = "anthropic"
	ProviderOpenAICompat = "openai_compat"
)

// Camera source mode constants.
const (
	CameraModeConnect = "connect"
	CameraModeURL     = "url"
)

// Action mode constants.
const (
	ActionNotify = "notify"
	ActionPause  = "pause"
)

// Timelapse recording mode constants.
const (
	TimelapseOff      = "off"
	TimelapseLayer    = "layer"
	TimelapseInterval = "interval"
)

// SecretMask is returned by GET /settings when an API key is stored so secrets never leave the server.
const SecretMask = "********"

// FlexNumber unmarshals numbers from either JSON numbers or quoted strings (manifest defaults).
type FlexNumber float64

// UnmarshalJSON parses a float from raw JSON or quoted text.
func (n *FlexNumber) UnmarshalJSON(raw []byte) error {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text == "" || text == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("invalid number %q: %w", text, err)
	}
	*n = FlexNumber(v)
	return nil
}

// FlexBool unmarshals booleans from either JSON booleans or quoted strings.
type FlexBool bool

// UnmarshalJSON parses a boolean from raw JSON or quoted text.
func (b *FlexBool) UnmarshalJSON(raw []byte) error {
	text := strings.ToLower(strings.Trim(strings.TrimSpace(string(raw)), `"`))
	if text == "" || text == "null" || text == "false" || text == "0" || text == "no" || text == "off" {
		*b = false
		return nil
	}
	if text == "true" || text == "1" || text == "yes" || text == "on" {
		*b = true
		return nil
	}
	return fmt.Errorf("invalid bool %q", text)
}

// ManifestSettings mirrors all keys declared under `options:` in module.yaml.
type ManifestSettings struct {
	PageSetup              FlexBool   `json:"page_setup"`
	Stage1Provider         string     `json:"stage1_provider"`
	Stage1BaseURL          string     `json:"stage1_base_url"`
	Stage1Model            string     `json:"stage1_model"`
	Stage1APIKey           string     `json:"stage1_api_key"`
	Stage1Think            FlexBool   `json:"stage1_think"`
	Stage1FallbackProvider string     `json:"stage1_fallback_provider"`
	Stage1FallbackBaseURL  string     `json:"stage1_fallback_base_url"`
	Stage1FallbackModel    string     `json:"stage1_fallback_model"`
	Stage1FallbackAPIKey   string     `json:"stage1_fallback_api_key"`
	Stage2Provider         string     `json:"stage2_provider"`
	Stage2BaseURL          string     `json:"stage2_base_url"`
	Stage2Model            string     `json:"stage2_model"`
	Stage2APIKey           string     `json:"stage2_api_key"`
	Stage2Think            FlexBool   `json:"stage2_think"`
	Stage2FallbackProvider string     `json:"stage2_fallback_provider"`
	Stage2FallbackBaseURL  string     `json:"stage2_fallback_base_url"`
	Stage2FallbackModel    string     `json:"stage2_fallback_model"`
	Stage2FallbackAPIKey   string     `json:"stage2_fallback_api_key"`
	Stage2FallbackThink    FlexBool   `json:"stage2_fallback_think"`
	DefaultAction          string     `json:"default_action"`
	LiveFrameURL           string     `json:"live_frame_url"`
	WarmupSeconds          FlexNumber `json:"warmup_seconds"`
	FallbackMaxWaitS       FlexNumber `json:"fallback_max_wait_s"`
	MQTTHost               string     `json:"mqtt_host"`
	MQTTPort               FlexNumber `json:"mqtt_port"`
	MQTTPrefix             string     `json:"mqtt_prefix"`
}

// ProviderSlot configures one AI model endpoint (Stage 1 or Stage 2, Primary or Fallback).
type ProviderSlot struct {
	Provider          string  `json:"provider"`
	BaseURL           string  `json:"base_url"`
	Model             string  `json:"model"`
	APIKey            string  `json:"api_key"`
	Think             bool    `json:"think"`
	ConnectTimeoutSec float64 `json:"connect_timeout_sec"`
	ReadTimeoutSec    float64 `json:"read_timeout_sec"`
}

// PrinterConfig stores per-printer monitoring and camera overrides.
type PrinterConfig struct {
	Enabled                 bool    `json:"enabled"`
	Action                  string  `json:"action"`
	CameraMode              string  `json:"camera_mode"`
	LiveFrameURL            string  `json:"live_frame_url"`
	WarmupSeconds           float64 `json:"warmup_seconds"`
	ZMinStepMM              float64 `json:"z_min_step_mm"`
	ZMaxStepMM              float64 `json:"z_max_step_mm"`
	ZSettleSeconds          float64 `json:"z_settle_seconds"`
	MinCheckIntervalSec     float64 `json:"min_check_interval_sec"`
	FallbackMaxWaitSec      float64 `json:"fallback_max_wait_sec"`
	Stage1FailThreshold     float64 `json:"stage1_fail_threshold"`
	Stage1WarnEscalateCount int     `json:"stage1_warn_escalate_count"`
	Stage2PauseThreshold    float64 `json:"stage2_pause_threshold"`
	CustomPromptNotes       string  `json:"custom_prompt_notes"`
	TimelapseMode           string  `json:"timelapse_mode"`
	TimelapseIntervalSec    float64 `json:"timelapse_interval_sec"`
	TimelapseFPS            int     `json:"timelapse_fps"`
	TimelapseMaxSessions    int     `json:"timelapse_max_sessions"`
}

// MQTTConfig stores optional smart-home MQTT notification settings.
type MQTTConfig struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Prefix string `json:"prefix"`
}

// RuntimeConfig is the full persisted configuration served by GET/PUT /settings.
type RuntimeConfig struct {
	PageSetup        bool                     `json:"page_setup"`
	Stage1Primary    ProviderSlot             `json:"stage1_primary"`
	Stage1Fallback   ProviderSlot             `json:"stage1_fallback"`
	Stage2Primary    ProviderSlot             `json:"stage2_primary"`
	Stage2Fallback   ProviderSlot             `json:"stage2_fallback"`
	MaxImageWidth    int                      `json:"max_image_width"`
	JPEGQuality      int                      `json:"jpeg_quality"`
	MaxHistoryFrames int                      `json:"max_history_frames"`
	MQTT             MQTTConfig               `json:"mqtt"`
	DefaultPrinter   PrinterConfig            `json:"default_printer"`
	Printers         map[string]PrinterConfig `json:"printers"`
}

// DefaultManifestSettings returns the defaults matching module.yaml.
func DefaultManifestSettings() ManifestSettings {
	return ManifestSettings{
		PageSetup:              true,
		Stage1Provider:         ProviderNone,
		Stage1FallbackProvider: ProviderNone,
		Stage2Provider:         ProviderNone,
		Stage2Think:            true,
		Stage2FallbackProvider: ProviderNone,
		Stage2FallbackThink:    true,
		DefaultAction:          ActionNotify,
		WarmupSeconds:          45,
		FallbackMaxWaitS:       60,
		MQTTPort:               1883,
		MQTTPrefix:             "prusa/printer-hawk",
	}
}

// DefaultPrinterConfig returns default monitoring parameters for newly seen printers.
func DefaultPrinterConfig() PrinterConfig {
	return PrinterConfig{
		Enabled:                 false,
		Action:                  ActionNotify,
		CameraMode:              CameraModeConnect,
		LiveFrameURL:            "",
		WarmupSeconds:           45.0,
		ZMinStepMM:              0.04,
		ZMaxStepMM:              1.20,
		ZSettleSeconds:          4.0,
		MinCheckIntervalSec:     15.0,
		FallbackMaxWaitSec:      60.0,
		Stage1FailThreshold:     0.65,
		Stage1WarnEscalateCount: 2,
		Stage2PauseThreshold:    0.75,
		CustomPromptNotes:       "",
		TimelapseMode:           TimelapseOff,
		TimelapseIntervalSec:    30.0,
		TimelapseFPS:            24,
		TimelapseMaxSessions:    20,
	}
}

// FromManifest constructs a baseline RuntimeConfig from module.yaml options.
func FromManifest(ms ManifestSettings) RuntimeConfig {
	defPrinter := DefaultPrinterConfig()
	if ms.DefaultAction == ActionPause || ms.DefaultAction == ActionNotify {
		defPrinter.Action = ms.DefaultAction
	}
	if strings.TrimSpace(ms.LiveFrameURL) != "" {
		defPrinter.CameraMode = CameraModeURL
		defPrinter.LiveFrameURL = strings.TrimSpace(ms.LiveFrameURL)
	}
	if float64(ms.WarmupSeconds) > 0 {
		defPrinter.WarmupSeconds = float64(ms.WarmupSeconds)
	}
	if float64(ms.FallbackMaxWaitS) > 0 {
		defPrinter.FallbackMaxWaitSec = float64(ms.FallbackMaxWaitS)
	}

	port := int(ms.MQTTPort)
	if port <= 0 {
		port = 1883
	}
	prefix := strings.TrimSpace(ms.MQTTPrefix)
	if prefix == "" {
		prefix = "prusa/printer-hawk"
	}

	return Normalize(RuntimeConfig{
		PageSetup: bool(ms.PageSetup),
		Stage1Primary: ProviderSlot{
			Provider:          ms.Stage1Provider,
			BaseURL:           ms.Stage1BaseURL,
			Model:             ms.Stage1Model,
			APIKey:            ms.Stage1APIKey,
			Think:             bool(ms.Stage1Think),
			ConnectTimeoutSec: 3.0,
			ReadTimeoutSec:    75.0,
		},
		Stage1Fallback: ProviderSlot{
			Provider:          ms.Stage1FallbackProvider,
			BaseURL:           ms.Stage1FallbackBaseURL,
			Model:             ms.Stage1FallbackModel,
			APIKey:            ms.Stage1FallbackAPIKey,
			ConnectTimeoutSec: 5.0,
			ReadTimeoutSec:    45.0,
		},
		Stage2Primary: ProviderSlot{
			Provider:          ms.Stage2Provider,
			BaseURL:           ms.Stage2BaseURL,
			Model:             ms.Stage2Model,
			APIKey:            ms.Stage2APIKey,
			Think:             bool(ms.Stage2Think),
			ConnectTimeoutSec: 5.0,
			ReadTimeoutSec:    60.0,
		},
		Stage2Fallback: ProviderSlot{
			Provider:          ms.Stage2FallbackProvider,
			BaseURL:           ms.Stage2FallbackBaseURL,
			Model:             ms.Stage2FallbackModel,
			APIKey:            ms.Stage2FallbackAPIKey,
			Think:             bool(ms.Stage2FallbackThink),
			ConnectTimeoutSec: 3.0,
			ReadTimeoutSec:    120.0,
		},
		MaxImageWidth:    1024,
		JPEGQuality:      82,
		MaxHistoryFrames: 20,
		MQTT: MQTTConfig{
			Host:   strings.TrimSpace(ms.MQTTHost),
			Port:   port,
			Prefix: prefix,
		},
		DefaultPrinter: defPrinter,
		Printers:       map[string]PrinterConfig{},
	})
}

// Normalize clamps ranges and populates defaults across a RuntimeConfig.
func Normalize(cfg RuntimeConfig) RuntimeConfig {
	cfg.Stage1Primary = normalizeSlot(cfg.Stage1Primary, 3.0, 75.0)
	cfg.Stage1Fallback = normalizeSlot(cfg.Stage1Fallback, 5.0, 45.0)
	cfg.Stage2Primary = normalizeSlot(cfg.Stage2Primary, 5.0, 60.0)
	cfg.Stage2Fallback = normalizeSlot(cfg.Stage2Fallback, 3.0, 120.0)
	if cfg.MaxImageWidth < 256 || cfg.MaxImageWidth > 2048 {
		cfg.MaxImageWidth = 1024
	}
	if cfg.JPEGQuality < 40 || cfg.JPEGQuality > 95 {
		cfg.JPEGQuality = 82
	}
	if cfg.MaxHistoryFrames <= 0 || cfg.MaxHistoryFrames > 100 {
		cfg.MaxHistoryFrames = 20
	}
	if cfg.MQTT.Port <= 0 || cfg.MQTT.Port > 65535 {
		cfg.MQTT.Port = 1883
	}
	cfg.MQTT.Host = strings.TrimSpace(cfg.MQTT.Host)
	cfg.MQTT.Prefix = strings.Trim(strings.TrimSpace(cfg.MQTT.Prefix), "/")
	if cfg.MQTT.Prefix == "" {
		cfg.MQTT.Prefix = "prusa/printer-hawk"
	}
	cfg.DefaultPrinter = NormalizePrinter(cfg.DefaultPrinter, DefaultPrinterConfig())
	if cfg.Printers == nil {
		cfg.Printers = map[string]PrinterConfig{}
	}
	cleanPrinters := make(map[string]PrinterConfig, len(cfg.Printers))
	for id, pc := range cfg.Printers {
		if strings.TrimSpace(id) == "" {
			continue
		}
		cleanPrinters[id] = NormalizePrinter(pc, cfg.DefaultPrinter)
	}
	cfg.Printers = cleanPrinters
	return cfg
}

// NormalizePrinter fills defaults and validates ranges for one printer override.
func NormalizePrinter(pc PrinterConfig, fallback PrinterConfig) PrinterConfig {
	if pc.Action != ActionPause && pc.Action != ActionNotify {
		pc.Action = fallback.Action
	}
	if pc.Action != ActionPause && pc.Action != ActionNotify {
		pc.Action = ActionNotify
	}
	pc.LiveFrameURL = strings.TrimSpace(pc.LiveFrameURL)
	if pc.CameraMode != CameraModeURL && pc.CameraMode != CameraModeConnect {
		pc.CameraMode = fallback.CameraMode
	}
	if pc.CameraMode != CameraModeURL && pc.CameraMode != CameraModeConnect {
		pc.CameraMode = CameraModeConnect
	}
	if pc.WarmupSeconds <= 0 {
		pc.WarmupSeconds = fallback.WarmupSeconds
	}
	if pc.ZMinStepMM <= 0 {
		pc.ZMinStepMM = fallback.ZMinStepMM
	}
	if pc.ZMaxStepMM <= pc.ZMinStepMM {
		pc.ZMaxStepMM = fallback.ZMaxStepMM
	}
	if pc.ZSettleSeconds <= 0 {
		pc.ZSettleSeconds = fallback.ZSettleSeconds
	}
	if pc.MinCheckIntervalSec <= 0 {
		pc.MinCheckIntervalSec = fallback.MinCheckIntervalSec
	}
	if pc.FallbackMaxWaitSec <= 0 {
		pc.FallbackMaxWaitSec = fallback.FallbackMaxWaitSec
	}
	if pc.Stage1FailThreshold <= 0 || pc.Stage1FailThreshold > 1 {
		pc.Stage1FailThreshold = fallback.Stage1FailThreshold
	}
	if pc.Stage1WarnEscalateCount <= 0 {
		pc.Stage1WarnEscalateCount = fallback.Stage1WarnEscalateCount
	}
	if pc.Stage2PauseThreshold <= 0 || pc.Stage2PauseThreshold > 1 {
		pc.Stage2PauseThreshold = fallback.Stage2PauseThreshold
	}
	pc.CustomPromptNotes = strings.TrimSpace(pc.CustomPromptNotes)
	switch strings.ToLower(strings.TrimSpace(pc.TimelapseMode)) {
	case TimelapseLayer:
		pc.TimelapseMode = TimelapseLayer
	case TimelapseInterval:
		pc.TimelapseMode = TimelapseInterval
	case TimelapseOff:
		pc.TimelapseMode = TimelapseOff
	default:
		if fallback.TimelapseMode == TimelapseLayer || fallback.TimelapseMode == TimelapseInterval {
			pc.TimelapseMode = fallback.TimelapseMode
		} else {
			pc.TimelapseMode = TimelapseOff
		}
	}
	if pc.TimelapseIntervalSec < 5 {
		if fallback.TimelapseIntervalSec >= 5 {
			pc.TimelapseIntervalSec = fallback.TimelapseIntervalSec
		} else {
			pc.TimelapseIntervalSec = 30.0
		}
	}
	if pc.TimelapseFPS <= 0 {
		pc.TimelapseFPS = fallback.TimelapseFPS
	}
	if pc.TimelapseFPS <= 0 {
		pc.TimelapseFPS = 24
	}
	if pc.TimelapseFPS > 60 {
		pc.TimelapseFPS = 60
	}
	if pc.TimelapseMaxSessions <= 0 {
		pc.TimelapseMaxSessions = fallback.TimelapseMaxSessions
	}
	if pc.TimelapseMaxSessions <= 0 {
		pc.TimelapseMaxSessions = 20
	}
	if pc.TimelapseMaxSessions > 200 {
		pc.TimelapseMaxSessions = 200
	}
	return pc
}

func normalizeSlot(s ProviderSlot, defConn, defRead float64) ProviderSlot {
	p := strings.ToLower(strings.TrimSpace(s.Provider))
	switch p {
	case ProviderOllama, ProviderGemini, ProviderAnthropic, ProviderOpenAICompat, ProviderNone:
		s.Provider = p
	default:
		s.Provider = ProviderNone
	}
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	s.Model = strings.TrimSpace(s.Model)
	s.APIKey = strings.TrimSpace(s.APIKey)
	if s.ConnectTimeoutSec <= 0 {
		s.ConnectTimeoutSec = defConn
	}
	if s.ReadTimeoutSec <= 0 {
		s.ReadTimeoutSec = defRead
	}
	return s
}

// EffectivePrinter returns the printer override if present, or the normalized default config.
func (c RuntimeConfig) EffectivePrinter(printerID string) PrinterConfig {
	if pc, ok := c.Printers[printerID]; ok {
		return NormalizePrinter(pc, c.DefaultPrinter)
	}
	return c.DefaultPrinter
}

// Redact returns a copy with stored API keys replaced by SecretMask for API/UI reads.
func (c RuntimeConfig) Redact() RuntimeConfig {
	out := c
	out.Stage1Primary = maskSlot(out.Stage1Primary)
	out.Stage1Fallback = maskSlot(out.Stage1Fallback)
	out.Stage2Primary = maskSlot(out.Stage2Primary)
	out.Stage2Fallback = maskSlot(out.Stage2Fallback)
	return out
}

func maskSlot(s ProviderSlot) ProviderSlot {
	if s.APIKey != "" {
		s.APIKey = SecretMask
	}
	return s
}

// MergeUpdate merges an operator UI update into the current config while preserving masked secrets
// and keeping PageSetup controlled strictly by the manifest option.
func MergeUpdate(current, incoming RuntimeConfig) RuntimeConfig {
	incoming.PageSetup = current.PageSetup
	incoming.Stage1Primary.APIKey = resolveSecret(current.Stage1Primary.APIKey, incoming.Stage1Primary.APIKey)
	incoming.Stage1Fallback.APIKey = resolveSecret(current.Stage1Fallback.APIKey, incoming.Stage1Fallback.APIKey)
	incoming.Stage2Primary.APIKey = resolveSecret(current.Stage2Primary.APIKey, incoming.Stage2Primary.APIKey)
	incoming.Stage2Fallback.APIKey = resolveSecret(current.Stage2Fallback.APIKey, incoming.Stage2Fallback.APIKey)
	return Normalize(incoming)
}

func resolveSecret(existing, sent string) string {
	trimmed := strings.TrimSpace(sent)
	if trimmed == SecretMask || trimmed == "" && existing != "" && sent == SecretMask {
		return existing
	}
	if sent == "" {
		return ""
	}
	return trimmed
}

// ResolveTestSlot fills in preserved secret if the incoming test request sent SecretMask.
func ResolveTestSlot(slotName string, sent ProviderSlot, current RuntimeConfig) ProviderSlot {
	var existing string
	switch slotName {
	case "stage1_primary":
		existing = current.Stage1Primary.APIKey
	case "stage1_fallback":
		existing = current.Stage1Fallback.APIKey
	case "stage2_primary":
		existing = current.Stage2Primary.APIKey
	case "stage2_fallback":
		existing = current.Stage2Fallback.APIKey
	}
	sent.APIKey = resolveSecret(existing, sent.APIKey)
	return normalizeSlot(sent, 5.0, 60.0)
}

// LoadFromDisk reads persisted runtime settings from <dataDir>/settings.json if present,
// otherwise returns manifest baseline. Note: PageSetup is always taken from manifest.
func LoadFromDisk(dataDir string, manifest ManifestSettings) (RuntimeConfig, error) {
	base := FromManifest(manifest)
	if strings.TrimSpace(dataDir) == "" {
		return base, nil
	}
	path := filepath.Join(dataDir, "settings.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return base, nil
		}
		return base, fmt.Errorf("read %s: %w", path, err)
	}
	var saved RuntimeConfig
	if err := json.Unmarshal(raw, &saved); err != nil {
		return base, fmt.Errorf("parse %s: %w", path, err)
	}
	saved.PageSetup = bool(manifest.PageSetup)
	return Normalize(saved), nil
}

// SaveToDisk atomically writes RuntimeConfig to <dataDir>/settings.json with 0600 permissions.
func SaveToDisk(dataDir string, cfg RuntimeConfig) error {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, "settings.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
