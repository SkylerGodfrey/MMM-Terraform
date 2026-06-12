package photos

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "existing.jpg"), jpegBytes(t, 40, 30), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a photo"), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewStore(dir)
}

func TestListFiltersToImages(t *testing.T) {
	s := newTestStore(t)
	photos, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) != 1 || photos[0].Name != "existing.jpg" {
		t.Fatalf("want only existing.jpg, got %v", photos)
	}
}

func TestSaveNormalizesJpegAndSanitizes(t *testing.T) {
	s := newTestStore(t)
	photo, err := s.Save("../../etc/Family Photo!.JPEG", bytes.NewReader(jpegBytes(t, 40, 30)))
	if err != nil {
		t.Fatal(err)
	}
	if photo.Name != "Family_Photo_.jpg" {
		t.Errorf("unexpected stored name %q", photo.Name)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), photo.Name)); err != nil {
		t.Errorf("photo not in album dir: %v", err)
	}
}

func TestSaveDuplicateGetsSuffix(t *testing.T) {
	s := newTestStore(t)
	photo, err := s.Save("existing.jpg", bytes.NewReader(jpegBytes(t, 40, 30)))
	if err != nil {
		t.Fatal(err)
	}
	if photo.Name != "existing-1.jpg" {
		t.Errorf("want existing-1.jpg, got %q", photo.Name)
	}
}

func TestSaveRejectsNonImages(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Save("malware.exe", strings.NewReader("nope")); !errors.Is(err, ErrNotAnImage) {
		t.Errorf("want ErrNotAnImage for extension, got %v", err)
	}
	if _, err := s.Save("fake.jpg", strings.NewReader("not actually a jpeg")); !errors.Is(err, ErrNotAnImage) {
		t.Errorf("want ErrNotAnImage for content, got %v", err)
	}
	photos, _ := s.List()
	if len(photos) != 1 {
		t.Errorf("rejected uploads must not be stored: %v", photos)
	}
}

func TestDeleteAndTraversalRejected(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete("existing.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("existing.jpg"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	for _, evil := range []string{"../config.yaml", "..%2Fx.jpg", ".thumbs/existing.jpg.jpg", "a/b.jpg"} {
		if err := s.Delete(evil); !errors.Is(err, ErrNotFound) {
			t.Errorf("traversal name %q: want ErrNotFound, got %v", evil, err)
		}
	}
}

func TestThumbGeneratedCachedAndScaled(t *testing.T) {
	s := newTestStore(t)
	big := filepath.Join(s.Dir(), "big.png")
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1600, 1200))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	thumb, err := s.ThumbPath("big.png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(thumb, thumbDirName) {
		t.Fatalf("expected cached thumb path, got %s", thumb)
	}
	f, err := os.Open(thumb)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != thumbWidth {
		t.Errorf("want thumb width %d, got %d", thumbWidth, cfg.Width)
	}

	// Second call must reuse the cache (same modtime).
	info1, _ := os.Stat(thumb)
	thumb2, _ := s.ThumbPath("big.png")
	info2, _ := os.Stat(thumb2)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("thumbnail regenerated instead of cached")
	}

	// Thumbs must not show up in the album list.
	photos, _ := s.List()
	for _, p := range photos {
		if strings.Contains(p.Name, "thumb") {
			t.Errorf("thumb leaked into album list: %v", p)
		}
	}
}

func TestOriginalPathMissing(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.OriginalPath("nope.jpg"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}
