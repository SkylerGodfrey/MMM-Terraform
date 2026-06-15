package layoutviewer

// HOM-95 L6: live preview + revert. Apply the working-copy layout to the
// running mirror without touching modules.tf, with a one-click discard
// path and a 30-minute auto-revert guardrail so a forgotten browser tab
// can't desync the live mirror from source-of-truth.
//
// State on disk (next to config.js):
//   - config.js.preview-backup  — byte-for-byte snapshot of pre-preview config.js
//   - config.js.preview-meta.json — { startedAt, deadline, sourceWorkingCopy }
//
// The backup file's existence == "preview active". Surviving agent restart
// is therefore automatic; the auto-revert timer is reconstructed from the
// meta file on agent start.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
)

// previewWindow is how long a preview may stay applied before the agent
// auto-reverts. Live mirror should never silently disagree with modules.tf
// for longer than this; the editor pings often enough that a real user
// will commit (L5) or discard well before this fires.
const previewWindow = 30 * time.Minute

// mmHealthTimeout is the longest we wait for MagicMirror to come up after
// a pm2 restart before treating the preview as failed and auto-reverting.
const mmHealthTimeout = 15 * time.Second

// mmHealthTimeoutOverride lets tests shorten the window without faking the
// system clock. Unset (zero) means use mmHealthTimeout.
var mmHealthTimeoutOverride time.Duration

// mmHealthURL is what the agent polls to decide MagicMirror is healthy.
// Per workspace conventions, MM serves on localhost:8080.
const mmHealthURL = "http://127.0.0.1:8080"

type previewMeta struct {
	StartedAt time.Time `json:"startedAt"`
	Deadline  time.Time `json:"deadline"`
	Note      string    `json:"note,omitempty"`
}

// PreviewStore is the runtime guardrail around the live-preview lifecycle.
// It owns the backup/meta files, the auto-revert timer, and the one-at-a-
// time concurrency check so previewing twice in a row from two browsers
// can't double-snapshot a half-applied state.
type PreviewStore struct {
	configPath string
	mm         *mmconfig.Manager

	mu      sync.Mutex
	timer   *time.Timer
	healthCheck func() error // injectable for tests
}

// NewPreviewStore wires the store and recovers any pre-existing preview
// state on agent start. If a backup file from a previous run exists, the
// auto-revert timer is reconstructed from the meta deadline (or fires
// immediately if the deadline has passed).
func NewPreviewStore(configPath string, mm *mmconfig.Manager) *PreviewStore {
	s := &PreviewStore{
		configPath: configPath,
		mm:         mm,
		healthCheck: defaultHealthCheck,
	}
	s.recoverOnStart()
	return s
}

// Active returns whether a preview is currently applied and (if so) the
// meta for the editor's banner.
func (s *PreviewStore) Active() (bool, *previewMeta) {
	if s == nil {
		return false, nil
	}
	if _, err := os.Stat(s.backupPath()); err != nil {
		return false, nil
	}
	m, err := s.readMeta()
	if err != nil {
		// Backup file exists without meta — treat as active but with no
		// schedule info; the user can still discard.
		return true, &previewMeta{}
	}
	return true, m
}

// Preview snapshots the live config.js, applies the working copy on top of
// it, restarts MagicMirror, and waits for it to come back healthy. Failure
// at any step auto-reverts so the live mirror is never left worse off.
func (s *PreviewStore) Preview(wc *WorkingCopy) error {
	if s == nil {
		return errors.New("preview store not configured")
	}
	if wc == nil {
		return errors.New("working copy is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(s.backupPath()); err == nil {
		return ErrPreviewAlreadyActive
	}

	// 1. Snapshot — raw byte copy so revert is byte-identical.
	if err := copyFile(s.configPath, s.backupPath()); err != nil {
		return fmt.Errorf("snapshot config.js: %w", err)
	}

	// 2. Mutate config.js to reflect the working copy.
	if err := s.applyWorkingCopy(wc); err != nil {
		_ = restoreFile(s.backupPath(), s.configPath)
		return fmt.Errorf("apply working copy: %w", err)
	}

	// 3. Write meta with the deadline before restart, so a crash mid-restart
	// still leaves a deterministic state to recover.
	now := time.Now().UTC()
	meta := previewMeta{
		StartedAt: now,
		Deadline:  now.Add(previewWindow),
	}
	if err := s.writeMeta(&meta); err != nil {
		_ = s.discardLocked() // best-effort revert
		return fmt.Errorf("write preview meta: %w", err)
	}

	// 4. Restart MagicMirror.
	if err := s.mm.Restart(); err != nil {
		_ = s.discardLocked()
		return fmt.Errorf("restart MagicMirror: %w", err)
	}

	// 5. Wait for MM to come back healthy.
	if err := s.waitForMM(); err != nil {
		_ = s.discardLocked()
		return fmt.Errorf("MagicMirror didn't come up healthy: %w", err)
	}

	// 6. Schedule auto-revert.
	s.scheduleAutoRevertLocked(previewWindow)
	return nil
}

// Discard reverts to the snapshot and restarts MagicMirror. Idempotent:
// calling it when no preview is active returns nil.
func (s *PreviewStore) Discard() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.discardLocked()
}

func (s *PreviewStore) discardLocked() error {
	if _, err := os.Stat(s.backupPath()); err != nil {
		return nil
	}
	if err := restoreFile(s.backupPath(), s.configPath); err != nil {
		return fmt.Errorf("restore snapshot: %w", err)
	}
	_ = os.Remove(s.metaPath())
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	// Restart MM so the reverted config takes effect.
	if err := s.mm.Restart(); err != nil {
		// The live config has already been restored — log via error return.
		return fmt.Errorf("revert restart: %w", err)
	}
	return nil
}

// ErrPreviewAlreadyActive is returned by Preview when another preview is
// already live. The endpoint translates it to HTTP 409.
var ErrPreviewAlreadyActive = errors.New("a preview is already active")

// Finalize is called after the user has committed the working copy via
// Terraform — the live mirror already matches the durable source-of-truth,
// so the snapshot + meta + auto-revert timer should drop without touching
// the live config.js. No restart.
func (s *PreviewStore) Finalize() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.backupPath()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	_ = os.Remove(s.backupPath())
	_ = os.Remove(s.metaPath())
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	return nil
}

func (s *PreviewStore) backupPath() string { return s.configPath + ".preview-backup" }
func (s *PreviewStore) metaPath() string   { return s.configPath + ".preview-meta.json" }

func (s *PreviewStore) writeMeta(m *previewMeta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.metaPath(), data, 0o644)
}

func (s *PreviewStore) readMeta() (*previewMeta, error) {
	data, err := os.ReadFile(s.metaPath())
	if err != nil {
		return nil, err
	}
	var m previewMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *PreviewStore) scheduleAutoRevertLocked(in time.Duration) {
	if s.timer != nil {
		s.timer.Stop()
	}
	if in <= 0 {
		// Already past deadline; revert immediately on next tick so we
		// don't recurse-deadlock from inside scheduleAutoRevert.
		in = 1 * time.Millisecond
	}
	s.timer = time.AfterFunc(in, func() {
		if err := s.Discard(); err != nil {
			fmt.Fprintf(os.Stderr, "preview auto-revert failed: %v\n", err)
		}
	})
}

func (s *PreviewStore) recoverOnStart() {
	if _, err := os.Stat(s.backupPath()); err != nil {
		return // No preview to recover.
	}
	m, err := s.readMeta()
	if err != nil {
		// Meta missing — leave the backup in place so the user can discard
		// manually, but don't schedule a timer (we don't know when to fire).
		return
	}
	remaining := time.Until(m.Deadline)
	s.mu.Lock()
	s.scheduleAutoRevertLocked(remaining)
	s.mu.Unlock()
}

// applyWorkingCopy mutates config.js: per-module position overrides and the
// MMM-LayoutBounds config.layout block. Uses mmconfig's parser so the same
// jsonencode round-trip applies (so future Terraform reads see consistent
// state).
func (s *PreviewStore) applyWorkingCopy(wc *WorkingCopy) error {
	cfg, err := s.mm.ReadConfig()
	if err != nil {
		return err
	}

	for id, newPos := range wc.PendingPositions {
		if newPos == "" {
			continue
		}
		moved := false
		for i := range cfg.Modules {
			if cfg.Modules[i].ID == id {
				cfg.Modules[i].Position = newPos
				moved = true
				break
			}
		}
		if !moved {
			return fmt.Errorf("pendingPositions[%s]: no live module with that ID", id)
		}
	}

	if wc.Layout != nil {
		boundsFound := false
		for i := range cfg.Modules {
			if cfg.Modules[i].Module == "MMM-LayoutBounds" {
				if cfg.Modules[i].Config == nil {
					cfg.Modules[i].Config = map[string]any{}
				}
				cfg.Modules[i].Config["layout"] = wc.Layout
				boundsFound = true
				break
			}
		}
		if !boundsFound {
			// Not a fatal error — the working copy can still carry pending
			// moves usefully without an MMM-LayoutBounds module. Don't warn
			// here; the editor doesn't render bounds-only state anyway.
		}
	}

	// HOM-99: arbitrary per-module config edits. Each patch is shallow-
	// merged into the existing module config so the editor can change e.g.
	// `lat`/`lon` on weather without resending the whole config map.
	for id, patch := range wc.ModuleConfigs {
		if len(patch) == 0 {
			continue
		}
		for i := range cfg.Modules {
			if cfg.Modules[i].ID != id {
				continue
			}
			if cfg.Modules[i].Config == nil {
				cfg.Modules[i].Config = map[string]any{}
			}
			for k, v := range patch {
				cfg.Modules[i].Config[k] = v
			}
			break
		}
	}

	return s.mm.WriteConfig(cfg)
}

func (s *PreviewStore) waitForMM() error {
	if s.healthCheck == nil {
		return nil
	}
	timeout := mmHealthTimeout
	if mmHealthTimeoutOverride > 0 {
		timeout = mmHealthTimeoutOverride
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := s.healthCheck(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(400 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("timeout")
	}
	return lastErr
}

// defaultHealthCheck pings MagicMirror's HTTP port; any 2xx/3xx counts as
// alive (we're just checking it's serving, not what it's serving).
func defaultHealthCheck() error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(mmHealthURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// ---------- file helpers ----------

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".preview-backup.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, dst)
}

// restoreFile is byte-identical revert: rename the backup back into place
// (atomic) and delete the backup. The AC explicitly requires byte equality.
func restoreFile(backup, target string) error {
	if err := os.Rename(backup, target); err != nil {
		return err
	}
	return nil
}
