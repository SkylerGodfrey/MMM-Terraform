// Package canvaseditor serves the Canvas v2 layout editor (HOM-108) at
// /canvas. The editor is a single-page app embedded in the agent binary
// so the family-Pi mirror needs no node/npm install dance — the agent's
// systemd unit is the only deploy step.
//
// The editor's save flow writes BOTH the canvas-layout.json (the live
// document MMM-Canvas reads) AND a Pi-resident pages.tf that mirrors the
// document as `magicmirror_canvas` + `magicmirror_page` HCL resources.
// The .tf file isn't run through `terraform apply` here — it's a durable
// IaC mirror so the human's `modules.tf` workflow stays usable. The
// canvas API endpoints already give MMM-Canvas live reflow without a
// restart (HOM-105 fs.watch); the .tf write is the reproducibility
// guarantee, not the propagation mechanism.
//
// Auth: /canvas inherits whatever the family-portal data plane uses
// (LAN-only by deployment, no /api/v1 secret on the editor surface).
// The underlying /api/v1/canvas and /api/v1/pages routes the editor
// posts back to DO require the Bearer token; the editor's save handler
// uses the in-process canvasStore directly so it doesn't need the key.
package canvaseditor

import (
	_ "embed"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/canvas"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/scenes2sync"
	"github.com/gin-gonic/gin"
)

//go:embed web/index.html
var indexHTML []byte

// Register mounts the editor page + its API endpoints under /canvas.
//
// canvasStore is the in-process layout store the editor posts back to;
// mm is used to enumerate available modules for the picker rail; the
// pagesTfPath is where we write the regenerated HCL each save.
func Register(router *gin.Engine, canvasStore *canvas.Store, mm *mmconfig.Manager, pagesTfPath string) {
	h := &handlers{store: canvasStore, mm: mm, pagesTfPath: pagesTfPath}

	page := func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	}
	router.GET("/canvas", page)
	router.GET("/canvas/", page)

	router.GET("/canvas/api/state", h.getState)
	router.POST("/canvas/api/save", h.postSave)
	// HOM-111 polish: debug border toggle (fast path) + modules CRUD
	// (Modules section). All in-process so the editor doesn't need the
	// Bearer key the public /api/v1 routes require.
	router.POST("/canvas/api/debug-toggle", h.postDebugToggle)
	router.POST("/canvas/api/modules", h.postModule)
	router.PUT("/canvas/api/modules/:id", h.putModule)
	router.DELETE("/canvas/api/modules/:id", h.deleteModule)
}

type handlers struct {
	store       *canvas.Store
	mm          *mmconfig.Manager
	pagesTfPath string
}

// stateResponse is what the editor hydrates from on page load. modules
// is a flat list of {id, module} entries so the picker rail can render
// without a second round-trip.
type stateResponse struct {
	Document canvas.Document  `json:"document"`
	Modules  []moduleSummary  `json:"modules"`
	PagesTf  pagesTfFileState `json:"pagesTf"`
}

type moduleSummary struct {
	ID       string         `json:"id"`
	Module   string         `json:"module"`
	Header   string         `json:"header,omitempty"`
	Position string         `json:"position,omitempty"`
	Classes  string         `json:"classes,omitempty"`
	Config   map[string]any `json:"config,omitempty"`
}

type pagesTfFileState struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

func (h *handlers) getState(c *gin.Context) {
	doc, err := h.store.Load()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	cfg, err := h.mm.ReadConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	mods := make([]moduleSummary, 0, len(cfg.Modules))
	for _, m := range cfg.Modules {
		mods = append(mods, moduleSummary{
			ID:       m.ID,
			Module:   m.Module,
			Header:   m.Header,
			Position: m.Position,
			Classes:  m.Classes,
			Config:   m.Config,
		})
	}
	pagesTfExists := false
	if h.pagesTfPath != "" {
		if _, err := os.Stat(h.pagesTfPath); err == nil {
			pagesTfExists = true
		}
	}
	c.JSON(http.StatusOK, stateResponse{
		Document: doc,
		Modules:  mods,
		PagesTf: pagesTfFileState{
			Path:   h.pagesTfPath,
			Exists: pagesTfExists,
		},
	})
}

// saveRequest is the editor's full local state on Save. The handler
// treats the incoming document as the desired state and replaces the
// stored canvas, sections, and pages atomically via SaveDocument.
// HOM-119 added Sections; pages absent from the request are removed.
type saveRequest struct {
	Canvas   canvas.Canvas             `json:"canvas"`
	Sections map[string]canvas.Section `json:"sections"`
	Pages    map[string]canvas.Page    `json:"pages"`
}

type saveResponse struct {
	PagesWritten    []string         `json:"pagesWritten"`
	PagesDeleted    []string         `json:"pagesDeleted"`
	SectionsWritten []string         `json:"sectionsWritten"`
	SectionsDeleted []string         `json:"sectionsDeleted"`
	PagesTfPath     string           `json:"pagesTfPath"`
	PagesTfBytes    int              `json:"pagesTfBytes"`
	Scenes2Sync     *scenes2SyncInfo `json:"scenes2Sync,omitempty"`
}

// HOM-127: surfaces the MMM-Scenes2 / MMM-Canvas auto-sync result so
// the editor can show a toast "added Scenes2 button for <page>".
type scenes2SyncInfo struct {
	AddedScenes   []string `json:"addedScenes,omitempty"`
	RemovedScenes []string `json:"removedScenes,omitempty"`
	CanvasUpdated bool     `json:"canvasUpdated"`
	ScenesUpdated bool     `json:"scenesUpdated"`
}

func (h *handlers) postSave(c *gin.Context) {
	var req saveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.Pages == nil {
		req.Pages = map[string]canvas.Page{}
	}
	if req.Sections == nil {
		req.Sections = map[string]canvas.Section{}
	}

	// Diff against the existing doc so the response can name what got
	// added/removed (purely for the editor's "wrote N pages, removed M
	// sections" toast; the actual write is atomic).
	existing, err := h.store.Load()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	out := saveResponse{
		PagesWritten:    []string{},
		PagesDeleted:    []string{},
		SectionsWritten: []string{},
		SectionsDeleted: []string{},
	}
	for name := range req.Pages {
		out.PagesWritten = append(out.PagesWritten, name)
	}
	for name := range existing.Pages {
		if _, ok := req.Pages[name]; !ok {
			out.PagesDeleted = append(out.PagesDeleted, name)
		}
	}
	for name := range req.Sections {
		out.SectionsWritten = append(out.SectionsWritten, name)
	}
	for name := range existing.Sections {
		if _, ok := req.Sections[name]; !ok {
			out.SectionsDeleted = append(out.SectionsDeleted, name)
		}
	}

	// Atomic full-doc replace. Sections + pages cross-reference (page
	// includes section by name, override targets section/module pair)
	// so the granular SaveCanvas/SaveSection/SavePage path can't validate
	// without temporarily corrupt intermediate states. SaveDocument
	// runs the whole validator once and writes the doc in one go.
	doc := canvas.Document{
		Canvas:   req.Canvas,
		Sections: req.Sections,
		Pages:    req.Pages,
	}
	if _, err := h.store.SaveDocument(doc); err != nil {
		h.canvasError(c, err)
		return
	}

	if h.pagesTfPath != "" {
		hcl := emitPagesTf(req.Canvas, req.Sections, req.Pages)
		if err := writeAtomic(h.pagesTfPath, []byte(hcl), 0o644); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "write pages.tf: " + err.Error()})
			return
		}
		out.PagesTfPath = h.pagesTfPath
		out.PagesTfBytes = len(hcl)
	}

	// HOM-127: keep MMM-Scenes2 + MMM-Canvas in step with the canvas
	// page set so a newly-added page is reachable from the mirror
	// without the user hand-editing two module config blocks. We
	// reconcile against the full page set rather than diffing this
	// save's adds/removes — that self-heals pages that pre-date the
	// sync feature or got out of step via direct config edits. The
	// reconcile runs on every save; idempotent when nothing changed.
	pageNames := make([]string, 0, len(req.Pages))
	for name := range req.Pages {
		pageNames = append(pageNames, name)
	}
	syncRes, err := scenes2sync.Reconcile(h.mm, pageNames)
	if err != nil {
		// Layout is already saved; sync failure shouldn't undo that.
		// Surface in the response so the editor toast can warn.
		out.Scenes2Sync = &scenes2SyncInfo{}
		c.JSON(http.StatusOK, gin.H{
			"saveResponse":     out,
			"scenes2SyncError": err.Error(),
		})
		return
	}
	if syncRes.CanvasUpdated || syncRes.ScenesUpdated || len(syncRes.AddedScenes) > 0 || len(syncRes.RemovedScenes) > 0 {
		out.Scenes2Sync = &scenes2SyncInfo{
			AddedScenes:   syncRes.AddedScenes,
			RemovedScenes: syncRes.RemovedScenes,
			CanvasUpdated: syncRes.CanvasUpdated,
			ScenesUpdated: syncRes.ScenesUpdated,
		}
	}
	c.JSON(http.StatusOK, out)
}

// HOM-111: fast-path debug toggle. The editor's debug button calls
// this to flip canvas-layout.json's debug flags directly, so MMM-Canvas
// reflows the visible borders via fs.watch without a full Save round.
type debugToggleRequest struct {
	Borders *bool `json:"borders,omitempty"`
	Labels  *bool `json:"labels,omitempty"`
}

func (h *handlers) postDebugToggle(c *gin.Context) {
	var req debugToggleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: " + err.Error()})
		return
	}
	doc, err := h.store.Load()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	cv := doc.Canvas
	if req.Borders != nil {
		cv.DebugBorders = *req.Borders
	}
	if req.Labels != nil {
		cv.DebugLabels = *req.Labels
	}
	if _, err := h.store.SaveCanvas(cv); err != nil {
		h.canvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, cv)
}

// HOM-111: Modules registry CRUD. These hit mmconfig.Manager directly
// so the unauthenticated editor route can drive them without going
// through the Bearer-token-protected /api/v1/modules endpoints. The
// payload shape matches mmconfig.Module so the same JSON the public
// API accepts lands here unchanged.
func (h *handlers) postModule(c *gin.Context) {
	var req mmconfig.Module
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.Module == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "module name is required"})
		return
	}
	created, err := h.mm.CreateModule(&req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (h *handlers) putModule(c *gin.Context) {
	var req mmconfig.Module
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: " + err.Error()})
		return
	}
	req.ID = c.Param("id")
	updated, err := h.mm.UpdateModule(&req)
	if err != nil {
		if err == mmconfig.ErrModuleNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "module not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (h *handlers) deleteModule(c *gin.Context) {
	if err := h.mm.DeleteModule(c.Param("id")); err != nil {
		if err == mmconfig.ErrModuleNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "module not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func (h *handlers) canvasError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, canvas.ErrPageNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, canvas.ErrInvalidSlot),
		errors.Is(err, canvas.ErrSlotOverlap),
		errors.Is(err, canvas.ErrUnknownModule):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

// writeAtomic is the same temp+rename pattern the canvas store uses;
// duplicated here because pulling it out into a shared util pkg over a
// 20-line helper is more friction than the benefit at this stage.
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
