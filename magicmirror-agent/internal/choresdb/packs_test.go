package choresdb

import (
	"errors"
	"testing"
)

// TestPacksCreateIfMissing verifies the agent's defensive create-if-missing:
// the seed schema (ddlV1) does NOT include the packs table (it predates the
// module's packs migration), so the first pack write must create it, and reads
// must then round-trip the row including the JSON members array.
func TestPacksCreateIfMissing(t *testing.T) {
	s := openTestStore(t)

	// Before any write the table doesn't exist yet — reads tolerate that and
	// return an empty list (so the portal + overlap validation work pre-create).
	if list, err := s.ListPacks(); err != nil || len(list) != 0 {
		t.Fatalf("ListPacks before create should be empty, got %v / %v", list, err)
	}
	if _, err := s.GetPack("anything"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetPack before create should be ErrNotFound, got %v", err)
	}

	p, err := s.UpsertPack(Pack{
		Name:      "Kanto",
		StartDate: "2026-07-01",
		EndDate:   "2026-07-07",
		Members:   []string{"1", "4", "7"},
	})
	if err != nil {
		t.Fatalf("upsert (create-if-missing): %v", err)
	}
	if p.ID == "" || p.CreatedAt == "" {
		t.Fatalf("upsert should stamp id + created_at: %+v", p)
	}
	if len(p.Members) != 3 || p.Members[1] != "4" {
		t.Fatalf("members round-trip mismatch: %+v", p.Members)
	}

	// Now the table exists; list/get work.
	list, err := s.ListPacks()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != p.ID {
		t.Fatalf("list mismatch: %+v", list)
	}
	got, err := s.GetPack(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Kanto" {
		t.Fatalf("get mismatch: %+v", got)
	}
}

func TestPacksUpsertReplaceAndDelete(t *testing.T) {
	s := openTestStore(t)

	p, err := s.UpsertPack(Pack{Name: "A", StartDate: "2026-08-01", EndDate: "2026-08-10", Members: []string{"25"}})
	if err != nil {
		t.Fatal(err)
	}

	// Replace by id preserves created_at and overwrites the other fields.
	p2, err := s.UpsertPack(Pack{ID: p.ID, Name: "A2", StartDate: "2026-08-02", EndDate: "2026-08-12", Members: []string{"133"}, CreatedAt: p.CreatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if p2.ID != p.ID || p2.Name != "A2" || p2.CreatedAt != p.CreatedAt {
		t.Fatalf("replace mismatch: %+v", p2)
	}
	if len(p2.Members) != 1 || p2.Members[0] != "133" {
		t.Fatalf("members not replaced: %+v", p2.Members)
	}

	if err := s.DeletePack(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPack(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
	// Delete of missing id is a no-op.
	if err := s.DeletePack("nope"); err != nil {
		t.Fatalf("delete of missing id should be a no-op: %v", err)
	}
}

func TestPacksRefusedOnSchemaMismatch(t *testing.T) {
	path := newSchemaDB(t, SchemaVersion+1)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.UpsertPack(Pack{Name: "A", StartDate: "2026-08-01", EndDate: "2026-08-10"}); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("UpsertPack: want ErrSchemaMismatch, got %v", err)
	}
	if err := s.DeletePack("x"); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("DeletePack: want ErrSchemaMismatch, got %v", err)
	}
}

func TestDecodeMembers(t *testing.T) {
	cases := map[string]int{
		``:                 0,
		`[]`:               0,
		`["1","4-mega-x"]`: 2,
		`not json`:         0,
		`null`:             0,
	}
	for raw, want := range cases {
		if got := decodeMembers(raw); len(got) != want {
			t.Errorf("decodeMembers(%q) len = %d, want %d", raw, len(got), want)
		}
	}
}
