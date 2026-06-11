package api

import (
	"errors"
	"net/http"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmversion"
	"github.com/gin-gonic/gin"
)

// getMMVersion returns the core MagicMirror version (read-only)
func (s *Server) getMMVersion(c *gin.Context) {
	version, err := s.mmVersions.CoreVersion()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"version": version,
	})
}

// listInstalledModules returns all module directories with git info
func (s *Server) listInstalledModules(c *gin.Context) {
	installed, err := s.mmVersions.ListInstalled()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"installed": installed,
	})
}

// getInstalledModule returns git info for a single module directory
func (s *Server) getInstalledModule(c *gin.Context) {
	module, err := s.mmVersions.GetInstalled(c.Param("name"))
	if err != nil {
		s.installedError(c, err)
		return
	}
	c.JSON(http.StatusOK, module)
}

// putInstalledModule converges a module install to the requested version
func (s *Server) putInstalledModule(c *gin.Context) {
	var req struct {
		Repository string `json:"repository"`
		Version    string `json:"version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	module, err := s.mmVersions.Converge(c.Param("name"), req.Repository, req.Version)
	if err != nil {
		s.installedError(c, err)
		return
	}
	c.JSON(http.StatusOK, module)
}

// deleteInstalledModule removes a module directory (git clones only)
func (s *Server) deleteInstalledModule(c *gin.Context) {
	if err := s.mmVersions.Remove(c.Param("name")); err != nil {
		s.installedError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "Module removed",
	})
}

// installedError maps mmversion errors to HTTP status codes
func (s *Server) installedError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, mmversion.ErrModuleNotFound):
		status = http.StatusNotFound
	case errors.Is(err, mmversion.ErrInvalidName),
		errors.Is(err, mmversion.ErrRepositoryRequired),
		errors.Is(err, mmversion.ErrNotGitRepo):
		status = http.StatusBadRequest
	}
	c.JSON(status, gin.H{
		"error": err.Error(),
	})
}
