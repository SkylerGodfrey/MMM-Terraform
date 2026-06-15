package api

import (
	"errors"
	"net/http"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/canvas"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
	"github.com/gin-gonic/gin"
)

// canvasModuleLister adapts mmconfig.Manager to the canvas.ModuleLister
// interface — keeps the canvas package free of any mmconfig import.
//
// Returns BOTH the agent-assigned module IDs AND the module class names
// (e.g. "clock", "MMM-CalendarExt3"). MMM-Canvas (HOM-105) matches DOM
// wrappers via `.module.<class-name>`, so slot.Module is most usefully
// the class name; legacy HCL referencing `magicmirror_module.<name>.id`
// also continues to validate.
type canvasModuleLister struct{ mm *mmconfig.Manager }

func (l *canvasModuleLister) ListModuleIDs() ([]string, error) {
	mods, err := l.mm.ListModules()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(mods)*2)
	for _, m := range mods {
		if m.ID != "" {
			out = append(out, m.ID)
		}
		if m.Module != "" {
			out = append(out, m.Module)
		}
	}
	return out, nil
}

// getCanvasDocument returns the entire layout document. Useful for the
// editor on initial load — one round trip gets pages + canvas globals.
func (s *Server) getCanvasDocument(c *gin.Context) {
	doc, err := s.canvasStore.Load()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, doc)
}

// updateCanvas replaces the singleton canvas block (width, height,
// debug flags, default page). Slot validation re-runs across all pages
// so shrinking the canvas below an existing slot rejects cleanly.
func (s *Server) updateCanvas(c *gin.Context) {
	var req canvas.Canvas
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}

	doc, err := s.canvasStore.SaveCanvas(req)
	if err != nil {
		s.canvasError(c, err)
		return
	}
	s.mmManager.ScheduleRestart()
	c.JSON(http.StatusOK, doc.Canvas)
}

// getPage returns a single named page's slots, or 404 if the page does
// not exist. Used by the provider's Read on a magicmirror_page resource.
func (s *Server) getPage(c *gin.Context) {
	page, err := s.canvasStore.GetPage(c.Param("name"))
	if err != nil {
		s.canvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

// putPage replaces a named page wholesale. Create-or-update semantics:
// non-existent pages are created, existing pages are replaced. The
// provider uses this for both Create and Update so the agent doesn't
// have to distinguish.
func (s *Server) putPage(c *gin.Context) {
	name := c.Param("name")
	var req canvas.Page
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}

	if _, err := s.canvasStore.SavePage(name, req); err != nil {
		s.canvasError(c, err)
		return
	}
	// The mirror needs to re-read the layout document on a page change;
	// for now the simplest signal is a debounced restart matching what
	// module CRUD does. A finer-grained notification can land in HOM-105
	// (LAYOUT_UPDATE socket message).
	s.mmManager.ScheduleRestart()
	c.JSON(http.StatusOK, req)
}

// deletePage removes a named page. Idempotent at the HTTP layer:
// repeated deletes after the page is gone return 404 with the standard
// not-found error body, matching the deleteModule pattern.
func (s *Server) deletePage(c *gin.Context) {
	if _, err := s.canvasStore.DeletePage(c.Param("name")); err != nil {
		s.canvasError(c, err)
		return
	}
	s.mmManager.ScheduleRestart()
	c.JSON(http.StatusOK, gin.H{"message": "Page deleted"})
}

// canvasError translates package sentinels into HTTP status codes so
// the provider can distinguish "not found" (resource drift) from
// validation errors (bad plan).
func (s *Server) canvasError(c *gin.Context, err error) {
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
