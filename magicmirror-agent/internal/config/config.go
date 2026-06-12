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
	ChoresFile string `mapstructure:"chores_file"`
	PhotosDir  string `mapstructure:"photos_dir"`
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

// PhotosDir returns the slideshow album directory edited by the family
// portal, defaulting to the imagePaths value used in the live Terraform
// config (modules/MagicMirrorPhotos under the MagicMirror install).
func (c *Config) PhotosDir() string {
	if c.Portal.PhotosDir != "" {
		return c.Portal.PhotosDir
	}
	return filepath.Join(c.MagicMirror.InstallPath(), "modules", "MagicMirrorPhotos")
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
