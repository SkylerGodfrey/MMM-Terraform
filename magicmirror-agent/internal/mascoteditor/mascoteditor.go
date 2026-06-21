// Package mascoteditor serves the MMM-Mascot sprite-layout editor
// (HOM-123) at /mascot. Mirrors the canvaseditor pattern: SPA embedded
// in the binary so the family-Pi deploy is one systemd unit, no Node
// install dance.
//
// The editor's save flow writes BOTH the mascot-layout.json (the live
// document MMM-Mascot reads via fs.watch) AND a Pi-resident mascots.tf
// that mirrors the document as `magicmirror_mascot_layout` HCL — the
// IaC reproducibility guarantee the workspace [[terraform-managed-state]]
// convention requires.
//
// Auth: /mascot inherits the family-portal data-plane stance — LAN-only
// by deployment, no Bearer secret on the editor surface. The save
// handler uses the in-process mascot.Store directly so it doesn't need
// the API key the public /api/v1/* routes require.
package mascoteditor

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mascot"
	"github.com/gin-gonic/gin"
)

//go:embed web/index.html
var indexHTML []byte

// Register mounts the editor page + its API endpoints under /mascot.
//
// store is the in-process mascot document store; spritesDir is scanned
// for available sprite catalog entries; mascotsTfPath is where the
// regenerated HCL mirror lands on Save (empty disables the .tf emit).
func Register(router *gin.Engine, store *mascot.Store, spritesDir, mascotsTfPath string) {
	h := &handlers{store: store, spritesDir: spritesDir, mascotsTfPath: mascotsTfPath}

	page := func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	}
	router.GET("/mascot", page)
	router.GET("/mascot/", page)

	router.GET("/mascot/api/state", h.getState)
	router.POST("/mascot/api/save", h.postSave)
	router.GET("/mascot/api/sprites", h.getCatalog)

	// Serve the sprite assets so the editor can render a live animated
	// preview of each tag (HOM-117). Same files MagicMirror serves on
	// :8080, but the editor lives on :8484 — exposing them here keeps the
	// preview same-origin (no canvas-taint, no CORS dance).
	if spritesDir != "" {
		router.Static("/mascot/sprites", spritesDir)
		// Phase 2 (HOM-117): upload/slice/export endpoints only make sense
		// when there's a sprites dir to write into.
		h.registerImport(router)
	}
}

type handlers struct {
	store         *mascot.Store
	spritesDir    string
	mascotsTfPath string
}

// stateResponse is what the editor hydrates from on page load.
type stateResponse struct {
	Document   mascot.Document `json:"document"`
	Catalog    []catalogEntry  `json:"catalog"`
	MascotsTf  tfFileState     `json:"mascotsTf"`
	SpritesDir string          `json:"spritesDir"`
}

// catalogEntry is one sprite available on disk. States lists the per-
// state assets present (default, halloween, …) so the editor can show
// users which holiday skins exist; each carries the animation tags found
// in its Aseprite JSON so the rotation UI can offer them (HOM-117).
type catalogEntry struct {
	ID     string       `json:"id"`
	States []stateEntry `json:"states"`
}

// stateEntry is one skin (default, halloween, …) and the animation tags
// declared in its JSON's meta.frameTags. Tags drive the per-sprite
// rotation picker.
type stateEntry struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

type tfFileState struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

func (h *handlers) getState(c *gin.Context) {
	doc, err := h.store.Load()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	catalog, err := scanCatalog(h.spritesDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "scan sprites dir: " + err.Error()})
		return
	}
	tfExists := false
	if h.mascotsTfPath != "" {
		if _, err := os.Stat(h.mascotsTfPath); err == nil {
			tfExists = true
		}
	}
	c.JSON(http.StatusOK, stateResponse{
		Document:   doc,
		Catalog:    catalog,
		SpritesDir: h.spritesDir,
		MascotsTf: tfFileState{
			Path:   h.mascotsTfPath,
			Exists: tfExists,
		},
	})
}

// getCatalog is a lightweight catalog-only refresh, useful when the
// user has just added new sprite assets to the Pi and wants the picker
// to pick them up without reloading the editor.
func (h *handlers) getCatalog(c *gin.Context) {
	catalog, err := scanCatalog(h.spritesDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"catalog": catalog})
}

// saveRequest is the editor's full local state on Save. The handler
// treats the incoming document as the desired state and replaces the
// stored document atomically via SaveDocument.
type saveRequest struct {
	Canvas   mascot.Canvas    `json:"canvas"`
	Sprites  []mascot.Sprite  `json:"sprites"`
	Holidays []mascot.Holiday `json:"holidays"`
}

type saveResponse struct {
	Sprites        int    `json:"sprites"`
	Holidays       int    `json:"holidays"`
	MascotsTfPath  string `json:"mascotsTfPath"`
	MascotsTfBytes int    `json:"mascotsTfBytes"`
}

func (h *handlers) postSave(c *gin.Context) {
	var req saveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.Sprites == nil {
		req.Sprites = []mascot.Sprite{}
	}
	if req.Holidays == nil {
		req.Holidays = []mascot.Holiday{}
	}

	// Validate that every rotation only names animation tags that exist on
	// disk. The store can't do this (it has no filesystem access by
	// design), so we check it here against a fresh catalog scan before
	// persisting. A typo'd tag would otherwise save fine and silently fall
	// back to idle on the mirror.
	if err := h.validateRotationTags(req.Sprites); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	doc := mascot.Document{
		Canvas:   req.Canvas,
		Sprites:  req.Sprites,
		Holidays: req.Holidays,
	}
	if _, err := h.store.SaveDocument(doc); err != nil {
		h.mascotError(c, err)
		return
	}

	out := saveResponse{
		Sprites:  len(req.Sprites),
		Holidays: len(req.Holidays),
	}
	if h.mascotsTfPath != "" {
		hcl := emitMascotsTf(req.Canvas, req.Sprites, req.Holidays)
		if err := writeAtomic(h.mascotsTfPath, []byte(hcl), 0o644); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "write mascots.tf: " + err.Error()})
			return
		}
		out.MascotsTfPath = h.mascotsTfPath
		out.MascotsTfBytes = len(hcl)
	}
	c.JSON(http.StatusOK, out)
}

// validateRotationTags rejects a save whose rotation names a tag absent
// from the sprite's on-disk animations. It is lenient when the sprite's
// assets aren't in the catalog yet (mid-rsync): it can't prove the tag
// missing, so it allows the save and lets the runtime fall back to idle.
func (h *handlers) validateRotationTags(sprites []mascot.Sprite) error {
	catalog, err := scanCatalog(h.spritesDir)
	if err != nil {
		return fmt.Errorf("scan sprites dir: %w", err)
	}
	tags := catalogTags(catalog)
	for _, s := range sprites {
		if s.Rotation == nil {
			continue
		}
		known, ok := tags[s.Sprite]
		if !ok {
			continue // assets not deployed yet — defer to runtime fallback
		}
		for _, a := range s.Rotation.Animations {
			if _, found := known[a]; !found {
				return fmt.Errorf("%w: sprite %q has no animation %q (available: %s)",
					mascot.ErrInvalidRotation, s.Sprite, a, strings.Join(sortedKeys(known), ", "))
			}
		}
	}
	return nil
}

func (h *handlers) mascotError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, mascot.ErrInvalidSprite),
		errors.Is(err, mascot.ErrInvalidHoliday),
		errors.Is(err, mascot.ErrInvalidRotation),
		errors.Is(err, mascot.ErrDuplicateSpriteID):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

// scanCatalog inspects spritesDir (typically <MM>/modules/MMM-Mascot/sprites)
// and reports the sprite ids plus which state files are present. Missing
// dir returns an empty catalog (the agent may run before the module is
// rsynced over).
func scanCatalog(dir string) ([]catalogEntry, error) {
	if dir == "" {
		return []catalogEntry{}, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []catalogEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]catalogEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || name[0] == '_' || name[0] == '.' {
			continue
		}
		states, err := collectStates(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if len(states) == 0 {
			continue
		}
		out = append(out, catalogEntry{ID: name, States: states})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// collectStates lists each skin in a sprite directory and the animation
// tags declared in its JSON. A state is "present" when it has at least a
// .png or .json; tags come from the JSON's meta.frameTags (best-effort —
// a missing or unparseable JSON yields no tags rather than an error, so
// one broken file doesn't blank the whole catalog).
func collectStates(dir string) ([]stateEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := filepath.Ext(name)
		if ext != ".png" && ext != ".json" {
			continue
		}
		seen[name[:len(name)-len(ext)]] = struct{}{}
	}
	out := make([]stateEntry, 0, len(seen))
	for state := range seen {
		out = append(out, stateEntry{
			Name: state,
			Tags: readFrameTags(filepath.Join(dir, state+".json")),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// readFrameTags pulls the animation tag names out of an Aseprite "Array"
// JSON (meta.frameTags[].name). Returns nil on any read/parse failure —
// the rotation picker simply shows no tags for that skin.
func readFrameTags(jsonPath string) []string {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil
	}
	var parsed struct {
		Meta struct {
			FrameTags []struct {
				Name string `json:"name"`
			} `json:"frameTags"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	tags := make([]string, 0, len(parsed.Meta.FrameTags))
	for _, t := range parsed.Meta.FrameTags {
		if t.Name != "" {
			tags = append(tags, t.Name)
		}
	}
	return tags
}

// sortedKeys returns a set's keys in sorted order, for stable error text.
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// catalogTags flattens a catalog into a sprite-id → set-of-tags map (the
// union of tags across all the sprite's skins). Used to validate that a
// rotation only names animations that actually exist on disk.
func catalogTags(catalog []catalogEntry) map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{}, len(catalog))
	for _, e := range catalog {
		set := map[string]struct{}{}
		for _, st := range e.States {
			for _, t := range st.Tags {
				set[t] = struct{}{}
			}
		}
		out[e.ID] = set
	}
	return out
}

// writeAtomic mirrors the canvaseditor helper. Duplicated rather than
// pulled into a shared util pkg for the same reason canvaseditor does
// it: shared-util friction outweighs the 20-line helper.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
