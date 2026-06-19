package canvas

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// stubLister returns whatever module IDs the test seeded. nil means
// "skip cross-resource validation" — used to exercise pure geometry
// checks without setting up modules.
type stubLister struct {
	ids []string
	err error
}

func (s *stubLister) ListModuleIDs() ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.ids, nil
}

func newStore(t *testing.T, lister ModuleLister) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "canvas-layout.json")
	return NewStore(path, lister)
}

func TestLoad_MissingFileReturnsDefault(t *testing.T) {
	s := newStore(t, nil)
	doc, err := s.Load()
	if err != nil {
		t.Fatalf("Load on missing file should succeed, got: %v", err)
	}
	if doc.SchemaVersion != SchemaVersion {
		t.Errorf("schema version: want %d, got %d", SchemaVersion, doc.SchemaVersion)
	}
	if doc.Canvas.Width != DefaultWidth || doc.Canvas.Height != DefaultHeight {
		t.Errorf("default canvas dimensions: want %dx%d, got %dx%d",
			DefaultWidth, DefaultHeight, doc.Canvas.Width, doc.Canvas.Height)
	}
	if len(doc.Pages) != 0 {
		t.Errorf("default pages should be empty, got %d", len(doc.Pages))
	}
}

func TestSaveCanvas_RoundTrip(t *testing.T) {
	s := newStore(t, nil)
	want := Canvas{Width: 800, Height: 600, DebugBorders: true, DefaultPage: "home"}
	if _, err := s.SaveCanvas(want); err != nil {
		t.Fatalf("SaveCanvas: %v", err)
	}
	doc, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc.Canvas != want {
		t.Errorf("canvas round-trip mismatch: want %+v, got %+v", want, doc.Canvas)
	}
}

func TestSaveCanvas_RejectsNonPositiveDimensions(t *testing.T) {
	s := newStore(t, nil)
	if _, err := s.SaveCanvas(Canvas{Width: 0, Height: 100}); err == nil {
		t.Fatal("expected error for zero width, got nil")
	}
	if _, err := s.SaveCanvas(Canvas{Width: 100, Height: -1}); err == nil {
		t.Fatal("expected error for negative height, got nil")
	}
}

func TestSaveCanvas_RejectsShrinkBelowExistingSlots(t *testing.T) {
	s := newStore(t, &stubLister{ids: []string{"clock"}})
	if _, err := s.SaveCanvas(Canvas{Width: 1080, Height: 1780, DefaultPage: "home"}); err != nil {
		t.Fatalf("seed canvas: %v", err)
	}
	if _, err := s.SavePage("home", Page{Slots: []Slot{
		{Module: "clock", X: 900, Y: 1600, W: 150, H: 150},
	}}); err != nil {
		t.Fatalf("seed page: %v", err)
	}
	if _, err := s.SaveCanvas(Canvas{Width: 800, Height: 600}); err == nil {
		t.Fatal("expected shrink to fail when an existing slot would be out of bounds")
	}
}

func TestSavePage_HappyPath(t *testing.T) {
	s := newStore(t, &stubLister{ids: []string{"clock", "weather"}})
	if _, err := s.SaveCanvas(Canvas{Width: 1080, Height: 1780, DefaultPage: "home"}); err != nil {
		t.Fatalf("seed canvas: %v", err)
	}
	page := Page{Slots: []Slot{
		{Module: "clock", X: 40, Y: 40, W: 500, H: 200},
		{Module: "weather", X: 540, Y: 40, W: 500, H: 200},
	}}
	if _, err := s.SavePage("home", page); err != nil {
		t.Fatalf("SavePage: %v", err)
	}
	got, err := s.GetPage("home")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if len(got.Slots) != 2 {
		t.Fatalf("slot count: want 2, got %d", len(got.Slots))
	}
}

func TestSavePage_RejectsOutOfBoundsSlot(t *testing.T) {
	s := newStore(t, &stubLister{ids: []string{"clock"}})
	_, _ = s.SaveCanvas(Canvas{Width: 1080, Height: 1780, DefaultPage: "home"})

	cases := map[string]Slot{
		"negative x":        {Module: "clock", X: -1, Y: 0, W: 100, H: 100},
		"negative y":        {Module: "clock", X: 0, Y: -1, W: 100, H: 100},
		"zero width":        {Module: "clock", X: 0, Y: 0, W: 0, H: 100},
		"zero height":       {Module: "clock", X: 0, Y: 0, W: 100, H: 0},
		"exceeds canvas x":  {Module: "clock", X: 1000, Y: 0, W: 200, H: 100},
		"exceeds canvas y":  {Module: "clock", X: 0, Y: 1700, W: 100, H: 200},
		"empty module name": {Module: "", X: 0, Y: 0, W: 100, H: 100},
	}
	for name, slot := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.SavePage("home", Page{Slots: []Slot{slot}})
			if err == nil {
				t.Fatalf("expected error for %s, got nil", name)
			}
			if !errors.Is(err, ErrInvalidSlot) {
				t.Errorf("expected ErrInvalidSlot, got %v", err)
			}
		})
	}
}

func TestSavePage_RejectsOverlap(t *testing.T) {
	s := newStore(t, &stubLister{ids: []string{"a", "b"}})
	_, _ = s.SaveCanvas(Canvas{Width: 1080, Height: 1780, DefaultPage: "home"})
	_, err := s.SavePage("home", Page{Slots: []Slot{
		{Module: "a", X: 0, Y: 0, W: 500, H: 500},
		{Module: "b", X: 200, Y: 200, W: 500, H: 500},
	}})
	if err == nil {
		t.Fatal("expected overlap error, got nil")
	}
	if !errors.Is(err, ErrSlotOverlap) {
		t.Errorf("expected ErrSlotOverlap, got %v", err)
	}
}

func TestSavePage_EdgeAdjacencyIsNotOverlap(t *testing.T) {
	s := newStore(t, &stubLister{ids: []string{"a", "b"}})
	_, _ = s.SaveCanvas(Canvas{Width: 1080, Height: 1780, DefaultPage: "home"})
	_, err := s.SavePage("home", Page{Slots: []Slot{
		{Module: "a", X: 0, Y: 0, W: 500, H: 500},
		{Module: "b", X: 500, Y: 0, W: 500, H: 500}, // touches a.right
	}})
	if err != nil {
		t.Fatalf("edge-adjacent slots should not overlap, got: %v", err)
	}
}

func TestSavePage_HiddenSlotsExemptFromOverlap(t *testing.T) {
	s := newStore(t, &stubLister{ids: []string{"a", "b"}})
	_, _ = s.SaveCanvas(Canvas{Width: 1080, Height: 1780, DefaultPage: "home"})
	_, err := s.SavePage("home", Page{Slots: []Slot{
		{Module: "a", X: 0, Y: 0, W: 500, H: 500},
		{Module: "b", X: 100, Y: 100, W: 500, H: 500, Hidden: true},
	}})
	if err != nil {
		t.Fatalf("hidden slot should not trigger overlap, got: %v", err)
	}
}

func TestSavePage_RejectsUnknownModule(t *testing.T) {
	s := newStore(t, &stubLister{ids: []string{"clock"}})
	_, _ = s.SaveCanvas(Canvas{Width: 1080, Height: 1780, DefaultPage: "home"})
	_, err := s.SavePage("home", Page{Slots: []Slot{
		{Module: "ghost-module", X: 0, Y: 0, W: 100, H: 100},
	}})
	if err == nil {
		t.Fatal("expected unknown-module error, got nil")
	}
	if !errors.Is(err, ErrUnknownModule) {
		t.Errorf("expected ErrUnknownModule, got %v", err)
	}
}

func TestSavePage_NilListerSkipsModuleValidation(t *testing.T) {
	s := newStore(t, nil) // no lister
	_, _ = s.SaveCanvas(Canvas{Width: 1080, Height: 1780, DefaultPage: "home"})
	_, err := s.SavePage("home", Page{Slots: []Slot{
		{Module: "anything", X: 0, Y: 0, W: 100, H: 100},
	}})
	if err != nil {
		t.Fatalf("nil lister should skip module validation, got: %v", err)
	}
}

func TestDeletePage(t *testing.T) {
	s := newStore(t, &stubLister{ids: []string{"clock"}})
	_, _ = s.SaveCanvas(Canvas{Width: 1080, Height: 1780, DefaultPage: "home"})
	_, _ = s.SavePage("home", Page{Slots: []Slot{
		{Module: "clock", X: 0, Y: 0, W: 100, H: 100},
	}})

	if _, err := s.DeletePage("home"); err != nil {
		t.Fatalf("DeletePage: %v", err)
	}
	if _, err := s.GetPage("home"); !errors.Is(err, ErrPageNotFound) {
		t.Errorf("expected ErrPageNotFound after delete, got %v", err)
	}
	if _, err := s.DeletePage("home"); !errors.Is(err, ErrPageNotFound) {
		t.Errorf("second delete should return ErrPageNotFound, got %v", err)
	}
}

func TestAtomicWrite_LoadAfterMidwayCorruptionPath(t *testing.T) {
	// Validate temp-file pattern: writing then reading back parses cleanly.
	s := newStore(t, &stubLister{ids: []string{"clock"}})
	_, _ = s.SaveCanvas(Canvas{Width: 1080, Height: 1780, DefaultPage: "home"})
	_, _ = s.SavePage("home", Page{Slots: []Slot{
		{Module: "clock", X: 0, Y: 0, W: 100, H: 100},
	}})

	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse file: %v", err)
	}
	if doc.SchemaVersion != SchemaVersion {
		t.Errorf("on-disk schema version: want %d, got %d", SchemaVersion, doc.SchemaVersion)
	}
}
