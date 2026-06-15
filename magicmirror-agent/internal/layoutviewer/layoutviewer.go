// Package layoutviewer serves a read-only HTML view of the MagicMirror
// layout the live config.js is using (HOM-92), plus the L4 drag/resize
// editor on top (HOM-93). When a working-copy document exists, the viewer
// renders it instead of the live layout — so the user sees the in-flight
// edits without any restart of MM until L6 wires up live preview.
//
// Like the family portal (internal/portal), this is data-plane only — the
// agent already binds to the LAN/tailnet and the auth boundary lives on the
// /api/v1 control plane, not here.
package layoutviewer

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
	"github.com/gin-gonic/gin"
)

//go:embed web/index.html
var indexHTML []byte

// Register mounts the viewer page, the read-only state endpoint, the L4
// working-copy lifecycle endpoints (GET/PUT/DELETE), the L5 Terraform diff
// emitter, the L6 live preview/revert endpoints, and the durable HOM-96
// Save action. Pass an empty workingCopyPath to keep the page read-only.
func Register(router *gin.Engine, mm *mmconfig.Manager, workingCopyPath, configPath, modulesTfPath string) {
	store := &workingCopyStore{path: workingCopyPath}
	var preview *PreviewStore
	if configPath != "" && workingCopyPath != "" {
		preview = NewPreviewStore(configPath, mm)
	}

	page := func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	}
	router.GET("/layout", page)
	router.GET("/layout/", page)

	router.GET("/layout/api/state", func(c *gin.Context) {
		cfg, err := mm.ReadConfig()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		wc, _ := store.read()
		state := buildState(cfg.Modules, wc)
		if preview != nil {
			active, meta := preview.Active()
			state.PreviewActive = active
			if meta != nil {
				state.PreviewStartedAt = meta.StartedAt.Format(time.RFC3339)
				state.PreviewDeadline = meta.Deadline.Format(time.RFC3339)
			}
		}
		c.JSON(http.StatusOK, state)
	})

	if workingCopyPath == "" {
		return
	}

	router.GET("/layout/api/working-copy", func(c *gin.Context) {
		wc, err := store.read()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				c.Status(http.StatusNoContent)
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, wc)
	})

	router.PUT("/layout/api/working-copy", func(c *gin.Context) {
		var in WorkingCopy
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: " + err.Error()})
			return
		}
		// Stamp on the server so the client doesn't need a clock.
		in.SavedAt = time.Now().UTC().Format(time.RFC3339)
		in.Version = 1
		if err := validateWorkingCopy(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := store.write(&in); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, in)
	})

	router.DELETE("/layout/api/working-copy", func(c *gin.Context) {
		if err := store.remove(); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	})

	// HOM-95 L6 — live preview / revert. POST /preview applies the working
	// copy to the running mirror via mmconfig + pm2 restart, with a 30-min
	// auto-revert guardrail. POST /discard-preview restores the snapshot.
	if preview != nil {
		router.POST("/layout/api/preview", func(c *gin.Context) {
			wc, err := store.read()
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					c.JSON(http.StatusBadRequest, gin.H{"error": "no working copy to preview — make some edits first"})
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if err := preview.Preview(wc); err != nil {
				if errors.Is(err, ErrPreviewAlreadyActive) {
					c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			active, meta := preview.Active()
			out := gin.H{"previewActive": active}
			if meta != nil {
				out["startedAt"] = meta.StartedAt.Format(time.RFC3339)
				out["deadline"] = meta.Deadline.Format(time.RFC3339)
			}
			c.JSON(http.StatusOK, out)
		})

		router.POST("/layout/api/discard-preview", func(c *gin.Context) {
			if err := preview.Discard(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"previewActive": false})
		})

		// HOM-96: durable Save. Writes the Pi-resident modules.tf and the
		// live config.js, restarts MM, and cleans up working-copy + preview
		// state. The non-technical path — never asks the user to paste HCL.
		if modulesTfPath != "" {
			router.POST("/layout/api/save", func(c *gin.Context) {
				wc, err := store.read()
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						c.JSON(http.StatusBadRequest, gin.H{"error": "no working copy to save — make some edits first"})
						return
					}
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				result, err := performSave(modulesTfPath, wc, mm, preview, store)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				c.JSON(http.StatusOK, result)
			})
		}

		// Called after the user has committed the working copy via Terraform.
		// Wipes the preview snapshot + auto-revert timer + working-copy file
		// without touching the live config.js — Terraform already brought
		// modules.tf and config.js into sync.
		router.POST("/layout/api/finalize-preview", func(c *gin.Context) {
			if err := preview.Finalize(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			// Also clear the working copy so the editor returns to "no edits"
			// state on next reload — they're durable now.
			_ = store.remove()
			c.JSON(http.StatusOK, gin.H{"previewActive": false, "hasWorkingCopy": false})
		})
	}

	// HOM-94 L5 — emit a Terraform diff for modules.tf from the working-copy
	// doc. The agent doesn't see modules.tf (it lives in the dev repo), so
	// the editor uploads the file content with each request.
	router.POST("/layout/api/emit-terraform", func(c *gin.Context) {
		var in struct {
			ModulesTf string `json:"modulesTf"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: " + err.Error()})
			return
		}
		if strings.TrimSpace(in.ModulesTf) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "modulesTf content is required"})
			return
		}
		wc, err := store.read()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "no working copy to emit — make some edits first"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		cfg, err := mm.ReadConfig()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		result, err := emitTerraform([]byte(in.ModulesTf), wc, cfg.Modules)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	})
}

// WorkingCopy is the editor's in-flight layout document.
//
// `layout` conforms to _docs/layout-schema.schema.json — that's the part L5
// will emit as HCL for MMM-LayoutBounds. `pendingPositions` is the editor's
// extension carrying drag-to-move results (module ID → new region) so the
// schema-conforming bit stays exactly that. `moduleConfigs` (HOM-99) maps
// module ID → arbitrary config patch the editor wants merged into both the
// live config.js and the canonical modules.tf on save.
type WorkingCopy struct {
	Version          int                       `json:"version"`
	SavedAt          string                    `json:"savedAt,omitempty"`
	Layout           map[string]any            `json:"layout"`
	PendingPositions map[string]string         `json:"pendingPositions,omitempty"`
	ModuleConfigs    map[string]map[string]any `json:"moduleConfigs,omitempty"`
}

type workingCopyStore struct {
	path string
	mu   sync.Mutex
}

func (s *workingCopyStore) read() (*WorkingCopy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var wc WorkingCopy
	if err := json.Unmarshal(data, &wc); err != nil {
		return nil, err
	}
	return &wc, nil
}

func (s *workingCopyStore) write(wc *WorkingCopy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(wc, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "layout.json.tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
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
	return os.Rename(tmpPath, s.path)
}

func (s *workingCopyStore) remove() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.Remove(s.path)
}

// ---------- validator (hand-rolled, mirrors _docs/layout-schema.schema.json) ----------
//
// A small hand-written validator beats pulling in an ajv-equivalent Go lib
// for a 92-line schema: stable shape, edits to either side stay obvious.

var (
	cssLengthPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(px|vh|vw|em|rem|%)$`)
	validOverflow    = map[string]bool{"hidden": true, "visible": true, "scroll": true}
)

func validateWorkingCopy(wc *WorkingCopy) error {
	if wc == nil {
		return errors.New("working copy is required")
	}
	if wc.Layout == nil {
		return errors.New("working copy is missing 'layout'")
	}
	if err := validateLayout(wc.Layout); err != nil {
		return err
	}
	for moduleID, pos := range wc.PendingPositions {
		if moduleID == "" {
			return errors.New("pendingPositions has an empty key")
		}
		if !knownRegion(pos) {
			return errors.New("pendingPositions[" + moduleID + "]: unknown region '" + pos + "'")
		}
	}
	// HOM-99: moduleConfigs accepts arbitrary key/value patches keyed by
	// module ID. Shape-check only — semantic validity is the responsibility
	// of the module itself, since we don't know every module's config schema.
	for moduleID := range wc.ModuleConfigs {
		if moduleID == "" {
			return errors.New("moduleConfigs has an empty key")
		}
	}
	return nil
}

func validateLayout(layout map[string]any) error {
	if v, ok := layout["version"].(float64); !ok || int(v) != 1 {
		return errors.New("layout.version must be 1")
	}

	regions, ok := layout["regions"].(map[string]any)
	if !ok {
		return errors.New("layout.regions must be an object")
	}
	for id, raw := range regions {
		if !knownRegion(id) {
			return errors.New("layout.regions['" + id + "']: unknown region id")
		}
		if raw == nil {
			continue
		}
		obj, ok := raw.(map[string]any)
		if !ok {
			return errors.New("layout.regions['" + id + "']: must be null or an object")
		}
		mh, ok := obj["maxHeight"].(string)
		if !ok || mh == "" {
			return errors.New("layout.regions['" + id + "']: 'maxHeight' is required")
		}
		if !cssLengthPattern.MatchString(mh) {
			return errors.New("layout.regions['" + id + "'].maxHeight: '" + mh + "' is not a valid CSS length")
		}
		if mw, ok := obj["maxWidth"].(string); ok && mw != "" {
			if !cssLengthPattern.MatchString(mw) {
				return errors.New("layout.regions['" + id + "'].maxWidth: '" + mw + "' is not a valid CSS length")
			}
		}
		if of, ok := obj["overflow"].(string); ok && of != "" {
			if !validOverflow[of] {
				return errors.New("layout.regions['" + id + "'].overflow: must be hidden|visible|scroll")
			}
		}
		for k := range obj {
			switch k {
			case "maxHeight", "maxWidth", "overflow":
			default:
				return errors.New("layout.regions['" + id + "']: unknown property '" + k + "'")
			}
		}
	}

	if overridesRaw, ok := layout["moduleOverrides"]; ok && overridesRaw != nil {
		overrides, ok := overridesRaw.([]any)
		if !ok {
			return errors.New("layout.moduleOverrides must be an array")
		}
		for i, item := range overrides {
			obj, ok := item.(map[string]any)
			if !ok {
				return errors.New("layout.moduleOverrides[" + itoa(i) + "]: must be an object")
			}
			match, ok := obj["match"].(map[string]any)
			if !ok {
				return errors.New("layout.moduleOverrides[" + itoa(i) + "]: 'match' is required")
			}
			mod, ok := match["module"].(string)
			if !ok || mod == "" {
				return errors.New("layout.moduleOverrides[" + itoa(i) + "].match.module: required")
			}
			if reg, ok := match["region"].(string); ok && reg != "" && !knownRegion(reg) {
				return errors.New("layout.moduleOverrides[" + itoa(i) + "].match.region: unknown region '" + reg + "'")
			}
			// HOM-99: optional per-module CSS bounds + contain.
			if mh, ok := obj["maxHeight"].(string); ok && mh != "" && !cssLengthPattern.MatchString(mh) {
				return errors.New("layout.moduleOverrides[" + itoa(i) + "].maxHeight: '" + mh + "' is not a valid CSS length")
			}
			if mw, ok := obj["maxWidth"].(string); ok && mw != "" && !cssLengthPattern.MatchString(mw) {
				return errors.New("layout.moduleOverrides[" + itoa(i) + "].maxWidth: '" + mw + "' is not a valid CSS length")
			}
			if cm, ok := obj["containMode"].(string); ok && cm != "" {
				switch cm {
				case "paint", "layout", "size", "off":
				default:
					return errors.New("layout.moduleOverrides[" + itoa(i) + "].containMode: must be paint|layout|size|off")
				}
			}
		}
	}
	return nil
}

func knownRegion(id string) bool {
	for _, r := range regionOrder {
		if r == id {
			return true
		}
	}
	return false
}

func itoa(i int) string {
	// Avoid pulling strconv for one call; identical encoding for small i.
	if i < 0 {
		return "-" + itoa(-i)
	}
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + itoa(i%10)
}

// ---------- state builder ----------

// State is the JSON payload the viewer renders.
type State struct {
	Regions          []RegionState     `json:"regions"`
	ModuleOverrides  []ModuleOverride  `json:"moduleOverrides"`
	HasLayoutDoc     bool              `json:"hasLayoutDoc"`
	HasWorkingCopy   bool              `json:"hasWorkingCopy"`
	WorkingCopyAt    string            `json:"workingCopyAt,omitempty"`
	Viewport         Viewport          `json:"viewport"`
	PreviewActive    bool              `json:"previewActive"`
	PreviewStartedAt string            `json:"previewStartedAt,omitempty"`
	PreviewDeadline  string            `json:"previewDeadline,omitempty"`
}

// RegionState carries everything the viewer needs to draw one region row.
type RegionState struct {
	ID        string        `json:"id"`
	MaxHeight string        `json:"maxHeight,omitempty"`
	Overflow  string        `json:"overflow,omitempty"`
	Capped    bool          `json:"capped"`
	Suspended bool          `json:"suspended"`
	Reason    string        `json:"reason,omitempty"`
	Modules   []ModuleEntry `json:"modules"`
}

type ModuleEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Classes  string `json:"classes,omitempty"`
	Header   string `json:"header,omitempty"`
	// Moved=true when the working-copy has reassigned this module from its
	// live position. The viewer renders these chips with a "pending" badge.
	Moved bool `json:"moved,omitempty"`
}

type ModuleOverride struct {
	Module string `json:"module"`
	Region string `json:"region,omitempty"`
	Exempt bool   `json:"exempt"`
}

type Viewport struct {
	Width         int `json:"width"`
	Height        int `json:"height"`
	BottomReserve int `json:"bottomReserve"`
}

// schemaDefault is the per-region maxHeight from _docs/layout-schema.md.
// Used when no layout doc is in play (neither working-copy nor live).
var schemaDefault = map[string]string{
	"top_bar":       "60px",
	"top_left":      "480px",
	"top_center":    "480px",
	"top_right":     "480px",
	"upper_third":   "220px",
	"middle_center": "480px",
	"lower_third":   "200px",
	"bottom_left":   "280px",
	"bottom_center": "280px",
	"bottom_right":  "280px",
	"bottom_bar":    "80px",
}

// regionOrder fixes the render order so the page reads top → bottom no
// matter what order the layout doc lists regions in.
var regionOrder = []string{
	"top_bar",
	"top_left", "top_center", "top_right",
	"upper_third",
	"middle_center",
	"lower_third",
	"bottom_left", "bottom_center", "bottom_right",
	"bottom_bar",
	"fullscreen_above", "fullscreen_below",
}

// autoExempt mirrors MMM-LayoutBounds.AUTO_EXEMPT: fullscreen overlays own
// the whole viewport when active and are never capped.
var autoExempt = map[string]bool{
	"fullscreen_above": true,
	"fullscreen_below": true,
}

func buildState(modules []mmconfig.Module, wc *WorkingCopy) State {
	// Prefer the working-copy's layout doc when it's present; the live one
	// is the read fallback so the page never has to redraw on save.
	liveLayout, hasLive := extractLayoutDoc(modules)
	var layout map[string]any
	hasDoc := false
	if wc != nil && wc.Layout != nil {
		layout = wc.Layout
		hasDoc = true
	} else if hasLive {
		layout = liveLayout
		hasDoc = true
	}

	overrides := extractOverrides(layout)

	// Apply pending position moves to a per-region grouping.
	modulesByRegion := map[string][]ModuleEntry{}
	pending := map[string]string{}
	if wc != nil {
		pending = wc.PendingPositions
	}
	for _, m := range modules {
		if m.Module == "MMM-LayoutBounds" {
			// Invisible utility module — keeps the map clean.
			continue
		}
		pos := m.Position
		moved := false
		if newPos, ok := pending[m.ID]; ok && newPos != "" && newPos != pos {
			pos = newPos
			moved = true
		}
		if pos == "" {
			continue
		}
		modulesByRegion[pos] = append(modulesByRegion[pos], ModuleEntry{
			ID:      m.ID,
			Name:    m.Module,
			Classes: m.Classes,
			Header:  m.Header,
			Moved:   moved,
		})
	}

	// Group exempt modules per region — matches MMM-LayoutBounds.computeExemptRegions.
	// Use the post-move positions so dragging a chip in/out of a fullscreen
	// overlay correctly flips the destination region's suspension state.
	exemptModules := map[string]map[string]bool{}
	noteExempt := func(region, name string) {
		if region == "" || name == "" {
			return
		}
		set, ok := exemptModules[region]
		if !ok {
			set = map[string]bool{}
			exemptModules[region] = set
		}
		set[name] = true
	}
	for region, mods := range modulesByRegion {
		if !autoExempt[region] {
			continue
		}
		for _, m := range mods {
			noteExempt(region, m.Name)
		}
	}
	for _, ov := range overrides {
		if !ov.Exempt {
			continue
		}
		if ov.Region != "" {
			noteExempt(ov.Region, ov.Module)
			continue
		}
		for region, mods := range modulesByRegion {
			for _, m := range mods {
				if m.Name == ov.Module {
					noteExempt(region, ov.Module)
				}
			}
		}
	}

	regions := make([]RegionState, 0, len(regionOrder))
	for _, id := range regionOrder {
		rs := RegionState{
			ID:      id,
			Modules: modulesByRegion[id],
		}

		if autoExempt[id] {
			rs.Suspended = true
			rs.Reason = "fullscreen overlay (auto-exempt)"
			regions = append(regions, rs)
			continue
		}

		rule, hasRule := layoutRegion(layout, id)
		switch {
		case hasDoc && !hasRule:
			if len(rs.Modules) == 0 {
				continue
			}
			rs.Reason = "no cap declared"
		case hasDoc && rule == nil:
			rs.Reason = "explicitly uncapped (null)"
		case hasDoc && rule != nil:
			rs.MaxHeight = stringField(rule, "maxHeight")
			rs.Overflow = stringField(rule, "overflow")
			if rs.Overflow == "" {
				rs.Overflow = "hidden"
			}
			rs.Capped = rs.MaxHeight != ""
		default:
			if def, ok := schemaDefault[id]; ok {
				rs.MaxHeight = def
				rs.Overflow = "hidden"
				rs.Capped = true
				rs.Reason = "schema default"
			} else if len(rs.Modules) == 0 {
				continue
			}
		}

		if cap := exemptModules[id]; len(cap) > 0 {
			rs.Suspended = true
			names := make([]string, 0, len(cap))
			for n := range cap {
				names = append(names, n)
			}
			sort.Strings(names)
			rs.Reason = "cap suspended — exempt: " + joinNames(names)
		}

		regions = append(regions, rs)
	}

	state := State{
		Regions:         regions,
		ModuleOverrides: overrides,
		HasLayoutDoc:    hasDoc,
		Viewport: Viewport{
			Width:         1080,
			Height:        1920,
			BottomReserve: 140,
		},
	}
	if wc != nil {
		state.HasWorkingCopy = true
		state.WorkingCopyAt = wc.SavedAt
	}
	return state
}

func extractLayoutDoc(modules []mmconfig.Module) (map[string]any, bool) {
	for _, m := range modules {
		if m.Module != "MMM-LayoutBounds" {
			continue
		}
		if m.Config == nil {
			return nil, false
		}
		layout, ok := m.Config["layout"].(map[string]any)
		if !ok {
			return nil, false
		}
		return layout, true
	}
	return nil, false
}

func layoutRegion(layout map[string]any, id string) (map[string]any, bool) {
	if layout == nil {
		return nil, false
	}
	regions, ok := layout["regions"].(map[string]any)
	if !ok {
		return nil, false
	}
	raw, ok := regions[id]
	if !ok {
		return nil, false
	}
	if raw == nil {
		return nil, true
	}
	obj, _ := raw.(map[string]any)
	return obj, true
}

func extractOverrides(layout map[string]any) []ModuleOverride {
	if layout == nil {
		return nil
	}
	raw, ok := layout["moduleOverrides"].([]any)
	if !ok {
		return nil
	}
	out := make([]ModuleOverride, 0, len(raw))
	for _, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		match, _ := obj["match"].(map[string]any)
		ov := ModuleOverride{
			Module: stringField(match, "module"),
			Region: stringField(match, "region"),
		}
		if v, ok := obj["exempt"].(bool); ok {
			ov.Exempt = v
		}
		if ov.Module == "" {
			continue
		}
		out = append(out, ov)
	}
	return out
}

func stringField(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	v, _ := m[k].(string)
	return v
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
