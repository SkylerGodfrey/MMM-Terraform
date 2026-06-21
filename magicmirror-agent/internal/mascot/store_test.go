package mascot

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLoadReturnsDefaultWhenFileMissing(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "mascot-layout.json"))
	doc, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion: want %d got %d", SchemaVersion, doc.SchemaVersion)
	}
	if doc.Canvas.Width != DefaultWidth || doc.Canvas.Height != DefaultHeight {
		t.Fatalf("Canvas: want %dx%d got %dx%d", DefaultWidth, DefaultHeight, doc.Canvas.Width, doc.Canvas.Height)
	}
	if got := len(doc.Holidays); got == 0 {
		t.Fatalf("default holidays: expected seed entries, got 0")
	}
}

func TestSaveDocumentRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "mascot-layout.json"))

	in := Document{
		Canvas: Canvas{Width: 1080, Height: 1780},
		Sprites: []Sprite{
			{ID: "cat1", Sprite: "cat-grey-tabby", X: 100, Y: 1500, W: 96, H: 96},
		},
		Holidays: []Holiday{{State: "halloween", Start: "10-15", End: "11-01"}},
	}

	if _, err := s.SaveDocument(in); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out.Sprites) != 1 || out.Sprites[0].ID != "cat1" {
		t.Fatalf("round-trip sprite mismatch: %+v", out.Sprites)
	}
	if out.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion after save: want %d got %d", SchemaVersion, out.SchemaVersion)
	}
}

func TestSaveRoundTripsRotation(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "mascot-layout.json"))
	in := Document{
		Canvas: Canvas{Width: 1080, Height: 1780},
		Sprites: []Sprite{{
			ID: "dog1", Sprite: "dog-brown", X: 100, Y: 1500, W: 96, H: 96,
			Rotation: &Rotation{Animations: []string{"idle", "barking-run"}, MinMs: 3000, MaxMs: 10000},
		}},
	}
	if _, err := s.SaveDocument(in); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := out.Sprites[0].Rotation
	if r == nil {
		t.Fatal("rotation dropped on round-trip")
	}
	if len(r.Animations) != 2 || r.Animations[0] != "idle" || r.MinMs != 3000 || r.MaxMs != 10000 {
		t.Fatalf("rotation mismatch: %+v", r)
	}
}

func TestSaveRejectsBadRotation(t *testing.T) {
	cases := []*Rotation{
		{Animations: nil, MinMs: 1000, MaxMs: 2000},                 // empty list
		{Animations: []string{""}, MinMs: 1000, MaxMs: 2000},        // empty tag
		{Animations: []string{"idle"}, MinMs: 0, MaxMs: 2000},       // non-positive min
		{Animations: []string{"idle"}, MinMs: 1000, MaxMs: 0},       // non-positive max
		{Animations: []string{"idle"}, MinMs: 5000, MaxMs: 2000},    // max < min
	}
	for i, r := range cases {
		dir := t.TempDir()
		s := NewStore(filepath.Join(dir, "mascot-layout.json"))
		doc := Document{
			Canvas:  Canvas{Width: 1080, Height: 1780},
			Sprites: []Sprite{{ID: "d", Sprite: "dog-brown", X: 0, Y: 0, W: 96, H: 96, Rotation: r}},
		}
		if _, err := s.SaveDocument(doc); !errors.Is(err, ErrInvalidRotation) {
			t.Errorf("case %d: want ErrInvalidRotation, got %v", i, err)
		}
	}
}

func TestSaveRejectsOutOfBoundsSprite(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "mascot-layout.json"))
	doc := Document{
		Canvas:  Canvas{Width: 1080, Height: 1780},
		Sprites: []Sprite{{ID: "x", Sprite: "cat", X: 1000, Y: 1700, W: 200, H: 200}},
	}
	_, err := s.SaveDocument(doc)
	if !errors.Is(err, ErrInvalidSprite) {
		t.Fatalf("want ErrInvalidSprite, got %v", err)
	}
}

func TestSaveRejectsDuplicateID(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "mascot-layout.json"))
	doc := Document{
		Canvas: Canvas{Width: 1080, Height: 1780},
		Sprites: []Sprite{
			{ID: "cat", Sprite: "cat-grey-tabby", X: 0, Y: 0, W: 96, H: 96},
			{ID: "cat", Sprite: "cat-grey-tabby", X: 200, Y: 0, W: 96, H: 96},
		},
	}
	_, err := s.SaveDocument(doc)
	if !errors.Is(err, ErrDuplicateSpriteID) {
		t.Fatalf("want ErrDuplicateSpriteID, got %v", err)
	}
}

func TestSaveRejectsBadHolidayFormat(t *testing.T) {
	cases := []Holiday{
		{State: "halloween", Start: "13-01", End: "11-01"}, // bad month
		{State: "halloween", Start: "10-32", End: "11-01"}, // bad day
		{State: "halloween", Start: "10-15", End: "9-30"},  // missing pad
		{State: "halloween", Start: "11-01", End: "10-15"}, // end < start
		{State: "", Start: "10-15", End: "11-01"},          // empty state
		{State: "default", Start: "10-15", End: "11-01"},   // reserved state
	}
	for i, h := range cases {
		dir := t.TempDir()
		s := NewStore(filepath.Join(dir, "mascot-layout.json"))
		doc := Document{
			Canvas:   Canvas{Width: 1080, Height: 1780},
			Holidays: []Holiday{h},
		}
		_, err := s.SaveDocument(doc)
		if !errors.Is(err, ErrInvalidHoliday) {
			t.Errorf("case %d: want ErrInvalidHoliday, got %v", i, err)
		}
	}
}
