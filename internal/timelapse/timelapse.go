// Package timelapse persists whole-print JPEG timelapse sessions per printer on disk,
// handles restart recovery of interrupted recordings, and prunes old sessions by retention limit.
package timelapse

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// StatusRecording indicates a print timelapse is actively collecting frames.
	StatusRecording = "recording"
	// StatusCompleted indicates a print finished normally and the timelapse closed cleanly.
	StatusCompleted = "completed"
	// StatusStopped indicates a print ended early, disconnected, or was interrupted by a restart.
	StatusStopped = "stopped"

	// MaxFramesPerSession bounds disk growth on exceptionally long or misconfigured prints.
	MaxFramesPerSession = 3000
)

// Session describes one recorded timelapse folder under <dataDir>/timelapses/<printerID>/<sessionID>.
type Session struct {
	ID             string `json:"id"`
	PrinterID      string `json:"printer_id"`
	PrinterName    string `json:"printer_name"`
	JobName        string `json:"job_name"`
	Mode           string `json:"mode"`
	Status         string `json:"status"`
	StartedAt      string `json:"started_at"`
	EndedAt        string `json:"ended_at,omitempty"`
	FPS            int    `json:"fps"`
	FrameCount     int    `json:"frame_count"`
	BytesTotal     int64  `json:"bytes_total"`
	ThumbnailFrame string `json:"thumbnail_frame,omitempty"`
}

// Store manages thread-safe persistent timelapse sessions under <dataDir>/timelapses.
type Store struct {
	mu      sync.Mutex
	rootDir string
}

// NewStore creates a timelapse Store under <dataDir>/timelapses and marks any leftover
// in-progress "recording" sessions from a previous process run as "stopped".
func NewStore(dataDir string) *Store {
	root := ""
	if strings.TrimSpace(dataDir) != "" {
		root = filepath.Join(dataDir, "timelapses")
	}
	st := &Store{rootDir: root}
	st.closeStaleRecordings(time.Now().UTC())
	return st
}

func (s *Store) closeStaleRecordings(now time.Time) {
	if s.rootDir == "" {
		return
	}
	printers, err := os.ReadDir(s.rootDir)
	if err != nil {
		return
	}
	for _, pe := range printers {
		if !pe.IsDir() || !safeSegment(pe.Name()) {
			continue
		}
		pDir := filepath.Join(s.rootDir, pe.Name())
		sessions, err := os.ReadDir(pDir)
		if err != nil {
			continue
		}
		for _, se := range sessions {
			if !se.IsDir() || !safeSegment(se.Name()) {
				continue
			}
			sessDir := filepath.Join(pDir, se.Name())
			meta, err := readMeta(sessDir)
			if err != nil || meta.Status != StatusRecording {
				continue
			}
			meta.Status = StatusStopped
			if meta.EndedAt == "" {
				meta.EndedAt = now.UTC().Format(time.RFC3339)
			}
			_ = writeMeta(sessDir, meta)
		}
	}
}

// Start creates a new recording session directory and initial meta.json for a print job.
func (s *Store) Start(printerID, printerName, jobPath, mode string, fps int, now time.Time) (*Session, error) {
	if s.rootDir == "" {
		return nil, errors.New("timelapse store disabled (empty data directory)")
	}
	if !safeSegment(printerID) {
		return nil, fmt.Errorf("invalid printer ID %q", printerID)
	}
	if fps <= 0 {
		fps = 24
	}
	jobName := cleanJobName(jobPath)
	stamp := now.UTC().Format("20060102_150405")
	baseID := stamp
	if slug := sanitizeSlug(jobName); slug != "" {
		baseID = stamp + "_" + slug
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	pDir := filepath.Join(s.rootDir, printerID)
	if err := os.MkdirAll(pDir, 0o750); err != nil {
		return nil, err
	}
	sessionID := baseID
	for suffix := 2; suffix < 1000; suffix++ {
		cand := filepath.Join(pDir, sessionID)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			break
		}
		sessionID = fmt.Sprintf("%s_%d", baseID, suffix)
	}
	sessDir := filepath.Join(pDir, sessionID)
	if err := os.MkdirAll(sessDir, 0o750); err != nil {
		return nil, err
	}

	sess := Session{
		ID:          sessionID,
		PrinterID:   printerID,
		PrinterName: strings.TrimSpace(printerName),
		JobName:     jobName,
		Mode:        mode,
		Status:      StatusRecording,
		StartedAt:   now.UTC().Format(time.RFC3339),
		FPS:         fps,
	}
	if sess.PrinterName == "" {
		sess.PrinterName = printerID
	}
	if err := writeMeta(sessDir, sess); err != nil {
		return nil, err
	}
	copySess := sess
	return &copySess, nil
}

// AddFrame appends one JPEG frame (%06d.jpg) to an active session and updates metadata atomically.
func (s *Store) AddFrame(printerID, sessionID string, jpg []byte) (string, error) {
	if s.rootDir == "" || !safeSegment(printerID) || !safeSegment(sessionID) {
		return "", errors.New("invalid timelapse session path")
	}
	if len(jpg) == 0 {
		return "", errors.New("empty timelapse JPEG frame")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sessDir := filepath.Join(s.rootDir, printerID, sessionID)
	meta, err := readMeta(sessDir)
	if err != nil {
		return "", err
	}
	if meta.FrameCount >= MaxFramesPerSession {
		return "", fmt.Errorf("session reached max %d frames", MaxFramesPerSession)
	}
	nextIdx := meta.FrameCount + 1
	frameName := fmt.Sprintf("%06d.jpg", nextIdx)
	if err := os.WriteFile(filepath.Join(sessDir, frameName), jpg, 0o600); err != nil {
		return "", err
	}
	meta.FrameCount = nextIdx
	meta.BytesTotal += int64(len(jpg))
	meta.ThumbnailFrame = frameName
	if err := writeMeta(sessDir, meta); err != nil {
		return "", err
	}
	return frameName, nil
}

// Finish transitions a session from recording to completed/stopped and prunes old sessions
// for the same printer beyond maxSessions (when maxSessions > 0).
func (s *Store) Finish(printerID, sessionID, status string, now time.Time, maxSessions int) error {
	if s.rootDir == "" || !safeSegment(printerID) || !safeSegment(sessionID) {
		return nil
	}
	if status != StatusCompleted && status != StatusStopped {
		status = StatusStopped
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sessDir := filepath.Join(s.rootDir, printerID, sessionID)
	meta, err := readMeta(sessDir)
	if err != nil {
		return err
	}
	meta.Status = status
	meta.EndedAt = now.UTC().Format(time.RFC3339)
	if err := writeMeta(sessDir, meta); err != nil {
		return err
	}
	if maxSessions > 0 {
		s.prunePrinterLocked(printerID, maxSessions)
	}
	return nil
}

func (s *Store) prunePrinterLocked(printerID string, maxSessions int) {
	pDir := filepath.Join(s.rootDir, printerID)
	entries, err := os.ReadDir(pDir)
	if err != nil {
		return
	}
	var finished []Session
	for _, e := range entries {
		if !e.IsDir() || !safeSegment(e.Name()) {
			continue
		}
		meta, err := readMeta(filepath.Join(pDir, e.Name()))
		if err != nil || meta.Status == StatusRecording {
			continue
		}
		finished = append(finished, meta)
	}
	if len(finished) <= maxSessions {
		return
	}
	slices.SortFunc(finished, func(a, b Session) int {
		if a.StartedAt != b.StartedAt {
			return strings.Compare(a.StartedAt, b.StartedAt)
		}
		return strings.Compare(a.ID, b.ID)
	})
	for len(finished) > maxSessions {
		_ = os.RemoveAll(filepath.Join(pDir, finished[0].ID))
		finished = finished[1:]
	}
}

// List returns all saved and active timelapse sessions sorted newest first.
func (s *Store) List(printerFilter string) []Session {
	if s.rootDir == "" {
		return []Session{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	printers, err := os.ReadDir(s.rootDir)
	if err != nil {
		return []Session{}
	}
	out := make([]Session, 0)
	for _, pe := range printers {
		if !pe.IsDir() || !safeSegment(pe.Name()) {
			continue
		}
		if printerFilter != "" && pe.Name() != printerFilter {
			continue
		}
		pDir := filepath.Join(s.rootDir, pe.Name())
		sessions, err := os.ReadDir(pDir)
		if err != nil {
			continue
		}
		for _, se := range sessions {
			if !se.IsDir() || !safeSegment(se.Name()) {
				continue
			}
			meta, err := readMeta(filepath.Join(pDir, se.Name()))
			if err != nil {
				continue
			}
			out = append(out, meta)
		}
	}
	slices.SortFunc(out, func(a, b Session) int {
		if a.StartedAt != b.StartedAt {
			return strings.Compare(b.StartedAt, a.StartedAt)
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out
}

// ReadFrame reads one JPEG frame from a timelapse session. Accepts either "000001.jpg" or "000001".
func (s *Store) ReadFrame(printerID, sessionID, frameName string) ([]byte, error) {
	if s.rootDir == "" || !safeSegment(printerID) || !safeSegment(sessionID) {
		return nil, os.ErrNotExist
	}
	cleaned := strings.TrimSuffix(strings.TrimSpace(frameName), ".jpg")
	if !safeSegment(cleaned) {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(s.rootDir, printerID, sessionID, cleaned+".jpg"))
}

// Delete deletes a complete timelapse session folder from disk.
func (s *Store) Delete(printerID, sessionID string) error {
	if s.rootDir == "" || !safeSegment(printerID) || !safeSegment(sessionID) {
		return os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sessDir := filepath.Join(s.rootDir, printerID, sessionID)
	if _, err := os.Stat(sessDir); err != nil {
		return err
	}
	return os.RemoveAll(sessDir)
}

func readMeta(sessDir string) (Session, error) {
	raw, err := os.ReadFile(filepath.Join(sessDir, "meta.json"))
	if err != nil {
		return Session{}, err
	}
	var meta Session
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Session{}, err
	}
	return meta, nil
}

func writeMeta(sessDir string, meta Session) error {
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(sessDir, "meta.json.tmp")
	dst := filepath.Join(sessDir, "meta.json")
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func safeSegment(s string) bool {
	if s == "" || s == "." || s == ".." || strings.Contains(s, "..") || strings.ContainsAny(s, `/\`) {
		return false
	}
	return true
}

func cleanJobName(jobPath string) string {
	base := strings.TrimSpace(filepath.Base(strings.ReplaceAll(jobPath, `\`, "/")))
	if base == "" || base == "." || base == "/" {
		return "print"
	}
	for _, ext := range []string{".bgcode", ".gcode", ".gc"} {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			base = base[:len(base)-len(ext)]
			break
		}
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return "print"
	}
	return base
}

func sanitizeSlug(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_':
			if !lastDash && b.Len() > 0 {
				b.WriteRune(r)
				lastDash = true
			}
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
		if b.Len() >= 36 {
			break
		}
	}
	return strings.Trim(b.String(), "-_")
}
