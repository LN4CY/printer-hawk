// Package tracker implements per-layer Z tracking with Z-hop filtering,
// downward park/heatup re-baselining, and long-layer timer fallback.
package tracker

import "math"

// Trigger describes why a vision inspection was triggered.
type Trigger struct {
	Source string   `json:"source"`
	Z      *float64 `json:"z,omitempty"`
	Layer  int      `json:"layer"`
}

// Params tunes LayerTracker sensitivity and cadence.
type Params struct {
	WarmupSeconds     float64
	ZMinStepMM        float64
	ZMaxStepMM        float64
	ZSettleSeconds    float64
	MinCheckIntervalS float64
	FallbackMaxWaitS  float64
	RebaselineDropMM  float64
}

// DefaultParams returns balanced defaults for multi-material and fast FFF prints.
func DefaultParams() Params {
	return Params{
		WarmupSeconds:     45.0,
		ZMinStepMM:        0.04,
		ZMaxStepMM:        1.20,
		ZSettleSeconds:    4.0,
		MinCheckIntervalS: 15.0,
		FallbackMaxWaitS:  60.0,
		RebaselineDropMM:  1.0,
	}
}

// Snapshot exposes tracker diagnostics for API/UI status views.
type Snapshot struct {
	Active          bool     `json:"active"`
	ConfirmedZ      *float64 `json:"confirmed_z"`
	CandidateZ      *float64 `json:"candidate_z"`
	LayerCount      int      `json:"layer_count"`
	LastCheckAgoSec *float64 `json:"last_check_ago_sec,omitempty"`
}

// LayerTracker tracks printer Z coordinates and emits settled inspection triggers.
type LayerTracker struct {
	params Params

	printingStartedAt *float64
	confirmedZ        *float64
	candidateZ        *float64
	candidateSince    *float64
	candidateMode     string
	layerCount        int
	lastCheckAt       float64
}

// New creates a LayerTracker with the given parameters (applying defaults for <= 0 fields).
func New(p Params) *LayerTracker {
	def := DefaultParams()
	if p.WarmupSeconds < 0 {
		p.WarmupSeconds = def.WarmupSeconds
	}
	if p.ZMinStepMM <= 0 {
		p.ZMinStepMM = def.ZMinStepMM
	}
	if p.ZMaxStepMM <= 0 {
		p.ZMaxStepMM = def.ZMaxStepMM
	}
	if p.ZSettleSeconds < 0 {
		p.ZSettleSeconds = def.ZSettleSeconds
	}
	if p.MinCheckIntervalS < 0 {
		p.MinCheckIntervalS = def.MinCheckIntervalS
	}
	if p.FallbackMaxWaitS <= 0 {
		p.FallbackMaxWaitS = def.FallbackMaxWaitS
	}
	if p.RebaselineDropMM <= 0 {
		p.RebaselineDropMM = def.RebaselineDropMM
	}
	return &LayerTracker{params: p}
}

// Reset clears per-print layer tracking state.
func (t *LayerTracker) Reset() {
	t.printingStartedAt = nil
	t.confirmedZ = nil
	t.candidateZ = nil
	t.candidateSince = nil
	t.candidateMode = ""
	t.layerCount = 0
	t.lastCheckAt = 0
}

// EnsureStarted marks the print start monotonic timestamp (in seconds) if unset.
func (t *LayerTracker) EnsureStarted(now float64) {
	if t.printingStartedAt == nil {
		v := now
		t.printingStartedAt = &v
	}
}

// Snapshot returns a serializable state view for status endpoints.
func (t *LayerTracker) Snapshot(now float64) Snapshot {
	snap := Snapshot{
		Active:     t.printingStartedAt != nil,
		LayerCount: t.layerCount,
	}
	if t.confirmedZ != nil {
		v := *t.confirmedZ
		snap.ConfirmedZ = &v
	}
	if t.candidateZ != nil {
		v := *t.candidateZ
		snap.CandidateZ = &v
	}
	if t.lastCheckAt > 0 {
		ago := math.Max(0, now-t.lastCheckAt)
		snap.LastCheckAgoSec = &ago
	}
	return snap
}

// LayerCount returns the current tracked layer count.
func (t *LayerTracker) LayerCount() int {
	return t.layerCount
}

// MarkChecked records an external/manual check timestamp.
func (t *LayerTracker) MarkChecked(now float64) {
	t.lastCheckAt = now
}

func (t *LayerTracker) clearCandidate() {
	t.candidateZ = nil
	t.candidateSince = nil
	t.candidateMode = ""
}

func (t *LayerTracker) rateLimited(now float64) bool {
	return t.lastCheckAt > 0 && (now-t.lastCheckAt) < t.params.MinCheckIntervalS
}

func floatPtr(v float64) *float64 {
	return &v
}

// UpdateZ evaluates a Z height reading (in mm) and emits a Trigger when a layer settles.
func (t *LayerTracker) UpdateZ(now float64, z *float64) *Trigger {
	t.EnsureStarted(now)
	if z == nil || t.printingStartedAt == nil {
		return nil
	}
	if (now - *t.printingStartedAt) < t.params.WarmupSeconds {
		return nil
	}

	zv := *z
	// Initial baseline once nozzle is above bed contact (> 0.05 mm).
	if t.confirmedZ == nil {
		if zv <= 0.05 {
			t.clearCandidate()
			return nil
		}
		if t.candidateZ == nil || math.Abs(zv-*t.candidateZ) > 0.01 {
			t.candidateZ = floatPtr(zv)
			t.candidateSince = floatPtr(now)
			return nil
		}
		if t.candidateSince != nil && (now-*t.candidateSince) >= t.params.ZSettleSeconds {
			t.confirmedZ = floatPtr(zv)
			t.layerCount = 1
			t.lastCheckAt = now
			t.clearCandidate()
			return &Trigger{Source: "layer_baseline", Z: floatPtr(zv), Layer: t.layerCount}
		}
		return nil
	}

	delta := zv - *t.confirmedZ

	// Downward re-baseline (e.g. warmup ended at Z=40mm park height before layer 1 at Z=0.2mm).
	if zv > 0.05 && delta < -t.params.RebaselineDropMM {
		if t.candidateMode != "rebaseline" || t.candidateZ == nil || math.Abs(zv-*t.candidateZ) > 0.01 {
			t.candidateMode = "rebaseline"
			t.candidateZ = floatPtr(zv)
			t.candidateSince = floatPtr(now)
			return nil
		}
		if t.candidateSince != nil && (now-*t.candidateSince) >= t.params.ZSettleSeconds {
			oldZ := *t.confirmedZ
			t.confirmedZ = floatPtr(zv)
			t.clearCandidate()
			if oldZ >= 5.0 && zv <= 1.0 {
				t.layerCount = 1
			}
			if t.rateLimited(now) {
				return nil
			}
			t.lastCheckAt = now
			return &Trigger{Source: "layer_rebaseline", Z: floatPtr(zv), Layer: t.layerCount}
		}
		return nil
	}

	// Normal same-layer Z-hop return, flat move, or large toolchange Z-lift.
	if delta < t.params.ZMinStepMM || delta > t.params.ZMaxStepMM {
		t.clearCandidate()
		return nil
	}

	// Potential real layer advance within [ZMinStepMM, ZMaxStepMM].
	if t.candidateMode != "advance" || t.candidateZ == nil || zv < (*t.candidateZ-0.01) || math.Abs(zv-*t.candidateZ) > 0.01 {
		t.candidateMode = "advance"
		t.candidateZ = floatPtr(zv)
		t.candidateSince = floatPtr(now)
		return nil
	}

	if t.candidateSince != nil && (now-*t.candidateSince) >= t.params.ZSettleSeconds {
		if t.rateLimited(now) {
			return nil
		}
		t.confirmedZ = floatPtr(zv)
		t.layerCount++
		t.lastCheckAt = now
		t.clearCandidate()
		return &Trigger{Source: "layer_advance", Z: floatPtr(zv), Layer: t.layerCount}
	}

	return nil
}

// CheckTimerFallback triggers when a long layer exceeds FallbackMaxWaitS without an inspection.
func (t *LayerTracker) CheckTimerFallback(now float64) *Trigger {
	t.EnsureStarted(now)
	if t.printingStartedAt == nil {
		return nil
	}
	if (now - *t.printingStartedAt) < t.params.WarmupSeconds {
		return nil
	}
	baseline := *t.printingStartedAt
	if t.lastCheckAt > 0 {
		baseline = t.lastCheckAt
	}
	if (now - baseline) < t.params.FallbackMaxWaitS {
		return nil
	}
	if t.rateLimited(now) {
		return nil
	}
	t.lastCheckAt = now
	var zCopy *float64
	if t.confirmedZ != nil {
		zCopy = floatPtr(*t.confirmedZ)
	}
	return &Trigger{Source: "timer_fallback", Z: zCopy, Layer: t.layerCount}
}
