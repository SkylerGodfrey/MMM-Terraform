package api

import (
	"errors"
	"net/http"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mascot"
	"github.com/gin-gonic/gin"
)

// getMascotLayout returns the entire mascot layout document. Used by
// the terraform-provider's magicmirror_mascot_layout Read; the editor
// uses /mascot/api/state which bundles the catalog too.
func (s *Server) getMascotLayout(c *gin.Context) {
	doc, err := s.mascotStore.Load()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, doc)
}

// putMascotLayout replaces the entire mascot document atomically. The
// store re-validates every sprite + holiday before committing — bad
// geometry or malformed MM-DD surfaces as 400.
func (s *Server) putMascotLayout(c *gin.Context) {
	var req mascot.Document
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}
	doc, err := s.mascotStore.SaveDocument(req)
	if err != nil {
		s.mascotError(c, err)
		return
	}
	// No ScheduleRestart: MMM-Mascot (HOM-123) watches mascot-layout.json
	// with fs.watch and re-mounts sprites without an MM restart.
	c.JSON(http.StatusOK, doc)
}

func (s *Server) mascotError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, mascot.ErrInvalidSprite),
		errors.Is(err, mascot.ErrInvalidHoliday),
		errors.Is(err, mascot.ErrDuplicateSpriteID):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
