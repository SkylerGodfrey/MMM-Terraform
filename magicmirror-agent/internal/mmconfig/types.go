package mmconfig

import "errors"

var (
	// ErrModuleNotFound is returned when a module doesn't exist
	ErrModuleNotFound = errors.New("module not found")
	// ErrInvalidConfig is returned when the config is malformed
	ErrInvalidConfig = errors.New("invalid configuration")
)

// MagicMirrorConfig represents the full Magic Mirror configuration
type MagicMirrorConfig struct {
	Global  GlobalConfig `json:"global"`
	Modules []Module     `json:"modules"`
}

// GlobalConfig represents global Magic Mirror settings
type GlobalConfig struct {
	Address          string   `json:"address,omitempty"`
	Port             int      `json:"port,omitempty"`
	BasePath         string   `json:"basePath,omitempty"`
	IPWhitelist      []string `json:"ipWhitelist,omitempty"`
	Zoom             float64  `json:"zoom,omitempty"`
	Language         string   `json:"language,omitempty"`
	Locale           string   `json:"locale,omitempty"`
	LogLevel         []string `json:"logLevel,omitempty"`
	TimeFormat       int      `json:"timeFormat,omitempty"`
	Units            string   `json:"units,omitempty"`
	ServerOnly       bool     `json:"serverOnly,omitempty"`
	ElectronOptions  any      `json:"electronOptions,omitempty"`
	CustomCSS        string   `json:"customCss,omitempty"`
}

// Module represents a Magic Mirror module configuration
type Module struct {
	ID       string         `json:"id,omitempty"`
	Module   string         `json:"module"`
	Position string         `json:"position,omitempty"`
	Header   string         `json:"header,omitempty"`
	Disabled bool           `json:"disabled,omitempty"`
	Classes  string         `json:"classes,omitempty"`
	Config   map[string]any `json:"config,omitempty"`

	// Terraform-managed metadata (stored in config._terraform)
	TerraformManaged bool `json:"_terraform_managed,omitempty"`
}

// ValidPositions lists all valid Magic Mirror positions
var ValidPositions = []string{
	"top_bar",
	"top_left",
	"top_center",
	"top_right",
	"upper_third",
	"middle_center",
	"lower_third",
	"bottom_left",
	"bottom_center",
	"bottom_right",
	"bottom_bar",
	"fullscreen_above",
	"fullscreen_below",
}

// IsValidPosition checks if a position is valid
func IsValidPosition(pos string) bool {
	for _, valid := range ValidPositions {
		if pos == valid {
			return true
		}
	}
	return false
}
