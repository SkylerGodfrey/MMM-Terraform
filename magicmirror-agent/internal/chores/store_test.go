package chores

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const sampleYAML = `chores:
  - name: Take out trash
    assignee: Dad
    repeat: daily
    priority: high
    completed: false
    tokens: 2
    id: chtgo0x
  - name: Wash dishes
    assignee: Savannah
    repeat: 2
    completed: true
    completedAt: "2026-06-11T19:04:00.000Z"
    tokens: 2
    id: c3adv3l
  - name: Water the plants
    anyone: true
    repeat: weekly
    completed: true
    claimedBy: Gavin
    completedAt: "2026-06-11T20:00:00.000Z"
    tokens: 3
    id: c9afofp
`

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chores.yaml")
	if err := os.WriteFile(path, []byte(sampleYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewStore(path)
}

func intPtr(v int) *int { return &v }

func TestListReturnsAllFields(t *testing.T) {
	s := newTestStore(t)
	chores, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(chores) != 3 {
		t.Fatalf("want 3 chores, got %d", len(chores))
	}
	if chores[1]["completedAt"] != "2026-06-11T19:04:00.000Z" {
		t.Errorf("module-owned field missing from list: %v", chores[1])
	}
}

func TestCreateAppendsWithGeneratedID(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(Input{Name: "Feed the dog", Assignee: "Gavin", Repeat: "daily", Tokens: intPtr(1)})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := created["id"].(string)
	if !regexp.MustCompile(`^c[0-9a-z]{6}$`).MatchString(id) {
		t.Errorf("id %q does not match node_helper scheme", id)
	}
	if created["completed"] != false {
		t.Errorf("new chore should start uncompleted")
	}

	chores, _ := s.List()
	if len(chores) != 4 || chores[3]["name"] != "Feed the dog" {
		t.Errorf("chore not appended: %v", chores)
	}
}

func TestUpdatePreservesModuleOwnedFields(t *testing.T) {
	s := newTestStore(t)
	// Rename the completed bounty chore; claimedBy/completedAt must survive.
	updated, err := s.Update("c9afofp", Input{Name: "Water all the plants", Anyone: true, Repeat: "weekly", Tokens: intPtr(3)})
	if err != nil {
		t.Fatal(err)
	}
	if updated["claimedBy"] != "Gavin" || updated["completedAt"] == nil || updated["completed"] != true {
		t.Errorf("module-owned fields lost on update: %v", updated)
	}
	if updated["name"] != "Water all the plants" {
		t.Errorf("name not updated: %v", updated)
	}
}

func TestUpdateSwitchesAssignedToBounty(t *testing.T) {
	s := newTestStore(t)
	updated, err := s.Update("chtgo0x", Input{Name: "Take out trash", Anyone: true, Repeat: "daily"})
	if err != nil {
		t.Fatal(err)
	}
	if updated["anyone"] != true {
		t.Errorf("anyone not set: %v", updated)
	}
	if _, has := updated["assignee"]; has {
		t.Errorf("assignee should be removed for bounty chores: %v", updated)
	}
	if _, has := updated["tokens"]; has {
		t.Errorf("omitted tokens should clear the key: %v", updated)
	}
	if _, has := updated["priority"]; has {
		t.Errorf("omitted priority should clear the key: %v", updated)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete("c3adv3l"); err != nil {
		t.Fatal(err)
	}
	chores, _ := s.List()
	if len(chores) != 2 {
		t.Fatalf("want 2 chores after delete, got %d", len(chores))
	}
	if err := s.Delete("nope"); err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestUpdateNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Update("nope", Input{Name: "x", Assignee: "Dad"}); err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestNumericRepeatFromJSONStaysWholeNumber(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(Input{Name: "Laundry", Assignee: "Mom", Repeat: float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if created["repeat"] != 2 {
		t.Errorf("want repeat 2 (int), got %T %v", created["repeat"], created["repeat"])
	}
	raw, _ := os.ReadFile(s.Path())
	if strings.Contains(string(raw), "repeat: 2.0") || !strings.Contains(string(raw), "repeat: 2") {
		t.Errorf("repeat serialized badly:\n%s", raw)
	}
}

func TestValidation(t *testing.T) {
	s := newTestStore(t)
	cases := []Input{
		{Name: "", Assignee: "Dad"},
		{Name: "x"},
		{Name: "x", Assignee: "Dad", Anyone: true},
		{Name: "x", Assignee: "Dad", Repeat: "fortnightly"},
		{Name: "x", Assignee: "Dad", Repeat: float64(1.5)},
		{Name: "x", Assignee: "Dad", Repeat: 0},
		{Name: "x", Assignee: "Dad", Priority: "urgent"},
		{Name: "x", Assignee: "Dad", Tokens: intPtr(-1)},
		{Name: "x", Assignee: "Dad", Tokens: intPtr(100)},
	}
	for i, in := range cases {
		if _, err := s.Create(in); err == nil {
			t.Errorf("case %d: want validation error for %+v", i, in)
		}
	}
	chores, _ := s.List()
	if len(chores) != 3 {
		t.Errorf("failed validations must not write: %d chores", len(chores))
	}
}

func TestFilePermissionsAfterWrite(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(Input{Name: "Feed the dog", Assignee: "Gavin"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("want 0644 after atomic write, got %v", info.Mode().Perm())
	}
}

func TestSerializedStyleMatchesHandWrittenFile(t *testing.T) {
	s := newTestStore(t)
	// A no-op update on an untouched chore should keep the file's shape:
	// name first, 2-space list indent, ids last-ish — minimal diff churn.
	if _, err := s.Update("chtgo0x", Input{Name: "Take out trash", Assignee: "Dad", Repeat: "daily", Priority: "high", Tokens: intPtr(2)}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.Path())
	got := string(raw)
	for _, want := range []string{
		"  - name: Take out trash\n    assignee: Dad\n    repeat: daily\n    priority: high\n    completed: false\n    tokens: 2\n    id: chtgo0x",
		"  - name: Water the plants\n    anyone: true\n    repeat: weekly\n    completed: true\n    completedAt:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("serialized style drifted; want block:\n%s\ngot file:\n%s", want, got)
		}
	}
}

func TestMissingFileIsCleanError(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "missing.yaml"))
	if _, err := s.List(); err == nil {
		t.Error("want error for missing file")
	}
}
