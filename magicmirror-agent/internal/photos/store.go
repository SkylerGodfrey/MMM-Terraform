// Package photos manages the MMM-ImageSlideshow album directory for the
// family portal. The slideshow only accepts bmp/jpg/gif/png (its
// validImageFileExtensions default — note: not ".jpeg", which is why
// uploads are normalized), and only rescans the directory when MagicMirror
// restarts, which the API layer handles (HOM-6).
package photos

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/disintegration/imaging"
)

var ErrNotFound = errors.New("photo not found")
var ErrNotAnImage = errors.New("not a supported image type")

// ErrStorage marks filesystem failures so handlers can hide path detail
// from the family-facing UI while it still lands in the agent log.
var ErrStorage = errors.New("photos storage error")

const thumbDirName = ".thumbs"
const thumbWidth = 480

var allowedExt = map[string]bool{".bmp": true, ".jpg": true, ".gif": true, ".png": true}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

type Photo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type Store struct {
	dir string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) List() ([]Photo, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %w", ErrStorage, s.dir, err)
	}
	photos := make([]Photo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !allowedExt[strings.ToLower(filepath.Ext(entry.Name()))] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		photos = append(photos, Photo{Name: entry.Name(), Size: info.Size()})
	}
	sort.Slice(photos, func(i, j int) bool { return photos[i].Name < photos[j].Name })
	return photos, nil
}

// Save writes an uploaded image into the album. The stored name is the
// sanitized upload name (".jpeg" normalized to ".jpg" for the slideshow's
// extension filter), suffixed if it would overwrite an existing photo.
func (s *Store) Save(name string, src io.Reader) (Photo, error) {
	name = sanitizeName(name)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".jpeg" {
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ".jpg"
		ext = ".jpg"
	}
	if !allowedExt[ext] {
		return Photo{}, fmt.Errorf("%w: %s", ErrNotAnImage, ext)
	}

	path, name, err := s.freePath(name)
	if err != nil {
		return Photo{}, err
	}
	tmp, err := os.CreateTemp(s.dir, ".upload-*")
	if err != nil {
		return Photo{}, fmt.Errorf("%w: %w", ErrStorage, err)
	}
	defer os.Remove(tmp.Name())
	size, err := io.Copy(tmp, src)
	if err != nil {
		tmp.Close()
		return Photo{}, fmt.Errorf("%w: %w", ErrStorage, err)
	}
	if err := tmp.Close(); err != nil {
		return Photo{}, fmt.Errorf("%w: %w", ErrStorage, err)
	}

	// Reject files that merely claim an image extension.
	if _, err := imaging.Open(tmp.Name()); err != nil {
		return Photo{}, fmt.Errorf("%w: unreadable image: %v", ErrNotAnImage, err)
	}

	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return Photo{}, fmt.Errorf("%w: %w", ErrStorage, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return Photo{}, fmt.Errorf("%w: %w", ErrStorage, err)
	}
	return Photo{Name: name, Size: size}, nil
}

func (s *Store) Delete(name string) error {
	path, err := s.photoPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("%w: %w", ErrStorage, err)
	}
	os.Remove(filepath.Join(s.dir, thumbDirName, name+".jpg")) // best-effort cache cleanup
	return nil
}

// OriginalPath resolves a photo name to its file path for serving.
func (s *Store) OriginalPath(name string) (string, error) {
	path, err := s.photoPath(name)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		return "", ErrNotFound
	}
	return path, nil
}

// ThumbPath returns a cached EXIF-oriented thumbnail for the photo,
// generating it on first request. Falls back to the original on any
// processing failure (e.g. animated gifs) — the browser can scale.
func (s *Store) ThumbPath(name string) (string, error) {
	original, err := s.OriginalPath(name)
	if err != nil {
		return "", err
	}
	thumb := filepath.Join(s.dir, thumbDirName, name+".jpg")

	originalInfo, err := os.Stat(original)
	if err != nil {
		return "", ErrNotFound
	}
	if thumbInfo, err := os.Stat(thumb); err == nil && thumbInfo.ModTime().After(originalInfo.ModTime()) {
		return thumb, nil
	}

	img, err := imaging.Open(original, imaging.AutoOrientation(true))
	if err != nil || img.Bounds().Dx() <= thumbWidth {
		return original, nil
	}
	img = imaging.Resize(img, thumbWidth, 0, imaging.Box)
	if err := os.MkdirAll(filepath.Join(s.dir, thumbDirName), 0o755); err != nil {
		return original, nil
	}
	tmp := thumb + ".tmp.jpg" // imaging.Save picks the encoder from the extension
	if err := imaging.Save(img, tmp, imaging.JPEGQuality(78)); err != nil {
		os.Remove(tmp)
		return original, nil
	}
	if err := os.Rename(tmp, thumb); err != nil {
		os.Remove(tmp)
		return original, nil
	}
	return thumb, nil
}

// photoPath validates a client-supplied name and resolves it inside the
// album dir. Pre-existing photos (rsync'd in before the portal) may
// contain characters upload sanitization would never produce (~, spaces),
// so only path traversal and hidden files are rejected — anything List()
// returns must resolve here.
func (s *Store) photoPath(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) ||
		strings.HasPrefix(name, ".") || !allowedExt[strings.ToLower(filepath.Ext(name))] {
		return "", ErrNotFound
	}
	return filepath.Join(s.dir, name), nil
}

func (s *Store) freePath(name string) (string, string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	candidate := name
	for i := 0; ; i++ {
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		path := filepath.Join(s.dir, candidate)
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

func sanitizeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = unsafeChars.ReplaceAllString(name, "_")
	name = strings.TrimLeft(name, "._")
	if name == "" {
		name = "photo.jpg"
	}
	return name
}
