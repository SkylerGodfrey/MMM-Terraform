package api

import (
	"errors"
	"log"
	"net/http"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/photos"
	"github.com/gin-gonic/gin"
)

// maxUploadBytes caps a single photo upload; phone originals run 2–12MB.
const maxUploadBytes = 50 << 20

func (s *Server) listPhotos(c *gin.Context) {
	list, err := s.photoStore.List()
	if err != nil {
		photoError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"photos": list})
}

func (s *Server) uploadPhoto(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadBytes)
	file, header, err := c.Request.FormFile("photo")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "That photo was too large or the upload was cut short. Try again."})
		return
	}
	defer file.Close()

	photo, err := s.photoStore.Save(header.Filename, file)
	if err != nil {
		photoError(c, err)
		return
	}
	// The slideshow only rescans its directory on restart.
	s.mmManager.ScheduleRestart()
	c.JSON(http.StatusCreated, photo)
}

func (s *Server) deletePhoto(c *gin.Context) {
	if err := s.photoStore.Delete(c.Param("name")); err != nil {
		photoError(c, err)
		return
	}
	s.mmManager.ScheduleRestart()
	c.Status(http.StatusNoContent)
}

func (s *Server) servePhoto(c *gin.Context) {
	path, err := s.photoStore.OriginalPath(c.Param("name"))
	if err != nil {
		photoError(c, err)
		return
	}
	c.File(path)
}

func (s *Server) servePhotoThumb(c *gin.Context) {
	path, err := s.photoStore.ThumbPath(c.Param("name"))
	if err != nil {
		photoError(c, err)
		return
	}
	c.Header("Cache-Control", "max-age=300")
	c.File(path)
}

func photoError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, photos.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "That photo is already gone. Pull to refresh."})
	case errors.Is(err, photos.ErrNotAnImage):
		c.JSON(http.StatusBadRequest, gin.H{"error": "That file isn't a photo the mirror can show — use JPG or PNG."})
	case errors.Is(err, photos.ErrStorage):
		log.Printf("portal photos: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "The mirror couldn't save that change. Try again in a minute."})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
