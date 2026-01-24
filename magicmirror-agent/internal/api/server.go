package api

import (
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/skyler/magicmirror-agent/internal/config"
	"github.com/skyler/magicmirror-agent/internal/mmconfig"
)

// Server represents the API server
type Server struct {
	config    *config.Config
	router    *gin.Engine
	mmManager *mmconfig.Manager
}

// NewServer creates a new API server
func NewServer(cfg *config.Config) *Server {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(gin.Logger())

	s := &Server{
		config:    cfg,
		router:    router,
		mmManager: mmconfig.NewManager(cfg.MagicMirror.ConfigPath, cfg.MagicMirror.RestartCommand),
	}

	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	// Health check (no auth required)
	s.router.GET("/health", s.healthCheck)
	s.router.GET("/api/v1/version", s.getVersion)

	// API routes with optional authentication
	api := s.router.Group("/api/v1")
	if s.config.Auth.Enabled {
		api.Use(s.authMiddleware())
	}

	// Config endpoints
	api.GET("/config", s.getConfig)
	api.PUT("/config", s.updateConfig)

	// Module endpoints
	api.GET("/modules", s.listModules)
	api.GET("/modules/:id", s.getModule)
	api.POST("/modules", s.createModule)
	api.PUT("/modules/:id", s.updateModule)
	api.DELETE("/modules/:id", s.deleteModule)

	// Service control
	api.POST("/restart", s.restartMagicMirror)
}

// Run starts the HTTP server
func (s *Server) Run() error {
	addr := fmt.Sprintf("%s:%d", s.config.Server.Host, s.config.Server.Port)
	return s.router.Run(addr)
}
