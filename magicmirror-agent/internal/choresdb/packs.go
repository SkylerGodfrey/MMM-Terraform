package choresdb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ---- packs (Pokémon Theme v2, HOM-149 / HOM-150) ----------------------------
//
// A "pack" is a household-global, date-ranged encounter pool. The MMM-Chores
// module (store/db.js + store/packs.js) owns the schema and the runtime
// resolver; this package is the agent's row-access layer over the SAME table so
// the family-portal CRUD endpoints (HOM-150) write rows indistinguishable from
// the module's. The sequential/non-overlapping rule and the active-pool resolver
// live in the internal/packs package (mirroring store/packs.js), not here — this
// file is purely typed row access.
//
// The `packs` table is created by the module's additive migration. Because the
// agent may run against a DB the module created at an earlier point (before the
// packs migration shipped) we create it defensively if missing, using the exact
// DDL from CONTRACT.md §3 so the two writers never diverge.

// Pack is one row of the packs table. Members is the decoded JSON array of
// form-entry keys (the household-global encounter pool for the date range).
type Pack struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	StartDate string   `json:"startDate"` // 'yyyy-mm-dd'
	EndDate   string   `json:"endDate"`   // 'yyyy-mm-dd', inclusive
	Members   []string `json:"members"`   // form-entry keys
	CreatedAt string   `json:"createdAt"`
}

// packsCreateDDL matches CONTRACT.md §3 verbatim. Used only to create-if-missing
// when the agent meets a DB whose module predates the packs migration. The Node
// module remains the schema owner; this never runs an ALTER or a migration.
const packsCreateDDL = `
CREATE TABLE IF NOT EXISTS packs (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	start_date  TEXT NOT NULL,
	end_date    TEXT NOT NULL,
	members     TEXT NOT NULL,
	created_at  TEXT NOT NULL
)`

// ensurePacksTable creates the packs table if it does not yet exist. It is a
// guarded write (the table is part of the module-owned schema), so it requires a
// writable store. Idempotent.
func (s *Store) ensurePacksTable() error {
	if err := s.requireWritable(); err != nil {
		return err
	}
	if _, err := s.db.Exec(packsCreateDDL); err != nil {
		return fmt.Errorf("%w: ensure packs table: %w", ErrStorage, err)
	}
	return nil
}

func scanPack(row interface{ Scan(...any) error }) (*Pack, error) {
	var p Pack
	var members string
	if err := row.Scan(&p.ID, &p.Name, &p.StartDate, &p.EndDate, &members, &p.CreatedAt); err != nil {
		return nil, err
	}
	p.Members = decodeMembers(members)
	return &p, nil
}

// decodeMembers turns the JSON-text members column into a string slice, never
// nil (an empty/invalid value yields an empty slice, matching packs.js slicing).
func decodeMembers(raw string) []string {
	out := []string{}
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// isNoSuchTable reports whether err is SQLite's "no such table" error. A DB the
// module created before the packs migration shipped won't have the table yet; on
// reads we treat that as "no packs" rather than an error, so the portal can list
// (and the overlap validation can read) before the first write creates it.
func isNoSuchTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table: packs")
}

// ListPacks returns every pack ordered by start_date ASC (chronological, which
// the overlap math relies on — the yyyy-mm-dd format sorts lexicographically).
// A not-yet-created table reads as an empty list (see isNoSuchTable).
func (s *Store) ListPacks() ([]Pack, error) {
	rows, err := s.db.Query(
		"SELECT id, name, start_date, end_date, members, created_at FROM packs ORDER BY start_date ASC",
	)
	if err != nil {
		if isNoSuchTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: list packs: %w", ErrStorage, err)
	}
	defer rows.Close()

	var out []Pack
	for rows.Next() {
		p, err := scanPack(rows)
		if err != nil {
			return nil, fmt.Errorf("%w: scan pack: %w", ErrStorage, err)
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: list packs: %w", ErrStorage, err)
	}
	return out, nil
}

// GetPack returns a pack by id, or ErrNotFound.
func (s *Store) GetPack(id string) (*Pack, error) {
	row := s.db.QueryRow(
		"SELECT id, name, start_date, end_date, members, created_at FROM packs WHERE id = ?", id,
	)
	p, err := scanPack(row)
	if errors.Is(err, sql.ErrNoRows) || isNoSuchTable(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: get pack: %w", ErrStorage, err)
	}
	return p, nil
}

// UpsertPack inserts or replaces a pack, generating a "pk"+12hex id when empty
// and stamping created_at when empty (matching db.js upsertPack semantics). The
// caller (internal/packs) is responsible for the overlap validation; this is the
// raw write. The packs table is created-if-missing first.
func (s *Store) UpsertPack(p Pack) (*Pack, error) {
	if err := s.ensurePacksTable(); err != nil {
		return nil, err
	}
	id := p.ID
	if id == "" {
		id = newID("pk")
	}
	createdAt := p.CreatedAt
	if createdAt == "" {
		createdAt = nowISO()
	}
	members := p.Members
	if members == nil {
		members = []string{}
	}
	encoded, err := json.Marshal(members)
	if err != nil {
		return nil, fmt.Errorf("%w: encode pack members: %w", ErrStorage, err)
	}

	_, err = s.db.Exec(`
		INSERT INTO packs (id, name, start_date, end_date, members, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			start_date = excluded.start_date,
			end_date = excluded.end_date,
			members = excluded.members`,
		id, p.Name, p.StartDate, p.EndDate, string(encoded), createdAt,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: upsert pack: %w", ErrStorage, err)
	}
	return s.GetPack(id)
}

// DeletePack removes a pack by id. Deleting a missing id is a no-op (matching
// db.js deletePack, which never errors on absence). The table is created-if-
// missing first so a delete against a fresh DB doesn't error on a missing table.
func (s *Store) DeletePack(id string) error {
	if err := s.ensurePacksTable(); err != nil {
		return err
	}
	if _, err := s.db.Exec("DELETE FROM packs WHERE id = ?", id); err != nil {
		return fmt.Errorf("%w: delete pack: %w", ErrStorage, err)
	}
	return nil
}
