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
	Document canvas.Document    `json:"document"`
	Modules  []moduleSummary    `json:"modules"`
	PagesTf  pagesTfFileState   `json:"pagesTf"`
}

type moduleSummary struct {
	ID     string `json:"id"`
	Module string `json:"module"`
	Header string `json:"header,omitempty"`
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
			ID:     m.ID,
			Module: m.Module,
			Header: m.Header,
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
// treats the incoming document as the desired state and reconciles
// against the store: pages absent from the request but present in
// the store are removed; pages present are upserted.
type saveRequest struct {
	Canvas canvas.Canvas            `json:"canvas"`
	Pages  map[string]canvas.Page   `json:"pages"`
}

type saveResponse struct {
	PagesWritten   []string `json:"pagesWritten"`
	PagesDeleted   []string `json:"pagesDeleted"`
	PagesTfPath    string   `json:"pagesTfPath"`
	PagesTfBytes   int      `json:"pagesTfBytes"`
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

	// Reconcile pages: figure out what to add/update vs. what to delete.
	existing, err := h.store.Load()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	existingNames := map[string]struct{}{}
	for name := range existing.Pages {
		existingNames[name] = struct{}{}
	}

	out := saveResponse{
		PagesWritten: []string{},
		PagesDeleted: []string{},
	}

	// Canvas singleton first — slot validators re-run page bounds against
	// the new dimensions, so doing this before pages lets us reject a
	// shrink that would break existing slots without partial writes.
	if _, err := h.store.SaveCanvas(req.Canvas); err != nil {
		h.canvasError(c, err)
		return
	}

	for name, page := range req.Pages {
		if _, err := h.store.SavePage(name, page); err != nil {
			h.canvasError(c, err)
			return
		}
		out.PagesWritten = append(out.PagesWritten, name)
		delete(existingNames, name)
	}
	for name := range existingNames {
		if _, err := h.store.DeletePage(name); err != nil && !errors.Is(err, canvas.ErrPageNotFound) {
			h.canvasError(c, err)
			return
		}
		out.PagesDeleted = append(out.PagesDeleted, name)
	}

	if h.pagesTfPath != "" {
		hcl := emitPagesTf(req.Canvas, req.Pages)
		if err := writeAtomic(h.pagesTfPath, []byte(hcl), 0o644); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "write pages.tf: " + err.Error()})
			return
		}
		out.PagesTfPath = h.pagesTfPath
		out.PagesTfBytes = len(hcl)
	}

	c.JSON(http.StatusOK, out)
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
