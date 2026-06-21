package config

import (
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// Config holds the agent configuration
type Config struct {
	Server      ServerConfig      `mapstructure:"server"`
	MagicMirror MagicMirrorConfig `mapstructure:"magicmirror"`
	Auth        AuthConfig        `mapstructure:"auth"`
	Portal      PortalConfig      `mapstructure:"portal"`
}

// PortalConfig holds family-portal settings
type PortalConfig struct {
	ChoresFile       string `mapstructure:"chores_file"`
	ChoresDBPath     string `mapstructure:"chores_db_path"`
	PhotosDir        string `mapstructure:"photos_dir"`
	RewardsFile      string `mapstructure:"rewards_file"`
	RewardsImagesDir string `mapstructure:"rewards_images_dir"`
	PokedexPath      string `mapstructure:"pokedex_path"`
}

// ServerConfig holds HTTP server settings
type ServerConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

// MagicMirrorConfig holds Magic Mirror specific settings
type MagicMirrorConfig struct {
	ConfigPath     string `mapstructure:"config_path"`
	RestartCommand string `mapstructure:"restart_command"`
	Path           string `mapstructure:"path"`
	// ModulesTfPath overrides where the Pi-resident modules.tf lives
	// (HOM-96). Empty falls back to the dir of ConfigPath.
	ModulesTfPath string `mapstructure:"modules_tf_path"`
}

// InstallPath returns the MagicMirror install directory, defaulting to
// the grandparent of config_path (config.js lives in <install>/config/).
func (m MagicMirrorConfig) InstallPath() string {
	if m.Path != "" {
		return m.Path
	}
	return filepath.Dir(filepath.Dir(m.ConfigPath))
}

// ChoresFile returns the configured chores.yaml path, defaulting to the
// MMM-Chores module directory under the MagicMirror install — so existing
// agent configs need no edit for the family portal.
func (c *Config) ChoresFile() string {
	if c.Portal.ChoresFile != "" {
		return c.Portal.ChoresFile
	}
	return filepath.Join(c.MagicMirror.InstallPath(), "modules", "MMM-Chores", "chores.yaml")
}

// ChoresDBPath returns the MMM-Chores runtime-state SQLite file the family
// portal reads and writes (HOM-132). The Node module owns the schema; the
// agent opens the same file in WAL mode for concurrent access. Defaults to the
// module's data/chores.db under the MagicMirror install (matching store/index.js
// in MMM-Chores), so existing agent configs need no edit.
func (c *Config) ChoresDBPath() string {
	if c.Portal.ChoresDBPath != "" {
		return c.Portal.ChoresDBPath
	}
	return filepath.Join(c.MagicMirror.InstallPath(), "modules", "MMM-Chores", "data", "chores.db")
}

// PokedexPath returns the Pokémon Theme v2 dataset the family portal reads to
// power the pack member picker and the caught-dex admin tool (HOM-150 / HOM-153).
// The dataset is owned by MMM-Chores (themes/pokemon/data/pokedex.json, HOM-147);
// the agent only reads it. Defaults under the MMM-Chores module dir so existing
// agent configs need no edit; override via portal.pokedex_path if relocated.
func (c *Config) PokedexPath() string {
	if c.Portal.PokedexPath != "" {
		return c.Portal.PokedexPath
	}
	return filepath.Join(c.MagicMirror.InstallPath(), "modules", "MMM-Chores", "themes", "pokemon", "data", "pokedex.json")
}

// PhotosDir returns the slideshow album directory edited by the family
// portal, defaulting to the imagePaths value used in the live Terraform
// config (modules/MagicMirrorPhotos under the MagicMirror install).
func (c *Config) PhotosDir() string {
	if c.Portal.PhotosDir != "" {
		return c.Portal.PhotosDir
	}
	return filepath.Join(c.MagicMirror.InstallPath(), "modules", "MagicMirrorPhotos")
}

// RewardsFile returns the configured rewards.yaml path, defaulting next
// to chores.yaml in the MMM-Chores module dir — the same convention the
// node_helper uses when loading rewards data.
func (c *Config) RewardsFile() string {
	if c.Portal.RewardsFile != "" {
		return c.Portal.RewardsFile
	}
	return filepath.Join(c.MagicMirror.InstallPath(), "modules", "MMM-Chores", "rewards.yaml")
}

// RewardsImagesDir returns the directory where reward image uploads
// land. Defaults to MMM-Chores/rewards-images/, matching where the
// module expects bare-filename images (see MMM-Chores.js getRewardImage).
func (c *Config) RewardsImagesDir() string {
	if c.Portal.RewardsImagesDir != "" {
		return c.Portal.RewardsImagesDir
	}
	return filepath.Join(c.MagicMirror.InstallPath(), "modules", "MMM-Chores", "rewards-images")
}

// CanvasLayoutPath returns the on-disk path of the Canvas v2 layout
// document (HOM-104). Sits next to config.js so it inherits the same
// systemd ReadWritePaths the agent already has. Separate from the L4
// working copy because the canvas document is the durable source of
// truth, not an editor scratch file.
func (c *Config) CanvasLayoutPath() string {
	return filepath.Join(filepath.Dir(c.MagicMirror.ConfigPath), "canvas-layout.json")
}

// PagesTfPath returns where the /canvas editor (HOM-108) writes the
// generated `magicmirror_canvas` + `magicmirror_page` HCL resources.
// Sits next to modules.tf so the human-edited modules.tf and the
// editor-owned pages.tf live side by side; both pick up the existing
// systemd ReadWritePaths for the config dir.
func (c *Config) PagesTfPath() string {
	return filepath.Join(filepath.Dir(c.MagicMirror.ConfigPath), "pages.tf")
}

// MascotLayoutPath returns the on-disk path of the MMM-Mascot sprite
// layout document (HOM-123). Sits next to config.js (same systemd
// ReadWritePaths as canvas-layout.json). The MMM-Mascot module reads
// the same file via fs.watch for hot-reload.
func (c *Config) MascotLayoutPath() string {
	return filepath.Join(filepath.Dir(c.MagicMirror.ConfigPath), "mascot-layout.json")
}

// MascotsTfPath returns where the /mascot editor (HOM-123) writes the
// generated `magicmirror_mascot_layout` HCL mirror. Sits next to
// pages.tf so the editor-owned IaC files cluster together.
func (c *Config) MascotsTfPath() string {
	return filepath.Join(filepath.Dir(c.MagicMirror.ConfigPath), "mascots.tf")
}

// MascotSpritesDir returns the directory the /mascot editor scans for
// available sprite catalog entries (HOM-123). Defaults to the
// MMM-Mascot module's bundled sprites/ folder; override only if you
// keep a custom sprite library elsewhere on the Pi.
func (c *Config) MascotSpritesDir() string {
	return filepath.Join(c.MagicMirror.InstallPath(), "modules", "MMM-Mascot", "sprites")
}

// ModulesTfPath returns the location of the Pi-resident modules.tf the
// agent reads and writes on Save (HOM-96). Defaults next to config.js so
// the existing systemd ReadWritePaths cover it. Override via the
// `magicmirror.modules_tf_path` config key if you want it elsewhere.
func (c *Config) ModulesTfPath() string {
	if c.MagicMirror.ModulesTfPath != "" {
		return c.MagicMirror.ModulesTfPath
	}
	return filepath.Join(filepath.Dir(c.MagicMirror.ConfigPath), "modules.tf")
}

// AuthConfig holds authentication settings
type AuthConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	APIKey  string `mapstructure:"api_key"`
}

// DefaultConfig returns sensible defaults
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host: "127.0.0.1",
			Port: 8484,
		},
		MagicMirror: MagicMirrorConfig{
			ConfigPath:     "/home/pi/MagicMirror/config/config.js",
			RestartCommand: "pm2 restart MagicMirror",
		},
		Auth: AuthConfig{
			Enabled: true,
			APIKey:  os.Getenv("MM_AGENT_API_KEY"),
		},
	}
}

// Load reads configuration from file
func Load(path string) (*Config, error) {
	viper.SetConfigFile(path)
	viper.SetConfigType("yaml")

	// Set defaults
	defaults := DefaultConfig()
	viper.SetDefault("server.host", defaults.Server.Host)
	viper.SetDefault("server.port", defaults.Server.Port)
	viper.SetDefault("magicmirror.config_path", defaults.MagicMirror.ConfigPath)
	viper.SetDefault("magicmirror.restart_command", defaults.MagicMirror.RestartCommand)
	viper.SetDefault("auth.enabled", defaults.Auth.Enabled)

	// Allow environment variable overrides
	viper.SetEnvPrefix("MM_AGENT")
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	// Override API key from environment if set
	if envKey := os.Getenv("MM_AGENT_API_KEY"); envKey != "" {
		cfg.Auth.APIKey = envKey
	}

	return &cfg, nil
}
