// Package canvas owns the slot-based layout document for Canvas v2
// (HOM-102). Each page declares free-form rectangular slots; the
// MMM-Canvas module on the mirror reads this document via its
// node_helper and relocates module wrappers into the slots.
//
// Persistence is a single JSON file alongside config.js (default
// canvas-layout.json). This is deliberately separate from config.js
// — module config persists across layout edits per the registry
// decoupling decision (see HOM-102 + project_module_registry_decoupling).
package canvas

import "errors"

// SchemaVersion is the layout document version this store reads and
// writes. Bump when the on-disk shape changes incompatibly.
//
// v2 (HOM-102): the slot-based layout doc the editor and MMM-Canvas
// share; pages own a flat slot list.
//
// v3 (HOM-119): adds named sections — reusable slot groups that any
// page can include with `pages.<name>.sections`. Pages can override the
// section's default slot geometry per-page via `sectionOverrides`.
// Backward-compat: v2 docs load unchanged (Sections is omitempty); the
// store rewrites them as v3 on the next save.
const SchemaVersion = 3

// Default canvas dimensions match the portrait Pi screen 1:1. The old
// 1080×1780 default reserved a 140 px Scenes2 indicator strip (HOM-51);
// HOM-120 retires the reservation because Scenes2 is now a regular
// canvas slot, not a fixed bottom strip — so the canvas can claim the
// full viewport. Slot coordinates are physical pixels at the mirror,
// so set Canvas.Width/Height to match the actual screen resolution in
// the editor when it differs from the default.
const (
	DefaultWidth  = 1080
	DefaultHeight = 1920
)

// Document is the entire on-disk layout, versioned for forward compat.
type Document struct {
	SchemaVersion int                `json:"schemaVersion"`
	Canvas        Canvas             `json:"canvas"`
	Sections      map[string]Section `json:"sections,omitempty"`
	Pages         map[string]Page    `json:"pages"`
}

// Canvas is the singleton global configuration for the layout surface.
type Canvas struct {
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	DebugBorders bool   `json:"debugBorders"`
	DebugLabels  bool   `json:"debugLabels"`
	DefaultPage  string `json:"defaultPage"`
}

// Page is a named slot set. The active page determines which slots
// render. Module config persists in config.js regardless of slot
// placement (HOM-102 registry decoupling).
//
// HOM-119: pages can opt into reusable Sections via Sections (the list
// of section names to include) and tweak any section slot's geometry
// for this page via SectionOverrides. Overrides match the section slot
// by Section + Module; one override per (section, module) tuple.
type Page struct {
	Slots            []Slot            `json:"slots"`
	Sections         []string          `json:"sections,omitempty"`
	SectionOverrides []SectionOverride `json:"sectionOverrides,omitempty"`
}

// Section is a reusable, named group of slots. Pages opt in via
// Page.Sections; the section's slots render on the page using the
// section's default geometry unless the page has a matching
// SectionOverride. HOM-119.
//
// DefaultOnNewPage marks the section as "auto-included when a new page
// is created" — useful for chrome that should appear everywhere (e.g.
// the MMM-Scenes2 strip or a daily-pokemon mascot). The editor adds
// the section name to a new page's Sections list at creation time;
// users can still uncheck it per-page.
type Section struct {
	Slots            []Slot `json:"slots"`
	DefaultOnNewPage bool   `json:"defaultOnNewPage,omitempty"`
}

// SectionOverride replaces a single section slot's geometry for one
// page. Edits to the section default propagate to pages without a
// matching override; pages with an override stay pinned (HOM-119 UX).
// Module is the section slot's module — sections forbid duplicate
// modules so this uniquely identifies the slot within the section.
type SectionOverride struct {
	Section string `json:"section"`
	Module  string `json:"module"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	W       int    `json:"w"`
	H       int    `json:"h"`
	ZIndex  int    `json:"zIndex,omitempty"`
	Hidden  bool   `json:"hidden,omitempty"`
}

// AsSlot lifts an override into a Slot for merge-time rendering. The
// override carries the same fields as a Slot plus the Section pointer,
// so this is mechanical.
func (o SectionOverride) AsSlot() Slot {
	return Slot{
		Module: o.Module,
		X:      o.X,
		Y:      o.Y,
		W:      o.W,
		H:      o.H,
		ZIndex: o.ZIndex,
		Hidden: o.Hidden,
	}
}

// Slot places a single module into a rectangular region of the canvas.
// `Module` is the magicmirror module ID (the same ID returned by
// /api/v1/modules); the canvas resolves it to a DOM wrapper at render
// time. Negative dimensions, out-of-bounds rects, or overlapping slots
// within a page are rejected at the storage layer.
type Slot struct {
	Module string `json:"module"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	ZIndex int    `json:"zIndex,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

var (
	// ErrPageNotFound is returned by GetPage/DeletePage when no page
	// exists under the given name.
	ErrPageNotFound = errors.New("page not found")

	// ErrSectionNotFound is returned by GetSection/DeleteSection when
	// no section exists under the given name. HOM-119.
	ErrSectionNotFound = errors.New("section not found")

	// ErrInvalidSlot is returned when a slot fails geometry validation
	// (negative dimensions, out-of-bounds, etc.). Wrap with %w to keep
	// the sentinel discoverable while attaching context.
	ErrInvalidSlot = errors.New("invalid slot")

	// ErrSlotOverlap is returned when two visible slots within the same
	// page would overlap. Hidden slots are exempt.
	ErrSlotOverlap = errors.New("slots overlap within page")

	// ErrUnknownModule is returned when a slot references a module ID
	// that does not exist in the config.js module list.
	ErrUnknownModule = errors.New("slot references unknown module")

	// ErrUnknownSection is returned when a page references a section
	// name that does not exist in the document. HOM-119.
	ErrUnknownSection = errors.New("page references unknown section")

	// ErrDuplicateSectionModule is returned when a section contains two
	// slots with the same module name — overrides identify slots by
	// module so this ambiguity is rejected at the storage layer. HOM-119.
	ErrDuplicateSectionModule = errors.New("section contains duplicate module")

	// ErrOrphanedOverride is returned when a page override targets a
	// section/module pair that isn't in the section. HOM-119.
	ErrOrphanedOverride = errors.New("section override has no matching slot")
)

// DefaultDocument returns the empty layout used when the on-disk file
// doesn't exist yet. Width/height match a portrait Pi; the default page
// is "home" with no slots — so an upgrade from no-canvas to canvas
// renders nothing through MMM-Canvas until pages are populated.
func DefaultDocument() Document {
	return Document{
		SchemaVersion: SchemaVersion,
		Canvas: Canvas{
			Width:       DefaultWidth,
			Height:      DefaultHeight,
			DefaultPage: "home",
		},
		Sections: map[string]Section{},
		Pages:    map[string]Page{},
	}
}

// ResolvePageSlots returns the slot list the renderer should draw for a
// given page: section slots (default geometry unless overridden) merged
// with the page's own slots. Returned slots are independent copies — the
// caller can mutate them without affecting the document. HOM-119.
//
// Render order: included sections in declaration order, then page-
// specific slots. Slots with explicit ZIndex still win at paint time;
// this order only matters for default stacking (CSS source order).
func (d Document) ResolvePageSlots(pageName string) []Slot {
	page, ok := d.Pages[pageName]
	if !ok {
		return nil
	}
	overrides := indexOverrides(page.SectionOverrides)
	out := make([]Slot, 0, len(page.Slots))
	for _, sectionName := range page.Sections {
		section, ok := d.Sections[sectionName]
		if !ok {
			continue
		}
		for _, slot := range section.Slots {
			if override, ok := overrides[overrideKey{sectionName, slot.Module}]; ok {
				out = append(out, override.AsSlot())
				continue
			}
			out = append(out, slot)
		}
	}
	out = append(out, page.Slots...)
	return out
}

type overrideKey struct {
	section string
	module  string
}

func indexOverrides(list []SectionOverride) map[overrideKey]SectionOverride {
	out := make(map[overrideKey]SectionOverride, len(list))
	for _, o := range list {
		out[overrideKey{o.Section, o.Module}] = o
	}
	return out
}
