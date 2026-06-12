package api

import (
	"fmt"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/chores"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/config"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmversion"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/portal"
	"github.com/gin-gonic/gin"
)

// Server represents the API server
type Server struct {
	config     *config.Config
	router     *gin.Engine
	mmManager  *mmconfig.Manager
	mmVersions *mmversion.Manager
	choreStore *chores.Store
}

// NewServer creates a new API server
func NewServer(cfg *config.Config) *Server {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(gin.Logger())

	s := &Server{
		config:     cfg,
		router:     router,
		mmManager:  mmconfig.NewManager(cfg.MagicMirror.ConfigPath, cfg.MagicMirror.RestartCommand),
		mmVersions: mmversion.NewManager(cfg.MagicMirror.InstallPath()),
		choreStore: chores.NewStore(cfg.ChoresFile()),
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

	// Installed module (version) endpoints
	api.GET("/mm/version", s.getMMVersion)
	api.GET("/modules/installed", s.listInstalledModules)
	api.GET("/modules/installed/:name", s.getInstalledModule)
	api.PUT("/modules/installed/:name", s.putInstalledModule)
	api.DELETE("/modules/installed/:name", s.deleteInstalledModule)

	// Service control
	api.POST("/restart", s.restartMagicMirror)

	// Family portal (unauthenticated, LAN data plane — see internal/portal)
	portalAPI := portal.Register(s.router)
	portalAPI.GET("/chores", s.listChores)
	portalAPI.POST("/chores", s.createChore)
	portalAPI.PUT("/chores/:id", s.updateChore)
	portalAPI.DELETE("/chores/:id", s.deleteChore)
	portalAPI.GET("/assignees", s.listAssignees)
}

// Run starts the HTTP server
func (s *Server) Run() error {
	addr := fmt.Sprintf("%s:%d", s.config.Server.Host, s.config.Server.Port)
	return s.router.Run(addr)
}
