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
		// HOM-138: the portal now writes the multi-assignee shape — a single
		// `assignee` folds into `assignees: [..]` with a default `mode`.
		"  - name: Take out trash\n    assignees:\n      - Dad\n    mode: shared\n    repeat: daily\n    priority: high\n    completed: false\n    tokens: 2\n    id: chtgo0x",
		"  - name: Water the plants\n    anyone: true\n    repeat: weekly\n    completed: true\n    completedAt:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("serialized style drifted; want block:\n%s\ngot file:\n%s", want, got)
		}
	}
}

// ---- HOM-138 multi-assignee model -------------------------------------------

func assigneeStrings(chore map[string]any) []string {
	out := []string{}
	if list, ok := chore["assignees"].([]any); ok {
		for _, v := range list {
			if name, ok := v.(string); ok {
				out = append(out, name)
			}
		}
	}
	return out
}

func TestCreateMultiAssigneeWritesAssigneesAndMode(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(Input{
		Name:      "Set the table",
		Assignees: []string{"Gavin", "Savannah"},
		Mode:      "independent",
		TimeOfDay: "nightly",
		Tokens:    intPtr(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := assigneeStrings(created); len(got) != 2 || got[0] != "Gavin" || got[1] != "Savannah" {
		t.Errorf("assignees not written: %v", created["assignees"])
	}
	if created["mode"] != "independent" {
		t.Errorf("mode not written: %v", created["mode"])
	}
	if created["timeOfDay"] != "nightly" {
		t.Errorf("timeOfDay not written: %v", created["timeOfDay"])
	}
	if _, has := created["assignee"]; has {
		t.Errorf("legacy singular assignee should never be written: %v", created)
	}
}

func TestCreateDefaultsModeSharedAndOmitsDefaults(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(Input{Name: "Sweep", Assignees: []string{"Dad"}})
	if err != nil {
		t.Fatal(err)
	}
	if created["mode"] != "shared" {
		t.Errorf("mode should default to shared, got %v", created["mode"])
	}
	if _, has := created["timeOfDay"]; has {
		t.Errorf("anytime schedule should be omitted, got %v", created["timeOfDay"])
	}
	if _, has := created["autoApprove"]; has {
		t.Errorf("autoApprove:false should be omitted, got %v", created["autoApprove"])
	}
}

func TestAutoApproveWrittenWhenTrue(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(Input{Name: "Brush teeth", Assignees: []string{"Gavin"}, AutoApprove: true})
	if err != nil {
		t.Fatal(err)
	}
	if created["autoApprove"] != true {
		t.Errorf("autoApprove:true should be written, got %v", created["autoApprove"])
	}
}

func TestLegacyAssigneeFoldsIntoAssignees(t *testing.T) {
	s := newTestStore(t)
	// A client still sending the singular field gets folded into assignees[].
	created, err := s.Create(Input{Name: "Vacuum", Assignee: "Mom"})
	if err != nil {
		t.Fatal(err)
	}
	if got := assigneeStrings(created); len(got) != 1 || got[0] != "Mom" {
		t.Errorf("legacy assignee not folded: %v", created["assignees"])
	}
}

func TestListMigratesLegacyAssigneeOnRead(t *testing.T) {
	s := newTestStore(t)
	// The sample file's "Take out trash" has a legacy `assignee: Dad`.
	chores, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var trash map[string]any
	for _, c := range chores {
		if c["id"] == "chtgo0x" {
			trash = c
		}
	}
	if trash == nil {
		t.Fatal("missing chore")
	}
	if got := assigneeStrings(trash); len(got) != 1 || got[0] != "Dad" {
		t.Errorf("legacy assignee not migrated on read: %v", trash["assignees"])
	}
	if trash["mode"] != "shared" {
		t.Errorf("migrated chore should get default mode shared, got %v", trash["mode"])
	}
	if _, has := trash["assignee"]; has {
		t.Errorf("migrated read should drop legacy assignee, got %v", trash)
	}
}

func TestSwitchToBountyClearsMultiAssigneeFields(t *testing.T) {
	s := newTestStore(t)
	// Start as a scheduled, auto-approve, multi-assignee chore.
	if _, err := s.Update("chtgo0x", Input{
		Name: "Take out trash", Assignees: []string{"Dad", "Gavin"},
		Mode: "independent", TimeOfDay: "morning", AutoApprove: true,
	}); err != nil {
		t.Fatal(err)
	}
	// Now flip to a bounty — every assigned-only field must clear.
	updated, err := s.Update("chtgo0x", Input{Name: "Take out trash", Anyone: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"assignees", "mode", "timeOfDay", "autoApprove", "assignee"} {
		if _, has := updated[k]; has {
			t.Errorf("bounty chore should not carry %q: %v", k, updated)
		}
	}
}

func TestValidationMultiAssignee(t *testing.T) {
	s := newTestStore(t)
	bad := []Input{
		{Name: "x"}, // no assignees, not anyone
		{Name: "x", Assignees: []string{"Dad"}, Anyone: true},          // both assigned and anyone
		{Name: "x", Assignees: []string{"Dad"}, Mode: "team"},          // bad mode
		{Name: "x", Assignees: []string{"Dad"}, TimeOfDay: "midnight"}, // bad schedule
	}
	for i, in := range bad {
		if _, err := s.Create(in); err == nil {
			t.Errorf("case %d: want validation error for %+v", i, in)
		}
	}
}

func TestMissingFileIsCleanError(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "missing.yaml"))
	if _, err := s.List(); err == nil {
		t.Error("want error for missing file")
	}
}
