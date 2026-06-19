package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestMascotLayoutResource_ModelRoundTrip(t *testing.T) {
	want := &MascotLayout{
		Canvas: MascotCanvas{Width: 1080, Height: 1780},
		Sprites: []MascotSprite{
			{ID: "cat1", Sprite: "cat-grey-tabby", X: 100, Y: 1500, W: 96, H: 96},
			{ID: "dog1", Sprite: "dog-coonhound", X: 800, Y: 1500, W: 96, H: 96},
		},
		Holidays: []MascotHoliday{
			{State: "halloween", Start: "10-15", End: "11-01"},
			{State: "christmas", Start: "12-01", End: "12-26"},
		},
	}

	r := &MascotLayoutResource{}
	var model MascotLayoutResourceModel
	r.layoutToModel(want, &model)

	if model.CanvasWidth.ValueInt64() != 1080 || model.CanvasHeight.ValueInt64() != 1780 {
		t.Errorf("canvas: want 1080x1780, got %dx%d",
			model.CanvasWidth.ValueInt64(), model.CanvasHeight.ValueInt64())
	}
	if len(model.Sprites) != 2 {
		t.Fatalf("sprites: want 2, got %d", len(model.Sprites))
	}
	if model.Sprites[0].ID.ValueString() != "cat1" {
		t.Errorf("sprite[0].ID: want 'cat1', got %q", model.Sprites[0].ID.ValueString())
	}
	if len(model.Holidays) != 2 {
		t.Fatalf("holidays: want 2, got %d", len(model.Holidays))
	}
	if model.Holidays[1].State.ValueString() != "christmas" {
		t.Errorf("holidays[1].State: want 'christmas', got %q", model.Holidays[1].State.ValueString())
	}

	got := r.modelToLayout(&model)
	if got.Canvas != want.Canvas {
		t.Errorf("canvas: want %+v, got %+v", want.Canvas, got.Canvas)
	}
	if len(got.Sprites) != len(want.Sprites) {
		t.Fatalf("sprites len: want %d, got %d", len(want.Sprites), len(got.Sprites))
	}
	for i := range want.Sprites {
		if got.Sprites[i] != want.Sprites[i] {
			t.Errorf("sprite[%d] mismatch:\n want %+v\n got  %+v", i, want.Sprites[i], got.Sprites[i])
		}
	}
	for i := range want.Holidays {
		if got.Holidays[i] != want.Holidays[i] {
			t.Errorf("holiday[%d] mismatch:\n want %+v\n got  %+v", i, want.Holidays[i], got.Holidays[i])
		}
	}
}

func TestMascotLayoutResource_EmptyModelDoesNotPanic(t *testing.T) {
	r := &MascotLayoutResource{}
	model := MascotLayoutResourceModel{}
	got := r.modelToLayout(&model)
	if got.Canvas.Width != 0 || got.Canvas.Height != 0 {
		t.Errorf("empty canvas: want 0x0, got %+v", got.Canvas)
	}
	if len(got.Sprites) != 0 || len(got.Holidays) != 0 {
		t.Errorf("empty layout should have zero sprites/holidays, got %+v", got)
	}
}

func TestMascotLayoutResource_TypesStable(t *testing.T) {
	r := &MascotLayoutResource{}
	model := MascotLayoutResourceModel{
		CanvasWidth:  types.Int64Value(1080),
		CanvasHeight: types.Int64Value(1780),
		Sprites: []MascotSpriteModel{
			{
				ID: types.StringValue("x"), Sprite: types.StringValue("y"),
				X: types.Int64Value(10), Y: types.Int64Value(20),
				W: types.Int64Value(32), H: types.Int64Value(32),
			},
		},
	}
	got := r.modelToLayout(&model)
	if got.Sprites[0].X != 10 || got.Sprites[0].W != 32 {
		t.Errorf("int64 → int conversion lost precision: %+v", got.Sprites[0])
	}
}
