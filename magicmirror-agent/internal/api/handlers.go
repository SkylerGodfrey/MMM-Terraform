package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/skyler/magicmirror-agent/internal/mmconfig"
)

const agentVersion = "0.1.0"

// healthCheck returns server health status
func (s *Server) healthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "healthy",
	})
}

// getVersion returns the agent version
func (s *Server) getVersion(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"version":     agentVersion,
		"api_version": "v1",
	})
}

// getConfig returns the current Magic Mirror configuration
func (s *Server) getConfig(c *gin.Context) {
	cfg, err := s.mmManager.ReadConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, cfg)
}

// updateConfig updates the global Magic Mirror configuration
func (s *Server) updateConfig(c *gin.Context) {
	var req mmconfig.GlobalConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	if err := s.mmManager.UpdateGlobalConfig(&req); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Configuration updated",
	})
}

// listModules returns all configured modules
func (s *Server) listModules(c *gin.Context) {
	modules, err := s.mmManager.ListModules()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"modules": modules,
	})
}

// getModule returns a specific module by ID
func (s *Server) getModule(c *gin.Context) {
	id := c.Param("id")
	module, err := s.mmManager.GetModule(id)
	if err != nil {
		if err == mmconfig.ErrModuleNotFound {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "Module not found",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, module)
}

// createModule adds a new module to the configuration
func (s *Server) createModule(c *gin.Context) {
	var req mmconfig.Module
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.Module == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Module name is required",
		})
		return
	}

	module, err := s.mmManager.CreateModule(&req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusCreated, module)
}

// updateModule updates an existing module
func (s *Server) updateModule(c *gin.Context) {
	id := c.Param("id")

	var req mmconfig.Module
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	req.ID = id
	module, err := s.mmManager.UpdateModule(&req)
	if err != nil {
		if err == mmconfig.ErrModuleNotFound {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "Module not found",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, module)
}

// deleteModule removes a module from the configuration
func (s *Server) deleteModule(c *gin.Context) {
	id := c.Param("id")

	if err := s.mmManager.DeleteModule(id); err != nil {
		if err == mmconfig.ErrModuleNotFound {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "Module not found",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Module deleted",
	})
}

// restartMagicMirror restarts the Magic Mirror process
func (s *Server) restartMagicMirror(c *gin.Context) {
	if err := s.mmManager.Restart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Magic Mirror restarted",
	})
}
