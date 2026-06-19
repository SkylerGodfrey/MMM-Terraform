// Package scenes2sync keeps the MMM-Scenes2 scenario list and the
// MMM-Canvas sceneToPage map in step with the canvas-layout document's
// page set (HOM-127). When a canvas page is added or removed in /canvas,
// the agent updates both modules so the new page gets a Scenes2 button
// on the mirror without the user hand-editing two config blocks.
//
// Scope is deliberately narrow: this package only mutates Scenes2 and
// Canvas module config blobs through mmconfig.Manager — it does not
// touch canvas-layout.json or talk to MMM-Canvas's runtime.
package scenes2sync

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
)

// Module names this package expects to find in config.js.
const (
	ModuleCanvas  = "MMM-Canvas"
	ModuleScenes2 = "MMM-Scenes2"
)

// Result reports what changed so the caller can surface it in the API
// response or a log line.
type Result struct {
	AddedScenes   []string
	RemovedScenes []string
	CanvasUpdated bool
	ScenesUpdated bool
}

// Reconcile makes MMM-Canvas and MMM-Scenes2 reflect the given page set.
// Pages without a scene mapping in MMM-Canvas's sceneToPage get one
// (named after the page, lowercased); scenes mapped to pages no longer
// in the set are removed. Same drift-correction logic on the Scenes2
// scenario list. Missing-module is fine: workspaces without Scenes2 or
// Canvas get a silent no-op.
//
// Reconcile is preferred over a diff-based Sync because it self-heals
// drift — pages that pre-date the sync feature, hand-edited configs,
// and partially-applied prior runs all converge on the same end state.
//
// The two UpdateModule calls each call ScheduleRestart, but the manager
// debounces them into a single pm2 restart so the user sees one reload,
// not two.
func Reconcile(mm *mmconfig.Manager, pages []string) (Result, error) {
	var result Result

	modules, err := mm.ListModules()
	if err != nil {
		return result, fmt.Errorf("list modules: %w", err)
	}

	canvas := findByName(modules, ModuleCanvas)
	scenes2 := findByName(modules, ModuleScenes2)
	if canvas == nil && scenes2 == nil {
		return result, nil
	}

	// Read the current sceneToPage map (if any) from MMM-Canvas to learn
	// which pages already have a scene. Pages that match an existing
	// mapped page name are left alone; pages that don't get a fresh
	// scene named after them.
	currentMapping := readSceneToPage(canvas)
	mappedPages := make(map[string]bool, len(currentMapping))
	for _, page := range currentMapping {
		mappedPages[page] = true
	}
	pageSet := make(map[string]bool, len(pages))
	for _, p := range pages {
		pageSet[p] = true
	}

	addedSceneByPage := map[string]string{}
	for _, page := range pages {
		if mappedPages[page] {
			continue
		}
		scene := SceneNameFor(page)
		if scene == "" {
			continue
		}
		addedSceneByPage[page] = scene
		result.AddedScenes = append(result.AddedScenes, scene)
	}

	for scene, page := range currentMapping {
		if !pageSet[page] {
			result.RemovedScenes = append(result.RemovedScenes, scene)
		}
	}

	if canvas != nil && (len(addedSceneByPage) > 0 || len(result.RemovedScenes) > 0) {
		updated := applyCanvasAdapter(canvas, addedSceneByPage, result.RemovedScenes)
		if _, err := mm.UpdateModule(updated); err != nil {
			return result, fmt.Errorf("update MMM-Canvas: %w", err)
		}
		result.CanvasUpdated = true
	}

	if scenes2 != nil {
		addedSceneNames := make([]string, 0, len(addedSceneByPage))
		for _, scene := range addedSceneByPage {
			addedSceneNames = append(addedSceneNames, scene)
		}
		updated, changed := updateScenes2Scenario(scenes2, addedSceneNames, result.RemovedScenes)
		if changed {
			if _, err := mm.UpdateModule(updated); err != nil {
				return result, fmt.Errorf("update MMM-Scenes2: %w", err)
			}
			result.ScenesUpdated = true
		}
	}

	return result, nil
}

// readSceneToPage extracts the current scene→page map from MMM-Canvas's
// config. Missing or non-map values return empty.
func readSceneToPage(canvas *mmconfig.Module) map[string]string {
	out := map[string]string{}
	adapter, ok := canvas.Config["scenes2Adapter"].(map[string]any)
	if !ok {
		return out
	}
	mapping, ok := adapter["sceneToPage"].(map[string]any)
	if !ok {
		return out
	}
	for scene, page := range mapping {
		if s, ok := page.(string); ok {
			out[scene] = s
		}
	}
	return out
}

// applyCanvasAdapter writes new mappings + removes stale ones in a copy
// of the module config.
func applyCanvasAdapter(canvas *mmconfig.Module, added map[string]string, removed []string) *mmconfig.Module {
	cfg := cloneMap(canvas.Config)
	adapter := nestedMap(cfg, "scenes2Adapter")
	mapping := nestedMap(adapter, "sceneToPage")

	for page, scene := range added {
		mapping[scene] = page
	}
	for _, scene := range removed {
		delete(mapping, scene)
	}
	if _, ok := adapter["enabled"]; !ok {
		adapter["enabled"] = true
	}

	updated := *canvas
	updated.Config = cfg
	return &updated
}

// SceneNameFor derives a scene name from a page name: lowercase, with
// any non-`[a-z0-9_]` byte replaced by `_`. Empty input returns "".
//
// Mirrors what the user is likely to type by hand when they wire up a
// new page; existing legacy mappings (role1, recipesage) are NOT
// regenerated by Sync — they live in the configs from a prior session.
func SceneNameFor(page string) string {
	if page == "" {
		return ""
	}
	lower := strings.ToLower(page)
	return nonSafeChars.ReplaceAllString(lower, "_")
}

var nonSafeChars = regexp.MustCompile(`[^a-z0-9_]+`)

// updateScenes2Scenario rebuilds MMM-Scenes2's config.scenario list. The
// scenario entry for a removed page is dropped; remaining scenarios get
// the removed scene pruned from their exit list. The scenario for a
// new page is appended with active/inactive indicators derived from its
// position in the resulting list.
func updateScenes2Scenario(scenes2 *mmconfig.Module, addedScenes, removedScenes []string) (*mmconfig.Module, bool) {
	cfg := cloneMap(scenes2.Config)
	scenario, _ := cfg["scenario"].([]any)
	originalLen := len(scenario)
	changed := false

	// Pass 1: drop scenarios for removed scenes.
	removeSet := stringSet(removedScenes)
	if len(removeSet) > 0 {
		kept := make([]any, 0, len(scenario))
		for _, entry := range scenario {
			if name := sceneNameOfScenario(entry); name != "" {
				if _, drop := removeSet[name]; drop {
					changed = true
					continue
				}
			}
			kept = append(kept, entry)
		}
		scenario = kept
	}

	// Pass 2: prune removed scene names from remaining scenarios' exits.
	if len(removeSet) > 0 {
		for _, entry := range scenario {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			exits, ok := m["exit"].([]any)
			if !ok {
				continue
			}
			kept := make([]any, 0, len(exits))
			for _, x := range exits {
				if s, ok := x.(string); ok {
					if _, drop := removeSet[s]; drop {
						changed = true
						continue
					}
				}
				kept = append(kept, x)
			}
			m["exit"] = kept
		}
	}

	// Pass 3: append scenarios for new scenes (skip ones already in the
	// scenario list — supports re-runs that shouldn't dupe). Existing
	// scenarios pick up the new scene name in their exit lists.
	existing := existingSceneNames(scenario)
	for _, scene := range addedScenes {
		if scene == "" || existing[scene] {
			continue
		}
		exitList := make([]any, 0, len(existing))
		for _, e := range scenario {
			if name := sceneNameOfScenario(e); name != "" {
				exitList = append(exitList, name)
			}
		}
		idx := len(scenario)
		newEntry := map[string]any{
			"enter":             []any{scene},
			"exit":              exitList,
			"activeIndicator":   activeIndicator(idx),
			"inactiveIndicator": inactiveIndicator(idx),
			"life":              0,
		}
		// Update prior scenarios' exit lists to include the new scene.
		for _, e := range scenario {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			exits, _ := m["exit"].([]any)
			m["exit"] = append(exits, scene)
		}
		scenario = append(scenario, newEntry)
		existing[scene] = true
		changed = true
	}

	if !changed && len(scenario) == originalLen {
		return nil, false
	}
	cfg["scenario"] = scenario
	updated := *scenes2
	updated.Config = cfg
	return &updated, true
}

func sceneNameOfScenario(entry any) string {
	m, ok := entry.(map[string]any)
	if !ok {
		return ""
	}
	enters, ok := m["enter"].([]any)
	if !ok || len(enters) == 0 {
		return ""
	}
	switch v := enters[0].(type) {
	case string:
		return v
	case map[string]any:
		if role, ok := v["role"].(string); ok {
			return role
		}
	}
	return ""
}

func existingSceneNames(scenario []any) map[string]bool {
	out := map[string]bool{}
	for _, entry := range scenario {
		if name := sceneNameOfScenario(entry); name != "" {
			out[name] = true
		}
	}
	return out
}

func findByName(modules []mmconfig.Module, name string) *mmconfig.Module {
	for i := range modules {
		if modules[i].Module == name {
			return &modules[i]
		}
	}
	return nil
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func nestedMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		clone := cloneMap(existing)
		parent[key] = clone
		return clone
	}
	created := map[string]any{}
	parent[key] = created
	return created
}

func stringSet(list []string) map[string]struct{} {
	out := make(map[string]struct{}, len(list))
	for _, s := range list {
		if s == "" {
			continue
		}
		out[s] = struct{}{}
	}
	return out
}

// Active circled numbers: ❶❷❸❹❺❻❼❽❾❿ (U+2776..U+277F).
var activeGlyphs = []rune{'❶', '❷', '❸', '❹', '❺', '❻', '❼', '❽', '❾', '❿'}

// Inactive circled numbers: ①②③④⑤⑥⑦⑧⑨⑩ (U+2460..U+2469).
var inactiveGlyphs = []rune{'①', '②', '③', '④', '⑤', '⑥', '⑦', '⑧', '⑨', '⑩'}

func activeIndicator(idx int) string {
	if idx < len(activeGlyphs) {
		return string(activeGlyphs[idx])
	}
	return fmt.Sprintf("(%d)", idx+1)
}

func inactiveIndicator(idx int) string {
	if idx < len(inactiveGlyphs) {
		return string(inactiveGlyphs[idx])
	}
	return fmt.Sprintf("%d.", idx+1)
}
