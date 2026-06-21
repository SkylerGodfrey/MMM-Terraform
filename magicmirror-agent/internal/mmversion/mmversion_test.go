package mmversion

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	valid := []string{"MMM-Chores", "clock", "MMM-CalendarExt3", "mod_1.2", "a-b_c.d"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{"", ".", "..", "default", "../etc", "a/b", "a\\b", "a b", "mod;rm", "~mod", "a/../b"}
	for _, name := range invalid {
		if err := ValidateName(name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidateName(%q) = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestCoreVersion(t *testing.T) {
	mmPath := t.TempDir()
	pkg := `{"name": "magicmirror", "version": "2.34.0"}`
	if err := os.WriteFile(filepath.Join(mmPath, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(mmPath)
	got, err := m.CoreVersion()
	if err != nil {
		t.Fatalf("CoreVersion() error: %v", err)
	}
	if got != "2.34.0" {
		t.Errorf("CoreVersion() = %q, want %q", got, "2.34.0")
	}
}

func TestCoreVersionMissingFile(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.CoreVersion(); err == nil {
		t.Error("CoreVersion() with no package.json should error")
	}
}

func TestElectronVersion(t *testing.T) {
	mmPath := t.TempDir()
	electronDir := filepath.Join(mmPath, "node_modules", "electron")
	if err := os.MkdirAll(electronDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(electronDir, "package.json"), []byte(`{"version": "36.6.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := NewManager(mmPath).electronVersion(); got != "36.6.0" {
		t.Errorf("electronVersion() = %q, want %q", got, "36.6.0")
	}
}

func TestElectronVersionAbsentIsEmpty(t *testing.T) {
	// No node_modules/electron (e.g. a serveronly install) -> "" -> rebuild skipped.
	if got := NewManager(t.TempDir()).electronVersion(); got != "" {
		t.Errorf("electronVersion() = %q, want empty", got)
	}
}

func TestListInstalledSkipsDefaultAndHandlesNonGit(t *testing.T) {
	mmPath := t.TempDir()
	for _, dir := range []string{"default", "MMM-Chores"} {
		if err := os.MkdirAll(filepath.Join(mmPath, "modules", dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(mmPath, "modules", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(mmPath)
	installed, err := m.ListInstalled()
	if err != nil {
		t.Fatalf("ListInstalled() error: %v", err)
	}
	if len(installed) != 1 {
		t.Fatalf("ListInstalled() returned %d entries, want 1: %+v", len(installed), installed)
	}
	mod := installed[0]
	if mod.Name != "MMM-Chores" || mod.Ref != "" || mod.Commit != "" || mod.Repository != "" {
		t.Errorf("unexpected entry for non-git dir: %+v", mod)
	}
}

func TestGetInstalledNotFound(t *testing.T) {
	mmPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mmPath, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(mmPath)
	if _, err := m.GetInstalled("MMM-Missing"); !errors.Is(err, ErrModuleNotFound) {
		t.Errorf("GetInstalled(missing) = %v, want ErrModuleNotFound", err)
	}
	if _, err := m.GetInstalled("../escape"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("GetInstalled(traversal) = %v, want ErrInvalidName", err)
	}
}

// gitFixture creates a local repo with two tagged commits and returns
// its path plus the full sha of the v1.0.0 commit.
func gitFixture(t *testing.T) (repoPath, v1Sha string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repoPath = filepath.Join(t.TempDir(), "fixture-module")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}

	gitIn := func(args ...string) string {
		t.Helper()
		base := []string{
			"-c", "user.name=test",
			"-c", "user.email=test@example.com",
			"-c", "commit.gpgsign=false",
			"-c", "tag.gpgsign=false",
		}
		cmd := exec.Command("git", append(base, args...)...)
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	gitIn("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repoPath, "index.js"), []byte("// v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn("add", "index.js")
	gitIn("commit", "-q", "-m", "v1")
	gitIn("tag", "v1.0.0")
	v1Sha = gitIn("rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(repoPath, "index.js"), []byte("// v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn("add", "index.js")
	gitIn("commit", "-q", "-m", "v2")
	gitIn("tag", "v1.1.0")

	return repoPath, v1Sha
}

func TestConvergeCloneCheckoutAndUpgrade(t *testing.T) {
	repoPath, v1Sha := gitFixture(t)

	mmPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mmPath, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(mmPath)

	mod, err := m.Converge("MMM-Test", repoPath, "v1.0.0")
	if err != nil {
		t.Fatalf("Converge(clone @ v1.0.0) error: %v", err)
	}
	if mod.Ref != "v1.0.0" {
		t.Errorf("after clone, Ref = %q, want v1.0.0", mod.Ref)
	}
	if mod.Commit != v1Sha {
		t.Errorf("after clone, Commit = %q, want %q", mod.Commit, v1Sha)
	}
	if mod.Repository != repoPath {
		t.Errorf("after clone, Repository = %q, want %q", mod.Repository, repoPath)
	}

	// Existing clone: fetch + checkout a newer tag without re-cloning
	mod, err = m.Converge("MMM-Test", "", "v1.1.0")
	if err != nil {
		t.Fatalf("Converge(upgrade to v1.1.0) error: %v", err)
	}
	if mod.Ref != "v1.1.0" {
		t.Errorf("after upgrade, Ref = %q, want v1.1.0", mod.Ref)
	}

	// Checkout by full commit sha
	mod, err = m.Converge("MMM-Test", "", v1Sha)
	if err != nil {
		t.Fatalf("Converge(checkout sha) error: %v", err)
	}
	if mod.Commit != v1Sha {
		t.Errorf("after sha checkout, Commit = %q, want %q", mod.Commit, v1Sha)
	}
}

func TestConvergeMissingRepository(t *testing.T) {
	mmPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mmPath, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(mmPath)
	if _, err := m.Converge("MMM-New", "", "v1.0.0"); !errors.Is(err, ErrRepositoryRequired) {
		t.Errorf("Converge with no repo = %v, want ErrRepositoryRequired", err)
	}
}

func TestConvergeRejectsFlagLikeVersion(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.Converge("MMM-Test", "", "-b evil"); err == nil {
		t.Error("Converge with flag-like version should error")
	}
}

func TestRemove(t *testing.T) {
	repoPath, _ := gitFixture(t)

	mmPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mmPath, "modules", "MMM-Chores"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(mmPath)

	if err := m.Remove("MMM-Chores"); !errors.Is(err, ErrNotGitRepo) {
		t.Errorf("Remove(non-git dir) = %v, want ErrNotGitRepo", err)
	}

	if _, err := m.Converge("MMM-Test", repoPath, ""); err != nil {
		t.Fatalf("Converge for Remove setup failed: %v", err)
	}
	if err := m.Remove("MMM-Test"); err != nil {
		t.Errorf("Remove(git clone) error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mmPath, "modules", "MMM-Test")); !os.IsNotExist(err) {
		t.Error("module directory still exists after Remove")
	}

	if err := m.Remove("MMM-Test"); !errors.Is(err, ErrModuleNotFound) {
		t.Errorf("Remove(missing) = %v, want ErrModuleNotFound", err)
	}
	if err := m.Remove("default"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("Remove(default) = %v, want ErrInvalidName", err)
	}
}
