package scenes2sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
)

// seedConfigJS mirrors the live Pi config closely enough to exercise the
// real scenario / sceneToPage shapes: three buttons, two modules wired.
const seedConfigJS = `let config = {
	modules: [
		{
			module: "MMM-Canvas",
			position: "fullscreen_above",
			config: {
				activePage: "home",
				scenes2Adapter: {
					enabled: true,
					sceneToPage: {
						role1: "home",
						chores: "Chores",
						recipesage: "Menu"
					}
				}
			}
		},
		{
			module: "MMM-Scenes2",
			position: "bottom_bar",
			config: {
				scenario: [
					{ activeIndicator: "❶", enter: ["role1"], exit: ["chores", "recipesage"], inactiveIndicator: "①", life: 0 },
					{ activeIndicator: "❷", enter: ["chores"], exit: ["role1", "recipesage"], inactiveIndicator: "②", life: 0 },
					{ activeIndicator: "❸", enter: ["recipesage"], exit: ["role1", "chores"], inactiveIndicator: "③", life: 0 }
				]
			}
		}
	]
};
if (typeof module !== "undefined") { module.exports = config; }
`

func newSeededManager(t *testing.T) *mmconfig.Manager {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.js")
	if err := os.WriteFile(path, []byte(seedConfigJS), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return mmconfig.NewManager(path, "") // no restart command
}

func TestSceneNameFor(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"Mascots":   "mascots",
		"home":      "home",
		"My Page!":  "my_page_",
		"Two-Words": "two_words",
		"123":       "123",
	}
	for in, want := range cases {
		if got := SceneNameFor(in); got != want {
			t.Errorf("SceneNameFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSync_AddsPageToBothModules(t *testing.T) {
	mm := newSeededManager(t)
	// Full desired page set: the three already-mapped pages plus the new
	// Mascots page that needs a scene allocated.
	res, err := Reconcile(mm, []string{"home", "Chores", "Menu", "Mascots"})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !res.CanvasUpdated || !res.ScenesUpdated {
		t.Errorf("expected both modules updated; got %+v", res)
	}
	if len(res.AddedScenes) != 1 || res.AddedScenes[0] != "mascots" {
		t.Errorf("AddedScenes = %v, want [mascots]", res.AddedScenes)
	}

	mods, _ := mm.ListModules()
	canvas := findModule(mods, ModuleCanvas)
	mapping := digMap(t, canvas.Config, "scenes2Adapter", "sceneToPage")
	if mapping["mascots"] != "Mascots" {
		t.Errorf("sceneToPage missing mascots→Mascots; got %v", mapping)
	}
	// Legacy mappings untouched.
	if mapping["role1"] != "home" {
		t.Errorf("legacy role1→home was clobbered; got %v", mapping)
	}

	scenes2 := findModule(mods, ModuleScenes2)
	scenario, _ := scenes2.Config["scenario"].([]any)
	if len(scenario) != 4 {
		t.Fatalf("scenario len: want 4, got %d", len(scenario))
	}
	last := scenario[3].(map[string]any)
	if last["activeIndicator"] != "❹" || last["inactiveIndicator"] != "④" {
		t.Errorf("new scenario indicators: %v / %v", last["activeIndicator"], last["inactiveIndicator"])
	}
	if got := last["enter"].([]any); len(got) != 1 || got[0] != "mascots" {
		t.Errorf("new scenario enter: %v", got)
	}
	// New scenario's exit list = all three prior scene names.
	exits := toStrings(last["exit"].([]any))
	for _, want := range []string{"role1", "chores", "recipesage"} {
		if !contains(exits, want) {
			t.Errorf("new scenario exit list missing %q: %v", want, exits)
		}
	}
	// Every prior scenario must now have mascots in its exit list.
	for i := 0; i < 3; i++ {
		prior := scenario[i].(map[string]any)
		priorExits := toStrings(prior["exit"].([]any))
		if !contains(priorExits, "mascots") {
			t.Errorf("scenario[%d] missing mascots in exit: %v", i, priorExits)
		}
	}
}

func TestSync_RemovesPageFromBothModules(t *testing.T) {
	mm := newSeededManager(t)
	// Desired page set drops "Chores" — Reconcile should remove the
	// chores scene mapping + scenario.
	res, err := Reconcile(mm, []string{"home", "Menu"})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !res.CanvasUpdated || !res.ScenesUpdated {
		t.Errorf("expected both modules updated; got %+v", res)
	}

	mods, _ := mm.ListModules()
	canvas := findModule(mods, ModuleCanvas)
	mapping := digMap(t, canvas.Config, "scenes2Adapter", "sceneToPage")
	if _, ok := mapping["chores"]; ok {
		t.Errorf("chores still in sceneToPage after removal: %v", mapping)
	}

	scenes2 := findModule(mods, ModuleScenes2)
	scenario, _ := scenes2.Config["scenario"].([]any)
	if len(scenario) != 2 {
		t.Fatalf("scenario len after removal: want 2, got %d", len(scenario))
	}
	for i, entry := range scenario {
		m := entry.(map[string]any)
		exits := toStrings(m["exit"].([]any))
		if contains(exits, "chores") {
			t.Errorf("scenario[%d] still references removed scene chores: %v", i, exits)
		}
	}
}

func TestSync_NoOpWhenModulesAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.js")
	if err := os.WriteFile(path, []byte(`let config = { modules: [] }; if (typeof module !== "undefined") { module.exports = config; }`), 0o644); err != nil {
		t.Fatal(err)
	}
	mm := mmconfig.NewManager(path, "")
	res, err := Reconcile(mm, []string{"Mascots"})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.CanvasUpdated || res.ScenesUpdated {
		t.Errorf("expected no-op when modules absent, got %+v", res)
	}
}

func TestSync_IdempotentOnRepeat(t *testing.T) {
	mm := newSeededManager(t)
	pages := []string{"home", "Chores", "Menu", "Mascots"}
	if _, err := Reconcile(mm, pages); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	res, err := Reconcile(mm, pages)
	if err != nil {
		t.Fatalf("repeat reconcile: %v", err)
	}
	if res.CanvasUpdated || res.ScenesUpdated {
		t.Errorf("repeat reconcile should be a no-op; got %+v", res)
	}
	mods, _ := mm.ListModules()
	scenes2 := findModule(mods, ModuleScenes2)
	scenario, _ := scenes2.Config["scenario"].([]any)
	if len(scenario) != 4 {
		t.Errorf("repeat reconcile mutated scenario count: got %d", len(scenario))
	}
}

func TestActiveIndicatorOverflow(t *testing.T) {
	if got := activeIndicator(10); got != "(11)" {
		t.Errorf("activeIndicator(10) = %q, want (11)", got)
	}
	if got := inactiveIndicator(10); got != "11." {
		t.Errorf("inactiveIndicator(10) = %q, want 11.", got)
	}
}

// ---- helpers ----

func findModule(mods []mmconfig.Module, name string) *mmconfig.Module {
	for i := range mods {
		if mods[i].Module == name {
			return &mods[i]
		}
	}
	return nil
}

func digMap(t *testing.T, m map[string]any, keys ...string) map[string]any {
	t.Helper()
	for _, k := range keys {
		v, ok := m[k].(map[string]any)
		if !ok {
			t.Fatalf("dig %s: not a map at key %q (got %T)", strings.Join(keys, "."), k, m[k])
		}
		m = v
	}
	return m
}

func toStrings(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
