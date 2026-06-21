package mascoteditor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mascot"
)

// spriteWithRotation builds a one-sprite slice whose rotation names the
// given tags, for the rotation-tag validation tests.
func spriteWithRotation(spriteID string, tags []string) []mascot.Sprite {
	return []mascot.Sprite{{
		ID: "s1", Sprite: spriteID, X: 0, Y: 0, W: 16, H: 16,
		Rotation: &mascot.Rotation{Animations: tags, MinMs: 1000, MaxMs: 2000},
	}}
}

// writeSprite drops a minimal sprite dir (one skin) with the given frame
// tags so the catalog scanner has something to parse.
func writeSprite(t *testing.T, root, id, state string, tags []string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// PNG content is never parsed by the scanner — an empty file is enough
	// to mark the state "present".
	if err := os.WriteFile(filepath.Join(dir, state+".png"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	var ft string
	for i, tag := range tags {
		if i > 0 {
			ft += ","
		}
		ft += `{"name":"` + tag + `","from":0,"to":0,"direction":"forward"}`
	}
	json := `{"frames":[{"frame":{"x":0,"y":0,"w":16,"h":16},"duration":120}],"meta":{"frameTags":[` + ft + `]}}`
	if err := os.WriteFile(filepath.Join(dir, state+".json"), []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanCatalogReadsFrameTags(t *testing.T) {
	root := t.TempDir()
	writeSprite(t, root, "dog-brown", "default", []string{"idle", "barking-run", "sit"})

	catalog, err := scanCatalog(root)
	if err != nil {
		t.Fatalf("scanCatalog: %v", err)
	}
	if len(catalog) != 1 || catalog[0].ID != "dog-brown" {
		t.Fatalf("catalog: %+v", catalog)
	}
	if len(catalog[0].States) != 1 || catalog[0].States[0].Name != "default" {
		t.Fatalf("states: %+v", catalog[0].States)
	}
	tags := catalog[0].States[0].Tags
	if len(tags) != 3 {
		t.Fatalf("want 3 tags, got %v", tags)
	}
}

func TestValidateRotationTags(t *testing.T) {
	root := t.TempDir()
	writeSprite(t, root, "dog-brown", "default", []string{"idle", "barking-run"})
	h := &handlers{spritesDir: root}

	// A rotation naming a real tag passes.
	good := spriteWithRotation("dog-brown", []string{"idle", "barking-run"})
	if err := h.validateRotationTags(good); err != nil {
		t.Fatalf("valid rotation rejected: %v", err)
	}

	// A rotation naming a missing tag fails.
	bad := spriteWithRotation("dog-brown", []string{"idle", "moonwalk"})
	if err := h.validateRotationTags(bad); err == nil {
		t.Fatal("expected error for unknown tag, got nil")
	}

	// A sprite whose assets aren't on disk yet is allowed (deferred to runtime).
	absent := spriteWithRotation("not-deployed-yet", []string{"idle"})
	if err := h.validateRotationTags(absent); err != nil {
		t.Fatalf("absent sprite should be allowed, got: %v", err)
	}
}
