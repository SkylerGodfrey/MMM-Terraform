package mascot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store is the file-backed mascot-layout document. Concurrent reads are
// safe; writes serialize through the mutex. Writes are temp-file+rename
// atomic, matching the canvas store, so a partial write can never leave
// the document corrupted.
type Store struct {
	path string
	mu   sync.RWMutex
}

// NewStore wires up the store at the given file path. The file is
// created lazily on first write — if it does not exist yet, reads return
// DefaultDocument().
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Path returns the on-disk file path for diagnostics.
func (s *Store) Path() string { return s.path }

// Load reads the document from disk, returning a defaulted document
// when the file does not yet exist (the natural empty-overlay state).
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
		return Document{}, fmt.Errorf("read mascot document: %w", err)
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return Document{}, fmt.Errorf("parse mascot document: %w", err)
	}
	if doc.Sprites == nil {
		doc.Sprites = []Sprite{}
	}
	return doc, nil
}

// SaveDocument replaces the entire document atomically. Validates the
// canvas, every sprite (bounds + non-empty id/sprite), and every
// holiday (MM-DD format) before persisting.
func (s *Store) SaveDocument(doc Document) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ValidateDocument(&doc); err != nil {
		return Document{}, err
	}
	doc.SchemaVersion = SchemaVersion
	if err := s.writeLocked(doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// ValidateDocument runs every invariant the store enforces. Exposed
// separately so the terraform-provider (HOM-124) can validate locally
// before round-tripping through the agent.
func ValidateDocument(doc *Document) error {
	if doc.Canvas.Width <= 0 || doc.Canvas.Height <= 0 {
		return fmt.Errorf("%w: canvas dimensions must be positive, got %dx%d",
			ErrInvalidSprite, doc.Canvas.Width, doc.Canvas.Height)
	}
	if doc.Sprites == nil {
		doc.Sprites = []Sprite{}
	}
	seen := make(map[string]struct{}, len(doc.Sprites))
	for i, sp := range doc.Sprites {
		if err := validateSprite(sp, doc.Canvas); err != nil {
			return fmt.Errorf("sprite %d (%s): %w", i, sp.ID, err)
		}
		if _, dup := seen[sp.ID]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateSpriteID, sp.ID)
		}
		seen[sp.ID] = struct{}{}
	}
	for i, h := range doc.Holidays {
		if err := validateHoliday(h); err != nil {
			return fmt.Errorf("holiday %d (%s): %w", i, h.State, err)
		}
	}
	return nil
}

func validateSprite(s Sprite, c Canvas) error {
	if s.ID == "" {
		return fmt.Errorf("%w: sprite id is required", ErrInvalidSprite)
	}
	if s.Sprite == "" {
		return fmt.Errorf("%w: sprite catalog id is required", ErrInvalidSprite)
	}
	if s.W <= 0 || s.H <= 0 {
		return fmt.Errorf("%w: sprite dimensions must be positive, got %dx%d",
			ErrInvalidSprite, s.W, s.H)
	}
	if s.X < 0 || s.Y < 0 {
		return fmt.Errorf("%w: sprite origin must be non-negative, got %d,%d",
			ErrInvalidSprite, s.X, s.Y)
	}
	if s.X+s.W > c.Width || s.Y+s.H > c.Height {
		return fmt.Errorf("%w: sprite %d,%d %dx%d exceeds canvas %dx%d",
			ErrInvalidSprite, s.X, s.Y, s.W, s.H, c.Width, c.Height)
	}
	if err := validateRotation(s.Rotation); err != nil {
		return err
	}
	return nil
}

// validateRotation enforces the structural invariants of a rotation
// config. Tag *existence* is checked one layer up in the editor handler,
// where the sprite catalog is available — keeping this function free of
// filesystem access so the terraform provider can validate locally.
func validateRotation(r *Rotation) error {
	if r == nil {
		return nil
	}
	if len(r.Animations) == 0 {
		return fmt.Errorf("%w: animations list must not be empty", ErrInvalidRotation)
	}
	for i, a := range r.Animations {
		if a == "" {
			return fmt.Errorf("%w: animation %d is empty", ErrInvalidRotation, i)
		}
	}
	if r.MinMs <= 0 || r.MaxMs <= 0 {
		return fmt.Errorf("%w: intervals must be positive, got min=%d max=%d",
			ErrInvalidRotation, r.MinMs, r.MaxMs)
	}
	if r.MaxMs < r.MinMs {
		return fmt.Errorf("%w: maxMs %d precedes minMs %d", ErrInvalidRotation, r.MaxMs, r.MinMs)
	}
	return nil
}

func validateHoliday(h Holiday) error {
	if h.State == "" || h.State == DefaultState {
		return fmt.Errorf("%w: state must be a non-empty non-%q name", ErrInvalidHoliday, DefaultState)
	}
	if !mmDDPattern.MatchString(h.Start) {
		return fmt.Errorf("%w: start %q must be MM-DD", ErrInvalidHoliday, h.Start)
	}
	if !mmDDPattern.MatchString(h.End) {
		return fmt.Errorf("%w: end %q must be MM-DD", ErrInvalidHoliday, h.End)
	}
	// Wrap-around windows (Dec → Jan) are out of scope for v0. Reject
	// end < start so the user notices instead of silently never matching.
	if h.End < h.Start {
		return fmt.Errorf("%w: end %q precedes start %q (wrap-around not supported)",
			ErrInvalidHoliday, h.End, h.Start)
	}
	return nil
}

func (s *Store) writeLocked(doc Document) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mascot document: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create mascot document dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "mascot-layout.json.tmp.*")
	if err != nil {
		return fmt.Errorf("create temp mascot document: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp mascot document: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp mascot document: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("move mascot document into place: %w", err)
	}
	return nil
}
