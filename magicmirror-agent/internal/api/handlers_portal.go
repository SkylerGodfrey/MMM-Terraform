package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/chores"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

func (s *Server) listChores(c *gin.Context) {
	list, err := s.choreStore.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"chores": list})
}

func (s *Server) createChore(c *gin.Context) {
	var in chores.Input
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	chore, err := s.choreStore.Create(in)
	if err != nil {
		choreError(c, err)
		return
	}
	c.JSON(http.StatusCreated, chore)
}

func (s *Server) updateChore(c *gin.Context) {
	var in chores.Input
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	chore, err := s.choreStore.Update(c.Param("id"), in)
	if err != nil {
		choreError(c, err)
		return
	}
	c.JSON(http.StatusOK, chore)
}

func (s *Server) deleteChore(c *gin.Context) {
	if err := s.choreStore.Delete(c.Param("id")); err != nil {
		choreError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// listAssignees returns the known family names for the portal's picker:
// everyone assigned a chore today plus everyone with a token balance in
// rewards.yaml (kept next to chores.yaml), so a name survives even when
// its last chore is deleted.
func (s *Server) listAssignees(c *gin.Context) {
	names := map[string]bool{}

	list, err := s.choreStore.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, chore := range list {
		if name, ok := chore["assignee"].(string); ok && name != "" {
			names[name] = true
		}
	}

	rewardsPath := filepath.Join(filepath.Dir(s.choreStore.Path()), "rewards.yaml")
	if raw, err := os.ReadFile(rewardsPath); err == nil {
		var rewards struct {
			Users []struct {
				Name string `yaml:"name"`
			} `yaml:"users"`
		}
		if yaml.Unmarshal(raw, &rewards) == nil {
			for _, u := range rewards.Users {
				if u.Name != "" {
					names[u.Name] = true
				}
			}
		}
	}

	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	c.JSON(http.StatusOK, gin.H{"assignees": out})
}

func choreError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, chores.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "chore not found"})
	case errors.Is(err, os.ErrNotExist):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "chores file not found on the mirror"})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
