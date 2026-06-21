// Package mmversion inspects and converges MagicMirror module installs
// (git clone / fetch / checkout / npm install) under <mm_path>/modules,
// and reads the core MagicMirror version from <mm_path>/package.json.
package mmversion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrModuleNotFound is returned when a module directory doesn't exist
	ErrModuleNotFound = errors.New("module not installed")
	// ErrInvalidName is returned when a module name fails validation
	ErrInvalidName = errors.New("invalid module name")
	// ErrRepositoryRequired is returned when a clone is needed but no repository was given
	ErrRepositoryRequired = errors.New("module is not installed and no repository was provided")
	// ErrNotGitRepo is returned when deleting a module that isn't a git clone
	ErrNotGitRepo = errors.New("module directory is not a git repository; refusing to delete")
)

var validNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// InstalledModule describes a directory under <mm_path>/modules.
// Ref/Commit/Repository are empty strings for non-git directories.
type InstalledModule struct {
	Name       string `json:"name"`
	Ref        string `json:"ref"`
	Commit     string `json:"commit"`
	Repository string `json:"repository"`
}

// Manager performs version operations against a MagicMirror install
type Manager struct {
	mmPath     string
	gitTimeout time.Duration
	npmTimeout time.Duration
}

// NewManager creates a manager rooted at the MagicMirror install directory
func NewManager(mmPath string) *Manager {
	return &Manager{
		mmPath:     mmPath,
		gitTimeout: 5 * time.Minute,
		npmTimeout: 10 * time.Minute,
	}
}

// ValidateName rejects names that could escape <mm_path>/modules or
// touch the protected default modules directory.
func ValidateName(name string) error {
	if name == "" || name == "." || name == ".." || name == "default" {
		return ErrInvalidName
	}
	if !validNamePattern.MatchString(name) {
		return ErrInvalidName
	}
	return nil
}

// CoreVersion reads the MagicMirror version from <mm_path>/package.json
func (m *Manager) CoreVersion() (string, error) {
	data, err := os.ReadFile(filepath.Join(m.mmPath, "package.json"))
	if err != nil {
		return "", fmt.Errorf("failed to read MagicMirror package.json: %w", err)
	}

	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "", fmt.Errorf("failed to parse MagicMirror package.json: %w", err)
	}
	if pkg.Version == "" {
		return "", fmt.Errorf("MagicMirror package.json has no version field")
	}

	return pkg.Version, nil
}

// ListInstalled returns one entry per directory in <mm_path>/modules,
// skipping the built-in default modules directory.
func (m *Manager) ListInstalled() ([]InstalledModule, error) {
	entries, err := os.ReadDir(m.modulesDir())
	if err != nil {
		return nil, fmt.Errorf("failed to read modules directory: %w", err)
	}

	installed := make([]InstalledModule, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "default" {
			continue
		}
		installed = append(installed, m.inspect(entry.Name()))
	}

	return installed, nil
}

// GetInstalled returns info for a single module directory
func (m *Manager) GetInstalled(name string) (*InstalledModule, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}

	info, err := os.Stat(m.moduleDir(name))
	if err != nil || !info.IsDir() {
		return nil, ErrModuleNotFound
	}

	mod := m.inspect(name)
	return &mod, nil
}

// Converge ensures the module is installed at the requested version:
// clone if missing, fetch+checkout if a version is given, then npm install.
func (m *Manager) Converge(name, repository, version string) (*InstalledModule, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	// Guard against git flag injection; "--" handles this for clone but
	// checkout takes the ref positionally.
	if strings.HasPrefix(version, "-") {
		return nil, fmt.Errorf("invalid version %q", version)
	}

	dir := m.moduleDir(name)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		if repository == "" {
			return nil, ErrRepositoryRequired
		}
		if _, err := m.run(m.modulesDir(), m.gitTimeout, "git", "clone", "--", repository, name); err != nil {
			return nil, fmt.Errorf("git clone failed: %w", err)
		}
	}

	if version != "" {
		// Skip fetch/checkout/npm when already at the declared version so
		// no-op terraform applies stay fast.
		if current := m.inspect(name); matchesVersion(&current, version) {
			return &current, nil
		}
		if _, err := m.run(dir, m.gitTimeout, "git", "fetch", "--tags"); err != nil {
			return nil, fmt.Errorf("git fetch failed: %w", err)
		}
		if _, err := m.run(dir, m.gitTimeout, "git", "checkout", version, "--"); err != nil {
			return nil, fmt.Errorf("git checkout %s failed: %w", version, err)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		if _, err := m.run(dir, m.npmTimeout, "npm", "install", "--omit=dev"); err != nil {
			return nil, fmt.Errorf("npm install failed: %w", err)
		}
		// HOM-142: MagicMirror loads node_helpers inside Electron, so a module's
		// native addon (e.g. MMM-Chores' better-sqlite3) installed with plain
		// `npm install` is built for the system-Node ABI and fails to load —
		// silently degrading the module. Rebuild against Electron's ABI.
		if err := m.rebuildForElectron(dir); err != nil {
			return nil, err
		}
	}

	return m.GetInstalled(name)
}

// electronVersion reads the Electron version MagicMirror runs under, or ""
// when MagicMirror isn't an Electron install (e.g. serveronly) — in which case
// the system-Node ABI is already correct and no rebuild is needed.
func (m *Manager) electronVersion() string {
	data, err := os.ReadFile(filepath.Join(m.mmPath, "node_modules", "electron", "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	return pkg.Version
}

// rebuildForElectron rebuilds the module's native addons against Electron's
// ABI. A no-op (trivially successful) for modules with no native dependencies,
// and skipped entirely when MagicMirror isn't an Electron install.
func (m *Manager) rebuildForElectron(dir string) error {
	ev := m.electronVersion()
	if ev == "" {
		return nil
	}
	env := []string{
		"npm_config_runtime=electron",
		"npm_config_target=" + ev,
		"npm_config_disturl=https://electronjs.org/headers",
	}
	if _, err := m.runEnv(dir, m.npmTimeout, env, "npm", "rebuild"); err != nil {
		return fmt.Errorf("npm rebuild for electron %s failed: %w", ev, err)
	}
	return nil
}

// matchesVersion mirrors the provider's drift rule: the declared version
// matches a tag/describe ref exactly, a commit by prefix, or a describe
// output that starts with the declared tag.
func matchesVersion(mod *InstalledModule, version string) bool {
	if mod.Ref == "" && mod.Commit == "" {
		return false
	}
	return version == mod.Ref ||
		strings.HasPrefix(mod.Commit, version) ||
		strings.HasPrefix(mod.Ref, version)
}

// Remove deletes a module directory, refusing if it isn't a git clone
// so hand-deployed modules are protected.
func (m *Manager) Remove(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}

	dir := m.moduleDir(name)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ErrModuleNotFound
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return ErrNotGitRepo
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("failed to remove module directory: %w", err)
	}
	return nil
}

func (m *Manager) modulesDir() string {
	return filepath.Join(m.mmPath, "modules")
}

func (m *Manager) moduleDir(name string) string {
	return filepath.Join(m.modulesDir(), name)
}

// inspect gathers git info for a module directory; all fields except
// Name are empty for non-git directories.
func (m *Manager) inspect(name string) InstalledModule {
	mod := InstalledModule{Name: name}
	dir := m.moduleDir(name)

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return mod
	}

	if out, err := m.run(dir, m.gitTimeout, "git", "describe", "--tags", "--always"); err == nil {
		mod.Ref = out
	}
	if out, err := m.run(dir, m.gitTimeout, "git", "rev-parse", "HEAD"); err == nil {
		mod.Commit = out
	}
	if out, err := m.run(dir, m.gitTimeout, "git", "remote", "get-url", "origin"); err == nil {
		mod.Repository = out
	}

	return mod
}

// run executes a command with a timeout, returning trimmed stdout and
// surfacing stderr in the error message.
func (m *Manager) run(dir string, timeout time.Duration, name string, args ...string) (string, error) {
	return m.runEnv(dir, timeout, nil, name, args...)
}

// runEnv is run() with extra environment variables appended to the inherited
// environment (used to drive npm's native-build target — see rebuildForElectron).
func (m *Manager) runEnv(dir string, timeout time.Duration, env []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%s %s timed out after %s", name, strings.Join(args, " "), timeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}

	return strings.TrimSpace(stdout.String()), nil
}
