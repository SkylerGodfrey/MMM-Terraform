// Package mascot owns the sprite-layout document for MMM-Mascot (HOM-117).
// The document declares where each sprite sits on the mascot overlay,
// the canvas dimensions, and the holiday calendar that decides which
// sprite state ("default", "halloween", "christmas", …) is active
// today.
//
// Persistence is a single JSON file alongside config.js (default
// mascot-layout.json). The MMM-Mascot module reads the same file via
// fs.watch so saves hot-reload without a MagicMirror restart.
package mascot

import (
	"errors"
	"regexp"
)

// SchemaVersion is the layout document version this store reads and
// writes. Bump when the on-disk shape changes incompatibly.
const SchemaVersion = 1

// Default canvas dimensions match the working portrait canvas size used
// by Canvas v2 (1080×1780 = 1080×1920 panel minus the 140 px Scenes2
// strip). Override per-deploy from the editor.
const (
	DefaultWidth  = 1080
	DefaultHeight = 1780
)

// DefaultState is the fallback state name used when today doesn't fall
// within any holiday window or the active state has no asset.
const DefaultState = "default"

// Document is the entire on-disk layout.
type Document struct {
	SchemaVersion int       `json:"schemaVersion"`
	Canvas        Canvas    `json:"canvas"`
	Sprites       []Sprite  `json:"sprites"`
	Holidays      []Holiday `json:"holidays,omitempty"`
}

// Canvas holds the design-space dimensions sprite coordinates are
// expressed in. The MMM-Mascot module scales these to whatever DOM
// container Canvas v2 hands it.
type Canvas struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Sprite places one sprite asset on the overlay. Sprite is the catalog
// id (matches a directory under MMM-Mascot/sprites/). The asset that
// renders depends on the active state from the holiday calendar.
//
// Rotation is optional (HOM-117 behavioral states). When present the
// module cycles the sprite through the named animation tags instead of
// always playing "idle"; when nil the sprite plays "idle" forever, the
// pre-rotation behavior. Holidays still pick the *skin* (which PNG);
// rotation picks the *animation* (which tag inside it) — two independent
// axes.
type Sprite struct {
	ID       string    `json:"id"`
	Sprite   string    `json:"sprite"`
	X        int       `json:"x"`
	Y        int       `json:"y"`
	W        int       `json:"w"`
	H        int       `json:"h"`
	Rotation *Rotation `json:"rotation,omitempty"`
}

// Rotation cycles a sprite through a set of animation tags at random
// intervals. Animations are frame-tag names that must exist in the
// active skin's Aseprite JSON (idle, barking-run, …). Each dwell is a
// random duration in [MinMs, MaxMs]; the module never plays the same tag
// twice in a row (HOM-117). A single-element list is legal and renders
// as a static animation.
type Rotation struct {
	Animations []string `json:"animations"`
	MinMs      int      `json:"minMs"`
	MaxMs      int      `json:"maxMs"`
}

// Holiday is a date window during which a non-default state name is
// active. Start and End are MM-DD strings (e.g., "10-15"); the document
// is evaluated against the current local date and the first matching
// window in slice order wins.
//
// State maps onto the sprite-asset filename: a sprite with id "cat" in
// a holiday with state "halloween" loads sprites/cat/halloween.{png,json}
// (falling back to default.{png,json} when the asset is missing).
type Holiday struct {
	State string `json:"state"`
	Start string `json:"start"`
	End   string `json:"end"`
}

var (
	// ErrInvalidSprite is returned when a sprite fails geometry or
	// identity validation. Wrap with %w to keep the sentinel discoverable.
	ErrInvalidSprite = errors.New("invalid sprite")

	// ErrInvalidHoliday is returned when a holiday window fails format
	// or ordering validation.
	ErrInvalidHoliday = errors.New("invalid holiday")

	// ErrDuplicateSpriteID is returned when two sprites share the same id.
	// Sprites are identified by id throughout the editor and on the wire.
	ErrDuplicateSpriteID = errors.New("duplicate sprite id")

	// ErrInvalidRotation is returned when a sprite's rotation config fails
	// validation (empty animation list, non-positive interval, or
	// max < min).
	ErrInvalidRotation = errors.New("invalid rotation")
)

// DefaultDocument returns the empty layout used when the on-disk file
// doesn't exist yet. Seeded with the canonical holiday windows so a
// fresh install of HOM-124 has reasonable defaults without the user
// touching the editor.
func DefaultDocument() Document {
	return Document{
		SchemaVersion: SchemaVersion,
		Canvas: Canvas{
			Width:  DefaultWidth,
			Height: DefaultHeight,
		},
		Sprites:  []Sprite{},
		Holidays: DefaultHolidays(),
	}
}

// DefaultHolidays returns the seed list of holiday windows. The user
// picked "date-based, auto" so these are baked in; the editor can
// add/remove/edit them later. Easter and Thanksgiving have variable
// dates — we widen the windows rather than computing real dates (HOM-124).
func DefaultHolidays() []Holiday {
	return []Holiday{
		{State: "halloween", Start: "10-15", End: "11-01"},
		{State: "thanksgiving", Start: "11-20", End: "11-28"},
		{State: "christmas", Start: "12-01", End: "12-26"},
		{State: "valentines", Start: "02-10", End: "02-15"},
		{State: "fourth_of_july", Start: "07-01", End: "07-05"},
	}
}

// mmDDPattern enforces the "MM-DD" wire format for holiday windows.
// Leap-day (02-29) is allowed; the runtime simply never matches it in
// non-leap years.
var mmDDPattern = regexp.MustCompile(`^(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])$`)
