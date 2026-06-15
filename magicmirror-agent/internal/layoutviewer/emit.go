package layoutviewer

// HOM-94 L5: translate a working-copy layout document into a minimal HCL
// diff against my-mirror/modules.tf. The user reviews the diff, paste-or-
// patches it into their working tree, and commits — no automatic writes
// from the agent.
//
// hclwrite preserves byte positions for unchanged content, so as long as we
// only mutate the attributes we care about (per-module `position` and the
// layout_bounds `config`), unrelated bytes (comments, formatting, attribute
// order) survive untouched and the diff is naturally minimal.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// EmitResult is what the endpoint hands back to the editor. NewContent is
// the modules.tf the user should commit; Diff is a unified-diff text the UI
// renders inline; Summary is a structured rundown for the toast/banner.
type EmitResult struct {
	NewContent string        `json:"newContent"`
	Diff       string        `json:"diff"`
	Summary    EmitSummary   `json:"summary"`
	Changes    []EmitChange  `json:"changes"`
	Warnings   []string      `json:"warnings,omitempty"`
}

// EmitSummary is a numeric breakdown the UI shows next to the diff.
type EmitSummary struct {
	PositionMoves     int `json:"positionMoves"`
	LayoutBoundsTouched bool `json:"layoutBoundsTouched"`
	NoChange          bool `json:"noChange"`
}

// EmitChange describes one mutation the emitter applied. The UI lists these
// next to the diff so reviewers can map diff hunks → editor edits at a glance.
type EmitChange struct {
	Kind        string `json:"kind"` // "move" or "layout"
	ResourceTF  string `json:"resourceTf,omitempty"` // e.g. "magicmirror_module.photo_frame"
	Module      string `json:"module,omitempty"`
	FromRegion  string `json:"fromRegion,omitempty"`
	ToRegion    string `json:"toRegion,omitempty"`
	Description string `json:"description"`
}

// emitTerraform takes the live modules.tf, the working-copy doc, and the
// live module list (so pending position keys → physical module names) and
// returns a new modules.tf plus a unified diff.
func emitTerraform(modulesTf []byte, wc *WorkingCopy, liveModules []mmconfig.Module) (*EmitResult, error) {
	if wc == nil {
		return nil, errors.New("working copy is required")
	}

	original := append([]byte(nil), modulesTf...)
	file, diags := hclwrite.ParseConfig(modulesTf, "modules.tf", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse modules.tf: %s", diags.Error())
	}

	// Second parse with hclsyntax for value evaluation — hclwrite holds tokens
	// but doesn't expose evaluated string literals.
	syntaxFile, sDiags := hclsyntax.ParseConfig(modulesTf, "modules.tf", hcl.Pos{Line: 1, Column: 1})
	if sDiags.HasErrors() {
		return nil, fmt.Errorf("parse modules.tf (syntax): %s", sDiags.Error())
	}
	syntaxBody, ok := syntaxFile.Body.(*hclsyntax.Body)
	if !ok {
		return nil, errors.New("modules.tf body is not native HCL syntax")
	}

	// Index TF resources by their (module, position) tuple so pending moves
	// resolve to a single resource. Also track the resource label so we can
	// report which one we touched in the change list.
	type tfResource struct {
		Label        string // e.g. "photo_frame"
		ModuleName   string // e.g. "MMM-PhotoFrame"
		Position     string
		IsLayoutBounds bool
	}
	var allResources []*tfResource
	for _, block := range syntaxBody.Blocks {
		if block.Type != "resource" || len(block.Labels) < 2 || block.Labels[0] != "magicmirror_module" {
			continue
		}
		r := &tfResource{Label: block.Labels[1]}
		for name, attr := range block.Body.Attributes {
			switch name {
			case "module":
				if s, err := evalString(attr.Expr); err == nil {
					r.ModuleName = s
				}
			case "position":
				if s, err := evalString(attr.Expr); err == nil {
					r.Position = s
				}
			}
		}
		if r.ModuleName == "MMM-LayoutBounds" {
			r.IsLayoutBounds = true
		}
		allResources = append(allResources, r)
	}

	// Map of (module, position) → resource for move resolution.
	indexByModulePos := map[string][]*tfResource{}
	for _, r := range allResources {
		key := r.ModuleName + "@" + r.Position
		indexByModulePos[key] = append(indexByModulePos[key], r)
	}

	// Map live modules by ID so pendingPositions[id] resolves to (name, pos).
	liveByID := map[string]mmconfig.Module{}
	for _, m := range liveModules {
		liveByID[m.ID] = m
	}

	result := &EmitResult{}

	// --- Apply pending position moves ---
	// Iterate in a stable order so the diff (and the change list) reproduce
	// across runs even if Go's map iteration changes.
	pendingIDs := make([]string, 0, len(wc.PendingPositions))
	for id := range wc.PendingPositions {
		pendingIDs = append(pendingIDs, id)
	}
	sort.Strings(pendingIDs)

	for _, id := range pendingIDs {
		newRegion := wc.PendingPositions[id]
		live, ok := liveByID[id]
		if !ok {
			result.Warnings = append(result.Warnings, "pendingPositions["+id+"]: no matching live module (deleted?), skipping")
			continue
		}
		if live.Position == newRegion {
			// No-op move (user dragged back to original).
			continue
		}
		key := live.Module + "@" + live.Position
		candidates := indexByModulePos[key]
		switch len(candidates) {
		case 0:
			result.Warnings = append(result.Warnings, fmt.Sprintf("no TF resource for module %s in position %s — modules.tf may be out of sync", live.Module, live.Position))
			continue
		case 1:
			r := candidates[0]
			if err := setResourcePosition(file, r.Label, newRegion); err != nil {
				return nil, err
			}
			r.Position = newRegion // keep index in sync if multiple moves match later
			result.Changes = append(result.Changes, EmitChange{
				Kind:        "move",
				ResourceTF:  "magicmirror_module." + r.Label,
				Module:      live.Module,
				FromRegion:  live.Position,
				ToRegion:    newRegion,
				Description: fmt.Sprintf("magicmirror_module.%s.position: %s → %s", r.Label, live.Position, newRegion),
			})
			result.Summary.PositionMoves++
		default:
			result.Warnings = append(result.Warnings, fmt.Sprintf("ambiguous move for %s@%s — %d matching TF resources; resolve by hand", live.Module, live.Position, len(candidates)))
		}
	}

	// --- HOM-99: per-module config patches ---
	// Each entry in wc.ModuleConfigs shallow-merges into the live module's
	// existing config, and the merged config replaces the resource's
	// `config = jsonencode({...})` attribute in modules.tf. As with layout_
	// bounds, inline comments inside the regenerated block are lost.
	patchedIDs := make([]string, 0, len(wc.ModuleConfigs))
	for id := range wc.ModuleConfigs {
		patchedIDs = append(patchedIDs, id)
	}
	sort.Strings(patchedIDs)
	for _, id := range patchedIDs {
		patch := wc.ModuleConfigs[id]
		if len(patch) == 0 {
			continue
		}
		live, ok := liveByID[id]
		if !ok {
			result.Warnings = append(result.Warnings, "moduleConfigs["+id+"]: no matching live module, skipping")
			continue
		}
		// Use the post-move position so a TF resource match works even when
		// the user moved AND reconfigured the same module in one save.
		pos := live.Position
		if newPos, moved := wc.PendingPositions[id]; moved && newPos != "" {
			pos = newPos
		}
		key := live.Module + "@" + pos
		candidates := indexByModulePos[key]
		if len(candidates) != 1 {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("moduleConfigs[%s]: can't uniquely locate %s@%s (%d matches) — applied to live config only", id, live.Module, pos, len(candidates)))
			continue
		}
		r := candidates[0]
		merged := map[string]any{}
		for k, v := range live.Config {
			if k == "_terraform_managed" {
				continue
			}
			merged[k] = v
		}
		for k, v := range patch {
			merged[k] = v
		}
		expr, err := jsonencodeMapExpr(merged)
		if err != nil {
			return nil, err
		}
		if err := setResourceConfigRaw(file, r.Label, expr); err != nil {
			return nil, err
		}
		result.Changes = append(result.Changes, EmitChange{
			Kind:        "module-config",
			ResourceTF:  "magicmirror_module." + r.Label,
			Module:      live.Module,
			Description: "magicmirror_module." + r.Label + ".config patched (keys: " + joinSortedKeys(patch) + ")",
		})
	}

	// --- Regenerate layout_bounds config from working-copy layout ---
	// Compare what the working-copy says vs. what's already in modules.tf.
	// If they're equivalent, skip — keeps the identity case empty-diff.
	var layoutBounds *tfResource
	for _, r := range allResources {
		if r.IsLayoutBounds {
			layoutBounds = r
			break
		}
	}
	if layoutBounds != nil && wc.Layout != nil {
		// Pull the current config jsonencode body from the live resource via
		// mmconfig's parsing (we have the live module already). If the live
		// layout matches the working-copy, skip the rewrite.
		var liveLayoutForBounds map[string]any
		for _, m := range liveModules {
			if m.Module == "MMM-LayoutBounds" && m.Config != nil {
				liveLayoutForBounds, _ = m.Config["layout"].(map[string]any)
				break
			}
		}
		if !layoutsEqual(liveLayoutForBounds, wc.Layout) {
			expr, err := layoutBoundsConfigExpr(wc.Layout)
			if err != nil {
				return nil, err
			}
			if err := setResourceConfigRaw(file, layoutBounds.Label, expr); err != nil {
				return nil, err
			}
			result.Summary.LayoutBoundsTouched = true
			result.Changes = append(result.Changes, EmitChange{
				Kind:        "layout",
				ResourceTF:  "magicmirror_module." + layoutBounds.Label,
				Description: "magicmirror_module." + layoutBounds.Label + ".config.layout regenerated from working copy",
			})
			// L5 limitation: regenerating the config attribute can't preserve
			// inline comments inside the jsonencode body. Surgical sub-attribute
			// edits would, but they're scope-deferred until the user actually
			// asks for them. Flag the wipe so the reviewer doesn't lose context.
			result.Warnings = append(result.Warnings, "layout_bounds config regenerated — any inline comments inside the existing jsonencode block will be removed in the new modules.tf. Re-add them by hand after applying the diff if you want to keep them.")
		}
	}

	newContent := file.Bytes()
	result.NewContent = string(newContent)
	result.Diff = unifiedDiff(string(original), string(newContent), "modules.tf", "modules.tf")
	result.Summary.NoChange = result.Summary.PositionMoves == 0 && !result.Summary.LayoutBoundsTouched
	return result, nil
}

// evalString returns the static string value of an expression that's a
// quoted template with a single literal segment ("top_bar"). Anything else
// (variables, interpolations) returns an error — those resources just don't
// participate in the position index.
func evalString(expr hclsyntax.Expression) (string, error) {
	tpl, ok := expr.(*hclsyntax.TemplateExpr)
	if !ok {
		return "", errors.New("not a template expression")
	}
	if len(tpl.Parts) != 1 {
		return "", errors.New("template has multiple parts (interpolation?)")
	}
	lit, ok := tpl.Parts[0].(*hclsyntax.LiteralValueExpr)
	if !ok {
		return "", errors.New("template part is not a literal")
	}
	if lit.Val.Type() != cty.String {
		return "", errors.New("literal is not a string")
	}
	return lit.Val.AsString(), nil
}

// setResourcePosition finds the magicmirror_module.<label> block and sets
// its position attribute to newRegion. Comments and other attributes are
// preserved by hclwrite's token-level surgery.
func setResourcePosition(file *hclwrite.File, label, newRegion string) error {
	block := findModuleBlock(file, label)
	if block == nil {
		return fmt.Errorf("hclwrite: resource %q not found", label)
	}
	block.Body().SetAttributeValue("position", cty.StringVal(newRegion))
	return nil
}

// setResourceConfigRaw replaces the config attribute on a magicmirror_module
// resource with the tokens of the given expression (which encodes the new
// jsonencode({...}) call).
func setResourceConfigRaw(file *hclwrite.File, label string, exprTokens hclwrite.Tokens) error {
	block := findModuleBlock(file, label)
	if block == nil {
		return fmt.Errorf("hclwrite: resource %q not found", label)
	}
	block.Body().SetAttributeRaw("config", exprTokens)
	return nil
}

func findModuleBlock(file *hclwrite.File, label string) *hclwrite.Block {
	for _, b := range file.Body().Blocks() {
		if b.Type() != "resource" {
			continue
		}
		labels := b.Labels()
		if len(labels) < 2 || labels[0] != "magicmirror_module" {
			continue
		}
		if labels[1] == label {
			return b
		}
	}
	return nil
}

// layoutBoundsConfigExpr produces the hclwrite token stream for the layout
// bounds config attribute: jsonencode({ layout = { ... } }). The HCL is
// emitted in a stable order so the diff stays minimal across runs.
func layoutBoundsConfigExpr(layout map[string]any) (hclwrite.Tokens, error) {
	var b strings.Builder
	b.WriteString("jsonencode({\n")
	b.WriteString("    layout = {\n")
	b.WriteString("      version = 1\n")
	b.WriteString("      regions = {\n")

	regions, _ := layout["regions"].(map[string]any)
	for _, id := range regionOrder {
		raw, present := regions[id]
		if !present {
			continue
		}
		if raw == nil {
			fmt.Fprintf(&b, "        %s = null\n", id)
			continue
		}
		obj, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("region %s: expected object or null", id)
		}
		mh, _ := obj["maxHeight"].(string)
		of, _ := obj["overflow"].(string)
		mw, _ := obj["maxWidth"].(string)
		fmt.Fprintf(&b, "        %s = { maxHeight = %q", id, mh)
		if mw != "" {
			fmt.Fprintf(&b, ", maxWidth = %q", mw)
		}
		if of != "" {
			fmt.Fprintf(&b, ", overflow = %q", of)
		}
		b.WriteString(" }\n")
	}
	b.WriteString("      }\n")

	overrides, _ := layout["moduleOverrides"].([]any)
	b.WriteString("      moduleOverrides = [\n")
	for _, item := range overrides {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		match, _ := obj["match"].(map[string]any)
		mod, _ := match["module"].(string)
		reg, _ := match["region"].(string)
		exempt, _ := obj["exempt"].(bool)

		b.WriteString("        { match = { module = ")
		fmt.Fprintf(&b, "%q", mod)
		if reg != "" {
			fmt.Fprintf(&b, ", region = %q", reg)
		}
		b.WriteString(" }, exempt = ")
		if exempt {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
		b.WriteString(" },\n")
	}
	b.WriteString("      ]\n")
	b.WriteString("    }\n")
	b.WriteString("  })")

	wrapper := []byte("placeholder = " + b.String() + "\n")
	parsed, diags := hclwrite.ParseConfig(wrapper, "snippet", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return nil, fmt.Errorf("emit layout HCL: %s", diags.Error())
	}
	attr := parsed.Body().GetAttribute("placeholder")
	if attr == nil {
		return nil, errors.New("emit layout HCL: snippet parse lost attribute")
	}
	return attr.Expr().BuildTokens(nil), nil
}

// jsonencodeMapExpr wraps the map as `jsonencode({...})` HCL tokens. Used
// for per-module config patches (HOM-99) where the editor swaps the whole
// config attribute on a resource.
func jsonencodeMapExpr(m map[string]any) (hclwrite.Tokens, error) {
	var b strings.Builder
	b.WriteString("jsonencode(")
	writeHCLValue(&b, m, "  ")
	b.WriteString(")")
	wrapper := []byte("placeholder = " + b.String() + "\n")
	parsed, diags := hclwrite.ParseConfig(wrapper, "snippet", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return nil, fmt.Errorf("emit module config HCL: %s", diags.Error())
	}
	attr := parsed.Body().GetAttribute("placeholder")
	if attr == nil {
		return nil, errors.New("emit module config HCL: snippet parse lost attribute")
	}
	return attr.Expr().BuildTokens(nil), nil
}

// writeHCLValue serialises a Go value into HCL syntax. Handles the subset
// the parsed config.js produces (strings, float64, bool, []any, map[string]any
// — encoding/json's default Go types).
func writeHCLValue(b *strings.Builder, v any, indent string) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case string:
		fmt.Fprintf(b, "%q", x)
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case float64:
		// MM configs are mostly ints; emit without trailing zeros when whole.
		if x == float64(int64(x)) {
			fmt.Fprintf(b, "%d", int64(x))
		} else {
			fmt.Fprintf(b, "%g", x)
		}
	case int:
		fmt.Fprintf(b, "%d", x)
	case int64:
		fmt.Fprintf(b, "%d", x)
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[")
		for i, item := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeHCLValue(b, item, indent)
		}
		b.WriteString("]")
	case map[string]any:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		// Sort keys for deterministic output — keeps the diff stable across
		// runs the way `terraform fmt` would.
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("{\n")
		for _, k := range keys {
			fmt.Fprintf(b, "%s  %s = ", indent, k)
			writeHCLValue(b, x[k], indent+"  ")
			b.WriteString("\n")
		}
		fmt.Fprintf(b, "%s}", indent)
	default:
		// Fall back to JSON-as-string for anything unexpected so the user
		// at least sees what we got, instead of a panic.
		fmt.Fprintf(b, "%q", fmt.Sprintf("%v", v))
	}
}

// joinSortedKeys returns "a, b, c" for a map's keys, sorted.
func joinSortedKeys(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// layoutsEqual deep-compares two layout docs. Used to avoid rewriting the
// layout_bounds config when the working-copy doesn't actually differ.
func layoutsEqual(a, b map[string]any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return jsonString(a) == jsonString(b)
}

func jsonString(v any) string {
	// encoding/json sorts map keys, so equal documents serialize identically.
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
