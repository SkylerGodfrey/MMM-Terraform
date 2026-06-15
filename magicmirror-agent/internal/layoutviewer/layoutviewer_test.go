package layoutviewer

import (
	"testing"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
)

func TestBuildStateWithLayoutDoc(t *testing.T) {
	modules := []mmconfig.Module{
		{Module: "MMM-PhotoFrame", Position: "top_bar", Classes: "role1"},
		{Module: "MMM-CalendarExt3", Position: "middle_center", Classes: "role1"},
		{Module: "MMM-Chores", Position: "fullscreen_above", Classes: "chores"},
		{Module: "MMM-LayoutBounds", Position: "bottom_bar", Config: map[string]any{
			"layout": map[string]any{
				"version": float64(1),
				"regions": map[string]any{
					"top_bar":       map[string]any{"maxHeight": "420px", "overflow": "hidden"},
					"middle_center": map[string]any{"maxHeight": "520px", "overflow": "hidden"},
					"top_left":      nil,
				},
				"moduleOverrides": []any{
					map[string]any{
						"match":  map[string]any{"module": "MMM-PhotoFrame", "region": "top_bar"},
						"exempt": true,
					},
				},
			},
		}},
	}

	state := buildState(modules, nil)

	if !state.HasLayoutDoc {
		t.Fatal("expected HasLayoutDoc=true when MMM-LayoutBounds is present")
	}
	if len(state.ModuleOverrides) != 1 || state.ModuleOverrides[0].Module != "MMM-PhotoFrame" {
		t.Fatalf("expected 1 PhotoFrame override; got %+v", state.ModuleOverrides)
	}

	byID := map[string]RegionState{}
	for _, r := range state.Regions {
		byID[r.ID] = r
	}

	// LayoutBounds itself must not appear in any region's modules — it has
	// no visible UI and would confuse the map.
	for _, r := range byID {
		for _, m := range r.Modules {
			if m.Name == "MMM-LayoutBounds" {
				t.Errorf("MMM-LayoutBounds rendered in region %s", r.ID)
			}
		}
	}

	topBar := byID["top_bar"]
	if !topBar.Suspended {
		t.Errorf("top_bar should be suspended (PhotoFrame override). got: %+v", topBar)
	}
	if len(topBar.Modules) != 1 || topBar.Modules[0].Name != "MMM-PhotoFrame" {
		t.Errorf("top_bar should contain only PhotoFrame; got %+v", topBar.Modules)
	}

	middle := byID["middle_center"]
	if !middle.Capped || middle.MaxHeight != "520px" {
		t.Errorf("middle_center should be capped at 520px; got %+v", middle)
	}
	if len(middle.Modules) != 1 || middle.Modules[0].Name != "MMM-CalendarExt3" {
		t.Errorf("middle_center should contain CalendarExt3; got %+v", middle.Modules)
	}

	if !byID["fullscreen_above"].Suspended {
		t.Error("fullscreen_above should be auto-suspended")
	}
}

func TestBuildStateFallsBackToSchemaDefaults(t *testing.T) {
	// No MMM-LayoutBounds — viewer should still render with documented defaults.
	modules := []mmconfig.Module{
		{Module: "MMM-PhotoFrame", Position: "top_bar"},
		{Module: "MMM-CalendarExt3Agenda", Position: "top_left"},
	}

	state := buildState(modules, nil)

	if state.HasLayoutDoc {
		t.Error("HasLayoutDoc should be false when MMM-LayoutBounds is absent")
	}

	byID := map[string]RegionState{}
	for _, r := range state.Regions {
		byID[r.ID] = r
	}

	if byID["top_bar"].MaxHeight != "60px" {
		t.Errorf("top_bar default should be 60px (from schema doc); got %q", byID["top_bar"].MaxHeight)
	}
	if byID["middle_center"].MaxHeight != "480px" {
		t.Errorf("middle_center default should be 480px; got %q", byID["middle_center"].MaxHeight)
	}
}

func TestBuildStateAppliesPendingPositions(t *testing.T) {
	// Live: CalendarExt3 in top_bar; working-copy moves it to middle_center.
	// State should render it under middle_center with Moved=true.
	modules := []mmconfig.Module{
		{ID: "photo1", Module: "MMM-PhotoFrame", Position: "top_bar", Classes: "role1"},
		{ID: "cal1", Module: "MMM-CalendarExt3", Position: "top_bar", Classes: "role1"},
		{ID: "bounds", Module: "MMM-LayoutBounds", Position: "bottom_bar"},
	}
	wc := &WorkingCopy{
		Version: 1,
		Layout: map[string]any{
			"version":         float64(1),
			"regions":         map[string]any{"top_bar": map[string]any{"maxHeight": "420px"}, "middle_center": map[string]any{"maxHeight": "520px"}},
			"moduleOverrides": []any{},
		},
		PendingPositions: map[string]string{"cal1": "middle_center"},
	}

	state := buildState(modules, wc)
	if !state.HasWorkingCopy {
		t.Fatal("expected HasWorkingCopy=true")
	}

	byID := map[string]RegionState{}
	for _, r := range state.Regions {
		byID[r.ID] = r
	}

	for _, m := range byID["top_bar"].Modules {
		if m.Name == "MMM-CalendarExt3" {
			t.Errorf("MMM-CalendarExt3 should have moved out of top_bar; still listed there")
		}
	}
	found := false
	for _, m := range byID["middle_center"].Modules {
		if m.Name == "MMM-CalendarExt3" {
			if !m.Moved {
				t.Errorf("MMM-CalendarExt3 in middle_center should be marked Moved=true; got %+v", m)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("MMM-CalendarExt3 should be rendered in middle_center after pending move; got %+v", byID["middle_center"].Modules)
	}
}

func TestValidateWorkingCopyAcceptsLayoutFromSchemaDoc(t *testing.T) {
	wc := &WorkingCopy{
		Version: 1,
		Layout: map[string]any{
			"version": float64(1),
			"regions": map[string]any{
				"top_bar":          map[string]any{"maxHeight": "60px", "overflow": "hidden"},
				"top_left":         map[string]any{"maxHeight": "30vh"},
				"middle_center":    nil,
				"fullscreen_above": nil,
			},
			"moduleOverrides": []any{
				map[string]any{"match": map[string]any{"module": "MMM-Chores"}, "exempt": true},
				map[string]any{"match": map[string]any{"module": "MMM-PhotoFrame", "region": "top_bar"}, "exempt": true},
			},
		},
		PendingPositions: map[string]string{"abc": "middle_center"},
	}
	if err := validateWorkingCopy(wc); err != nil {
		t.Fatalf("schema-conforming doc rejected: %v", err)
	}
}

func TestValidateWorkingCopyRejectsBadInputs(t *testing.T) {
	cases := []struct {
		name string
		wc   *WorkingCopy
		want string
	}{
		{
			name: "wrong version",
			wc: &WorkingCopy{Version: 1, Layout: map[string]any{
				"version": float64(2),
				"regions": map[string]any{},
			}},
			want: "version must be 1",
		},
		{
			name: "unknown region id",
			wc: &WorkingCopy{Version: 1, Layout: map[string]any{
				"version": float64(1),
				"regions": map[string]any{"top_typo": map[string]any{"maxHeight": "60px"}},
			}},
			want: "unknown region id",
		},
		{
			name: "invalid CSS length",
			wc: &WorkingCopy{Version: 1, Layout: map[string]any{
				"version": float64(1),
				"regions": map[string]any{"top_bar": map[string]any{"maxHeight": "60"}},
			}},
			want: "not a valid CSS length",
		},
		{
			name: "bad overflow value",
			wc: &WorkingCopy{Version: 1, Layout: map[string]any{
				"version": float64(1),
				"regions": map[string]any{"top_bar": map[string]any{"maxHeight": "60px", "overflow": "clip"}},
			}},
			want: "overflow",
		},
		{
			name: "pendingPositions to unknown region",
			wc: &WorkingCopy{Version: 1, Layout: map[string]any{
				"version": float64(1),
				"regions": map[string]any{},
			}, PendingPositions: map[string]string{"x": "unknown_region"}},
			want: "unknown region",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWorkingCopy(tc.wc)
			if err == nil {
				t.Fatalf("expected error containing %q; got nil", tc.want)
			}
			if !contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q; got %q", tc.want, err.Error())
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestBuildStateAutoExemptsFullscreenWithoutOverride(t *testing.T) {
	// Even without an explicit moduleOverrides entry, anything in fullscreen_above
	// must trigger suspension — matches MMM-LayoutBounds.AUTO_EXEMPT.
	modules := []mmconfig.Module{
		{Module: "MMM-Chores", Position: "fullscreen_above"},
		{Module: "MMM-LayoutBounds", Position: "bottom_bar", Config: map[string]any{
			"layout": map[string]any{
				"version":         float64(1),
				"regions":         map[string]any{},
				"moduleOverrides": []any{},
			},
		}},
	}

	state := buildState(modules, nil)
	for _, r := range state.Regions {
		if r.ID == "fullscreen_above" && !r.Suspended {
			t.Errorf("fullscreen_above must be suspended even without explicit override; got %+v", r)
		}
	}
}
