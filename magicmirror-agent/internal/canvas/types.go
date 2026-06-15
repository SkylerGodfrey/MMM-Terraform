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
const SchemaVersion = 2

// Default canvas dimensions match the portrait Pi screen minus the
// 140px Scenes2 indicator strip (HOM-51 convention).
const (
	DefaultWidth  = 1080
	DefaultHeight = 1780
)

// Document is the entire on-disk layout, versioned for forward compat.
type Document struct {
	SchemaVersion int             `json:"schemaVersion"`
	Canvas        Canvas          `json:"canvas"`
	Pages         map[string]Page `json:"pages"`
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
type Page struct {
	Slots []Slot `json:"slots"`
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
		Pages: map[string]Page{},
	}
}
