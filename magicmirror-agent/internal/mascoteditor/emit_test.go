package mascoteditor

import (
	"strings"
	"testing"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mascot"
)

func TestEmitMascotsTfShape(t *testing.T) {
	c := mascot.Canvas{Width: 1080, Height: 1780}
	sprites := []mascot.Sprite{
		{ID: "cat1", Sprite: "cat-grey-tabby", X: 100, Y: 1500, W: 96, H: 96},
	}
	holidays := []mascot.Holiday{
		{State: "halloween", Start: "10-15", End: "11-01"},
	}

	hcl := emitMascotsTf(c, sprites, holidays)

	for _, want := range []string{
		`resource "magicmirror_mascot_layout" "default"`,
		`canvas_width  = 1080`,
		`canvas_height = 1780`,
		`id     = "cat1"`,
		`sprite = "cat-grey-tabby"`,
		`state = "halloween"`,
		`start = "10-15"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("emitMascotsTf missing %q\n--- got ---\n%s", want, hcl)
		}
	}
}

func TestEmitMascotsTfEmptyDoesNotPanic(t *testing.T) {
	hcl := emitMascotsTf(mascot.Canvas{Width: 1080, Height: 1780}, nil, nil)
	if !strings.Contains(hcl, "magicmirror_mascot_layout") {
		t.Fatalf("emit empty: missing resource block\n%s", hcl)
	}
	if !strings.Contains(hcl, "no sprites placed yet") {
		t.Fatalf("emit empty: missing empty-state comment\n%s", hcl)
	}
}

func TestEmitMascotsTfRotation(t *testing.T) {
	c := mascot.Canvas{Width: 1080, Height: 1780}
	sprites := []mascot.Sprite{{
		ID: "dog1", Sprite: "dog-brown", X: 100, Y: 1500, W: 96, H: 96,
		Rotation: &mascot.Rotation{Animations: []string{"idle", "barking-run"}, MinMs: 3000, MaxMs: 10000},
	}}

	hcl := emitMascotsTf(c, sprites, nil)

	for _, want := range []string{
		"rotation {",
		`animations = ["idle", "barking-run"]`,
		"min_ms     = 3000",
		"max_ms     = 10000",
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("emitMascotsTf missing %q\n--- got ---\n%s", want, hcl)
		}
	}
}

func TestEmitMascotsTfNoRotationBlockWhenNil(t *testing.T) {
	hcl := emitMascotsTf(mascot.Canvas{Width: 1080, Height: 1780},
		[]mascot.Sprite{{ID: "cat1", Sprite: "cat-grey-tabby", X: 0, Y: 0, W: 96, H: 96}}, nil)
	if strings.Contains(hcl, "rotation {") {
		t.Fatalf("emit should omit rotation block for a sprite without rotation\n%s", hcl)
	}
}
