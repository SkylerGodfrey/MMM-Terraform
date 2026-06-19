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
	// HOM-119: v2 documents have no Sections key — normalize to an
	// empty map so call sites can index without a nil check.
	if doc.Sections == nil {
		doc.Sections = map[string]Section{}
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
	// HOM-119: section slots and overrides must also fit the new canvas.
	for sectionName, section := range doc.Sections {
		for i, slot := range section.Slots {
			if err := validateSlotBounds(slot, canvas); err != nil {
				return Document{}, fmt.Errorf("section %q slot %d (%s) would be out of bounds in new canvas: %w",
					sectionName, i, slot.Module, err)
			}
		}
	}
	for pageName, page := range doc.Pages {
		for i, o := range page.SectionOverrides {
			if err := validateSlotBounds(o.AsSlot(), canvas); err != nil {
				return Document{}, fmt.Errorf("page %q override %d (section %s/%s) would be out of bounds in new canvas: %w",
					pageName, i, o.Section, o.Module, err)
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

// SaveSection upserts a named section. Validates each section slot's
// geometry against the current canvas and rejects duplicate modules
// within the section (overrides identify slots by module). HOM-119.
func (s *Store) SaveSection(name string, section Section) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if name == "" {
		return Document{}, fmt.Errorf("%w: section name is required", ErrInvalidSlot)
	}

	doc, err := s.loadLocked()
	if err != nil {
		return Document{}, err
	}

	if err := s.validateSection(section, doc.Canvas); err != nil {
		return Document{}, err
	}

	// Re-validate every page that references this section — module
	// removed from the section may orphan a page override; module
	// renames likewise.
	for pageName, page := range doc.Pages {
		if !containsString(page.Sections, name) {
			continue
		}
		trial := doc
		trial.Sections = cloneSections(doc.Sections)
		trial.Sections[name] = section
		if err := s.validatePage(page, trial); err != nil {
			return Document{}, fmt.Errorf("page %q would be invalid after this section change: %w", pageName, err)
		}
	}

	doc.Sections[name] = section
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = SchemaVersion
	}
	if err := s.writeLocked(doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// DeleteSection removes a named section. Pages that referenced it have
// the name stripped from Page.Sections and any matching overrides
// dropped so the store stays internally consistent. HOM-119.
func (s *Store) DeleteSection(name string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.loadLocked()
	if err != nil {
		return Document{}, err
	}
	if _, ok := doc.Sections[name]; !ok {
		return Document{}, ErrSectionNotFound
	}
	delete(doc.Sections, name)
	for pageName, page := range doc.Pages {
		page.Sections = removeString(page.Sections, name)
		page.SectionOverrides = filterOverrides(page.SectionOverrides, func(o SectionOverride) bool {
			return o.Section != name
		})
		doc.Pages[pageName] = page
	}
	if err := s.writeLocked(doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// GetSection returns a single named section. Sentinel for 404 mapping
// in the API layer.
func (s *Store) GetSection(name string) (Section, error) {
	doc, err := s.Load()
	if err != nil {
		return Section{}, err
	}
	section, ok := doc.Sections[name]
	if !ok {
		return Section{}, ErrSectionNotFound
	}
	return section, nil
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

	if err := s.validatePage(page, doc); err != nil {
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

// SaveDocument replaces the entire document atomically — canvas,
// sections, and pages all at once. The editor uses this for its Save
// button because sections + page edits made in the same session
// reference each other in ways the granular SaveCanvas/SaveSection/
// SavePage flow can't validate without intermediate corrupt states.
// External /api/v1/canvas/* endpoints continue to use the granular
// ops because they edit one resource at a time. HOM-119.
func (s *Store) SaveDocument(doc Document) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if doc.Canvas.Width <= 0 || doc.Canvas.Height <= 0 {
		return Document{}, fmt.Errorf("%w: canvas dimensions must be positive, got %dx%d",
			ErrInvalidSlot, doc.Canvas.Width, doc.Canvas.Height)
	}
	if doc.Sections == nil {
		doc.Sections = map[string]Section{}
	}
	if doc.Pages == nil {
		doc.Pages = map[string]Page{}
	}

	for name, section := range doc.Sections {
		if name == "" {
			return Document{}, fmt.Errorf("%w: section name is required", ErrInvalidSlot)
		}
		if err := s.validateSection(section, doc.Canvas); err != nil {
			return Document{}, fmt.Errorf("section %q: %w", name, err)
		}
	}
	for name, page := range doc.Pages {
		if name == "" {
			return Document{}, fmt.Errorf("%w: page name is required", ErrInvalidSlot)
		}
		if err := s.validatePage(page, doc); err != nil {
			return Document{}, fmt.Errorf("page %q: %w", name, err)
		}
	}

	doc.SchemaVersion = SchemaVersion
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
// document — bounds, module existence, section references, override
// targeting, and overlap on the MERGED slot set (sections + overrides +
// page slots). HOM-119 added the cross-section concerns.
//
// Errors wrap one of the package sentinels so callers can branch on the
// failure class.
func (s *Store) validatePage(page Page, doc Document) error {
	canvas := doc.Canvas
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

	// HOM-119: every included section must exist, and any override must
	// target a (section, module) pair the section actually contains.
	for _, sectionName := range page.Sections {
		if _, ok := doc.Sections[sectionName]; !ok {
			return fmt.Errorf("%w: %q", ErrUnknownSection, sectionName)
		}
	}
	for i, o := range page.SectionOverrides {
		section, ok := doc.Sections[o.Section]
		if !ok {
			return fmt.Errorf("%w: override %d references unknown section %q", ErrUnknownSection, i, o.Section)
		}
		if !containsString(page.Sections, o.Section) {
			return fmt.Errorf("%w: override %d targets section %q but the page doesn't include it", ErrOrphanedOverride, i, o.Section)
		}
		hasSlot := false
		for _, slot := range section.Slots {
			if slot.Module == o.Module {
				hasSlot = true
				break
			}
		}
		if !hasSlot {
			return fmt.Errorf("%w: override %d (section %s/%s)", ErrOrphanedOverride, i, o.Section, o.Module)
		}
		if err := validateSlotBounds(o.AsSlot(), canvas); err != nil {
			return fmt.Errorf("override %d (section %s/%s): %w", i, o.Section, o.Module, err)
		}
	}

	// Merge the resolved slot list — sections (with overrides) followed
	// by page slots — and check overlap + module uniqueness across the
	// whole render set. A module that appears in both a section and a
	// page slot would cause two materializes against the same wrapper.
	merged := mergedSlots(page, doc.Sections)
	for i := 0; i < len(merged); i++ {
		a := merged[i]
		if a.Hidden {
			continue
		}
		for j := i + 1; j < len(merged); j++ {
			b := merged[j]
			if b.Hidden {
				continue
			}
			if a.Module == b.Module {
				return fmt.Errorf("%w: module %q appears in both slot %d and slot %d on the page",
					ErrSlotOverlap, a.Module, i, j)
			}
			if rectsOverlap(a, b) {
				return fmt.Errorf("%w: slot %d (%s) and slot %d (%s)",
					ErrSlotOverlap, i, a.Module, j, b.Module)
			}
		}
	}
	return nil
}

// validateSection checks one section's slots against the canvas and
// rejects duplicate modules (overrides identify slots by module so the
// section must keep modules unique). HOM-119.
func (s *Store) validateSection(section Section, canvas Canvas) error {
	moduleIDs, err := s.knownModuleIDs()
	if err != nil {
		return fmt.Errorf("load module list for slot validation: %w", err)
	}
	seenModule := map[string]struct{}{}
	for i, slot := range section.Slots {
		if err := validateSlotBounds(slot, canvas); err != nil {
			return fmt.Errorf("slot %d (%s): %w", i, slot.Module, err)
		}
		if moduleIDs != nil {
			if _, ok := moduleIDs[slot.Module]; !ok {
				return fmt.Errorf("%w: slot %d references %q", ErrUnknownModule, i, slot.Module)
			}
		}
		if _, dup := seenModule[slot.Module]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateSectionModule, slot.Module)
		}
		seenModule[slot.Module] = struct{}{}
	}
	// Overlap within a section is rejected — overrides can move slots
	// around per-page, but the default geometry shouldn't conflict.
	for i := 0; i < len(section.Slots); i++ {
		a := section.Slots[i]
		if a.Hidden {
			continue
		}
		for j := i + 1; j < len(section.Slots); j++ {
			b := section.Slots[j]
			if b.Hidden {
				continue
			}
			if rectsOverlap(a, b) {
				return fmt.Errorf("%w: section slot %d (%s) and section slot %d (%s)",
					ErrSlotOverlap, i, a.Module, j, b.Module)
			}
		}
	}
	return nil
}

// mergedSlots is the validator's view of "what would render" for the
// page. The runtime ResolvePageSlots on Document is the same shape but
// reads from the document directly; this helper takes the in-flight
// page so SavePage can validate before committing.
func mergedSlots(page Page, sections map[string]Section) []Slot {
	overrides := indexOverrides(page.SectionOverrides)
	out := make([]Slot, 0, len(page.Slots))
	for _, sectionName := range page.Sections {
		section, ok := sections[sectionName]
		if !ok {
			continue
		}
		for _, slot := range section.Slots {
			if override, ok := overrides[overrideKey{sectionName, slot.Module}]; ok {
				out = append(out, override.AsSlot())
				continue
			}
			out = append(out, slot)
		}
	}
	out = append(out, page.Slots...)
	return out
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func removeString(list []string, s string) []string {
	out := list[:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func filterOverrides(list []SectionOverride, keep func(SectionOverride) bool) []SectionOverride {
	out := list[:0]
	for _, o := range list {
		if keep(o) {
			out = append(out, o)
		}
	}
	return out
}

func cloneSections(src map[string]Section) map[string]Section {
	out := make(map[string]Section, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
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
