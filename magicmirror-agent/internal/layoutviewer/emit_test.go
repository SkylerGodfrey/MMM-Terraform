package layoutviewer

import (
	"regexp"
	"strings"
	"testing"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
)

// fixtureTf is a trimmed slice of modules.tf — just enough resources to
// exercise the position-move and layout_bounds rewrite paths while keeping
// the diff readable. Comments are deliberate: the AC requires preserving them.
const fixtureTf = `# Top of file comment.

# A photo frame, declared first.
resource "magicmirror_module" "photo_frame" {
  module   = "MMM-PhotoFrame"
  position = "top_bar"
  classes  = "role1"

  config = jsonencode({
    imagePaths = ["modules/MagicMirrorPhotos"]
  })
}

# Calendar.
resource "magicmirror_module" "calendar_ext3" {
  module   = "MMM-CalendarExt3"
  position = "top_bar"
  classes  = "role1"

  config = jsonencode({
    mode = "month"
  })
}

# Layout bounds.
resource "magicmirror_module" "layout_bounds" {
  module   = "MMM-LayoutBounds"
  position = "bottom_bar"

  config = jsonencode({
    layout = {
      version = 1
      regions = {
        top_bar = { maxHeight = "420px", overflow = "hidden" }
      }
      moduleOverrides = [
        { match = { module = "MMM-Chores" }, exempt = true },
      ]
    }
  })
}
`

func liveModulesFixture() []mmconfig.Module {
	return []mmconfig.Module{
		{ID: "photo-id", Module: "MMM-PhotoFrame", Position: "top_bar"},
		{ID: "cal-id", Module: "MMM-CalendarExt3", Position: "top_bar"},
		{ID: "bounds-id", Module: "MMM-LayoutBounds", Position: "bottom_bar", Config: map[string]any{
			"layout": map[string]any{
				"version": float64(1),
				"regions": map[string]any{
					"top_bar": map[string]any{"maxHeight": "420px", "overflow": "hidden"},
				},
				"moduleOverrides": []any{
					map[string]any{"match": map[string]any{"module": "MMM-Chores"}, "exempt": true},
				},
			},
		}},
	}
}

func TestEmitIdentityProducesEmptyDiff(t *testing.T) {
	// Working-copy mirrors live exactly → no diff, no changes.
	wc := &WorkingCopy{
		Version: 1,
		Layout: map[string]any{
			"version": float64(1),
			"regions": map[string]any{
				"top_bar": map[string]any{"maxHeight": "420px", "overflow": "hidden"},
			},
			"moduleOverrides": []any{
				map[string]any{"match": map[string]any{"module": "MMM-Chores"}, "exempt": true},
			},
		},
		PendingPositions: map[string]string{},
	}
	res, err := emitTerraform([]byte(fixtureTf), wc, liveModulesFixture())
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if !res.Summary.NoChange {
		t.Errorf("expected NoChange=true; got %+v", res.Summary)
	}
	if res.Diff != "" {
		t.Errorf("expected empty diff; got:\n%s", res.Diff)
	}
	if len(res.Changes) != 0 {
		t.Errorf("expected no changes; got %+v", res.Changes)
	}
	if res.NewContent != fixtureTf {
		t.Errorf("NewContent should be byte-identical to original; differs")
	}
}

func TestEmitMovesOneModule(t *testing.T) {
	// Move CalendarExt3 from top_bar to middle_center.
	wc := &WorkingCopy{
		Version: 1,
		Layout: map[string]any{
			"version": float64(1),
			"regions": map[string]any{
				"top_bar": map[string]any{"maxHeight": "420px", "overflow": "hidden"},
			},
			"moduleOverrides": []any{
				map[string]any{"match": map[string]any{"module": "MMM-Chores"}, "exempt": true},
			},
		},
		PendingPositions: map[string]string{"cal-id": "middle_center"},
	}
	res, err := emitTerraform([]byte(fixtureTf), wc, liveModulesFixture())
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if res.Summary.PositionMoves != 1 {
		t.Errorf("expected 1 move; got %d", res.Summary.PositionMoves)
	}
	if res.Summary.LayoutBoundsTouched {
		t.Errorf("layout_bounds shouldn't be touched when only a position changes")
	}
	if !strings.Contains(res.NewContent, `position = "middle_center"`) {
		t.Errorf("new content should set position = \"middle_center\":\n%s", res.NewContent)
	}
	// PhotoFrame's position should remain top_bar.
	photo := blockSnippet(res.NewContent, "photo_frame")
	if !strings.Contains(photo, `position = "top_bar"`) {
		t.Errorf("photo_frame position should remain top_bar:\n%s", photo)
	}
	// Comments preserved.
	if !strings.Contains(res.NewContent, "# A photo frame, declared first.") {
		t.Errorf("comment should be preserved in output")
	}
	if !strings.Contains(res.NewContent, "# Calendar.") {
		t.Errorf("comment should be preserved in output")
	}
	// Diff should be minimal: at most a handful of lines.
	added, removed := countDiffLines(res.Diff)
	if added != 1 || removed != 1 {
		t.Errorf("expected 1 +/1 -; got %d +/%d - in diff:\n%s", added, removed, res.Diff)
	}
}

func TestEmitChangesOneRegionHeight(t *testing.T) {
	wc := &WorkingCopy{
		Version: 1,
		Layout: map[string]any{
			"version": float64(1),
			"regions": map[string]any{
				"top_bar": map[string]any{"maxHeight": "500px", "overflow": "hidden"},
			},
			"moduleOverrides": []any{
				map[string]any{"match": map[string]any{"module": "MMM-Chores"}, "exempt": true},
			},
		},
	}
	res, err := emitTerraform([]byte(fixtureTf), wc, liveModulesFixture())
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if res.Summary.PositionMoves != 0 {
		t.Errorf("expected no position moves; got %d", res.Summary.PositionMoves)
	}
	if !res.Summary.LayoutBoundsTouched {
		t.Errorf("layout_bounds should be touched when maxHeight changes")
	}
	if !strings.Contains(res.NewContent, `maxHeight = "500px"`) {
		t.Errorf("new content should contain new height:\n%s", res.NewContent)
	}
	if strings.Contains(res.NewContent, `maxHeight = "420px"`) {
		t.Errorf("old height should be gone from output")
	}
	// Photoframe and calendar resources untouched.
	if !strings.Contains(res.NewContent, "# A photo frame, declared first.") {
		t.Errorf("photo_frame comment should be preserved")
	}
}

func TestEmitAppliesModuleConfigPatch(t *testing.T) {
	// HOM-99: a moduleConfigs patch should rewrite the matching resource's
	// `config = jsonencode({...})` attribute and report a module-config change.
	wc := &WorkingCopy{
		Version: 1,
		Layout: map[string]any{
			"version":         float64(1),
			"regions":         map[string]any{"top_bar": map[string]any{"maxHeight": "420px", "overflow": "hidden"}},
			"moduleOverrides": []any{map[string]any{"match": map[string]any{"module": "MMM-Chores"}, "exempt": true}},
		},
		ModuleConfigs: map[string]map[string]any{
			"cal-id": {"mode": "week", "maxEventLines": float64(7)},
		},
	}
	res, err := emitTerraform([]byte(fixtureTf), wc, liveModulesFixture())
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	// hclwrite aligns equals signs across a block; match the value portion
	// only so attribute padding doesn't break the assertion.
	if !regexp.MustCompile(`mode\s*=\s*"week"`).MatchString(res.NewContent) {
		t.Errorf("expected new config to include mode=week; got:\n%s", res.NewContent)
	}
	if !regexp.MustCompile(`maxEventLines\s*=\s*7`).MatchString(res.NewContent) {
		t.Errorf("expected new config to include maxEventLines=7; got:\n%s", res.NewContent)
	}
	var sawConfigChange bool
	for _, c := range res.Changes {
		if c.Kind == "module-config" && c.Module == "MMM-CalendarExt3" {
			sawConfigChange = true
		}
	}
	if !sawConfigChange {
		t.Errorf("expected a module-config change for MMM-CalendarExt3; got %+v", res.Changes)
	}
}

func TestValidateAcceptsNewOverrideFields(t *testing.T) {
	wc := &WorkingCopy{
		Version: 1,
		Layout: map[string]any{
			"version": float64(1),
			"regions": map[string]any{},
			"moduleOverrides": []any{
				map[string]any{
					"match":       map[string]any{"module": "MMM-CalendarExt3Agenda", "region": "upper_third"},
					"maxHeight":   "240px",
					"maxWidth":    "100%",
					"containMode": "paint",
				},
			},
		},
	}
	if err := validateWorkingCopy(wc); err != nil {
		t.Fatalf("schema-conforming doc rejected: %v", err)
	}
}

func TestValidateRejectsBadContainMode(t *testing.T) {
	wc := &WorkingCopy{
		Version: 1,
		Layout: map[string]any{
			"version": float64(1),
			"regions": map[string]any{},
			"moduleOverrides": []any{
				map[string]any{
					"match":       map[string]any{"module": "MMM-CalendarExt3"},
					"containMode": "everything",
				},
			},
		},
	}
	err := validateWorkingCopy(wc)
	if err == nil || !contains(err.Error(), "containMode") {
		t.Errorf("expected containMode error; got %v", err)
	}
}

func TestEmitWarnsOnAmbiguousMove(t *testing.T) {
	// Two CalendarExt3 resources in the same position → can't disambiguate
	// which one to move. Should warn, not panic, and apply no change.
	tf := `resource "magicmirror_module" "a" {
  module   = "MMM-CalendarExt3"
  position = "top_bar"
}
resource "magicmirror_module" "b" {
  module   = "MMM-CalendarExt3"
  position = "top_bar"
}
`
	live := []mmconfig.Module{
		{ID: "a", Module: "MMM-CalendarExt3", Position: "top_bar"},
		{ID: "b", Module: "MMM-CalendarExt3", Position: "top_bar"},
	}
	wc := &WorkingCopy{
		Version:          1,
		Layout:           map[string]any{"version": float64(1), "regions": map[string]any{}, "moduleOverrides": []any{}},
		PendingPositions: map[string]string{"a": "middle_center"},
	}
	res, err := emitTerraform([]byte(tf), wc, live)
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Errorf("expected a warning about ambiguous move")
	}
	if res.Summary.PositionMoves != 0 {
		t.Errorf("expected no moves applied; got %d", res.Summary.PositionMoves)
	}
}

func TestUnifiedDiffEmpty(t *testing.T) {
	if got := unifiedDiff("same\n", "same\n", "a", "b"); got != "" {
		t.Errorf("identical inputs should produce empty diff; got %q", got)
	}
}

func TestUnifiedDiffSingleLineChange(t *testing.T) {
	old := "a\nb\nc\nd\ne\n"
	new := "a\nb\nX\nd\ne\n"
	d := unifiedDiff(old, new, "old", "new")
	if !strings.Contains(d, "-c\n") {
		t.Errorf("diff should remove c:\n%s", d)
	}
	if !strings.Contains(d, "+X\n") {
		t.Errorf("diff should add X:\n%s", d)
	}
	added, removed := countDiffLines(d)
	if added != 1 || removed != 1 {
		t.Errorf("expected 1 +/1 -; got %d +/%d -", added, removed)
	}
}

// --- helpers ---

func countDiffLines(d string) (added, removed int) {
	for _, line := range strings.Split(d, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "@@"):
			// headers — don't count
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return
}

// blockSnippet finds the substring containing `resource "magicmirror_module" "<label>"`
// through its closing brace — naive but enough for fixture-sized tests.
func blockSnippet(content, label string) string {
	needle := `resource "magicmirror_module" "` + label + `"`
	start := strings.Index(content, needle)
	if start < 0 {
		return ""
	}
	// Walk forward until matching closing brace at column 0.
	rest := content[start:]
	depth := 0
	for i, r := range rest {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return rest[:i+1]
			}
		}
	}
	return rest
}
