package timelapse

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreLifecycleRecoveryAndPruning(t *testing.T) {
	dataDir := t.TempDir()
	st := NewStore(dataDir)

	t0 := time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC)
	s1, err := st.Start("p1", "CORE One", "/usb/gearbox_cover.bgcode", "layer", 24, t0)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if s1.JobName != "gearbox_cover" || s1.Status != StatusRecording {
		t.Fatalf("unexpected start session: %+v", s1)
	}

	f1, err := st.AddFrame("p1", s1.ID, []byte("jpeg-1"))
	if err != nil || f1 != "000001.jpg" {
		t.Fatalf("AddFrame 1 failed: %q err=%v", f1, err)
	}
	f2, err := st.AddFrame("p1", s1.ID, []byte("jpeg-22"))
	if err != nil || f2 != "000002.jpg" {
		t.Fatalf("AddFrame 2 failed: %q err=%v", f2, err)
	}

	gotBytes, err := st.ReadFrame("p1", s1.ID, "000002.jpg")
	if err != nil || string(gotBytes) != "jpeg-22" {
		t.Fatalf("ReadFrame failed: %q err=%v", gotBytes, err)
	}
	if _, err := st.ReadFrame("p1", "../etc", "passwd"); err == nil {
		t.Fatal("expected traversal rejection")
	}

	// Simulate abrupt restart while s1 is still StatusRecording
	stRestart := NewStore(dataDir)
	listed := stRestart.List("p1")
	if len(listed) != 1 || listed[0].Status != StatusStopped {
		t.Fatalf("expected stale recording recovered to stopped, got %+v", listed)
	}

	// Create s2 and s3 with maxSessions=2 -> oldest finished session should be pruned
	s2, err := stRestart.Start("p1", "CORE One", "part2.gcode", "interval", 30, t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("Start s2 failed: %v", err)
	}
	_, _ = stRestart.AddFrame("p1", s2.ID, []byte("jpeg-s2"))
	if err := stRestart.Finish("p1", s2.ID, StatusCompleted, t0.Add(2*time.Minute), 2); err != nil {
		t.Fatalf("Finish s2 failed: %v", err)
	}

	s3, err := stRestart.Start("p1", "CORE One", "part3.gcode", "layer", 24, t0.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("Start s3 failed: %v", err)
	}
	_, _ = stRestart.AddFrame("p1", s3.ID, []byte("jpeg-s3"))
	if err := stRestart.Finish("p1", s3.ID, StatusCompleted, t0.Add(4*time.Minute), 2); err != nil {
		t.Fatalf("Finish s3 failed: %v", err)
	}

	listed = stRestart.List("p1")
	if len(listed) != 2 || listed[0].ID != s3.ID || listed[1].ID != s2.ID {
		t.Fatalf("expected 2 newest sessions after prune, got %+v", listed)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "timelapses", "p1", s1.ID)); !os.IsNotExist(err) {
		t.Fatalf("expected pruned s1 folder removed, stat err=%v", err)
	}

	if err := stRestart.Delete("p1", s2.ID); err != nil {
		t.Fatalf("Delete s2 failed: %v", err)
	}
	listed = stRestart.List("")
	if len(listed) != 1 || listed[0].ID != s3.ID {
		t.Fatalf("expected only s3 remaining after Delete, got %+v", listed)
	}
}
