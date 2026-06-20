package api

import (
	"errors"
	"fmt"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/canvas"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/canvaseditor"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/chores"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/choresdb"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/config"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/mascot"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/mascoteditor"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmversion"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/photos"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/portal"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/rewards"
	"github.com/gin-gonic/gin"
)

// Server represents the API server
type Server struct {
	config       *config.Config
	router       *gin.Engine
	mmManager    *mmconfig.Manager
	mmVersions   *mmversion.Manager
	choreStore   *chores.Store
	choresDBPath string // runtime-state SQLite; opened per-request (module owns it)
	photoStore   *photos.Store
	rewardStore  *rewards.Store
	canvasStore  *canvas.Store
	mascotStore  *mascot.Store
}

// retentionDaysDefault matches the MMM-Chores module's database.retentionDays
// default (store/index.js). The portal surfaces it on the activity log so it's
// clear why entries older than the window are absent. The module owns the
// actual prune; the agent only reports the window.
const retentionDaysDefault = 14

// openChoresDB opens the runtime-state SQLite per request rather than holding a
// handle, because the module owns the file's lifecycle (it may not exist until
// the module first runs, and is recreated on schema migration). Callers must
// Close the returned store. A nil store + nil error means "unavailable, degrade
// gracefully" — the family UI shows an empty queue/log rather than an error.
func (s *Server) openChoresDB() (*choresdb.Store, error) {
	store, err := choresdb.Open(s.choresDBPath)
	if err != nil {
		if errors.Is(err, choresdb.ErrUnavailable) {
			return nil, nil
		}
		return nil, err
	}
	return store, nil
}

// NewServer creates a new API server
func NewServer(cfg *config.Config) *Server {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(gin.Logger())

	s := &Server{
		config:       cfg,
		router:       router,
		mmManager:    mmconfig.NewManager(cfg.MagicMirror.ConfigPath, cfg.MagicMirror.RestartCommand),
		mmVersions:   mmversion.NewManager(cfg.MagicMirror.InstallPath()),
		choreStore:   chores.NewStore(cfg.ChoresFile()),
		choresDBPath: cfg.ChoresDBPath(),
		photoStore:   photos.NewStore(cfg.PhotosDir()),
		rewardStore:  rewards.NewStore(cfg.RewardsFile(), cfg.RewardsImagesDir()),
	}
	s.canvasStore = canvas.NewStore(cfg.CanvasLayoutPath(), &canvasModuleLister{mm: s.mmManager})
	s.mascotStore = mascot.NewStore(cfg.MascotLayoutPath())

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

	// MMM-Mascot layout (HOM-124). The full document round-trips on one
	// pair of endpoints because the resource is singleton — a single
	// magicmirror_mascot_layout block owns canvas + sprites + holidays
	// together. The /mascot editor uses its own unauthenticated routes.
	api.GET("/mascot-layout", s.getMascotLayout)
	api.PUT("/mascot-layout", s.putMascotLayout)

	// Family portal (unauthenticated, LAN data plane — see internal/portal)
	portalAPI := portal.Register(s.router)
	portalAPI.GET("/chores", s.listChores)
	portalAPI.POST("/chores", s.createChore)
	portalAPI.PUT("/chores/:id", s.updateChore)
	portalAPI.DELETE("/chores/:id", s.deleteChore)
	portalAPI.GET("/assignees", s.listAssignees)

	// Pending-approval queue (HOM-139) and activity log + revert (HOM-140).
	// Both read/write the MMM-Chores runtime-state SQLite the module owns.
	portalAPI.GET("/pending", s.listPending)
	portalAPI.POST("/pending/:id/approve", s.approvePending)
	portalAPI.POST("/pending/:id/deny", s.denyPending)
	portalAPI.GET("/events", s.listEvents)
	portalAPI.POST("/events/:id/revert", s.revertEvent)

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

	// Canvas v2 editor (HOM-108) — drag/resize/page-tabs/save editor for
	// the slot-based layout. Writes both the live canvas-layout.json (the
	// document MMM-Canvas reads via fs.watch) AND a pages.tf mirror so
	// the IaC story stays whole. Superseded the region-based /layout
	// editor (HOM-91) once every active module made it into a canvas page.
	canvaseditor.Register(s.router, s.canvasStore, s.mmManager, s.config.PagesTfPath())

	// MMM-Mascot sprite-layout editor (HOM-123) — drag/resize/save editor
	// for the mascot overlay. Writes both mascot-layout.json (the live
	// document MMM-Mascot reads via fs.watch) AND a mascots.tf mirror so
	// the IaC story stays whole — same dual-write pattern as the canvas
	// editor (HOM-108).
	mascoteditor.Register(s.router, s.mascotStore, s.config.MascotSpritesDir(), s.config.MascotsTfPath())
}

// Run starts the HTTP server
func (s *Server) Run() error {
	addr := fmt.Sprintf("%s:%d", s.config.Server.Host, s.config.Server.Port)
	return s.router.Run(addr)
}
