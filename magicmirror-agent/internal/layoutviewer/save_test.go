package layoutviewer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
)

// saveFixtureTf has a single magicmirror_module + the layout_bounds block,
// so a working-copy with one pending position + a layout edit exercises
// both write paths.
const saveFixtureTf = `# Saved fixture.
resource "magicmirror_module" "cal" {
  module   = "MMM-CalendarExt3"
  position = "top_bar"
  classes  = "role1"
}

resource "magicmirror_module" "bounds" {
  module   = "MMM-LayoutBounds"
  position = "bottom_bar"

  config = jsonencode({
    layout = {
      version = 1
      regions = {
        top_bar = { maxHeight = "420px", overflow = "hidden" }
      }
      moduleOverrides = []
    }
  })
}
`

func writeSaveFixture(t *testing.T) (modulesTfPath string, configPath string, mm *mmconfig.Manager, wcStore *workingCopyStore, preview *PreviewStore) {
	t.Helper()
	dir := t.TempDir()
	configPath = filepath.Join(dir, "config.js")
	if err := os.WriteFile(configPath, []byte(minimalConfigJS), 0o644); err != nil {
		t.Fatal(err)
	}
	modulesTfPath = filepath.Join(dir, "modules.tf")
	if err := os.WriteFile(modulesTfPath, []byte(saveFixtureTf), 0o644); err != nil {
		t.Fatal(err)
	}
	mm = mmconfig.NewManager(configPath, "true")
	wcStore = &workingCopyStore{path: filepath.Join(dir, "layout.json")}
	preview = NewPreviewStore(configPath, mm)
	preview.healthCheck = func() error { return nil }
	return
}

func TestPerformSaveNoWorkingCopyErrors(t *testing.T) {
	modulesTfPath, _, mm, wc, pv := writeSaveFixture(t)
	_, err := performSave(modulesTfPath, nil, mm, pv, wc)
	if err == nil {
		t.Fatal("expected error when working copy is nil")
	}
	if !strings.Contains(err.Error(), "no working copy") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPerformSaveAppliesChangesToBothFiles(t *testing.T) {
	modulesTfPath, configPath, mm, wcStore, pv := writeSaveFixture(t)
	// Working copy moves the calendar and changes the top_bar maxHeight.
	wc := &WorkingCopy{
		Version: 1,
		Layout: map[string]any{
			"version":         float64(1),
			"regions":         map[string]any{"top_bar": map[string]any{"maxHeight": "500px", "overflow": "hidden"}},
			"moduleOverrides": []any{},
		},
		PendingPositions: map[string]string{
			// minimalConfigJS has MMM-PhotoFrame in top_bar; the layout fixture
			// has MMM-CalendarExt3 in top_bar. Move the live PhotoFrame to top_left
			// — that demonstrates the config.js mutation path independently.
		},
	}
	// Provide the working copy file so the cleanup step has something to delete.
	if err := wcStore.write(wc); err != nil {
		t.Fatal(err)
	}

	result, err := performSave(modulesTfPath, wc, mm, pv, wcStore)
	if err != nil {
		t.Fatalf("performSave: %v", err)
	}
	if result.NoChange {
		t.Errorf("expected NoChange=false; the layout doc differs")
	}
	if !result.LayoutBoundsTouched {
		t.Errorf("layout_bounds should have been touched (maxHeight changed)")
	}

	// modules.tf should now reflect the new maxHeight.
	got, err := os.ReadFile(modulesTfPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `maxHeight = "500px"`) {
		t.Errorf("modules.tf should carry the new maxHeight:\n%s", string(got))
	}
	// Comment outside the layout_bounds block should still be there.
	if !strings.Contains(string(got), "# Saved fixture.") {
		t.Errorf("outer comments should be preserved")
	}

	// config.js should also reflect the new layout (via mmconfig).
	cfg, err := mm.ReadConfig()
	if err != nil {
		t.Fatal(err)
	}
	var sawNewHeight bool
	for _, m := range cfg.Modules {
		if m.Module != "MMM-LayoutBounds" {
			continue
		}
		layout, _ := m.Config["layout"].(map[string]any)
		regions, _ := layout["regions"].(map[string]any)
		if tb, _ := regions["top_bar"].(map[string]any); tb != nil {
			if mh, _ := tb["maxHeight"].(string); mh == "500px" {
				sawNewHeight = true
			}
		}
	}
	if !sawNewHeight {
		t.Errorf("config.js should carry top_bar maxHeight 500px after save")
	}

	// Working copy should be gone post-save.
	if _, err := os.Stat(wcStore.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("working copy should be cleared after save; got err=%v", err)
	}
	// No preview backup left around.
	if _, err := os.Stat(configPath + ".preview-backup"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no preview backup expected after a clean save; got err=%v", err)
	}
}

func TestPerformSaveNoOpWhenIdentity(t *testing.T) {
	modulesTfPath, _, mm, wcStore, pv := writeSaveFixture(t)

	// Working copy that's bit-identical to whatever live + modules.tf describe.
	wc := &WorkingCopy{
		Version: 1,
		Layout: map[string]any{
			"version":         float64(1),
			"regions":         map[string]any{"top_bar": map[string]any{"maxHeight": "420px", "overflow": "hidden"}},
			"moduleOverrides": []any{},
		},
	}
	if err := wcStore.write(wc); err != nil {
		t.Fatal(err)
	}

	originalTfBytes, _ := os.ReadFile(modulesTfPath)
	result, err := performSave(modulesTfPath, wc, mm, pv, wcStore)
	if err != nil {
		t.Fatalf("performSave: %v", err)
	}
	if !result.NoChange {
		t.Errorf("expected NoChange=true for identity save; got %+v", result)
	}
	gotTf, _ := os.ReadFile(modulesTfPath)
	if string(gotTf) != string(originalTfBytes) {
		t.Errorf("modules.tf should be byte-identical on no-op save")
	}
	if _, err := os.Stat(wcStore.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("identity save still clears working copy")
	}
}

func TestPerformSaveRollsBackOnHealthFailure(t *testing.T) {
	modulesTfPath, _, mm, wcStore, pv := writeSaveFixture(t)
	pv.healthCheck = func() error { return errors.New("simulated MM down") }
	prev := mmHealthTimeoutForTest(t, 500)
	defer prev()

	originalTfBytes, _ := os.ReadFile(modulesTfPath)
	wc := &WorkingCopy{
		Version: 1,
		Layout: map[string]any{
			"version":         float64(1),
			"regions":         map[string]any{"top_bar": map[string]any{"maxHeight": "999px"}},
			"moduleOverrides": []any{},
		},
	}
	_ = wcStore.write(wc)

	_, err := performSave(modulesTfPath, wc, mm, pv, wcStore)
	if err == nil {
		t.Fatal("expected save to fail when health check never passes")
	}

	// modules.tf should have been rolled back to original bytes.
	gotTf, _ := os.ReadFile(modulesTfPath)
	if string(gotTf) != string(originalTfBytes) {
		t.Errorf("modules.tf should roll back on save failure")
	}
}
