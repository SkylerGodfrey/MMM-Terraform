package layoutviewer

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
)

// minimalConfigJS is the smallest config.js mmconfig will accept — one
// module and the required MM exports. Enough for the snapshot/restore tests
// without standing up a real MagicMirror.
const minimalConfigJS = `let config = {
  address: "0.0.0.0",
  port: 8080,
  modules: [
    { module: "MMM-PhotoFrame", position: "top_bar" },
    {
      module: "MMM-LayoutBounds",
      position: "bottom_bar",
      config: {
        layout: {
          version: 1,
          regions: { top_bar: { maxHeight: "420px", overflow: "hidden" } },
          moduleOverrides: []
        }
      }
    }
  ]
};
if (typeof module !== "undefined") { module.exports = config; }
`

func writeConfig(t *testing.T, content string) (configPath string, mm *mmconfig.Manager) {
	t.Helper()
	dir := t.TempDir()
	configPath = filepath.Join(dir, "config.js")
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// restart command "true" succeeds without actually doing anything — lets
	// the lifecycle code exercise the restart branch without a real pm2.
	mm = mmconfig.NewManager(configPath, "true")
	return
}

func sha(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(data)
	return string(h[:])
}

func TestPreviewSnapshotAndDiscardIsByteIdentical(t *testing.T) {
	configPath, mm := writeConfig(t, minimalConfigJS)
	preBytes := sha(t, configPath)

	store := NewPreviewStore(configPath, mm)
	store.healthCheck = func() error { return nil } // skip real MM

	wc := &WorkingCopy{
		Version: 1,
		Layout: map[string]any{
			"version":         float64(1),
			"regions":         map[string]any{"top_bar": map[string]any{"maxHeight": "500px"}},
			"moduleOverrides": []any{},
		},
	}
	if err := store.Preview(wc); err != nil {
		t.Fatalf("preview: %v", err)
	}
	// Backup file should exist; config.js should be mutated.
	if _, err := os.Stat(configPath + ".preview-backup"); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
	postPreview := sha(t, configPath)
	if postPreview == preBytes {
		t.Errorf("preview should mutate config.js (live unchanged)")
	}

	if err := store.Discard(); err != nil {
		t.Fatalf("discard: %v", err)
	}
	// Live should be byte-identical to original.
	postDiscard := sha(t, configPath)
	if postDiscard != preBytes {
		t.Errorf("discard should restore byte-identical config.js")
	}
	if _, err := os.Stat(configPath + ".preview-backup"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("backup should be removed after discard, got err=%v", err)
	}
}

func TestPreviewRejectsConcurrent(t *testing.T) {
	configPath, mm := writeConfig(t, minimalConfigJS)
	store := NewPreviewStore(configPath, mm)
	store.healthCheck = func() error { return nil }

	wc := &WorkingCopy{Version: 1, Layout: map[string]any{
		"version": float64(1), "regions": map[string]any{}, "moduleOverrides": []any{},
	}}
	if err := store.Preview(wc); err != nil {
		t.Fatalf("first preview: %v", err)
	}
	defer store.Discard()

	if err := store.Preview(wc); !errors.Is(err, ErrPreviewAlreadyActive) {
		t.Errorf("expected ErrPreviewAlreadyActive on second preview; got %v", err)
	}
}

func TestPreviewHealthCheckFailureAutoReverts(t *testing.T) {
	configPath, mm := writeConfig(t, minimalConfigJS)
	preBytes := sha(t, configPath)

	store := NewPreviewStore(configPath, mm)
	// Force health check to fail every poll so Preview gives up + reverts.
	store.healthCheck = func() error { return errors.New("simulated MM down") }

	wc := &WorkingCopy{Version: 1, Layout: map[string]any{
		"version": float64(1),
		"regions": map[string]any{"top_bar": map[string]any{"maxHeight": "999px"}},
		"moduleOverrides": []any{},
	}}
	// Shorten timeout via the package constant indirection: just sleep a hair
	// and confirm Preview returned the failure + reverted.
	prevTimeout := mmHealthTimeoutForTest(t, 600*time.Millisecond)
	defer prevTimeout()

	err := store.Preview(wc)
	if err == nil {
		t.Fatal("expected Preview to fail when health check never passes")
	}
	if sha(t, configPath) != preBytes {
		t.Errorf("auto-revert should restore byte-identical config after health failure")
	}
	if _, err := os.Stat(configPath + ".preview-backup"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("backup should be removed after auto-revert, got %v", err)
	}
}

func TestPreviewRecoversOnAgentRestart(t *testing.T) {
	configPath, mm := writeConfig(t, minimalConfigJS)
	store := NewPreviewStore(configPath, mm)
	store.healthCheck = func() error { return nil }

	wc := &WorkingCopy{Version: 1, Layout: map[string]any{
		"version": float64(1), "regions": map[string]any{}, "moduleOverrides": []any{},
		},
	}
	if err := store.Preview(wc); err != nil {
		t.Fatalf("preview: %v", err)
	}

	// Pretend the agent restarted: drop the in-memory store, build a new one
	// pointing at the same files. It should rediscover the preview as active.
	store2 := NewPreviewStore(configPath, mm)
	active, meta := store2.Active()
	if !active {
		t.Error("expected new store to recover an in-progress preview")
	}
	if meta == nil || meta.Deadline.IsZero() {
		t.Errorf("expected meta with deadline after recovery; got %+v", meta)
	}

	if err := store2.Discard(); err != nil {
		t.Fatalf("discard after recovery: %v", err)
	}
}

// mmHealthTimeoutForTest temporarily shortens the package-level timeout
// constant so health-failure tests don't take 15s. Returns a restore func.
func mmHealthTimeoutForTest(t *testing.T, d time.Duration) func() {
	t.Helper()
	// Constant can't be reassigned at runtime; but waitForMM reads the
	// package var. We expose it as a var for testability — see preview.go.
	prev := mmHealthTimeoutOverride
	mmHealthTimeoutOverride = d
	return func() { mmHealthTimeoutOverride = prev }
}
