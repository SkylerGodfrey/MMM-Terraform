package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// slotModel is a convenience constructor — full SlotModel literals are
// noisy with all those types.Int64Value calls.
func slotModel(module string, x, y, w, h int, hidden bool) SlotModel {
	return SlotModel{
		Module: types.StringValue(module),
		X:      types.Int64Value(int64(x)),
		Y:      types.Int64Value(int64(y)),
		W:      types.Int64Value(int64(w)),
		H:      types.Int64Value(int64(h)),
		Hidden: types.BoolValue(hidden),
		ZIndex: types.Int64Value(0),
	}
}

func TestDetectSlotOverlaps(t *testing.T) {
	tests := []struct {
		name  string
		slots []SlotModel
		want  []overlapPair
	}{
		{
			name:  "empty list has no overlaps",
			slots: nil,
			want:  nil,
		},
		{
			name: "two disjoint slots side by side",
			slots: []SlotModel{
				slotModel("a", 0, 0, 100, 100, false),
				slotModel("b", 200, 0, 100, 100, false),
			},
			want: nil,
		},
		{
			name: "edge adjacency is not overlap",
			slots: []SlotModel{
				slotModel("a", 0, 0, 100, 100, false),
				slotModel("b", 100, 0, 100, 100, false), // touches a.right
			},
			want: nil,
		},
		{
			name: "one pixel overlap reported",
			slots: []SlotModel{
				slotModel("a", 0, 0, 100, 100, false),
				slotModel("b", 99, 99, 100, 100, false),
			},
			want: []overlapPair{{i: 0, j: 1, iModule: "a", jModule: "b"}},
		},
		{
			name: "hidden slot exempt even when overlapping",
			slots: []SlotModel{
				slotModel("a", 0, 0, 500, 500, false),
				slotModel("b", 100, 100, 500, 500, true), // hidden
			},
			want: nil,
		},
		{
			name: "multiple overlap pairs all reported",
			slots: []SlotModel{
				slotModel("a", 0, 0, 200, 200, false),
				slotModel("b", 100, 100, 200, 200, false),
				slotModel("c", 50, 50, 50, 50, false),
			},
			want: []overlapPair{
				{i: 0, j: 1, iModule: "a", jModule: "b"},
				{i: 0, j: 2, iModule: "a", jModule: "c"},
				// b vs c don't overlap: b is at (100,100,300,300), c at (50,50,100,100)
			},
		},
		{
			name: "fully contained slot reported",
			slots: []SlotModel{
				slotModel("outer", 0, 0, 1000, 1000, false),
				slotModel("inner", 200, 200, 100, 100, false),
			},
			want: []overlapPair{{i: 0, j: 1, iModule: "outer", jModule: "inner"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectSlotOverlaps(tt.slots)
			if len(got) != len(tt.want) {
				t.Fatalf("overlap count mismatch:\n want %d (%+v)\n got  %d (%+v)",
					len(tt.want), tt.want, len(got), got)
			}
			for k := range got {
				if got[k] != tt.want[k] {
					t.Errorf("pair %d: want %+v, got %+v", k, tt.want[k], got[k])
				}
			}
		})
	}
}

func TestPageResource_ModelRoundTrip(t *testing.T) {
	want := &Page{
		Slots: []Slot{
			{Module: "abc123", X: 40, Y: 40, W: 500, H: 200, ZIndex: 1, Hidden: false},
			{Module: "def456", X: 540, Y: 40, W: 500, H: 200, ZIndex: 0, Hidden: true},
		},
	}

	r := &PageResource{}
	model := PageResourceModel{Name: types.StringValue("home")}
	r.pageToModel(want, &model)

	if len(model.Slots) != 2 {
		t.Fatalf("slot count: want 2, got %d", len(model.Slots))
	}
	if model.Slots[0].Module.ValueString() != "abc123" {
		t.Errorf("slot[0].Module: want 'abc123', got %q", model.Slots[0].Module.ValueString())
	}
	if model.Slots[1].Hidden.ValueBool() != true {
		t.Errorf("slot[1].Hidden: want true, got false")
	}

	got := r.modelToPage(&model)
	if len(got.Slots) != len(want.Slots) {
		t.Fatalf("round-trip slot count mismatch: want %d, got %d", len(want.Slots), len(got.Slots))
	}
	for i := range got.Slots {
		if got.Slots[i] != want.Slots[i] {
			t.Errorf("slot %d round-trip mismatch:\n want %+v\n got  %+v", i, want.Slots[i], got.Slots[i])
		}
	}
}

func TestPageResource_EmptySlotsRoundTrip(t *testing.T) {
	// A page with no slots is legal — it just renders nothing. Make
	// sure the conversion doesn't panic or insert phantom entries.
	r := &PageResource{}
	model := PageResourceModel{Name: types.StringValue("blank")}
	got := r.modelToPage(&model)
	if got == nil {
		t.Fatal("modelToPage returned nil on empty page")
	}
	if len(got.Slots) != 0 {
		t.Errorf("expected zero slots, got %d", len(got.Slots))
	}
}
