package tracker

import (
	"encoding/json"
	"strings"
	"testing"
)

func fptr(v float64) *float64 { return &v }

func TestIgnoresZHopAndLargeLiftTriggersOnSettledLayer(t *testing.T) {
	tr := New(Params{
		WarmupSeconds:     10,
		ZMinStepMM:        0.04,
		ZMaxStepMM:        1.20,
		ZSettleSeconds:    4,
		MinCheckIntervalS: 15,
		FallbackMaxWaitS:  60,
		RebaselineDropMM:  1.0,
	})

	if trig := tr.UpdateZ(0, fptr(0.20)); trig != nil {
		t.Fatalf("expected nil during warmup, got %+v", trig)
	}
	if trig := tr.UpdateZ(5, fptr(0.20)); trig != nil {
		t.Fatalf("expected nil during warmup, got %+v", trig)
	}
	if trig := tr.UpdateZ(11, fptr(0.20)); trig != nil {
		t.Fatalf("expected nil while settling baseline, got %+v", trig)
	}
	trig1 := tr.UpdateZ(15.5, fptr(0.20))
	if trig1 == nil || trig1.Source != "layer_baseline" || trig1.Layer != 1 {
		t.Fatalf("expected layer_baseline layer 1, got %+v", trig1)
	}

	// Short Z-hop must not trigger
	if trig := tr.UpdateZ(20, fptr(0.60)); trig != nil {
		t.Fatalf("z-hop up should not trigger: %+v", trig)
	}
	if trig := tr.UpdateZ(22, fptr(0.20)); trig != nil {
		t.Fatalf("z-hop return should not trigger: %+v", trig)
	}

	// Large toolchange lift must not trigger
	if trig := tr.UpdateZ(25, fptr(5.00)); trig != nil {
		t.Fatalf("large lift should not trigger: %+v", trig)
	}
	if trig := tr.UpdateZ(30, fptr(0.20)); trig != nil {
		t.Fatalf("return from lift should not trigger: %+v", trig)
	}

	// Real next layer at Z=0.40 settles after 4s and rate limit passes
	if trig := tr.UpdateZ(32, fptr(0.40)); trig != nil {
		t.Fatalf("expected settle wait, got %+v", trig)
	}
	if trig := tr.UpdateZ(35, fptr(0.40)); trig != nil {
		t.Fatalf("expected settle wait, got %+v", trig)
	}
	trig2 := tr.UpdateZ(36.5, fptr(0.40))
	if trig2 == nil || trig2.Source != "layer_advance" || trig2.Layer != 2 || trig2.Z == nil || *trig2.Z != 0.40 {
		t.Fatalf("expected layer_advance at 0.40 layer 2, got %+v", trig2)
	}
}

func TestRebaselinesDownwardFromHeatupParkHeight(t *testing.T) {
	tr := New(Params{
		WarmupSeconds:     10,
		ZMinStepMM:        0.04,
		ZMaxStepMM:        1.20,
		ZSettleSeconds:    4,
		MinCheckIntervalS: 15,
		FallbackMaxWaitS:  60,
		RebaselineDropMM:  1.0,
	})

	tr.UpdateZ(0, fptr(40.0))
	tr.UpdateZ(11, fptr(40.0))
	trigPark := tr.UpdateZ(15.5, fptr(40.0))
	if trigPark == nil || trigPark.Source != "layer_baseline" {
		t.Fatalf("expected initial park baseline, got %+v", trigPark)
	}

	// Within rate limit: confirmed_z still drops cleanly to 0.20mm
	tr.UpdateZ(20, fptr(0.20))
	if trig := tr.UpdateZ(24.5, fptr(0.20)); trig != nil {
		t.Fatalf("expected rate-limited suppression, got %+v", trig)
	}
	snap := tr.Snapshot(24.5)
	if snap.ConfirmedZ == nil || *snap.ConfirmedZ != 0.20 {
		t.Fatalf("expected confirmed_z re-baselined to 0.20, got %+v", snap.ConfirmedZ)
	}

	// Next layer advance now fires cleanly from 0.20 -> 0.30
	tr.UpdateZ(32, fptr(0.30))
	trig2 := tr.UpdateZ(37, fptr(0.30))
	if trig2 == nil || trig2.Source != "layer_advance" || trig2.Layer != 2 {
		t.Fatalf("expected layer_advance layer 2, got %+v", trig2)
	}

	// Outside rate limit: emits layer_rebaseline and resets layer count
	tr.Reset()
	tr.UpdateZ(0, fptr(40.0))
	tr.UpdateZ(11, fptr(40.0))
	tr.UpdateZ(15.5, fptr(40.0))
	tr.UpdateZ(35, fptr(0.20))
	trigRe := tr.UpdateZ(40, fptr(0.20))
	if trigRe == nil || trigRe.Source != "layer_rebaseline" || trigRe.Layer != 1 {
		t.Fatalf("expected layer_rebaseline layer 1, got %+v", trigRe)
	}

	// Timer fallback triggers after FallbackMaxWaitS
	if trig := tr.CheckTimerFallback(99); trig != nil {
		t.Fatalf("expected no timer fallback before 60s, got %+v", trig)
	}
	if trig := tr.CheckTimerFallback(101); trig == nil || trig.Source != "timer_fallback" {
		t.Fatalf("expected timer_fallback after 60s, got %+v", trig)
	}
}

func TestSnapshotOmitsNilFieldsInJSON(t *testing.T) {
	tr := New(DefaultParams())
	raw, err := json.Marshal(tr.Snapshot(0))
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, "null") {
		t.Fatalf("expected nil pointer fields omitted from JSON, got %s", text)
	}
	if strings.Contains(text, "confirmed_z") || strings.Contains(text, "candidate_z") {
		t.Fatalf("expected confirmed_z and candidate_z omitted when nil, got %s", text)
	}
}
