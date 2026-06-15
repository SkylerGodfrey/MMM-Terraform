package api

import (
	"errors"
	"log"
	"net/http"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/rewards"
	"github.com/gin-gonic/gin"
)

// maxRewardImageBytes caps reward image uploads. They're displayed in a
// small tile, so we don't need to accept full phone originals — but
// 10MB still leaves room for unedited PNGs.
const maxRewardImageBytes = 10 << 20

func (s *Server) listRewards(c *gin.Context) {
	list, err := s.rewardStore.List()
	if err != nil {
		rewardError(c, err)
		return
	}
	users, err := s.rewardStore.Users()
	if err != nil {
		rewardError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"rewards": list, "users": users})
}

func (s *Server) createReward(c *gin.Context) {
	var in rewards.Input
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	reward, err := s.rewardStore.Create(in)
	if err != nil {
		rewardError(c, err)
		return
	}
	c.JSON(http.StatusCreated, reward)
}

func (s *Server) updateReward(c *gin.Context) {
	var in rewards.Input
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	reward, err := s.rewardStore.Update(c.Param("id"), in)
	if err != nil {
		rewardError(c, err)
		return
	}
	c.JSON(http.StatusOK, reward)
}

func (s *Server) deleteReward(c *gin.Context) {
	if err := s.rewardStore.Delete(c.Param("id")); err != nil {
		rewardError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// uploadRewardImage stores an image in rewards-images/ and returns the
// final filename. The portal then sets that filename as the reward's
// `image` field on save. Decoupled from reward creation so a user can
// upload first and pick later, and so existing rewards can swap images
// without a full PUT cycle.
func (s *Server) uploadRewardImage(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRewardImageBytes)
	file, header, err := c.Request.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "That image was too large or the upload was cut short. Try again."})
		return
	}
	defer file.Close()

	name, err := s.rewardStore.SaveImage(header.Filename, file)
	if err != nil {
		rewardError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"image": name})
}

func (s *Server) serveRewardImage(c *gin.Context) {
	path, err := s.rewardStore.ImagePath(c.Param("name"))
	if err != nil {
		rewardError(c, err)
		return
	}
	c.Header("Cache-Control", "max-age=300")
	c.File(path)
}

func rewardError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, rewards.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "That reward is gone — it may have been removed on the mirror. Pull to refresh."})
	case errors.Is(err, rewards.ErrDuplicateName):
		c.JSON(http.StatusConflict, gin.H{"error": "There's already a reward with that name."})
	case errors.Is(err, rewards.ErrNotAnImage):
		c.JSON(http.StatusBadRequest, gin.H{"error": "That file isn't an image the mirror can show — use JPG, PNG, GIF, or WEBP."})
	case errors.Is(err, rewards.ErrStorage):
		log.Printf("portal rewards: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "The mirror couldn't save that change. Try again in a minute."})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
