package packs

import (
	"errors"
	"sort"
	"testing"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/choresdb"
)

// fakeDB is an in-memory PackDB for testing the overlap rule and resolver
// without standing up SQLite. UpsertPack mimics choresdb: it stamps an id and
// created_at when empty and replaces by id otherwise.
type fakeDB struct {
	packs map[string]choresdb.Pack
	seq   int
}

func newFakeDB() *fakeDB { return &fakeDB{packs: map[string]choresdb.Pack{}} }

func (f *fakeDB) ListPacks() ([]choresdb.Pack, error) {
	out := make([]choresdb.Pack, 0, len(f.packs))
	for _, p := range f.packs {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartDate < out[j].StartDate })
	return out, nil
}

func (f *fakeDB) GetPack(id string) (*choresdb.Pack, error) {
	p, ok := f.packs[id]
	if !ok {
		return nil, choresdb.ErrNotFound
	}
	cp := p
	return &cp, nil
}

func (f *fakeDB) UpsertPack(p choresdb.Pack) (*choresdb.Pack, error) {
	if p.ID == "" {
		f.seq++
		p.ID = "pk" + string(rune('0'+f.seq))
	}
	if p.CreatedAt == "" {
		p.CreatedAt = "2026-06-21T00:00:00.000Z"
	}
	if p.Members == nil {
		p.Members = []string{}
	}
	f.packs[p.ID] = p
	cp := p
	return &cp, nil
}

func (f *fakeDB) DeletePack(id string) error {
	delete(f.packs, id)
	return nil
}

// ---- create / round-trip ----------------------------------------------------

func TestCreateRoundTrip(t *testing.T) {
	db := newFakeDB()
	p, err := Create(db, Fields{
		Name:      "  Kanto Starters  ",
		StartDate: "2026-07-01",
		EndDate:   "2026-07-07",
		Members:   []string{"1", "4", "7"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.Name != "Kanto Starters" {
		t.Errorf("name should be trimmed, got %q", p.Name)
	}
	if p.ID == "" || p.CreatedAt == "" {
		t.Errorf("create should stamp id + created_at, got %+v", p)
	}
	if len(p.Members) != 3 || p.Members[0] != "1" {
		t.Errorf("members round-trip mismatch: %+v", p.Members)
	}

	list, err := List(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != p.ID {
		t.Fatalf("list should contain the created pack: %+v", list)
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	db := newFakeDB()
	cases := []Fields{
		{Name: "", StartDate: "2026-07-01", EndDate: "2026-07-02"},  // empty name
		{Name: "X", StartDate: "2026-7-1", EndDate: "2026-07-02"},   // bad start format
		{Name: "X", StartDate: "2026-07-01", EndDate: "07/02/2026"}, // bad end format
		{Name: "X", StartDate: "2026-07-05", EndDate: "2026-07-01"}, // start after end
	}
	for i, f := range cases {
		if _, err := Create(db, f); !errors.Is(err, ErrValidation) {
			t.Errorf("case %d: want ErrValidation, got %v", i, err)
		}
	}
}

// ---- overlap rejection (matches store/packs.js) -----------------------------

func TestCreateRejectsOverlap(t *testing.T) {
	db := newFakeDB()
	if _, err := Create(db, Fields{Name: "A", StartDate: "2026-07-10", EndDate: "2026-07-20"}); err != nil {
		t.Fatal(err)
	}

	overlapping := []Fields{
		{Name: "exact", StartDate: "2026-07-10", EndDate: "2026-07-20"},
		{Name: "inside", StartDate: "2026-07-12", EndDate: "2026-07-15"},
		{Name: "straddle-start", StartDate: "2026-07-05", EndDate: "2026-07-11"},
		{Name: "straddle-end", StartDate: "2026-07-19", EndDate: "2026-07-25"},
		{Name: "touch-end", StartDate: "2026-07-20", EndDate: "2026-07-22"}, // inclusive: shared day overlaps
	}
	for _, f := range overlapping {
		if _, err := Create(db, f); !errors.Is(err, ErrValidation) {
			t.Errorf("%s should be rejected as overlap, got %v", f.Name, err)
		}
	}

	// Adjacent, non-overlapping ranges are allowed (inclusive ranges, so the
	// next pack must start the day AFTER the previous ends).
	if _, err := Create(db, Fields{Name: "before", StartDate: "2026-07-01", EndDate: "2026-07-09"}); err != nil {
		t.Errorf("adjacent-before should be allowed: %v", err)
	}
	if _, err := Create(db, Fields{Name: "after", StartDate: "2026-07-21", EndDate: "2026-07-30"}); err != nil {
		t.Errorf("adjacent-after should be allowed: %v", err)
	}
}

func TestUpdateExcludesSelf(t *testing.T) {
	db := newFakeDB()
	a, err := Create(db, Fields{Name: "A", StartDate: "2026-08-01", EndDate: "2026-08-10"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(db, Fields{Name: "B", StartDate: "2026-08-20", EndDate: "2026-08-25"}); err != nil {
		t.Fatal(err)
	}

	// Updating A's own range (no overlap with B) must succeed — it must not
	// conflict with itself.
	updated, err := Update(db, a.ID, Fields{StartDate: "2026-08-02", EndDate: "2026-08-12"})
	if err != nil {
		t.Fatalf("self-overlap should be excluded: %v", err)
	}
	if updated.StartDate != "2026-08-02" || updated.EndDate != "2026-08-12" {
		t.Errorf("update did not apply new range: %+v", updated)
	}
	if updated.Name != "A" {
		t.Errorf("unset name should be preserved: %+v", updated)
	}
	if updated.CreatedAt != a.CreatedAt {
		t.Errorf("created_at should be preserved: %q != %q", updated.CreatedAt, a.CreatedAt)
	}

	// Moving A onto B must be rejected.
	if _, err := Update(db, a.ID, Fields{StartDate: "2026-08-21", EndDate: "2026-08-24"}); !errors.Is(err, ErrValidation) {
		t.Errorf("update overlapping a different pack should reject, got %v", err)
	}

	// Unknown id -> ErrNotFound.
	if _, err := Update(db, "nope", Fields{Name: "X"}); !errors.Is(err, choresdb.ErrNotFound) {
		t.Errorf("update of unknown id: want ErrNotFound, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	db := newFakeDB()
	p, err := Create(db, Fields{Name: "A", StartDate: "2026-09-01", EndDate: "2026-09-05"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Delete(db, p.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := List(db)
	if len(list) != 0 {
		t.Fatalf("pack should be gone, got %+v", list)
	}
	// Deleting a missing id is a no-op.
	if err := Delete(db, "nope"); err != nil {
		t.Errorf("delete of missing id should be a no-op, got %v", err)
	}
}

// ---- resolver / fallback (matches store/packs.js) ---------------------------

func TestResolvePoolActivePack(t *testing.T) {
	db := newFakeDB()
	p, err := Create(db, Fields{
		Name:      "Active",
		StartDate: "2026-10-01",
		EndDate:   "2026-10-31",
		Members:   []string{"25", "133"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A date inside the range resolves to the pack.
	pool, err := ResolvePool(db, "2026-10-15")
	if err != nil {
		t.Fatal(err)
	}
	if pool.Source != "pack:"+p.ID {
		t.Errorf("want pack source, got %q", pool.Source)
	}
	if len(pool.Members) != 2 || pool.Members[0] != "25" {
		t.Errorf("pool members mismatch: %+v", pool.Members)
	}

	// Inclusive boundaries.
	for _, d := range []string{"2026-10-01", "2026-10-31"} {
		pl, _ := ResolvePool(db, d)
		if pl.Source != "pack:"+p.ID {
			t.Errorf("boundary %s should be in range, got %q", d, pl.Source)
		}
	}
}

func TestResolvePoolFallback(t *testing.T) {
	db := newFakeDB()
	if _, err := Create(db, Fields{Name: "A", StartDate: "2026-11-01", EndDate: "2026-11-10"}); err != nil {
		t.Fatal(err)
	}

	// A date outside any pack -> the tracked Unown fallback.
	pool, err := ResolvePool(db, "2026-12-25")
	if err != nil {
		t.Fatal(err)
	}
	if pool.Source != "fallback-unown" {
		t.Errorf("want fallback source, got %q", pool.Source)
	}
	if len(pool.Members) != 28 {
		t.Fatalf("fallback should have 28 Unown keys, got %d", len(pool.Members))
	}
	if pool.Members[0] != "201-a" || pool.Members[25] != "201-z" {
		t.Errorf("fallback letter keys malformed: %+v", pool.Members[:1])
	}
	if pool.Members[26] != "201-exclamation" || pool.Members[27] != "201-question" {
		t.Errorf("fallback should end with exclamation/question, got %+v", pool.Members[26:])
	}
}

func TestResolvePoolNilDB(t *testing.T) {
	pool, err := ResolvePool(nil, "2026-12-25")
	if err != nil {
		t.Fatal(err)
	}
	if pool.Source != "fallback-unown" || len(pool.Members) != 28 {
		t.Errorf("nil db should yield the Unown fallback, got %+v", pool)
	}
}
