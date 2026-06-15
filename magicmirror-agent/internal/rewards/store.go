// Package rewards edits the MMM-Chores rewards.yaml on behalf of the
// family portal. The file is shared with the module's node_helper.js,
// which mutates users[].tokens and appends redemptions[] — so the store
// parses the document as generic maps and only touches the fields the
// portal owns, leaving module-written fields intact. Writes are
// temp-file+rename atomic; an agent-vs-node_helper race is
// last-write-wins by design (same trade-off as chores, HOM-61).
package rewards

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/disintegration/imaging"
	"gopkg.in/yaml.v3"
)

var ErrNotFound = errors.New("reward not found")
var ErrDuplicateName = errors.New("a reward with that name already exists")

// ErrStorage marks read/write failures so handlers can hide filesystem
// detail from the family-facing UI while it still lands in the agent log.
var ErrStorage = errors.New("rewards storage error")

// ErrNotAnImage matches the photos package — the portal sheet should
// pre-validate, but a determined client could still upload garbage.
var ErrNotAnImage = errors.New("not a supported image type")

var allowedImageExt = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Input is what the portal POST/PUT body decodes to. Cost is required;
// Quantity is optional (nil = unlimited stock); Image is the filename
// inside rewards-images/, set after a successful upload.
type Input struct {
	Name       string   `json:"name"`
	Cost       *int     `json:"cost"`
	Quantity   *int     `json:"quantity"`
	Image      string   `json:"image"`
	AssignedTo []string `json:"assignedTo"`
}

type Store struct {
	path     string
	imageDir string
	mu       sync.Mutex
}

func NewStore(path, imageDir string) *Store {
	return &Store{path: path, imageDir: imageDir}
}

func (s *Store) Path() string     { return s.path }
func (s *Store) ImageDir() string { return s.imageDir }

// List returns the rewards exactly as stored (all fields), for display.
// Each reward gets a stamped id if it didn't already have one, so the
// portal can address it stably across renames.
func (s *Store) List() ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	rewards := rewardSlice(doc)
	if stampIDs(rewards) {
		doc["rewards"] = toAnySlice(rewards)
		if err := s.save(doc); err != nil {
			return nil, err
		}
	}
	return rewards, nil
}

// Users returns the users[] section of rewards.yaml as the portal needs
// it for the assignee picker (name + token balance). The module owns
// this list, so reads only — never written here.
func (s *Store) Users() ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	users, _ := doc["users"].([]any)
	out := make([]map[string]any, 0, len(users))
	for _, item := range users {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
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
	rewards := rewardSlice(doc)
	stampIDs(rewards)
	if nameTaken(rewards, in.Name, "") {
		return nil, ErrDuplicateName
	}

	reward := map[string]any{}
	applyInput(reward, in)
	reward["id"] = newID(rewards)

	rewards = append(rewards, reward)
	doc["rewards"] = toAnySlice(rewards)
	if err := s.save(doc); err != nil {
		return nil, err
	}
	return reward, nil
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
	rewards := rewardSlice(doc)
	stampIDs(rewards)
	reward := findReward(rewards, id)
	if reward == nil {
		return nil, ErrNotFound
	}
	if nameTaken(rewards, in.Name, id) {
		return nil, ErrDuplicateName
	}
	applyInput(reward, in)
	doc["rewards"] = toAnySlice(rewards)
	if err := s.save(doc); err != nil {
		return nil, err
	}
	return reward, nil
}

// Delete removes the reward by id. If its image is no longer referenced
// by any remaining reward, the file is removed too — best-effort, an
// orphan won't fail the call.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return err
	}
	rewards := rewardSlice(doc)
	stampIDs(rewards)

	var removedImage string
	idx := -1
	for i, r := range rewards {
		if r["id"] == id {
			idx = i
			if name, ok := r["image"].(string); ok {
				removedImage = name
			}
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	rewards = append(rewards[:idx], rewards[idx+1:]...)
	doc["rewards"] = toAnySlice(rewards)
	if err := s.save(doc); err != nil {
		return err
	}

	if removedImage != "" && !imageStillUsed(rewards, removedImage) {
		_ = os.Remove(filepath.Join(s.imageDir, removedImage))
	}
	return nil
}

// SaveImage stores an uploaded image in rewards-images/ and returns the
// final filename (sanitized, suffixed to avoid overwrites). Matches the
// photo store's reject-non-images check.
func (s *Store) SaveImage(name string, src io.Reader) (string, error) {
	name = sanitizeName(name)
	ext := strings.ToLower(filepath.Ext(name))
	if !allowedImageExt[ext] {
		return "", fmt.Errorf("%w: %s", ErrNotAnImage, ext)
	}
	if err := os.MkdirAll(s.imageDir, 0o755); err != nil {
		return "", fmt.Errorf("%w: %w", ErrStorage, err)
	}

	path, final, err := s.freePath(name)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(s.imageDir, ".reward-*")
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrStorage, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return "", fmt.Errorf("%w: %w", ErrStorage, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrStorage, err)
	}
	if _, err := imaging.Open(tmp.Name()); err != nil {
		return "", fmt.Errorf("%w: unreadable image: %v", ErrNotAnImage, err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", fmt.Errorf("%w: %w", ErrStorage, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("%w: %w", ErrStorage, err)
	}
	return final, nil
}

// ImagePath resolves a stored image name to a path for serving.
func (s *Store) ImagePath(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) ||
		strings.HasPrefix(name, ".") || !allowedImageExt[strings.ToLower(filepath.Ext(name))] {
		return "", ErrNotFound
	}
	path := filepath.Join(s.imageDir, name)
	if _, err := os.Stat(path); err != nil {
		return "", ErrNotFound
	}
	return path, nil
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

// Top-level key order matches the hand-written file (users → rewards →
// redemptions); per-reward key order matches the existing entries so
// portal writes produce minimal diffs.
var topKeyOrder = []string{"users", "rewards", "redemptions"}
var rewardKeyOrder = []string{"name", "cost", "image", "quantity", "assignedTo", "id"}

func docNode(doc map[string]any) (*yaml.Node, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}
	known := map[string]bool{}
	for _, k := range topKeyOrder {
		known[k] = true
	}
	extras := make([]string, 0)
	for k := range doc {
		if !known[k] {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)

	for _, key := range append(append([]string{}, topKeyOrder...), extras...) {
		value, ok := doc[key]
		if !ok {
			continue
		}
		var valueNode yaml.Node
		if key == "rewards" {
			valueNode = yaml.Node{Kind: yaml.SequenceNode}
			for _, item := range toAnySlice(rewardSlice(doc)) {
				reward, _ := item.(map[string]any)
				if reward == nil {
					var n yaml.Node
					if err := n.Encode(item); err != nil {
						return nil, err
					}
					valueNode.Content = append(valueNode.Content, &n)
					continue
				}
				n, err := rewardNode(reward)
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

func rewardNode(reward map[string]any) (*yaml.Node, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	known := map[string]bool{}
	for _, k := range rewardKeyOrder {
		known[k] = true
	}
	extras := make([]string, 0)
	for k := range reward {
		if !known[k] {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)

	for _, key := range append(append([]string{}, rewardKeyOrder...), extras...) {
		value, ok := reward[key]
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
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".rewards-*.yaml")
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
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func rewardSlice(doc map[string]any) []map[string]any {
	list, _ := doc["rewards"].([]any)
	rewards := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			rewards = append(rewards, m)
		}
	}
	return rewards
}

func toAnySlice(rewards []map[string]any) []any {
	out := make([]any, len(rewards))
	for i, r := range rewards {
		out[i] = r
	}
	return out
}

func findReward(rewards []map[string]any, id string) map[string]any {
	for _, r := range rewards {
		if r["id"] == id {
			return r
		}
	}
	return nil
}

func nameTaken(rewards []map[string]any, name, excludeID string) bool {
	for _, r := range rewards {
		if r["id"] == excludeID {
			continue
		}
		if existing, _ := r["name"].(string); strings.EqualFold(existing, name) {
			return true
		}
	}
	return false
}

func imageStillUsed(rewards []map[string]any, image string) bool {
	for _, r := range rewards {
		if got, _ := r["image"].(string); got == image {
			return true
		}
	}
	return false
}

// applyInput sets portal-owned fields, deleting keys whose value is the
// default so the YAML stays close to the hand-written style.
func applyInput(reward map[string]any, in Input) {
	reward["name"] = in.Name

	if in.Cost != nil {
		reward["cost"] = *in.Cost
	} else {
		delete(reward, "cost")
	}

	if in.Quantity != nil {
		reward["quantity"] = *in.Quantity
	} else {
		delete(reward, "quantity")
	}

	if in.Image == "" {
		delete(reward, "image")
	} else {
		reward["image"] = in.Image
	}

	if len(in.AssignedTo) == 0 {
		delete(reward, "assignedTo")
	} else {
		assignees := make([]any, len(in.AssignedTo))
		for i, name := range in.AssignedTo {
			assignees[i] = name
		}
		reward["assignedTo"] = assignees
	}
}

// stampIDs gives any reward without an id a short random one. Returns
// true if the slice was modified, so callers know to persist.
func stampIDs(rewards []map[string]any) bool {
	used := map[string]bool{}
	for _, r := range rewards {
		if id, ok := r["id"].(string); ok && id != "" {
			used[id] = true
		}
	}
	changed := false
	for _, r := range rewards {
		if id, ok := r["id"].(string); ok && id != "" {
			continue
		}
		id := makeID(used)
		used[id] = true
		r["id"] = id
		changed = true
	}
	return changed
}

func newID(rewards []map[string]any) string {
	used := map[string]bool{}
	for _, r := range rewards {
		if id, ok := r["id"].(string); ok {
			used[id] = true
		}
	}
	return makeID(used)
}

// makeID matches node_helper.js's "c" + 6 base36 scheme used for chores;
// rewards share the alphabet so it reads consistently in the YAML.
func makeID(used map[string]bool) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	for {
		id := make([]byte, 7)
		id[0] = 'r'
		for i := 1; i < 7; i++ {
			id[i] = alphabet[rand.Intn(len(alphabet))]
		}
		if !used[string(id)] {
			return string(id)
		}
	}
}

func validate(in Input) error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return errors.New("name is required")
	}
	if len(name) > 60 {
		return errors.New("name must be 60 characters or fewer")
	}
	if in.Cost == nil {
		return errors.New("cost is required")
	}
	if *in.Cost < 0 || *in.Cost > 999 {
		return errors.New("cost must be between 0 and 999 tokens")
	}
	if in.Quantity != nil && (*in.Quantity < 0 || *in.Quantity > 9999) {
		return errors.New("quantity must be between 0 and 9999, or omitted for unlimited stock")
	}
	if in.Image != "" {
		if in.Image != filepath.Base(in.Image) || strings.ContainsAny(in.Image, `/\`) ||
			strings.HasPrefix(in.Image, ".") || !allowedImageExt[strings.ToLower(filepath.Ext(in.Image))] {
			return errors.New("image must be a stored filename in rewards-images/")
		}
	}
	for _, name := range in.AssignedTo {
		if strings.TrimSpace(name) == "" {
			return errors.New("assignee names cannot be blank")
		}
	}
	return nil
}

func sanitizeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = unsafeChars.ReplaceAllString(name, "_")
	name = strings.TrimLeft(name, "._")
	if name == "" {
		name = "reward.png"
	}
	return name
}

func (s *Store) freePath(name string) (string, string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	candidate := name
	for i := 0; ; i++ {
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		path := filepath.Join(s.imageDir, candidate)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path, candidate, nil
		} else if err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrStorage, err)
		}
		if i > 1000 {
			return "", "", fmt.Errorf("%w: no free filename for %s", ErrStorage, name)
		}
	}
}
