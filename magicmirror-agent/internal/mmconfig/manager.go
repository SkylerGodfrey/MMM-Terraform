package mmconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// restartDebounce delays the post-write restart so a terraform apply that
// touches many resources restarts MagicMirror once, not once per resource.
var restartDebounce = 3 * time.Second

// Manager handles Magic Mirror configuration operations
type Manager struct {
	configPath     string
	restartCommand string
	mu             sync.RWMutex

	restartMu    sync.Mutex
	restartTimer *time.Timer
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
	if err := m.writeConfigInternal(cfg); err != nil {
		return err
	}
	m.scheduleRestart()
	return nil
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
	m.scheduleRestart()

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
	m.scheduleRestart()

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
	if err := m.writeConfigInternal(cfg); err != nil {
		return err
	}
	m.scheduleRestart()
	return nil
}

// scheduleRestart restarts MagicMirror after a debounce window so config
// changes take effect without restarting once per write.
func (m *Manager) scheduleRestart() {
	if m.restartCommand == "" {
		return
	}

	m.restartMu.Lock()
	defer m.restartMu.Unlock()

	if m.restartTimer != nil {
		m.restartTimer.Stop()
	}
	m.restartTimer = time.AfterFunc(restartDebounce, func() {
		if err := m.Restart(); err != nil {
			log.Printf("post-write MagicMirror restart failed: %v", err)
		}
	})
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

// parseConfigJS extracts configuration from Magic Mirror's config.js by
// evaluating it as JavaScript, so unquoted keys, comments, single quotes,
// and trailing commas are all handled.
func parseConfigJS(data []byte) (*MagicMirrorConfig, error) {
	vm := goja.New()
	if _, err := vm.RunString(string(data)); err != nil {
		return nil, fmt.Errorf("failed to evaluate config.js: %w", err)
	}

	v, err := vm.RunString("JSON.stringify(config)")
	if err != nil {
		return nil, fmt.Errorf("config.js did not define a 'config' object: %w", err)
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(v.String()), &raw); err != nil {
		return nil, fmt.Errorf("failed to decode config object: %w", err)
	}

	return configFromMap(raw)
}

// configFromMap splits MagicMirror's flat top-level config into known global
// settings, modules, and unmodeled extras.
func configFromMap(raw map[string]any) (*MagicMirrorConfig, error) {
	cfg := &MagicMirrorConfig{}

	if mods, ok := raw["modules"]; ok {
		b, err := json.Marshal(mods)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &cfg.Modules); err != nil {
			return nil, fmt.Errorf("failed to decode modules: %w", err)
		}
		delete(raw, "modules")
	}

	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &cfg.Global); err != nil {
		return nil, fmt.Errorf("failed to decode global settings: %w", err)
	}

	for _, k := range jsonFieldNames(GlobalConfig{}) {
		delete(raw, k)
	}
	if len(raw) > 0 {
		cfg.Extras = raw
	}

	return cfg, nil
}

// configToMap flattens the config back into MagicMirror's top-level format.
func configToMap(cfg *MagicMirrorConfig) (map[string]any, error) {
	out := make(map[string]any, len(cfg.Extras)+16)
	for k, v := range cfg.Extras {
		out[k] = v
	}

	b, err := json.Marshal(cfg.Global)
	if err != nil {
		return nil, err
	}
	var global map[string]any
	if err := json.Unmarshal(b, &global); err != nil {
		return nil, err
	}
	for k, v := range global {
		out[k] = v
	}

	modules := cfg.Modules
	if modules == nil {
		modules = []Module{}
	}
	modsJSON, err := json.Marshal(modules)
	if err != nil {
		return nil, err
	}
	out["modules"] = json.RawMessage(modsJSON)

	return out, nil
}

// generateConfigJS generates a Magic Mirror config.js file in the flat
// top-level format MagicMirror expects.
func generateConfigJS(cfg *MagicMirrorConfig) ([]byte, error) {
	flat, err := configToMap(cfg)
	if err != nil {
		return nil, err
	}

	configJSON, err := json.MarshalIndent(flat, "", "  ")
	if err != nil {
		return nil, err
	}

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
