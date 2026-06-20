// Package chores edits the MMM-Chores chores.yaml on behalf of the family
// portal. The file is shared with the module's node_helper.js, which stamps
// ids, resets recurring chores, and records completion state — so the store
// parses chores as generic maps and only touches the fields the portal owns,
// leaving module-written fields (completed, completedAt, claimedBy, …) and
// anything added in the future intact. Writes are temp-file+rename atomic;
// an agent-vs-node_helper race is last-write-wins by design (HOM-61).
package chores

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

var ErrNotFound = errors.New("chore not found")

// ErrStorage marks read/write failures so handlers can hide filesystem
// detail from the family-facing UI while it still lands in the agent log.
var ErrStorage = errors.New("chores storage error")

// Fields the portal is allowed to set. "anyone" and "assignees" are mutually
// exclusive: bounty chores have anyone=true and no assignees.
//
// HOM-138 multi-assignee model (matches MMM-Chores store/choreModel.js):
//   - Assignees: 0, 1, or many names; replaces the legacy single Assignee.
//   - Mode: "independent" (each assignee earns their own tokens) | "shared"
//     (first to complete closes it for everyone). Single-assignee behaves the
//     same either way; the module defaults shared.
//   - TimeOfDay: "anytime" | "morning" | "nightly" (HOM-134 scheduling gate).
//   - AutoApprove: skip the parent verification queue (HOM-135).
//
// Assignee (singular) is still accepted on input for back-compat: it is folded
// into Assignees on apply. Anyone (bounty) takes precedence over both.
type Input struct {
	Name        string   `json:"name"`
	Assignee    string   `json:"assignee"` // legacy single assignee, folded into Assignees
	Assignees   []string `json:"assignees"`
	Mode        string   `json:"mode"`      // "independent" | "shared" (default shared when assigned)
	TimeOfDay   string   `json:"timeOfDay"` // "anytime" | "morning" | "nightly"
	Anyone      bool     `json:"anyone"`
	AutoApprove bool     `json:"autoApprove"`
	Repeat      any      `json:"repeat"`   // "daily" | "weekly" | "monthly" | positive int | nil
	Priority    string   `json:"priority"` // "high" | "low" | "" (normal)
	Tokens      *int     `json:"tokens"`
}

// inputAssignees returns the effective assignee list, folding a legacy single
// Assignee into Assignees and dropping blanks. Order is preserved.
func inputAssignees(in Input) []string {
	out := make([]string, 0, len(in.Assignees)+1)
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range in.Assignees {
		add(name)
	}
	add(in.Assignee)
	return out
}

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Path() string { return s.path }

// List returns the chores for display. Legacy single-assignee chores are
// normalized on read (assignee → assignees, default mode) the same way the
// module's migrateChores does, so the portal UI always sees the multi-assignee
// shape. This does not persist — the file is migrated lazily by whichever writer
// touches the chore next (matching the module's read-then-persist pattern).
func (s *Store) List() ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	chores := choreSlice(doc)
	for _, chore := range chores {
		migrateChore(chore)
	}
	return chores, nil
}

// migrateChore normalizes one chore in place: legacy `assignee: X` becomes
// `assignees: [X]`, and assigned chores get a default `mode`. Bounty chores
// (anyone:true) are left alone. Mirrors store/choreModel.js migrateChores.
func migrateChore(chore map[string]any) {
	if v, _ := chore["anyone"].(bool); v {
		return
	}
	if _, ok := chore["assignees"].([]any); !ok {
		var list []any
		if name, ok := chore["assignee"].(string); ok && name != "" {
			list = append(list, name)
		}
		chore["assignees"] = list
		delete(chore, "assignee")
	}
	if mode, _ := chore["mode"].(string); mode != "independent" && mode != "shared" {
		chore["mode"] = "shared"
	}
}

func (s *Store) Create(in Input) (map[string]any, error) {
	if err := validate(in); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	chores := choreSlice(doc)

	chore := map[string]any{"completed": false}
	applyInput(chore, in)
	chore["id"] = newID(chores)

	doc["chores"] = append(chores, chore)
	if err := s.save(doc); err != nil {
		return nil, err
	}
	return chore, nil
}

func (s *Store) Update(id string, in Input) (map[string]any, error) {
	if err := validate(in); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	chore := findChore(choreSlice(doc), id)
	if chore == nil {
		return nil, ErrNotFound
	}
	applyInput(chore, in)
	if err := s.save(doc); err != nil {
		return nil, err
	}
	return chore, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return err
	}
	chores := choreSlice(doc)
	for i, c := range chores {
		if c["id"] == id {
			doc["chores"] = append(chores[:i], chores[i+1:]...)
			return s.save(doc)
		}
	}
	return ErrNotFound
}

func (s *Store) load() (map[string]any, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %w", ErrStorage, s.path, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: parsing %s: %w", ErrStorage, s.path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

// Key order for serialized chores, matching the hand-written file and
// js-yaml's output so portal writes produce minimal diffs. Unknown keys
// land at the end, alphabetically.
var keyOrder = []string{"name", "assignee", "assignees", "mode", "anyone", "timeOfDay", "autoApprove", "due", "repeat", "priority", "completed", "completedAt", "claimedBy", "tokens", "id"}

func docNode(doc map[string]any) (*yaml.Node, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		if k != "chores" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	keys = append([]string{"chores"}, keys...)

	for _, key := range keys {
		value, ok := doc[key]
		if !ok {
			continue
		}
		var valueNode yaml.Node
		if key == "chores" {
			valueNode = yaml.Node{Kind: yaml.SequenceNode}
			list, _ := value.([]any)
			if list == nil {
				if typed, ok := value.([]map[string]any); ok {
					for _, m := range typed {
						list = append(list, m)
					}
				}
			}
			for _, item := range list {
				chore, ok := item.(map[string]any)
				if !ok {
					var n yaml.Node
					if err := n.Encode(item); err != nil {
						return nil, err
					}
					valueNode.Content = append(valueNode.Content, &n)
					continue
				}
				n, err := choreNode(chore)
				if err != nil {
					return nil, err
				}
				valueNode.Content = append(valueNode.Content, n)
			}
		} else if err := valueNode.Encode(value); err != nil {
			return nil, err
		}
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key}
		root.Content = append(root.Content, keyNode, &valueNode)
	}
	return root, nil
}

func choreNode(chore map[string]any) (*yaml.Node, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	known := map[string]bool{}
	for _, k := range keyOrder {
		known[k] = true
	}
	extras := make([]string, 0)
	for k := range chore {
		if !known[k] {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)

	for _, key := range append(append([]string{}, keyOrder...), extras...) {
		value, ok := chore[key]
		if !ok {
			continue
		}
		var valueNode yaml.Node
		if err := valueNode.Encode(value); err != nil {
			return nil, err
		}
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key}
		node.Content = append(node.Content, keyNode, &valueNode)
	}
	return node, nil
}

func (s *Store) save(doc map[string]any) error {
	if err := s.write(doc); err != nil {
		return fmt.Errorf("%w: writing %s: %w", ErrStorage, s.path, err)
	}
	return nil
}

func (s *Store) write(doc map[string]any) error {
	node, err := docNode(doc)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".chores-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp uses 0600; the module (node_helper.js) must be able to read.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func choreSlice(doc map[string]any) []map[string]any {
	list, _ := doc["chores"].([]any)
	chores := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			chores = append(chores, m)
		}
	}
	return chores
}

func findChore(chores []map[string]any, id string) map[string]any {
	for _, c := range chores {
		if c["id"] == id {
			return c
		}
	}
	return nil
}

// applyInput sets portal-owned fields, deleting keys whose value is the
// default so the YAML stays in the hand-written style the module expects
// (no "priority: normal", no "anyone: false").
func applyInput(chore map[string]any, in Input) {
	chore["name"] = in.Name

	// Always drop the legacy singular field; the model is multi-assignee now.
	delete(chore, "assignee")

	if in.Anyone {
		// Bounty chores have no assignees/mode/schedule/auto-approve.
		chore["anyone"] = true
		delete(chore, "assignees")
		delete(chore, "mode")
		delete(chore, "timeOfDay")
		delete(chore, "autoApprove")
	} else {
		delete(chore, "anyone")

		assignees := inputAssignees(in)
		list := make([]any, len(assignees))
		for i, name := range assignees {
			list[i] = name
		}
		chore["assignees"] = list

		// Always write mode for assigned chores (matches the module's migrate,
		// which stamps a mode on every assigned chore). Default "shared".
		mode := in.Mode
		if mode != "independent" && mode != "shared" {
			mode = "shared"
		}
		chore["mode"] = mode

		// Omit timeOfDay when "anytime" (the default the module assumes on read).
		if in.TimeOfDay == "" || in.TimeOfDay == "anytime" {
			delete(chore, "timeOfDay")
		} else {
			chore["timeOfDay"] = in.TimeOfDay
		}

		// Omit autoApprove when false.
		if in.AutoApprove {
			chore["autoApprove"] = true
		} else {
			delete(chore, "autoApprove")
		}
	}

	if f, ok := in.Repeat.(float64); ok { // JSON numbers decode as float64
		in.Repeat = int(f)
	}
	if in.Repeat == nil {
		delete(chore, "repeat")
	} else {
		chore["repeat"] = in.Repeat
	}

	if in.Priority == "" {
		delete(chore, "priority")
	} else {
		chore["priority"] = in.Priority
	}

	if in.Tokens == nil {
		delete(chore, "tokens")
	} else {
		chore["tokens"] = *in.Tokens
	}
}

// newID matches node_helper.js's scheme: "c" + 6 base36 chars, unique
// within the file.
func newID(chores []map[string]any) string {
	used := map[string]bool{}
	for _, c := range chores {
		if id, ok := c["id"].(string); ok {
			used[id] = true
		}
	}
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	for {
		id := make([]byte, 7)
		id[0] = 'c'
		for i := 1; i < 7; i++ {
			id[i] = alphabet[rand.Intn(len(alphabet))]
		}
		if !used[string(id)] {
			return string(id)
		}
	}
}

func validate(in Input) error {
	if in.Name == "" {
		return errors.New("name is required")
	}
	assignees := inputAssignees(in)
	if !in.Anyone && len(assignees) == 0 {
		return errors.New("pick at least one person, or open the chore to anyone")
	}
	if in.Anyone && (len(assignees) > 0) {
		return errors.New("a chore is either assigned or open to anyone, not both")
	}
	if in.Mode != "" && in.Mode != "independent" && in.Mode != "shared" {
		return errors.New("mode must be independent or shared")
	}
	if in.TimeOfDay != "" && in.TimeOfDay != "anytime" && in.TimeOfDay != "morning" && in.TimeOfDay != "nightly" {
		return errors.New("schedule must be anytime, morning, or nightly")
	}
	switch r := in.Repeat.(type) {
	case nil:
	case string:
		if r != "daily" && r != "weekly" && r != "monthly" {
			return fmt.Errorf("repeat must be daily, weekly, monthly, or a number of days")
		}
	case int:
		if r < 1 || r > 365 {
			return errors.New("repeat days must be between 1 and 365")
		}
	case float64: // JSON numbers decode as float64
		if r != float64(int(r)) || r < 1 || r > 365 {
			return errors.New("repeat days must be a whole number between 1 and 365")
		}
	default:
		return errors.New("repeat must be daily, weekly, monthly, or a number of days")
	}
	if in.Priority != "" && in.Priority != "high" && in.Priority != "low" {
		return errors.New("priority must be high, low, or empty")
	}
	if in.Tokens != nil && (*in.Tokens < 0 || *in.Tokens > 99) {
		return errors.New("tokens must be between 0 and 99")
	}
	return nil
}
