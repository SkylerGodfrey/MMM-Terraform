package canvas

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ModuleLister is satisfied by mmconfig.Manager; the store uses it to
// validate that slot.Module refers to a real module ID. Decoupled via
// interface so the canvas package doesn't import mmconfig.
type ModuleLister interface {
	ListModuleIDs() ([]string, error)
}

// Store is the file-backed canvas document. Concurrent reads are safe;
// writes serialize through the mutex. Writes are temp-file+rename atomic,
// matching mmconfig.Manager so a partial write can never leave the
// document corrupted.
type Store struct {
	path         string
	moduleLister ModuleLister
	mu           sync.RWMutex
}

// NewStore wires up the store at the given file path. The file is
// created lazily on first write — if the file does not exist yet,
// reads return DefaultDocument().
func NewStore(path string, moduleLister ModuleLister) *Store {
	return &Store{path: path, moduleLister: moduleLister}
}

// Path returns the on-disk file path for diagnostics.
func (s *Store) Path() string { return s.path }

// Load reads the document from disk, returning a defaulted document
// when the file does not yet exist (the natural empty-canvas state).
func (s *Store) Load() (Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (Document, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultDocument(), nil
	}
	if err != nil {
		return Document{}, fmt.Errorf("read canvas document: %w", err)
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return Document{}, fmt.Errorf("parse canvas document: %w", err)
	}
	if doc.Pages == nil {
		doc.Pages = map[string]Page{}
	}
	return doc, nil
}

// SaveCanvas overwrites the singleton canvas block. Validates dimensions
// before persisting and re-validates every existing slot against the new
// dimensions — shrinking the canvas to make slots out-of-bounds is
// rejected so the user has to move the slots first.
func (s *Store) SaveCanvas(canvas Canvas) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if canvas.Width <= 0 || canvas.Height <= 0 {
		return Document{}, fmt.Errorf("%w: canvas dimensions must be positive, got %dx%d",
			ErrInvalidSlot, canvas.Width, canvas.Height)
	}

	doc, err := s.loadLocked()
	if err != nil {
		return Document{}, err
	}

	for pageName, page := range doc.Pages {
		for i, slot := range page.Slots {
			if err := validateSlotBounds(slot, canvas); err != nil {
				return Document{}, fmt.Errorf("page %q slot %d (%s) would be out of bounds in new canvas: %w",
					pageName, i, slot.Module, err)
			}
		}
	}

	doc.Canvas = canvas
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = SchemaVersion
	}
	if err := s.writeLocked(doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// SavePage replaces the named page's slot list. Validates every slot
// (bounds + overlap + module existence) before committing — the entire
// page either lands valid or no write happens.
func (s *Store) SavePage(name string, page Page) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if name == "" {
		return Document{}, fmt.Errorf("%w: page name is required", ErrInvalidSlot)
	}

	doc, err := s.loadLocked()
	if err != nil {
		return Document{}, err
	}

	if err := s.validatePage(page, doc.Canvas); err != nil {
		return Document{}, err
	}

	doc.Pages[name] = page
	if err := s.writeLocked(doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// DeletePage removes a named page. Idempotent: deleting a non-existent
// page returns ErrPageNotFound so the API can surface 404; callers that
// want delete-or-noop should check for the sentinel.
func (s *Store) DeletePage(name string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.loadLocked()
	if err != nil {
		return Document{}, err
	}
	if _, ok := doc.Pages[name]; !ok {
		return Document{}, ErrPageNotFound
	}
	delete(doc.Pages, name)
	if err := s.writeLocked(doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// GetPage returns a single named page. Returns ErrPageNotFound when
// missing so the API layer can map to 404.
func (s *Store) GetPage(name string) (Page, error) {
	doc, err := s.Load()
	if err != nil {
		return Page{}, err
	}
	page, ok := doc.Pages[name]
	if !ok {
		return Page{}, ErrPageNotFound
	}
	return page, nil
}

// validatePage runs the full slot validation suite against the given
// canvas dimensions. Errors wrap one of the package sentinels so callers
// can branch on the failure class.
func (s *Store) validatePage(page Page, canvas Canvas) error {
	moduleIDs, err := s.knownModuleIDs()
	if err != nil {
		return fmt.Errorf("load module list for slot validation: %w", err)
	}

	for i, slot := range page.Slots {
		if err := validateSlotBounds(slot, canvas); err != nil {
			return fmt.Errorf("slot %d (%s): %w", i, slot.Module, err)
		}
		if moduleIDs != nil {
			if _, ok := moduleIDs[slot.Module]; !ok {
				return fmt.Errorf("%w: slot %d references %q", ErrUnknownModule, i, slot.Module)
			}
		}
	}

	for i := 0; i < len(page.Slots); i++ {
		a := page.Slots[i]
		if a.Hidden {
			continue
		}
		for j := i + 1; j < len(page.Slots); j++ {
			b := page.Slots[j]
			if b.Hidden {
				continue
			}
			if rectsOverlap(a, b) {
				return fmt.Errorf("%w: slot %d (%s) and slot %d (%s)",
					ErrSlotOverlap, i, a.Module, j, b.Module)
			}
		}
	}
	return nil
}

// knownModuleIDs returns the set of valid module IDs, or nil if the
// lister was not configured (tests that don't care about cross-resource
// validation can pass a nil lister).
func (s *Store) knownModuleIDs() (map[string]struct{}, error) {
	if s.moduleLister == nil {
		return nil, nil
	}
	ids, err := s.moduleLister.ListModuleIDs()
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out, nil
}

func validateSlotBounds(slot Slot, canvas Canvas) error {
	if slot.Module == "" {
		return fmt.Errorf("%w: slot module is required", ErrInvalidSlot)
	}
	if slot.W <= 0 || slot.H <= 0 {
		return fmt.Errorf("%w: slot dimensions must be positive, got %dx%d",
			ErrInvalidSlot, slot.W, slot.H)
	}
	if slot.X < 0 || slot.Y < 0 {
		return fmt.Errorf("%w: slot origin must be non-negative, got %d,%d",
			ErrInvalidSlot, slot.X, slot.Y)
	}
	if slot.X+slot.W > canvas.Width || slot.Y+slot.H > canvas.Height {
		return fmt.Errorf("%w: slot %d,%d %dx%d exceeds canvas %dx%d",
			ErrInvalidSlot, slot.X, slot.Y, slot.W, slot.H, canvas.Width, canvas.Height)
	}
	return nil
}

// rectsOverlap returns true when two slot rects share any pixel. Edge
// adjacency (e.g., a.X+a.W == b.X) is NOT overlap — slots that touch but
// don't cross are allowed, matching how the user expects to lay them out
// side-by-side.
func rectsOverlap(a, b Slot) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func (s *Store) writeLocked(doc Document) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal canvas document: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create canvas document dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "canvas-layout.json.tmp.*")
	if err != nil {
		return fmt.Errorf("create temp canvas document: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp canvas document: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp canvas document: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("move canvas document into place: %w", err)
	}
	return nil
}
