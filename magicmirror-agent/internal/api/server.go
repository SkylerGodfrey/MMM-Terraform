package api

import (
	"fmt"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/canvas"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/canvaseditor"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/chores"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/config"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/layoutviewer"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmversion"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/photos"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/portal"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/rewards"
	"github.com/gin-gonic/gin"
)

// Server represents the API server
type Server struct {
	config     *config.Config
	router     *gin.Engine
	mmManager  *mmconfig.Manager
	mmVersions *mmversion.Manager
	choreStore  *chores.Store
	photoStore  *photos.Store
	rewardStore *rewards.Store
	canvasStore *canvas.Store
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
		choreStore:  chores.NewStore(cfg.ChoresFile()),
		photoStore:  photos.NewStore(cfg.PhotosDir()),
		rewardStore: rewards.NewStore(cfg.RewardsFile(), cfg.RewardsImagesDir()),
	}
	s.canvasStore = canvas.NewStore(cfg.CanvasLayoutPath(), &canvasModuleLister{mm: s.mmManager})

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

	// Canvas v2 layout document (HOM-104). Pages are referenced by name;
	// the canvas singleton (debug flags, dimensions) lives on /canvas.
	// The full document is also exposed on /canvas/document so the editor
	// can hydrate everything in one round trip.
	api.GET("/canvas/document", s.getCanvasDocument)
	api.PUT("/canvas", s.updateCanvas)
	api.GET("/pages/:name", s.getPage)
	api.PUT("/pages/:name", s.putPage)
	api.DELETE("/pages/:name", s.deletePage)

	// Family portal (unauthenticated, LAN data plane — see internal/portal)
	portalAPI := portal.Register(s.router)
	portalAPI.GET("/chores", s.listChores)
	portalAPI.POST("/chores", s.createChore)
	portalAPI.PUT("/chores/:id", s.updateChore)
	portalAPI.DELETE("/chores/:id", s.deleteChore)
	portalAPI.GET("/assignees", s.listAssignees)
	portalAPI.GET("/photos", s.listPhotos)
	portalAPI.POST("/photos", s.uploadPhoto)
	portalAPI.DELETE("/photos/:name", s.deletePhoto)
	s.router.GET("/portal/photos/:name", s.servePhoto)
	s.router.GET("/portal/thumbs/:name", s.servePhotoThumb)
	portalAPI.GET("/rewards", s.listRewards)
	portalAPI.POST("/rewards", s.createReward)
	portalAPI.PUT("/rewards/:id", s.updateReward)
	portalAPI.DELETE("/rewards/:id", s.deleteReward)
	portalAPI.POST("/rewards/image", s.uploadRewardImage)
	s.router.GET("/portal/rewards-images/:name", s.serveRewardImage)

	// Layout viewer (HOM-92, L3 of HOM-91 Epic) — read-only visualisation of
	// the active layout document, plus the L4 drag/resize editor (HOM-93)
	// that persists its in-flight document to layout.json next to config.js,
	// the L5 Terraform diff emitter (HOM-94), and the L6 live preview/revert
	// (HOM-95).
	layoutviewer.Register(s.router, s.mmManager, s.config.LayoutWorkingCopyPath(), s.config.MagicMirror.ConfigPath, s.config.ModulesTfPath())

	// Canvas v2 editor (HOM-108) — drag/resize/page-tabs/save editor for
	// the slot-based layout. Writes both the live canvas-layout.json (the
	// document MMM-Canvas reads via fs.watch) AND a pages.tf mirror so
	// the IaC story stays whole. Distinct from /layout, which keeps
	// driving the region-based legacy editor.
	canvaseditor.Register(s.router, s.canvasStore, s.mmManager, s.config.PagesTfPath())
}

// Run starts the HTTP server
func (s *Server) Run() error {
	addr := fmt.Sprintf("%s:%d", s.config.Server.Host, s.config.Server.Port)
	return s.router.Run(addr)
}
