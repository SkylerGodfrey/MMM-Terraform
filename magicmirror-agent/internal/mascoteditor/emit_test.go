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
