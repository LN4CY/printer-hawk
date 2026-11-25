// Package hawk orchestrates multi-printer layer tracking, frame acquisition, dual-AI checks,
// rolling frame history, native Prusa Connect failure events, and optional MQTT alerts.
package hawk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/busapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/event"
	"github.com/prusa3d/prusa-connect/sdk/storageapi"

	"github.com/ln4cy/printer-hawk/internal/config"
	"github.com/ln4cy/printer-hawk/internal/mqtt"
	"github.com/ln4cy/printer-hawk/internal/tracker"
	"github.com/ln4cy/printer-hawk/internal/vision"
)

const (
	verbPause         = "core.command.pause_print"
	verbStop          = "core.command.stop_print"
	maxFrameBytes     = 8 << 20
	staleSnapshotAge  = 3 * time.Minute
	liveCacheTTL      = time.Second
	maxHistoryRecords = 120
)

// Devices is the subset of deviceapi.Client used by Printer Hawk.
type Devices interface {
	List(ctx context.Context, params deviceapi.ListParams) (deviceapi.Page, error)
	Get(ctx context.Context, params deviceapi.GetParams) (deviceapi.Device, error)
	Command(ctx context.Context, params deviceapi.CommandParams) (deviceapi.Result, error)
}

// Storage is the subset of storageapi.Client used to read Prusa Connect snapshots.
type Storage interface {
	Read(ctx context.Context, params storageapi.ReadParams) (storageapi.Chunk, error)
}

// Emitter publishes extension events on Prusa Connect's event stream.
type Emitter interface {
	Emit(ctx context.Context, endpoint, key string, data any) error
}

// Subscriber opens Prusa Connect's event stream.
type Subscriber func(ctx context.Context, params busapi.SubscribeParams, onWindow func(busapi.Window) error) error

// SnapshotEvent matches com.prusa.connect.camera.snapshot.captured event payloads.
type SnapshotEvent struct {
	Camera  string    `json:"camera"`
	Printer string    `json:"printer,omitempty"`
	Storage string    `json:"storage"`
	Path    string    `json:"path"`
	Bytes   int64     `json:"bytes"`
	Taken   time.Time `json:"taken"`
}

// CheckRecord records one Stage 1 (+ optional Stage 2) vision evaluation.
type CheckRecord struct {
	ID          string              `json:"id"`
	Timestamp   string              `json:"ts"`
	PrinterID   string              `json:"printer_id"`
	PrinterName string              `json:"printer_name"`
	Trigger     string              `json:"trigger"`
	ImageSource string              `json:"image_source"`
	FrameID     string              `json:"frame_id,omitempty"`
	Z           *float64            `json:"z,omitempty"`
	Layer       int                 `json:"layer"`
	State       string              `json:"printer_state"`
	Stage1      vision.StageResult  `json:"stage1"`
	Stage2      *vision.StageResult `json:"stage2,omitempty"`
	Action      string              `json:"action"`
	DryRun      bool                `json:"dry_run"`
	PauseError  string              `json:"pause_error,omitempty"`
}

// PrinterSummary is the per-printer live view serialized by GET /status.
type PrinterSummary struct {
	ID                  string               `json:"id"`
	Name                string               `json:"name"`
	Model               string               `json:"model"`
	Connected           bool                 `json:"connected"`
	State               string               `json:"state"`
	Z                   *float64             `json:"z,omitempty"`
	BedTemp             *float64             `json:"bed_temp,omitempty"`
	BedTarget           *float64             `json:"bed_target,omitempty"`
	NozzleTemp          *float64             `json:"nozzle_temp,omitempty"`
	NozzleTarget        *float64             `json:"nozzle_target,omitempty"`
	JobProgress         *float64             `json:"job_progress,omitempty"`
	JobPath             string               `json:"job_path,omitempty"`
	Config              config.PrinterConfig `json:"config"`
	Tracker             tracker.Snapshot     `json:"tracker"`
	AlertActive         bool                 `json:"alert_active"`
	PausedForJob        bool                 `json:"paused_for_job"`
	ConsecutiveWarnings int                  `json:"consecutive_warnings"`
	LastError           string               `json:"last_error,omitempty"`
	HasLiveSource       bool                 `json:"has_live_source"`
	HasAnalyzedFrame    bool                 `json:"has_analyzed_frame"`
	LastCheck           *CheckRecord         `json:"last_check,omitempty"`
}

// StatusPayload is returned by GET /status.
type StatusPayload struct {
	Service        string               `json:"service"`
	PageSetup      bool                 `json:"page_setup"`
	MQTTEnabled    bool                 `json:"mqtt_enabled"`
	MQTTLastError  string               `json:"mqtt_last_error,omitempty"`
	Stage1Primary  config.ProviderSlot  `json:"stage1_primary"`
	Stage1Fallback config.ProviderSlot  `json:"stage1_fallback"`
	Stage2Primary  config.ProviderSlot  `json:"stage2_primary"`
	Stage2Fallback config.ProviderSlot  `json:"stage2_fallback"`
	Printers       []PrinterSummary     `json:"printers"`
	History        []CheckRecord        `json:"history"`
}

type printerState struct {
	info                deviceapi.Device
	telemetry           vision.Telemetry
	tracker             *tracker.LayerTracker
	lastShot            SnapshotEvent
	prevJPG             []byte
	refJPG              []byte
	lastAnalyzedJPG     []byte
	lastFrameSHA        string
	liveJPG             []byte
	liveSource          string
	liveAt              time.Time
	alertActive         bool
	pausedForJob        bool
	consecutiveWarnings int
	lastError           string
	lastCheck           *CheckRecord
	lastMQTTPrinting    *bool
}

// Service coordinates multi-printer monitoring, AI checks, frame caching, and persistence.
type Service struct {
	devices   Devices
	storage   Storage
	emitter   Emitter
	subscribe Subscriber
	log       *slog.Logger
	dataDir   string
	now       func() time.Time
	clockMono func() float64
	httpCli   *http.Client
	mqttCli   *mqtt.Client

	mu       sync.Mutex
	cfg      config.RuntimeConfig
	printers map[string]*printerState
	history  []CheckRecord
	cursor   string
}

// New builds a Printer Hawk service. Call Configure before Run.
func New(
	devices Devices,
	storage Storage,
	emitter Emitter,
	subscribe Subscriber,
	dataDir string,
	log *slog.Logger,
) *Service {
	if log == nil {
		log = slog.Default()
	}
	start := time.Now()
	s := &Service{
		devices:   devices,
		storage:   storage,
		emitter:   emitter,
		subscribe: subscribe,
		log:       log,
		dataDir:   dataDir,
		now:       time.Now,
		clockMono: func() float64 { return time.Since(start).Seconds() },
		httpCli:   &http.Client{Timeout: 6 * time.Second},
		mqttCli:   mqtt.New("", 1883, ""),
		printers:  map[string]*printerState{},
	}
	s.loadHistory()
	return s
}

// Configure updates runtime settings and adjusts per-printer trackers.
func (s *Service) Configure(cfg config.RuntimeConfig) {
	cfg = config.Normalize(cfg)
	s.mqttCli.Update(cfg.MQTT.Host, cfg.MQTT.Port, cfg.MQTT.Prefix)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	for id, ps := range s.printers {
		pc := cfg.EffectivePrinter(id)
		ps.tracker = tracker.New(trackerParamsFromConfig(pc))
	}
}

// Config returns the current redacted configuration for reads.
func (s *Service) Config() config.RuntimeConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Redact()
}

// RawConfig returns the unredacted configuration for internal secret resolution/testing.
func (s *Service) RawConfig() config.RuntimeConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// UpdateConfig merges incoming settings, persists them to settings.json, and applies them live.
func (s *Service) UpdateConfig(incoming config.RuntimeConfig) (config.RuntimeConfig, error) {
	s.mu.Lock()
	merged := config.MergeUpdate(s.cfg, incoming)
	s.mu.Unlock()

	if err := config.SaveToDisk(s.dataDir, merged); err != nil {
		return config.RuntimeConfig{}, fmt.Errorf("save settings: %w", err)
	}
	s.Configure(merged)
	return merged.Redact(), nil
}

// Run starts the event stream consumer and periodic Z/timer fallback polling until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.syncFleet(ctx)

	go s.runPollLoop(ctx)

	if s.subscribe == nil {
		<-ctx.Done()
		return
	}

	params := busapi.SubscribeParams{
		Types: []string{
			event.TypeCameraSnapshotCaptured,
			event.TypeDeviceStateChanged,
			event.TypeDevicePresenceChanged,
			event.TypeDeviceTelemetrySampled,
			event.TypeDeviceRecordChanged,
			event.TypeDeviceArrived,
		},
	}

	for wait := time.Second; ctx.Err() == nil; {
		s.mu.Lock()
		params.Resume = s.cursor
		s.mu.Unlock()

		read := false
		err := s.subscribe(ctx, params, func(w busapi.Window) error {
			read = true
			for _, raw := range w.Events {
				s.handleEvent(ctx, raw)
			}
			if w.Cursor != "" {
				s.mu.Lock()
				s.cursor = w.Cursor
				s.mu.Unlock()
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		if read {
			wait = time.Second
		}
		s.log.WarnContext(ctx, "event stream ended; reconnecting", "error", err, "wait", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

func (s *Service) runPollLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick(ctx)
		}
	}
}

// Tick polls all known devices once, handles offline/reset transitions, and evaluates Z/timer triggers.
func (s *Service) Tick(ctx context.Context) {
	s.syncFleet(ctx)

	s.mu.Lock()
	ids := slices.Sorted(maps.Keys(s.printers))
	cfg := s.cfg
	s.mu.Unlock()

	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		pc := cfg.EffectivePrinter(id)
		s.tickPrinter(ctx, id, pc, cfg)
	}
}

func (s *Service) syncFleet(ctx context.Context) {
	if s.devices == nil {
		return
	}
	res, err := s.devices.List(ctx, deviceapi.ListParams{Limit: deviceapi.MaxPageLimit})
	if err != nil {
		s.log.DebugContext(ctx, "device.list skipped", "error", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, dev := range res.Items {
		if dev.Retired {
			delete(s.printers, dev.ID)
			continue
		}
		s.upsertDeviceLocked(dev)
	}
}

func (s *Service) upsertDeviceLocked(dev deviceapi.Device) *printerState {
	ps, ok := s.printers[dev.ID]
	if !ok {
		pc := s.cfg.EffectivePrinter(dev.ID)
		ps = &printerState{
			tracker: tracker.New(trackerParamsFromConfig(pc)),
		}
		s.printers[dev.ID] = ps
	}
	ps.info = dev
	ps.telemetry = ParseTelemetry(dev)
	return ps
}

func (s *Service) tickPrinter(ctx context.Context, id string, pc config.PrinterConfig, cfg config.RuntimeConfig) {
	dev, err := s.devices.Get(ctx, deviceapi.GetParams{ID: id})
	if err != nil {
		s.mu.Lock()
		if ps, ok := s.printers[id]; ok {
			ps.lastError = err.Error()
		}
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	if dev.Retired {
		delete(s.printers, id)
		s.mu.Unlock()
		return
	}
	ps := s.upsertDeviceLocked(dev)
	tele := ps.telemetry

	if !dev.Connected {
		wasPrinting := ps.lastMQTTPrinting != nil && *ps.lastMQTTPrinting
		hadAlert := ps.alertActive
		if ps.tracker.Snapshot(s.clockMono()).Active || ps.pausedForJob {
			ps.tracker.Reset()
			ps.prevJPG = nil
			ps.refJPG = nil
			ps.lastFrameSHA = ""
			ps.consecutiveWarnings = 0
			ps.pausedForJob = false
		}
		ps.alertActive = false
		f := false
		ps.lastMQTTPrinting = &f
		s.mu.Unlock()
		if wasPrinting || hadAlert {
			_ = s.mqttCli.Publish(id+"/printing", []byte("OFF"), true)
			_ = s.mqttCli.Publish(id+"/alert", []byte("OFF"), true)
		}
		return
	}

	isPrinting := tele.State == "PRINTING"
	isPaused := tele.State == "PAUSED" || tele.State == "ATTENTION"
	publishPrinting := ps.lastMQTTPrinting == nil || *ps.lastMQTTPrinting != isPrinting
	ps.lastMQTTPrinting = &isPrinting

	if !isPrinting {
		if !isPaused && ps.tracker.Snapshot(s.clockMono()).Active {
			ps.tracker.Reset()
			ps.prevJPG = nil
			ps.refJPG = nil
			ps.lastFrameSHA = ""
			ps.consecutiveWarnings = 0
			ps.pausedForJob = false
		}
		s.mu.Unlock()
		if publishPrinting {
			_ = s.mqttCli.Publish(id+"/printing", boolPayload(isPrinting), true)
		}
		return
	}

	if !pc.Enabled || ps.pausedForJob {
		s.mu.Unlock()
		if publishPrinting {
			_ = s.mqttCli.Publish(id+"/printing", []byte("ON"), true)
		}
		return
	}

	nowMono := s.clockMono()
	trig := ps.tracker.UpdateZ(nowMono, tele.Z)
	if trig == nil {
		trig = ps.tracker.CheckTimerFallback(nowMono)
	}
	s.mu.Unlock()

	if publishPrinting {
		_ = s.mqttCli.Publish(id+"/printing", []byte("ON"), true)
	}
	if trig != nil {
		if _, err := s.runInspection(ctx, id, cfg, pc, *trig, true, false); err != nil {
			s.setPrinterError(id, err.Error())
			s.log.WarnContext(ctx, "inspection failed", "printer", id, "trigger", trig.Source, "error", err)
		}
	}
}

func (s *Service) handleEvent(ctx context.Context, raw json.RawMessage) {
	var env event.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	switch env.Type {
	case event.TypeCameraSnapshotCaptured:
		var shot SnapshotEvent
		if err := json.Unmarshal(env.Data, &shot); err != nil || shot.Printer == "" || shot.Path == "" {
			return
		}
		if !shot.Taken.IsZero() && s.now().Sub(shot.Taken) > staleSnapshotAge {
			return
		}
		s.mu.Lock()
		if ps, ok := s.printers[shot.Printer]; ok {
			ps.lastShot = shot
		}
		s.mu.Unlock()

	case event.TypeDeviceStateChanged, event.TypeDevicePresenceChanged, event.TypeDeviceArrived, event.TypeDeviceRecordChanged:
		if env.Subject == "" {
			return
		}
		dev, err := s.devices.Get(ctx, deviceapi.GetParams{ID: env.Subject})
		if err == nil {
			s.mu.Lock()
			if dev.Retired {
				delete(s.printers, dev.ID)
			} else {
				s.upsertDeviceLocked(dev)
			}
			s.mu.Unlock()
		}
	}
}

// Inspect executes an on-demand dry-run check for a printer (never sends a pause command).
func (s *Service) Inspect(ctx context.Context, printerID string, verify bool) (CheckRecord, error) {
	if s.devices != nil {
		if dev, err := s.devices.Get(ctx, deviceapi.GetParams{ID: printerID}); err == nil {
			s.mu.Lock()
			s.upsertDeviceLocked(dev)
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	ps, ok := s.printers[printerID]
	if !ok {
		s.mu.Unlock()
		return CheckRecord{}, fmt.Errorf("unknown printer %q", printerID)
	}
	layer := ps.tracker.LayerCount()
	z := ps.telemetry.Z
	cfg := s.cfg
	pc := cfg.EffectivePrinter(printerID)
	s.mu.Unlock()

	trig := tracker.Trigger{Source: "manual_inspect", Z: z, Layer: layer}
	return s.runInspection(ctx, printerID, cfg, pc, trig, false, verify)
}

// FalseAlarm clears active alerts/latches AND promotes the latest analyzed frame as the new clean baseline.
func (s *Service) FalseAlarm(printerID string) (bool, error) {
	s.mu.Lock()
	ps, ok := s.printers[printerID]
	if !ok {
		s.mu.Unlock()
		return false, fmt.Errorf("unknown printer %q", printerID)
	}
	ps.alertActive = false
	ps.pausedForJob = false
	ps.consecutiveWarnings = 0
	if len(ps.lastAnalyzedJPG) > 0 {
		ps.refJPG = slices.Clone(ps.lastAnalyzedJPG)
		ps.prevJPG = slices.Clone(ps.lastAnalyzedJPG)
	}
	ps.tracker.MarkChecked(s.clockMono())
	s.mu.Unlock()

	_ = s.mqttCli.Publish(printerID+"/alert", []byte("OFF"), true)
	return true, nil
}

// ClearAlert clears any active alert and pause latch for a printer.
func (s *Service) ClearAlert(printerID string) error {
	s.mu.Lock()
	ps, ok := s.printers[printerID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown printer %q", printerID)
	}
	ps.alertActive = false
	ps.pausedForJob = false
	ps.consecutiveWarnings = 0
	s.mu.Unlock()

	_ = s.mqttCli.Publish(printerID+"/alert", []byte("OFF"), true)
	return nil
}

// Pause sends core.command.pause_print to the printer.
func (s *Service) Pause(ctx context.Context, printerID string) (deviceapi.Result, error) {
	res, err := s.devices.Command(ctx, deviceapi.CommandParams{ID: printerID, Verb: verbPause})
	if err != nil {
		return deviceapi.Result{}, err
	}
	if !res.OK {
		return res, errors.New(nonEmptyStr(res.MachineReason, "printer refused pause command"))
	}
	s.mu.Lock()
	if ps, ok := s.printers[printerID]; ok {
		ps.pausedForJob = true
	}
	s.mu.Unlock()
	return res, nil
}

// Stop sends core.command.stop_print to the printer and clears active alert state.
func (s *Service) Stop(ctx context.Context, printerID string) (deviceapi.Result, error) {
	res, err := s.devices.Command(ctx, deviceapi.CommandParams{ID: printerID, Verb: verbStop})
	if err != nil {
		return deviceapi.Result{}, err
	}
	if !res.OK {
		return res, errors.New(nonEmptyStr(res.MachineReason, "printer refused stop command"))
	}
	_ = s.ClearAlert(printerID)
	return res, nil
}

// LiveJPEG returns a briefly cached live JPEG for one printer along with its source label.
func (s *Service) LiveJPEG(ctx context.Context, printerID string) ([]byte, string, error) {
	s.mu.Lock()
	ps, ok := s.printers[printerID]
	if !ok {
		s.mu.Unlock()
		return nil, "", fmt.Errorf("unknown printer %q", printerID)
	}
	if len(ps.liveJPG) > 0 && s.now().Sub(ps.liveAt) < liveCacheTTL {
		jpg := slices.Clone(ps.liveJPG)
		src := ps.liveSource
		s.mu.Unlock()
		return jpg, src, nil
	}
	pc := s.cfg.EffectivePrinter(printerID)
	shot := ps.lastShot
	s.mu.Unlock()

	jpg, _, src, err := s.fetchBestFrame(ctx, pc, shot)
	if err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	if cur, ok := s.printers[printerID]; ok {
		cur.liveJPG = slices.Clone(jpg)
		cur.liveSource = src
		cur.liveAt = s.now()
	}
	s.mu.Unlock()
	return jpg, src, nil
}

// StoredFrame reads one historical check JPEG stored on disk for a printer.
func (s *Service) StoredFrame(printerID, frameID string) ([]byte, error) {
	if s.dataDir == "" || !safeID(printerID) || !safeID(frameID) {
		return nil, os.ErrNotExist
	}
	path := filepath.Join(s.dataDir, "frames", printerID, frameID+".jpg")
	return os.ReadFile(path)
}

// Status returns the complete redacted fleet + history snapshot for GET /status.
func (s *Service) Status(printerFilter string) StatusPayload {
	s.mu.Lock()
	defer s.mu.Unlock()

	nowMono := s.clockMono()
	redacted := s.cfg.Redact()
	ids := slices.Sorted(maps.Keys(s.printers))
	printers := make([]PrinterSummary, 0, len(ids))

	for _, id := range ids {
		if printerFilter != "" && id != printerFilter {
			continue
		}
		ps := s.printers[id]
		pc := s.cfg.EffectivePrinter(id)
		tele := ps.telemetry
		summary := PrinterSummary{
			ID:                  id,
			Name:                nonEmptyStr(ps.info.Name, id),
			Model:               ps.info.Model,
			Connected:           ps.info.Connected,
			State:               tele.State,
			Z:                   tele.Z,
			BedTemp:             tele.BedTemp,
			BedTarget:           tele.BedTarget,
			NozzleTemp:          tele.NozzleTemp,
			NozzleTarget:        tele.NozzleTarget,
			JobProgress:         tele.JobProgress,
			JobPath:             tele.JobPath,
			Config:              pc,
			Tracker:             ps.tracker.Snapshot(nowMono),
			AlertActive:         ps.alertActive,
			PausedForJob:        ps.pausedForJob,
			ConsecutiveWarnings: ps.consecutiveWarnings,
			LastError:           ps.lastError,
			HasLiveSource:       pc.LiveFrameURL != "" || ps.lastShot.Path != "",
			HasAnalyzedFrame:    len(ps.lastAnalyzedJPG) > 0,
			LastCheck:           ps.lastCheck,
		}
		printers = append(printers, summary)
	}

	hist := make([]CheckRecord, 0, len(s.history))
	for i := len(s.history) - 1; i >= 0; i-- {
		rec := s.history[i]
		if printerFilter != "" && rec.PrinterID != printerFilter {
			continue
		}
		hist = append(hist, rec)
		if len(hist) >= 30 {
			break
		}
	}

	return StatusPayload{
		Service:        "printer-hawk",
		PageSetup:      redacted.PageSetup,
		MQTTEnabled:    s.mqttCli.Enabled(),
		MQTTLastError:  s.mqttCli.LastError(),
		Stage1Primary:  redacted.Stage1Primary,
		Stage1Fallback: redacted.Stage1Fallback,
		Stage2Primary:  redacted.Stage2Primary,
		Stage2Fallback: redacted.Stage2Fallback,
		Printers:       printers,
		History:        hist,
	}
}

func (s *Service) runInspection(
	ctx context.Context,
	printerID string,
	cfg config.RuntimeConfig,
	pc config.PrinterConfig,
	trig tracker.Trigger,
	allowAction bool,
	forceStage2 bool,
) (CheckRecord, error) {
	s.mu.Lock()
	ps, ok := s.printers[printerID]
	if !ok {
		s.mu.Unlock()
		return CheckRecord{}, fmt.Errorf("unknown printer %q", printerID)
	}
	shot := ps.lastShot
	tele := ps.telemetry
	prev := slices.Clone(ps.prevJPG)
	ref := slices.Clone(ps.refJPG)
	s.mu.Unlock()

	rawJPG, sha, imgSource, err := s.fetchBestFrame(ctx, pc, shot)
	if err != nil {
		return CheckRecord{}, err
	}
	resized, err := vision.ResizeJPEG(rawJPG, cfg.MaxImageWidth, cfg.JPEGQuality)
	if err != nil {
		return CheckRecord{}, err
	}

	promptCtx := vision.BuildContextPrompt(tele, trig.Source, trig.Layer, pc.CustomPromptNotes)
	s1, err := vision.RunStage1(ctx, cfg.Stage1Primary, cfg.Stage1Fallback, resized, prev, promptCtx)
	if err != nil {
		return CheckRecord{}, err
	}

	s.mu.Lock()
	ps = s.printers[printerID]
	if ps == nil {
		s.mu.Unlock()
		return CheckRecord{}, fmt.Errorf("printer %q removed", printerID)
	}
	ps.lastFrameSHA = sha
	ps.lastAnalyzedJPG = resized
	ps.lastError = ""

	switch s1.Status {
	case "warning":
		ps.consecutiveWarnings++
	case "ok":
		ps.consecutiveWarnings = 0
	}
	escalate := forceStage2 ||
		(s1.Status == "failure" && s1.Confidence >= pc.Stage1FailThreshold) ||
		(ps.consecutiveWarnings >= pc.Stage1WarnEscalateCount)
	s.mu.Unlock()

	var s2 *vision.StageResult
	action := "none"
	pauseErr := ""
	confirmedFailure := false

	if escalate && (slotReady(cfg.Stage2Primary) || slotReady(cfg.Stage2Fallback)) {
		res2, err := vision.RunStage2(ctx, cfg.Stage2Primary, cfg.Stage2Fallback, resized, prev, ref, promptCtx, s1)
		if err != nil {
			return CheckRecord{}, err
		}
		s2 = &res2
		confirmedFailure = res2.ConfirmPause != nil && *res2.ConfirmPause &&
			res2.Status == "failure" && res2.Confidence >= pc.Stage2PauseThreshold
	} else if !escalate && s1.Status == "ok" {
		s.updateCleanFrames(printerID, resized)
	} else if escalate && !slotReady(cfg.Stage2Primary) && !slotReady(cfg.Stage2Fallback) {
		// Single-stage mode when no Stage 2 provider is configured.
		confirmedFailure = s1.Status == "failure" && s1.Confidence >= pc.Stage1FailThreshold
	}

	if s2 != nil && !confirmedFailure && s1.Status == "ok" {
		s.updateCleanFrames(printerID, resized)
	}

	if confirmedFailure {
		if allowAction {
			s.setPrinterAlert(printerID, true)
			_ = s.mqttCli.Publish(printerID+"/alert", []byte("ON"), true)
			if pc.Action == config.ActionPause && s.devices != nil {
				res, cmdErr := s.devices.Command(ctx, deviceapi.CommandParams{ID: printerID, Verb: verbPause})
				switch {
				case cmdErr != nil:
					action = "pause_failed"
					pauseErr = cmdErr.Error()
				case !res.OK:
					action = "pause_refused"
					pauseErr = res.MachineReason
				default:
					action = "paused"
					s.setPrinterPaused(printerID, true)
				}
			} else {
				action = "notified"
			}
		} else {
			action = "dry_run_would_" + pc.Action
		}
	}

	nowUTC := s.now().UTC()
	frameID := fmt.Sprintf("%d", nowUTC.UnixMilli())
	s.saveFrame(printerID, frameID, resized, cfg.MaxHistoryFrames)

	rec := CheckRecord{
		ID:          frameID,
		Timestamp:   nowUTC.Format(time.RFC3339),
		PrinterID:   printerID,
		PrinterName: nonEmptyStr(tele.PrinterName, printerID),
		Trigger:     trig.Source,
		ImageSource: imgSource,
		FrameID:     frameID,
		Z:           tele.Z,
		Layer:       trig.Layer,
		State:       tele.State,
		Stage1:      s1,
		Stage2:      s2,
		Action:      action,
		DryRun:      !allowAction,
		PauseError:  pauseErr,
	}
	s.appendRecord(printerID, rec)

	_ = s.mqttCli.Publish(printerID+"/status", mustJSON(rec), true)
	if confirmedFailure && allowAction && s.emitter != nil {
		_ = s.emitter.Emit(ctx, "failure", printerID, rec)
	}
	return rec, nil
}

func (s *Service) fetchBestFrame(ctx context.Context, pc config.PrinterConfig, shot SnapshotEvent) ([]byte, string, string, error) {
	if pc.CameraMode == config.CameraModeURL && pc.LiveFrameURL != "" {
		if raw, err := s.fetchHTTPJPEG(ctx, pc.LiveFrameURL); err == nil {
			sum := sha256.Sum256(raw)
			return raw, hex.EncodeToString(sum[:]), "live_url", nil
		} else if shot.Path == "" {
			return nil, "", "", fmt.Errorf("fetch live_frame_url: %w", err)
		}
	}
	if shot.Path != "" && s.storage != nil {
		raw, err := s.readStorageJPEG(ctx, shot)
		if err == nil {
			sum := sha256.Sum256(raw)
			return raw, hex.EncodeToString(sum[:]), "connect_snapshot", nil
		}
		if pc.LiveFrameURL != "" {
			if httpRaw, httpErr := s.fetchHTTPJPEG(ctx, pc.LiveFrameURL); httpErr == nil {
				sum := sha256.Sum256(httpRaw)
				return httpRaw, hex.EncodeToString(sum[:]), "live_url", nil
			}
		}
		return nil, "", "", err
	}
	if pc.LiveFrameURL != "" {
		raw, err := s.fetchHTTPJPEG(ctx, pc.LiveFrameURL)
		if err != nil {
			return nil, "", "", err
		}
		sum := sha256.Sum256(raw)
		return raw, hex.EncodeToString(sum[:]), "live_url", nil
	}
	return nil, "", "", errors.New("no camera snapshot captured yet and no live_frame_url configured")
}

func (s *Service) fetchHTTPJPEG(ctx context.Context, targetURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpCli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, targetURL)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFrameBytes))
	if err != nil {
		return nil, err
	}
	if len(raw) < 3 || raw[0] != 0xFF || raw[1] != 0xD8 || raw[2] != 0xFF {
		return nil, fmt.Errorf("URL %s did not return a JPEG", targetURL)
	}
	return raw, nil
}

func (s *Service) readStorageJPEG(ctx context.Context, shot SnapshotEvent) ([]byte, error) {
	var frame []byte
	for {
		chunk, err := s.storage.Read(ctx, storageapi.ReadParams{
			Storage: shot.Storage,
			Path:    shot.Path,
			Offset:  int64(len(frame)),
			Length:  storageapi.MaxReadBytes,
		})
		if err != nil {
			return nil, fmt.Errorf("read %s/%s: %w", shot.Storage, shot.Path, err)
		}
		frame = append(frame, chunk.Data...)
		if len(frame) > maxFrameBytes {
			return nil, fmt.Errorf("snapshot exceeds %d bytes", maxFrameBytes)
		}
		if chunk.EOF || len(chunk.Data) == 0 {
			return frame, nil
		}
	}
}

func (s *Service) updateCleanFrames(printerID string, resized []byte) {
	s.mu.Lock()
	if ps, ok := s.printers[printerID]; ok {
		if len(ps.refJPG) == 0 {
			ps.refJPG = slices.Clone(resized)
		}
		ps.prevJPG = slices.Clone(resized)
	}
	s.mu.Unlock()
}

func (s *Service) setPrinterAlert(printerID string, active bool) {
	s.mu.Lock()
	if ps, ok := s.printers[printerID]; ok {
		ps.alertActive = active
	}
	s.mu.Unlock()
}

func (s *Service) setPrinterPaused(printerID string, paused bool) {
	s.mu.Lock()
	if ps, ok := s.printers[printerID]; ok {
		ps.pausedForJob = paused
	}
	s.mu.Unlock()
}

func (s *Service) setPrinterError(printerID, msg string) {
	s.mu.Lock()
	if ps, ok := s.printers[printerID]; ok {
		ps.lastError = msg
	}
	s.mu.Unlock()
}

func (s *Service) appendRecord(printerID string, rec CheckRecord) {
	s.mu.Lock()
	if ps, ok := s.printers[printerID]; ok {
		copyRec := rec
		ps.lastCheck = &copyRec
	}
	s.history = append(s.history, rec)
	if len(s.history) > maxHistoryRecords {
		s.history = slices.Clone(s.history[len(s.history)-maxHistoryRecords:])
	}
	snapshot := slices.Clone(s.history)
	s.mu.Unlock()

	if s.dataDir == "" {
		return
	}
	_ = os.MkdirAll(s.dataDir, 0o750)
	if raw, err := json.MarshalIndent(snapshot, "", "  "); err == nil {
		tmp := filepath.Join(s.dataDir, "history.json.tmp")
		dst := filepath.Join(s.dataDir, "history.json")
		if os.WriteFile(tmp, raw, 0o600) == nil {
			_ = os.Rename(tmp, dst)
		}
	}
}

func (s *Service) loadHistory() {
	if s.dataDir == "" {
		return
	}
	raw, err := os.ReadFile(filepath.Join(s.dataDir, "history.json"))
	if err != nil {
		return
	}
	var loaded []CheckRecord
	if json.Unmarshal(raw, &loaded) == nil {
		if len(loaded) > maxHistoryRecords {
			loaded = loaded[len(loaded)-maxHistoryRecords:]
		}
		s.history = loaded
	}
}

func (s *Service) saveFrame(printerID, frameID string, jpg []byte, maxKeep int) {
	if s.dataDir == "" || !safeID(printerID) || !safeID(frameID) {
		return
	}
	dir := filepath.Join(s.dataDir, "frames", printerID)
	if os.MkdirAll(dir, 0o750) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, frameID+".jpg"), jpg, 0o600)
	if maxKeep <= 0 {
		maxKeep = 20
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= maxKeep {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jpg") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	for len(names) > maxKeep {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// ParseTelemetry converts deviceapi.Device flat state paths into vision.Telemetry,
// zeroing stale measurements when the printer is offline.
func ParseTelemetry(dev deviceapi.Device) vision.Telemetry {
	out := vision.Telemetry{
		PrinterID:   dev.ID,
		PrinterName: nonEmptyStr(dev.Name, dev.ID),
		Model:       dev.Model,
		State:       "OFFLINE",
	}
	if !dev.Connected {
		zero := 0.0
		out.BedTemp = &zero
		out.BedTarget = &zero
		out.NozzleTemp = &zero
		out.NozzleTarget = &zero
		out.JobProgress = &zero
		return out
	}

	out.State = strings.ToUpper(nonEmptyStr(stateString(dev.State, "/state"), "UNKNOWN"))
	out.Z = stateFloat(dev.State, "/axis/z")
	out.BedTemp = stateFloat(dev.State, "/temperature/bed")
	out.BedTarget = stateFloat(dev.State, "/temperature/bed_target")
	out.JobProgress = stateFloat(dev.State, "/job/progress")
	out.JobPath = stateString(dev.State, "/job/path")

	for idx := range 16 {
		prefix := "/tools/" + strconv.Itoa(idx)
		if b, ok := stateBool(dev.State, prefix+"/active"); ok && b {
			tool := idx
			out.ActiveTool = &tool
			out.NozzleTemp = stateFloat(dev.State, prefix+"/nozzle_temp")
			out.NozzleTarget = stateFloat(dev.State, prefix+"/nozzle_target_temp")
			out.Material = stateString(dev.State, prefix+"/material")
			break
		}
	}
	if out.NozzleTemp == nil {
		out.NozzleTemp = stateFloat(dev.State, "/tools/0/nozzle_temp")
		out.NozzleTarget = stateFloat(dev.State, "/tools/0/nozzle_target_temp")
	}
	return out
}

func stateString(m map[string]json.RawMessage, key string) string {
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func stateFloat(m map[string]json.RawMessage, key string) *float64 {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return &f
	}
	return nil
}

func stateBool(m map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := m[key]
	if !ok {
		return false, false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b, true
	}
	return false, false
}

func trackerParamsFromConfig(pc config.PrinterConfig) tracker.Params {
	return tracker.Params{
		WarmupSeconds:     pc.WarmupSeconds,
		ZMinStepMM:        pc.ZMinStepMM,
		ZMaxStepMM:        pc.ZMaxStepMM,
		ZSettleSeconds:    pc.ZSettleSeconds,
		MinCheckIntervalS: pc.MinCheckIntervalSec,
		FallbackMaxWaitS:  pc.FallbackMaxWaitSec,
		RebaselineDropMM:  1.0,
	}
}

func slotReady(s config.ProviderSlot) bool {
	if s.Provider == "" || s.Provider == config.ProviderNone || s.Model == "" {
		return false
	}
	if (s.Provider == config.ProviderGemini || s.Provider == config.ProviderAnthropic) && s.APIKey == "" {
		return false
	}
	return true
}

func safeID(s string) bool {
	return s != "" && !strings.ContainsAny(s, "/\\..")
}

func nonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func boolPayload(v bool) []byte {
	if v {
		return []byte("ON")
	}
	return []byte("OFF")
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
