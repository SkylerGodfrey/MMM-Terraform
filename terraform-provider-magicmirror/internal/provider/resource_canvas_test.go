package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestCanvasResource_ModelRoundTrip(t *testing.T) {
	want := &CanvasConfig{
		Width:        800,
		Height:       1200,
		DebugBorders: true,
		DebugLabels:  false,
		DefaultPage:  "home",
	}

	r := &CanvasResource{}
	var model CanvasResourceModel
	r.canvasToModel(want, &model)

	if model.ID.ValueString() != "canvas" {
		t.Errorf("ID: want 'canvas', got %q", model.ID.ValueString())
	}
	if model.Width.ValueInt64() != 800 {
		t.Errorf("Width: want 800, got %d", model.Width.ValueInt64())
	}
	if model.Height.ValueInt64() != 1200 {
		t.Errorf("Height: want 1200, got %d", model.Height.ValueInt64())
	}
	if !model.DebugBorders.ValueBool() {
		t.Errorf("DebugBorders: want true, got false")
	}
	if model.DebugLabels.ValueBool() {
		t.Errorf("DebugLabels: want false, got true")
	}
	if model.DefaultPage.ValueString() != "home" {
		t.Errorf("DefaultPage: want 'home', got %q", model.DefaultPage.ValueString())
	}

	got := r.modelToCanvas(&model)
	if *got != *want {
		t.Errorf("round-trip mismatch:\n want %+v\n got  %+v", want, got)
	}
}

func TestCanvasResource_ModelToCanvasZeroDefaults(t *testing.T) {
	// Even when all inputs are null, modelToCanvas must not panic and
	// should produce zero-valued ints + empty strings — the framework
	// substitutes defaults before this layer sees the data, so zeros
	// here only show up if a test passed a fresh model directly.
	r := &CanvasResource{}
	model := CanvasResourceModel{}
	got := r.modelToCanvas(&model)
	if got.Width != 0 || got.Height != 0 {
		t.Errorf("expected zero dimensions on empty model, got %+v", got)
	}
	if got.DefaultPage != "" {
		t.Errorf("expected empty default page on empty model, got %q", got.DefaultPage)
	}
}

func TestCanvasResource_TypesStable(t *testing.T) {
	// Guards against silently widening Width/Height beyond int — the
	// agent uses Go int and Terraform sends int64. If someone changes
	// Width to types.Float64 the conversion in modelToCanvas would have
	// to change too.
	r := &CanvasResource{}
	model := CanvasResourceModel{
		Width:  types.Int64Value(1080),
		Height: types.Int64Value(1780),
	}
	got := r.modelToCanvas(&model)
	if got.Width != 1080 || got.Height != 1780 {
		t.Errorf("dimension conversion lost precision: %+v", got)
	}
}
