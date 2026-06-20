package choresdb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"testing"

	_ "modernc.org/sqlite"
)

// ddlV1 is a verbatim copy of the v1 migration in the MMM-Chores module's
// store/db.js (HOM-131). THE NODE MODULE IS THE SCHEMA OWNER — it creates and
// migrates these tables in production; the Go agent only opens the already-built
// file. This copy exists solely so tests can stand up a realistic schema without
// importing the Node module. If db.js's v1 migration ever changes, this must be
// updated to match, but Go must never run migrations against the live DB.
const ddlV1 = `
CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT
);

CREATE TABLE completions (
	chore_id     TEXT NOT NULL,
	user         TEXT NOT NULL,
	status       TEXT NOT NULL DEFAULT 'open',
	completed_at TEXT,
	updated_at   TEXT NOT NULL,
	PRIMARY KEY (chore_id, user)
);

CREATE TABLE pending_queue (
	id            TEXT PRIMARY KEY,
	chore_id      TEXT NOT NULL,
	user          TEXT NOT NULL,
	tokens        INTEGER NOT NULL DEFAULT 0,
	theme_payload TEXT,
	created_at    TEXT NOT NULL
);
CREATE INDEX idx_pending_chore_user ON pending_queue (chore_id, user);

CREATE TABLE events (
	id          TEXT PRIMARY KEY,
	type        TEXT NOT NULL,
	user        TEXT,
	payload     TEXT NOT NULL,
	created_at  TEXT NOT NULL,
	reverted_at TEXT
);
CREATE INDEX idx_events_created ON events (created_at);
CREATE INDEX idx_events_type    ON events (type);

CREATE TABLE theme_kv (
	theme_id TEXT NOT NULL,
	key      TEXT NOT NULL,
	value    TEXT NOT NULL,
	PRIMARY KEY (theme_id, key)
);
`

// newSchemaDB creates a fresh DB file at v1, exactly as the Node module would:
// it runs ddlV1, stamps PRAGMA user_version and meta.schema_version. Returns the
// file path; the caller opens it via Open() under test.
func newSchemaDB(t *testing.T, version int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chores.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(ddlV1); err != nil {
		t.Fatalf("exec ddl: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = " + itoa(version)); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO meta (key, value) VALUES ('schema_version', ?)", itoa(version),
	); err != nil {
		t.Fatalf("set meta schema_version: %v", err)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := newSchemaDB(t, SchemaVersion)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// ---- Open / schema-version guard --------------------------------------------

func TestOpenMissingFileIsUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.db")
	_, err := Open(path)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable for missing file, got %v", err)
	}
}

func TestOpenEmptyPathIsUnavailable(t *testing.T) {
	if _, err := Open(""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable for empty path, got %v", err)
	}
}

func TestOpenReadsSchemaVersion(t *testing.T) {
	s := openTestStore(t)
	if got := s.SchemaVersion(); got != SchemaVersion {
		t.Fatalf("schema version = %d, want %d", got, SchemaVersion)
	}
	if !s.Writable() {
		t.Fatal("store should be writable at matching schema version")
	}
}

func TestSchemaMismatchRefusesWrites(t *testing.T) {
	path := newSchemaDB(t, SchemaVersion+1) // a future schema the agent doesn't know
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open should succeed on mismatch (read-only): %v", err)
	}
	defer s.Close()

	if s.Writable() {
		t.Fatal("store must not be writable on schema mismatch")
	}
	// Reads still work.
	if _, err := s.ListCompletions(); err != nil {
		t.Fatalf("reads should work on mismatch: %v", err)
	}
	// Every write helper refuses with ErrSchemaMismatch.
	if _, err := s.UpsertCompletion("c1", "Dad", "done", ""); !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("UpsertCompletion: want ErrSchemaMismatch, got %v", err)
	}
	if _, err := s.InsertPending(PendingItem{ChoreID: "c1", User: "Dad"}); !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("InsertPending: want ErrSchemaMismatch, got %v", err)
	}
	if _, err := s.InsertEvent(EventInput{Type: "chore_completed"}); !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("InsertEvent: want ErrSchemaMismatch, got %v", err)
	}
	if err := s.ThemeSet("pokemon", "k", json.RawMessage(`1`)); !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("ThemeSet: want ErrSchemaMismatch, got %v", err)
	}
}

// ---- id scheme matches db.js -------------------------------------------------

func TestNewIDMatchesDbJsScheme(t *testing.T) {
	re := regexp.MustCompile(`^p[0-9a-f]{12}$`)
	for i := 0; i < 50; i++ {
		id := newID("p")
		if !re.MatchString(id) {
			t.Fatalf("id %q does not match prefix+12hex", id)
		}
	}
	if id := newID("e"); id[0] != 'e' || len(id) != 13 {
		t.Fatalf("event id %q malformed", id)
	}
}

// ---- completions -------------------------------------------------------------

func TestCompletionsLifecycle(t *testing.T) {
	s := openTestStore(t)

	// Missing row -> ErrNotFound.
	if _, err := s.GetCompletion("c1", "Dad"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// Upsert pending: completed_at auto-stamped.
	c, err := s.UpsertCompletion("c1", "Dad", "pending", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "pending" || c.CompletedAt == "" {
		t.Fatalf("pending should stamp completed_at, got %+v", c)
	}

	// Upsert done with explicit timestamp.
	c, err = s.UpsertCompletion("c1", "Dad", "done", "2026-06-20T10:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "done" || c.CompletedAt != "2026-06-20T10:00:00.000Z" {
		t.Fatalf("explicit completed_at not honored: %+v", c)
	}

	// Back to open: completed_at cleared.
	c, err = s.UpsertCompletion("c1", "Dad", "open", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "open" || c.CompletedAt != "" {
		t.Fatalf("open should clear completed_at: %+v", c)
	}

	// List reflects the single row.
	list, err := s.ListCompletions()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 completion, got %d", len(list))
	}
}

// ---- pending queue -----------------------------------------------------------

func TestPendingQueue(t *testing.T) {
	s := openTestStore(t)

	payload := json.RawMessage(`{"pokemon":"pikachu","cp":342}`)
	p, err := s.InsertPending(PendingItem{
		ChoreID:      "c1",
		User:         "Gavin",
		Tokens:       3,
		ThemePayload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^p[0-9a-f]{12}$`).MatchString(p.ID) {
		t.Fatalf("generated id %q not in db.js scheme", p.ID)
	}
	if p.Tokens != 3 || string(p.ThemePayload) != string(payload) {
		t.Fatalf("round-trip mismatch: %+v", p)
	}

	got, err := s.GetPending(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != p.ID {
		t.Fatalf("get returned wrong row: %+v", got)
	}

	// Second row, older created_at, to verify ASC ordering.
	if _, err := s.InsertPending(PendingItem{
		ChoreID:   "c2",
		User:      "Savannah",
		CreatedAt: "2020-01-01T00:00:00.000Z",
	}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListPending()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].User != "Savannah" {
		t.Fatalf("list should be oldest-first: %+v", list)
	}

	// Delete is idempotent.
	if err := s.DeletePending(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePending(p.ID); err != nil {
		t.Fatalf("delete of missing id should not error: %v", err)
	}
	if _, err := s.GetPending(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
}

func TestPendingNilPayloadStoresNull(t *testing.T) {
	s := openTestStore(t)
	p, err := s.InsertPending(PendingItem{ChoreID: "c1", User: "Dad"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ThemePayload != nil {
		t.Fatalf("nil payload should round-trip as nil, got %s", p.ThemePayload)
	}
}

// ---- events ------------------------------------------------------------------

func TestEvents(t *testing.T) {
	s := openTestStore(t)

	e1, err := s.InsertEvent(EventInput{
		Type:    "chore_completed",
		User:    "Dad",
		Payload: json.RawMessage(`{"choreId":"c1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if e1.ID[0] != 'e' || e1.RevertedAt != "" {
		t.Fatalf("bad event: %+v", e1)
	}

	// Nil payload becomes "{}".
	e2, err := s.InsertEvent(EventInput{Type: "tokens_earned", User: "Gavin"})
	if err != nil {
		t.Fatal(err)
	}
	if string(e2.Payload) != "{}" {
		t.Fatalf("nil payload should store {}, got %s", e2.Payload)
	}

	// Newest-first ordering. e2 inserted after e1; give it a later created_at
	// explicitly to make ordering deterministic regardless of clock resolution.
	if _, err := s.InsertEvent(EventInput{
		ID:        "emanual000001",
		Type:      "reward_redeemed",
		Payload:   json.RawMessage(`{}`),
		CreatedAt: "2099-01-01T00:00:00.000Z",
	}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListEvents(EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].ID != "emanual000001" {
		t.Fatalf("newest-first ordering broken: %+v", list)
	}

	// Type filter (OR set).
	filtered, err := s.ListEvents(EventFilter{Types: []string{"chore_completed", "tokens_earned"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("type filter: want 2, got %d", len(filtered))
	}

	// User filter + limit.
	byUser, err := s.ListEvents(EventFilter{User: "Dad", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(byUser) != 1 || byUser[0].User != "Dad" {
		t.Fatalf("user filter broken: %+v", byUser)
	}

	// markReverted is idempotent and hides the row by default.
	ok, err := s.MarkEventReverted(e1.ID)
	if err != nil || !ok {
		t.Fatalf("first revert should transition: ok=%v err=%v", ok, err)
	}
	ok, err = s.MarkEventReverted(e1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("second revert should be a no-op (idempotent)")
	}

	def, err := s.ListEvents(EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(def) != 2 {
		t.Fatalf("reverted row should be hidden by default, got %d", len(def))
	}
	all, err := s.ListEvents(EventFilter{IncludeReverted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("includeReverted should show all, got %d", len(all))
	}

	// Since filter.
	since, err := s.ListEvents(EventFilter{Since: "2098-01-01T00:00:00.000Z", IncludeReverted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(since) != 1 || since[0].ID != "emanual000001" {
		t.Fatalf("since filter broken: %+v", since)
	}

	// Get by id.
	if got, err := s.GetEvent(e1.ID); err != nil || got.ID != e1.ID {
		t.Fatalf("GetEvent: %+v err=%v", got, err)
	}
	if _, err := s.GetEvent("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// ---- theme KV ----------------------------------------------------------------

func TestThemeKV(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.ThemeGet("pokemon", "caught"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	if err := s.ThemeSet("pokemon", "caught", json.RawMessage(`["pikachu","eevee"]`)); err != nil {
		t.Fatal(err)
	}
	if err := s.ThemeSet("pokemon", "count", json.RawMessage(`2`)); err != nil {
		t.Fatal(err)
	}
	// Upsert overwrites.
	if err := s.ThemeSet("pokemon", "count", json.RawMessage(`3`)); err != nil {
		t.Fatal(err)
	}

	v, err := s.ThemeGet("pokemon", "count")
	if err != nil {
		t.Fatal(err)
	}
	if string(v) != "3" {
		t.Fatalf("upsert did not overwrite: %s", v)
	}

	all, err := s.ThemeGetAll("pokemon")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || string(all["caught"]) != `["pikachu","eevee"]` {
		t.Fatalf("get-all mismatch: %v", all)
	}

	// Invalid JSON is rejected.
	if err := s.ThemeSet("pokemon", "bad", json.RawMessage(`{not json`)); !errors.Is(err, ErrStorage) {
		t.Fatalf("invalid JSON should be ErrStorage, got %v", err)
	}
}

// TestCrossProcessRowsAreModuleCompatible documents and verifies the contract
// behind HOM-132: a row written by THIS Go package is indistinguishable from one
// the Node module would write. The schema is seeded from a verbatim copy of
// db.js's v1 DDL (the Node side is the schema owner); we insert via the Go
// helpers and read the raw columns back to confirm column names, the id scheme
// (prefix + 12 hex), and JSON-in-TEXT conventions all match what the module
// emits. Since we cannot import the Node module here, this is the cross-process
// fidelity check the ticket asks for.
func TestCrossProcessRowsAreModuleCompatible(t *testing.T) {
	path := newSchemaDB(t, SchemaVersion)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p, err := s.InsertPending(PendingItem{
		ChoreID:      "c9afofp",
		User:         "Gavin",
		Tokens:       3,
		ThemePayload: json.RawMessage(`{"pokemon":"snorlax"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Re-open the same file as a separate connection (simulating the "other
	// process") and read the raw row.
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	var id, choreID, user, themePayload, createdAt string
	var tokens int
	err = raw.QueryRow(
		"SELECT id, chore_id, user, tokens, theme_payload, created_at FROM pending_queue WHERE id = ?", p.ID,
	).Scan(&id, &choreID, &user, &tokens, &themePayload, &createdAt)
	if err != nil {
		t.Fatalf("second connection could not read the row: %v", err)
	}

	if !regexp.MustCompile(`^p[0-9a-f]{12}$`).MatchString(id) {
		t.Errorf("id %q does not match db.js newId('p') scheme", id)
	}
	if choreID != "c9afofp" || user != "Gavin" || tokens != 3 {
		t.Errorf("column round-trip mismatch: %s/%s/%d", choreID, user, tokens)
	}
	if themePayload != `{"pokemon":"snorlax"}` {
		t.Errorf("theme_payload should be stored as JSON text, got %q", themePayload)
	}
	// created_at must be an ISO-8601 millisecond UTC string like db.js nowIso().
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`).MatchString(createdAt) {
		t.Errorf("created_at %q is not in db.js toISOString() format", createdAt)
	}
}
