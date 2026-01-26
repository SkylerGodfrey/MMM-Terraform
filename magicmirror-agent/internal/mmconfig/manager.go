package mmconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Manager handles Magic Mirror configuration operations
type Manager struct {
	configPath     string
	restartCommand string
	mu             sync.RWMutex
}

// NewManager creates a new configuration manager
func NewManager(configPath, restartCommand string) *Manager {
	return &Manager{
		configPath:     configPath,
		restartCommand: restartCommand,
	}
}

// ReadConfig reads and parses the Magic Mirror configuration
func (m *Manager) ReadConfig() (*MagicMirrorConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.readConfigInternal()
}

func (m *Manager) readConfigInternal() (*MagicMirrorConfig, error) {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse the JavaScript config file
	// Magic Mirror config.js exports a config object
	// We need to extract the JSON-compatible portion
	cfg, err := parseConfigJS(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Ensure all modules have IDs
	for i := range cfg.Modules {
		if cfg.Modules[i].ID == "" {
			cfg.Modules[i].ID = generateModuleID(&cfg.Modules[i], i)
		}
	}

	return cfg, nil
}

// WriteConfig writes the configuration back to disk
func (m *Manager) WriteConfig(cfg *MagicMirrorConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.writeConfigInternal(cfg)
}

func (m *Manager) writeConfigInternal(cfg *MagicMirrorConfig) error {
	// Generate the config.js content
	content, err := generateConfigJS(cfg)
	if err != nil {
		return fmt.Errorf("failed to generate config: %w", err)
	}

	// Write to temp file first
	dir := filepath.Dir(m.configPath)
	tmpFile, err := os.CreateTemp(dir, "config.js.tmp.*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(content); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	tmpFile.Close()

	// Atomic rename
	if err := os.Rename(tmpPath, m.configPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to move config into place: %w", err)
	}

	return nil
}

// UpdateGlobalConfig updates the global configuration settings
func (m *Manager) UpdateGlobalConfig(global *GlobalConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := m.readConfigInternal()
	if err != nil {
		return err
	}

	cfg.Global = *global
	return m.writeConfigInternal(cfg)
}

// ListModules returns all modules
func (m *Manager) ListModules() ([]Module, error) {
	cfg, err := m.ReadConfig()
	if err != nil {
		return nil, err
	}
	return cfg.Modules, nil
}

// GetModule returns a specific module by ID
func (m *Manager) GetModule(id string) (*Module, error) {
	cfg, err := m.ReadConfig()
	if err != nil {
		return nil, err
	}

	for _, mod := range cfg.Modules {
		if mod.ID == id {
			return &mod, nil
		}
	}

	return nil, ErrModuleNotFound
}

// CreateModule adds a new module
func (m *Manager) CreateModule(module *Module) (*Module, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := m.readConfigInternal()
	if err != nil {
		return nil, err
	}

	// Generate ID if not provided
	if module.ID == "" {
		module.ID = generateModuleID(module, len(cfg.Modules))
	}

	// Mark as Terraform-managed
	module.TerraformManaged = true

	cfg.Modules = append(cfg.Modules, *module)

	if err := m.writeConfigInternal(cfg); err != nil {
		return nil, err
	}

	return module, nil
}

// UpdateModule updates an existing module
func (m *Manager) UpdateModule(module *Module) (*Module, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := m.readConfigInternal()
	if err != nil {
		return nil, err
	}

	found := false
	for i, mod := range cfg.Modules {
		if mod.ID == module.ID {
			module.TerraformManaged = true
			cfg.Modules[i] = *module
			found = true
			break
		}
	}

	if !found {
		return nil, ErrModuleNotFound
	}

	if err := m.writeConfigInternal(cfg); err != nil {
		return nil, err
	}

	return module, nil
}

// DeleteModule removes a module
func (m *Manager) DeleteModule(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := m.readConfigInternal()
	if err != nil {
		return err
	}

	found := false
	modules := make([]Module, 0, len(cfg.Modules))
	for _, mod := range cfg.Modules {
		if mod.ID == id {
			found = true
			continue
		}
		modules = append(modules, mod)
	}

	if !found {
		return ErrModuleNotFound
	}

	cfg.Modules = modules
	return m.writeConfigInternal(cfg)
}

// Restart restarts the Magic Mirror process
func (m *Manager) Restart() error {
	if m.restartCommand == "" {
		return fmt.Errorf("no restart command configured")
	}

	cmd := exec.Command("sh", "-c", m.restartCommand)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restart failed: %s: %w", string(output), err)
	}

	return nil
}

// generateModuleID creates a unique ID for a module
func generateModuleID(mod *Module, index int) string {
	data := fmt.Sprintf("%s-%s-%d", mod.Module, mod.Position, index)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:8])
}

// parseConfigJS extracts configuration from Magic Mirror's config.js
func parseConfigJS(data []byte) (*MagicMirrorConfig, error) {
	content := string(data)

	// Try to parse as JSON first (for Terraform-managed configs)
	var cfg MagicMirrorConfig
	if err := json.Unmarshal(data, &cfg); err == nil {
		return &cfg, nil
	}

	// Extract the config object from JavaScript
	// Handle: let config = { ... }; or var config = { ... };
	configStart := strings.Index(content, "{")
	if configStart == -1 {
		return nil, fmt.Errorf("no config object found")
	}

	// Find the matching closing brace
	configEnd := findMatchingBrace(content, configStart)
	if configEnd == -1 {
		return nil, fmt.Errorf("could not find end of config object")
	}

	jsonStr := content[configStart : configEnd+1]

	// Remove JavaScript comments
	jsonStr = removeJSComments(jsonStr)

	// Remove trailing commas (valid in JS, invalid in JSON)
	jsonStr = removeTrailingCommas(jsonStr)

	if err := json.Unmarshal([]byte(jsonStr), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config as JSON: %w", err)
	}

	return &cfg, nil
}

// findMatchingBrace finds the index of the closing brace that matches the opening brace at start
func findMatchingBrace(s string, start int) int {
	depth := 0
	inString := false
	stringChar := rune(0)
	escaped := false

	for i := start; i < len(s); i++ {
		c := rune(s[i])

		if escaped {
			escaped = false
			continue
		}

		if c == '\\' && inString {
			escaped = true
			continue
		}

		if (c == '"' || c == '\'' || c == '`') && !inString {
			inString = true
			stringChar = c
			continue
		}

		if c == stringChar && inString {
			inString = false
			continue
		}

		if inString {
			continue
		}

		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}

// removeJSComments removes both // and /* */ style comments
func removeJSComments(s string) string {
	// Remove multi-line comments
	multiLineComment := regexp.MustCompile(`/\*[\s\S]*?\*/`)
	s = multiLineComment.ReplaceAllString(s, "")

	// Remove single-line comments (but not inside strings)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		inString := false
		stringChar := rune(0)
		for j, c := range line {
			if (c == '"' || c == '\'' || c == '`') && !inString {
				inString = true
				stringChar = c
			} else if c == stringChar && inString {
				inString = false
			} else if c == '/' && j+1 < len(line) && line[j+1] == '/' && !inString {
				lines[i] = line[:j]
				break
			}
		}
	}

	return strings.Join(lines, "\n")
}

// removeTrailingCommas removes trailing commas before } or ]
func removeTrailingCommas(s string) string {
	// Remove trailing commas before closing braces/brackets
	trailingComma := regexp.MustCompile(`,(\s*[}\]])`)
	return trailingComma.ReplaceAllString(s, "$1")
}

// generateConfigJS generates a Magic Mirror config.js file
func generateConfigJS(cfg *MagicMirrorConfig) ([]byte, error) {
	// For Terraform-managed configs, we'll use a JSON-compatible format
	// wrapped in JavaScript module.exports

	configJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}

	// Generate proper Magic Mirror config.js format
	output := fmt.Sprintf(`/* Magic Mirror Config
 * Managed by Terraform - Manual edits may be overwritten
 * See: https://docs.magicmirror.builders/configuration/introduction.html
 */

let config = %s;

/*************** DO NOT EDIT THE LINE BELOW ***************/
if (typeof module !== "undefined") { module.exports = config; }
`, string(configJSON))

	return []byte(output), nil
}
