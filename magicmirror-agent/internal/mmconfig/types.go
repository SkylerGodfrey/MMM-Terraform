package mmconfig

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

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

	// Extras holds top-level config.js keys not modeled by GlobalConfig
	// (e.g. useHttps, electronSwitches) so writes don't destroy them.
	Extras map[string]any `json:"-"`
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

	// Extras holds module keys not modeled above (e.g. animateIn,
	// hiddenOnStartup) so writes don't destroy them.
	Extras map[string]any `json:"-"`
}

type moduleAlias Module

// UnmarshalJSON captures unknown module keys into Extras.
func (m *Module) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*moduleAlias)(m)); err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, k := range jsonFieldNames(moduleAlias{}) {
		delete(raw, k)
	}
	if len(raw) > 0 {
		m.Extras = raw
	}
	return nil
}

// MarshalJSON emits Extras alongside the known module fields.
func (m Module) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(moduleAlias(m))
	if err != nil {
		return nil, err
	}
	if len(m.Extras) == 0 {
		return base, nil
	}
	var merged map[string]any
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for k, v := range m.Extras {
		if _, exists := merged[k]; !exists {
			merged[k] = v
		}
	}
	return json.Marshal(merged)
}

// jsonFieldNames returns the json tag names of a struct's fields.
func jsonFieldNames(v any) []string {
	t := reflect.TypeOf(v)
	names := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		names = append(names, strings.Split(tag, ",")[0])
	}
	return names
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
