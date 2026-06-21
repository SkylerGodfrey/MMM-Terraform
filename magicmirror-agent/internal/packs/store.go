// Package packs implements the Pokémon Theme v2 pack rules for the family
// portal (HOM-150, ticket #4 of epic HOM-146). A "pack" is a household-global,
// date-ranged encounter pool: while the current local day falls inside a pack's
// inclusive [start_date, end_date] range, wild encounters draw from that pack's
// members (form-entry keys). Packs are SEQUENTIAL and NON-OVERLAPPING.
//
// This is the Go counterpart of MMM-Chores store/packs.js (HOM-149). It owns the
// overlap rule (rangesOverlap + the create/update guards) and the active-pool
// resolver; the choresdb package owns the typed row access over the SAME `packs`
// table. Both writers (this agent + the Node module) MUST apply the identical
// overlap rule so the table stays consistent — see store/packs.js for the
// reference implementation this file mirrors line-for-line.
package packs

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/choresdb"
)

// ErrValidation marks a rule violation (bad name/date, or an overlap) so handlers
// can surface the message to the family with a 400 rather than a 500.
var ErrValidation = errors.New("pack validation error")

// Message returns a family-friendly message for an ErrValidation produced here.
// Validation errors are formatted as "pack validation error: <human detail>";
// this strips the sentinel prefix so the portal can show just the detail. For
// non-validation errors it returns err.Error() unchanged.
func Message(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	const prefix = "pack validation error: "
	if errors.Is(err, ErrValidation) && strings.HasPrefix(msg, prefix) {
		return strings.TrimPrefix(msg, prefix)
	}
	return msg
}

// dateRE matches the 'yyyy-mm-dd' format packs use. Lexicographic order on this
// format matches chronological order, which the overlap math relies on (same as
// store/packs.js DATE_RE).
var dateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// UnownFallbackKeys is the no-pack fallback pool (CONTRACT.md §1 + §3): the 28
// Unown letter-forms, non-shiny normal-letter keys, modeled as `alt` form-
// entries on species #201. Mirrors UNOWN_FALLBACK_KEYS in store/packs.js.
var UnownFallbackKeys = buildUnownFallbackKeys()

func buildUnownFallbackKeys() []string {
	keys := make([]string, 0, 28)
	for c := byte('a'); c <= 'z'; c++ {
		keys = append(keys, "201-"+string(c))
	}
	keys = append(keys, "201-exclamation")
	keys = append(keys, "201-question")
	return keys
}

// PackDB is the slice of choresdb the pack rules need: typed row access over the
// `packs` table. *choresdb.Store satisfies it; tests use a fake.
type PackDB interface {
	ListPacks() ([]choresdb.Pack, error)
	GetPack(id string) (*choresdb.Pack, error)
	UpsertPack(p choresdb.Pack) (*choresdb.Pack, error)
	DeletePack(id string) error
}

// Fields is the caller-supplied shape for create/update. Members is the JSON
// array of form-entry keys; an empty/nil slice is allowed (an empty pool).
type Fields struct {
	Name      string
	StartDate string
	EndDate   string
	Members   []string
}

// Pool is the resolver result: the active encounter pool for a date. Source is
// "pack:<id>" when a pack covers the date, or "fallback-unown" otherwise.
type Pool struct {
	Source  string   `json:"source"`
	Members []string `json:"members"`
}

// LocalDate returns the local-time 'yyyy-mm-dd' for t (mirrors localDate in
// store/packs.js, which uses the JS local timezone). Lexicographic order on this
// format is chronological.
func LocalDate(t time.Time) string {
	return t.Format("2006-01-02")
}

// rangesOverlap is inclusive-range intersection on 'yyyy-mm-dd' strings, byte
// identical to store/packs.js rangesOverlap.
func rangesOverlap(aStart, aEnd, bStart, bEnd string) bool {
	return aStart <= bEnd && bStart <= aEnd
}

func assertDate(value, label string) error {
	if !dateRE.MatchString(value) {
		return fmt.Errorf("%w: Pack %s must be a 'yyyy-mm-dd' date (got: %s)", ErrValidation, label, value)
	}
	return nil
}

// cleanFields is the normalized output of validate: a trimmed name and a non-nil
// members slice. Mirrors validatePack's return value in store/packs.js.
type cleanFields struct {
	Name      string
	StartDate string
	EndDate   string
	Members   []string
}

// validate enforces the sequential/non-overlapping invariant against the
// existing packs, mirroring validatePack in store/packs.js. excludeID is the
// pack being updated (so it doesn't conflict with itself); pass "" on create.
func validate(db PackDB, f Fields, excludeID string) (cleanFields, error) {
	name := strings.TrimSpace(f.Name)
	if name == "" {
		return cleanFields{}, fmt.Errorf("%w: Pack name is required", ErrValidation)
	}
	if err := assertDate(f.StartDate, "start_date"); err != nil {
		return cleanFields{}, err
	}
	if err := assertDate(f.EndDate, "end_date"); err != nil {
		return cleanFields{}, err
	}
	if f.StartDate > f.EndDate {
		return cleanFields{}, fmt.Errorf("%w: Pack start_date must be on or before end_date", ErrValidation)
	}
	members := f.Members
	if members == nil {
		members = []string{}
	}

	existing, err := db.ListPacks()
	if err != nil {
		return cleanFields{}, err
	}
	for _, other := range existing {
		if excludeID != "" && other.ID == excludeID {
			continue
		}
		if rangesOverlap(f.StartDate, f.EndDate, other.StartDate, other.EndDate) {
			return cleanFields{}, fmt.Errorf(
				"%w: Pack date range %s..%s overlaps existing pack '%s' (%s..%s)",
				ErrValidation, f.StartDate, f.EndDate, other.Name, other.StartDate, other.EndDate,
			)
		}
	}

	return cleanFields{Name: name, StartDate: f.StartDate, EndDate: f.EndDate, Members: members}, nil
}

// List returns every pack, oldest-first (start_date ASC, from the DB).
func List(db PackDB) ([]choresdb.Pack, error) {
	return db.ListPacks()
}

// Create persists a new pack, rejecting (ErrValidation) on overlap with any
// existing pack. Mirrors createPack in store/packs.js.
func Create(db PackDB, f Fields) (*choresdb.Pack, error) {
	clean, err := validate(db, f, "")
	if err != nil {
		return nil, err
	}
	return db.UpsertPack(choresdb.Pack{
		Name:      clean.Name,
		StartDate: clean.StartDate,
		EndDate:   clean.EndDate,
		Members:   clean.Members,
	})
}

// Update edits a pack by id, rejecting (ErrValidation) if the new range overlaps
// a DIFFERENT pack, or ErrNotFound if the id is unknown. Unset fields are kept
// from the existing pack; created_at is preserved. Mirrors updatePack in
// store/packs.js.
func Update(db PackDB, id string, f Fields) (*choresdb.Pack, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: update requires a pack id", ErrValidation)
	}
	existing, err := db.GetPack(id)
	if err != nil {
		return nil, err
	}
	merged := Fields{
		Name:      coalesce(f.Name, existing.Name),
		StartDate: coalesce(f.StartDate, existing.StartDate),
		EndDate:   coalesce(f.EndDate, existing.EndDate),
		Members:   existing.Members,
	}
	if f.Members != nil {
		merged.Members = f.Members
	}
	clean, err := validate(db, merged, id)
	if err != nil {
		return nil, err
	}
	return db.UpsertPack(choresdb.Pack{
		ID:        id,
		Name:      clean.Name,
		StartDate: clean.StartDate,
		EndDate:   clean.EndDate,
		Members:   clean.Members,
		CreatedAt: existing.CreatedAt,
	})
}

// Delete removes a pack by id (no-op on a missing id).
func Delete(db PackDB, id string) error {
	return db.DeletePack(id)
}

// ResolvePool resolves the active encounter pool for a date (default: today,
// local when dateStr is ""). Mirrors resolvePool in store/packs.js:
//
//	-> { source: "pack:<id>",     members: [...formKey] }  if a pack covers it
//	-> { source: "fallback-unown", members: UnownFallbackKeys } otherwise
//
// A nil db (degraded mode) yields the Unown fallback so the mirror always has a
// tracked pool to draw from.
func ResolvePool(db PackDB, dateStr string) (Pool, error) {
	date := dateStr
	if date == "" {
		date = LocalDate(time.Now())
	}
	if db != nil {
		list, err := db.ListPacks()
		if err != nil {
			return Pool{}, err
		}
		for _, pack := range list {
			if pack.StartDate <= date && date <= pack.EndDate {
				return Pool{Source: "pack:" + pack.ID, Members: append([]string{}, pack.Members...)}, nil
			}
		}
	}
	return Pool{Source: "fallback-unown", Members: append([]string{}, UnownFallbackKeys...)}, nil
}

func coalesce(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
