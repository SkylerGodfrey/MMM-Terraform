// Package choresdb is the family portal's access layer for the MMM-Chores
// runtime-state SQLite database (HOM-132). The Node module (store/db.js,
// HOM-131) OWNS THE SCHEMA — it creates and migrates the tables. This package
// only opens the already-created file and reads/writes rows whose shape and id
// scheme match what the module writes, so module- and agent-written rows are
// indistinguishable.
//
// The DB is opened by BOTH processes concurrently, so we use WAL +
// busy_timeout. The driver is modernc.org/sqlite (pure Go, no CGO) so the
// agent still cross-compiles cleanly for the Pi via `make build-agent-arm64`.
//
// Definition state (chores.yaml) stays with the chores package; this package is
// strictly the runtime-state layer (completions / pending queue / event log /
// theme KV).
package choresdb

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SchemaVersion is the user_version the Node module (store/db.js) stamps for the
// schema this package understands. Writes are refused on a mismatch so the agent
// never corrupts a DB it doesn't recognize.
//
// v2 (HOM-150) adds the `packs` table for Pokémon Theme v2. The agent both reads
// and create-if-missing's that table (internal/choresdb/packs.go), so it fully
// understands v2 — keep this in lockstep with store/db.js SCHEMA_VERSION.
const SchemaVersion = 2

// ErrStorage marks read/write failures so handlers can hide filesystem/driver
// detail from the family-facing UI while it still lands in the agent log.
// Mirrors the chores package's ErrStorage pattern.
var ErrStorage = errors.New("chores db storage error")

// ErrNotFound is returned by get-style helpers when no row matches.
var ErrNotFound = errors.New("not found")

// ErrUnavailable is returned by Open when the DB file can't be opened (e.g. the
// module hasn't created it yet). It is a typed error so callers can degrade
// gracefully — the agent must never crash because the chores DB is absent.
var ErrUnavailable = errors.New("chores db unavailable")

// ErrSchemaMismatch is returned when the on-disk schema version is not the one
// this package was built against. Reads may still be attempted by callers, but
// writes are refused to avoid corrupting an unknown schema.
var ErrSchemaMismatch = errors.New("chores db schema version mismatch")

// Store wraps a *sql.DB opened against the MMM-Chores runtime-state file.
type Store struct {
	db   *sql.DB
	path string

	mu        sync.RWMutex
	schemaVer int
	writable  bool // false when schemaVer != SchemaVersion
}

// Open opens (does not create) the SQLite DB at path in WAL mode with a
// busy_timeout, then reads its schema version. The schema is owned by the Node
// module; if the file does not exist yet, Open returns ErrUnavailable wrapped so
// the caller can run degraded rather than crash.
//
// A schema-version mismatch is NOT a hard failure: the Store opens read-only
// (Writable() == false) and write helpers return ErrSchemaMismatch, while reads
// still work for display. The mismatch is the caller's to log.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty path", ErrUnavailable)
	}

	// DSN pragmas apply to every pooled connection. _pragma is the
	// modernc.org/sqlite syntax. mode=rw (not rwc) means SQLite will NOT create
	// the file — a missing module DB surfaces as an open/ping error here rather
	// than a silently-created empty file the module never wrote.
	dsn := "file:" + path +
		"?mode=rw" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: opening %s: %w", ErrUnavailable, path, err)
	}
	// sql.Open is lazy; force a real connection so a missing file fails here.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("%w: opening %s: %w", ErrUnavailable, path, err)
	}

	s := &Store{db: db, path: path}

	ver, err := s.readSchemaVersion()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("%w: reading schema version of %s: %w", ErrStorage, path, err)
	}
	s.schemaVer = ver
	s.writable = ver == SchemaVersion

	return s, nil
}

// Path returns the file the Store is bound to.
func (s *Store) Path() string { return s.path }

// SchemaVersion returns the user_version read from the DB at open time.
func (s *Store) SchemaVersion() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.schemaVer
}

// Writable reports whether the on-disk schema matches what this package writes.
// When false, every write helper returns ErrSchemaMismatch.
func (s *Store) Writable() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.writable
}

// Close releases the underlying connection pool.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// readSchemaVersion prefers PRAGMA user_version (the migration's source of
// truth in db.js) and falls back to meta.schema_version. A brand-new DB that
// the module created but hasn't migrated would report 0 here.
func (s *Store) readSchemaVersion() (int, error) {
	var ver int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&ver); err != nil {
		return 0, err
	}
	if ver != 0 {
		return ver, nil
	}
	// Fall back to meta.schema_version if present (table may not exist on a
	// totally empty DB — treat any error as "0, no schema yet").
	var raw sql.NullString
	row := s.db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'")
	if err := row.Scan(&raw); err != nil {
		return 0, nil //nolint:nilerr // absence means version 0, not a failure
	}
	if raw.Valid {
		if n, err := parseInt(raw.String); err == nil {
			return n, nil
		}
	}
	return 0, nil
}

// requireWritable guards every mutating helper.
func (s *Store) requireWritable() error {
	if !s.Writable() {
		return fmt.Errorf("%w: have %d, want %d", ErrSchemaMismatch, s.SchemaVersion(), SchemaVersion)
	}
	return nil
}

// ---- id / time helpers (match db.js) ----------------------------------------

// newID matches db.js newId(): prefix + crypto.randomBytes(6).toString("hex"),
// i.e. the prefix followed by 12 lowercase hex chars.
func newID(prefix string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read never fails on supported platforms; fall back to time so we
		// still produce a unique-enough id rather than panicking.
		now := uint64(time.Now().UnixNano())
		for i := 0; i < 6; i++ {
			b[i] = byte(now >> (8 * i))
		}
	}
	return prefix + hex.EncodeToString(b[:])
}

// nowISO matches db.js nowIso(): JS Date.toISOString() — UTC, millisecond
// precision, trailing "Z" (e.g. 2026-06-20T19:04:00.000Z).
func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func parseInt(s string) (int, error) {
	s = strings.TrimSpace(s)
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}
