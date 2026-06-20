package choresdb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ---- completions ------------------------------------------------------------

// Completion is one row of the completions table: per-(chore,user) status.
// Status is one of "open" | "pending" | "done" (db.js semantics).
type Completion struct {
	ChoreID     string `json:"choreId"`
	User        string `json:"user"`
	Status      string `json:"status"`
	CompletedAt string `json:"completedAt,omitempty"` // empty when status == open
	UpdatedAt   string `json:"updatedAt"`
}

func scanCompletion(row interface{ Scan(...any) error }) (*Completion, error) {
	var c Completion
	var completedAt sql.NullString
	if err := row.Scan(&c.ChoreID, &c.User, &c.Status, &completedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if completedAt.Valid {
		c.CompletedAt = completedAt.String
	}
	return &c, nil
}

// GetCompletion returns the (chore,user) row, or ErrNotFound if absent.
func (s *Store) GetCompletion(choreID, user string) (*Completion, error) {
	row := s.db.QueryRow(
		"SELECT chore_id, user, status, completed_at, updated_at FROM completions WHERE chore_id = ? AND user = ?",
		choreID, user,
	)
	c, err := scanCompletion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: get completion: %w", ErrStorage, err)
	}
	return c, nil
}

// ListCompletions returns every completions row (unordered, matching db.js).
func (s *Store) ListCompletions() ([]Completion, error) {
	rows, err := s.db.Query("SELECT chore_id, user, status, completed_at, updated_at FROM completions")
	if err != nil {
		return nil, fmt.Errorf("%w: list completions: %w", ErrStorage, err)
	}
	defer rows.Close()

	var out []Completion
	for rows.Next() {
		c, err := scanCompletion(rows)
		if err != nil {
			return nil, fmt.Errorf("%w: scan completion: %w", ErrStorage, err)
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: list completions: %w", ErrStorage, err)
	}
	return out, nil
}

// UpsertCompletion sets the (chore,user) status, matching db.js setCompletion:
// completedAt is stamped (when not supplied) for pending/done and cleared for
// open; updated_at is always stamped now. Returns the resulting row.
func (s *Store) UpsertCompletion(choreID, user, status, completedAt string) (*Completion, error) {
	if err := s.requireWritable(); err != nil {
		return nil, err
	}
	var at any
	if status == "open" {
		at = nil
	} else if completedAt != "" {
		at = completedAt
	} else {
		at = nowISO()
	}
	updatedAt := nowISO()

	_, err := s.db.Exec(`
		INSERT INTO completions (chore_id, user, status, completed_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(chore_id, user) DO UPDATE SET
			status = excluded.status,
			completed_at = excluded.completed_at,
			updated_at = excluded.updated_at`,
		choreID, user, status, at, updatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: upsert completion: %w", ErrStorage, err)
	}
	return s.GetCompletion(choreID, user)
}

// ---- pending queue ----------------------------------------------------------

// PendingItem is a verification-queue row awaiting parent approval. ThemePayload
// is the decoded theme_payload JSON (nil when the column is NULL).
type PendingItem struct {
	ID           string          `json:"id"`
	ChoreID      string          `json:"choreId"`
	User         string          `json:"user"`
	Tokens       int             `json:"tokens"`
	ThemePayload json.RawMessage `json:"themePayload,omitempty"`
	CreatedAt    string          `json:"createdAt"`
}

func scanPending(row interface{ Scan(...any) error }) (*PendingItem, error) {
	var p PendingItem
	var payload sql.NullString
	if err := row.Scan(&p.ID, &p.ChoreID, &p.User, &p.Tokens, &payload, &p.CreatedAt); err != nil {
		return nil, err
	}
	if payload.Valid && payload.String != "" {
		p.ThemePayload = json.RawMessage(payload.String)
	}
	return &p, nil
}

// InsertPending appends a pending-queue row, generating a "p"+12hex id when the
// item has none (matching db.js enqueuePending). themePayload may be nil. The
// inserted row is read back and returned.
func (s *Store) InsertPending(item PendingItem) (*PendingItem, error) {
	if err := s.requireWritable(); err != nil {
		return nil, err
	}
	id := item.ID
	if id == "" {
		id = newID("p")
	}
	createdAt := item.CreatedAt
	if createdAt == "" {
		createdAt = nowISO()
	}
	var payload any
	if len(item.ThemePayload) > 0 {
		payload = string(item.ThemePayload)
	} else {
		payload = nil
	}

	_, err := s.db.Exec(`
		INSERT INTO pending_queue (id, chore_id, user, tokens, theme_payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id, item.ChoreID, item.User, item.Tokens, payload, createdAt,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: insert pending: %w", ErrStorage, err)
	}
	return s.GetPending(id)
}

// GetPending returns a pending-queue row by id, or ErrNotFound.
func (s *Store) GetPending(id string) (*PendingItem, error) {
	row := s.db.QueryRow(
		"SELECT id, chore_id, user, tokens, theme_payload, created_at FROM pending_queue WHERE id = ?", id,
	)
	p, err := scanPending(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: get pending: %w", ErrStorage, err)
	}
	return p, nil
}

// ListPending returns the queue oldest-first (created_at ASC), matching db.js.
func (s *Store) ListPending() ([]PendingItem, error) {
	rows, err := s.db.Query(
		"SELECT id, chore_id, user, tokens, theme_payload, created_at FROM pending_queue ORDER BY created_at ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("%w: list pending: %w", ErrStorage, err)
	}
	defer rows.Close()

	var out []PendingItem
	for rows.Next() {
		p, err := scanPending(rows)
		if err != nil {
			return nil, fmt.Errorf("%w: scan pending: %w", ErrStorage, err)
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: list pending: %w", ErrStorage, err)
	}
	return out, nil
}

// DeletePending removes a pending-queue row. Deleting a missing id is a no-op
// (matching db.js removePending, which never errors on absence).
func (s *Store) DeletePending(id string) error {
	if err := s.requireWritable(); err != nil {
		return err
	}
	if _, err := s.db.Exec("DELETE FROM pending_queue WHERE id = ?", id); err != nil {
		return fmt.Errorf("%w: delete pending: %w", ErrStorage, err)
	}
	return nil
}

// ---- events / action log ----------------------------------------------------

// Event is one revertible action-log row. Payload is the decoded payload JSON.
// RevertedAt is empty unless the event has been undone.
type Event struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	User       string          `json:"user,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	CreatedAt  string          `json:"createdAt"`
	RevertedAt string          `json:"revertedAt,omitempty"`
}

func scanEvent(row interface{ Scan(...any) error }) (*Event, error) {
	var e Event
	var user, payload, revertedAt sql.NullString
	if err := row.Scan(&e.ID, &e.Type, &user, &payload, &e.CreatedAt, &revertedAt); err != nil {
		return nil, err
	}
	if user.Valid {
		e.User = user.String
	}
	if payload.Valid {
		e.Payload = json.RawMessage(payload.String)
	}
	if revertedAt.Valid {
		e.RevertedAt = revertedAt.String
	}
	return &e, nil
}

// EventInput is the caller-supplied shape for InsertEvent. ID/CreatedAt are
// generated when empty. Payload may be nil (stored as "{}", matching db.js).
type EventInput struct {
	ID        string
	Type      string
	User      string
	Payload   json.RawMessage
	CreatedAt string
}

// InsertEvent appends an event, generating an "e"+12hex id when empty (matching
// db.js logEvent). A nil/empty payload is stored as the JSON object "{}".
func (s *Store) InsertEvent(in EventInput) (*Event, error) {
	if err := s.requireWritable(); err != nil {
		return nil, err
	}
	id := in.ID
	if id == "" {
		id = newID("e")
	}
	createdAt := in.CreatedAt
	if createdAt == "" {
		createdAt = nowISO()
	}
	payload := "{}"
	if len(in.Payload) > 0 {
		payload = string(in.Payload)
	}
	var user any
	if in.User != "" {
		user = in.User
	} else {
		user = nil
	}

	_, err := s.db.Exec(`
		INSERT INTO events (id, type, user, payload, created_at, reverted_at)
		VALUES (?, ?, ?, ?, ?, NULL)`,
		id, in.Type, user, payload, createdAt,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: insert event: %w", ErrStorage, err)
	}
	return s.GetEvent(id)
}

// GetEvent returns an event by id, or ErrNotFound.
func (s *Store) GetEvent(id string) (*Event, error) {
	row := s.db.QueryRow(
		"SELECT id, type, user, payload, created_at, reverted_at FROM events WHERE id = ?", id,
	)
	e, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: get event: %w", ErrStorage, err)
	}
	return e, nil
}

// EventFilter mirrors db.js listEvents's filter object. Zero-value fields are
// omitted from the query. Types is an OR set. IncludeReverted defaults false
// (only un-reverted rows). Limit <= 0 means no limit.
type EventFilter struct {
	Types           []string
	User            string
	Since           string // created_at >= Since
	IncludeReverted bool
	Limit           int
}

// ListEvents returns events newest-first (created_at DESC), matching db.js.
func (s *Store) ListEvents(f EventFilter) ([]Event, error) {
	var where []string
	var args []any

	if len(f.Types) > 0 {
		placeholders := make([]string, len(f.Types))
		for i, t := range f.Types {
			placeholders[i] = "?"
			args = append(args, t)
		}
		where = append(where, "type IN ("+strings.Join(placeholders, ", ")+")")
	}
	if f.User != "" {
		where = append(where, "user = ?")
		args = append(args, f.User)
	}
	if f.Since != "" {
		where = append(where, "created_at >= ?")
		args = append(args, f.Since)
	}
	if !f.IncludeReverted {
		where = append(where, "reverted_at IS NULL")
	}

	sqlStr := "SELECT id, type, user, payload, created_at, reverted_at FROM events"
	if len(where) > 0 {
		sqlStr += " WHERE " + strings.Join(where, " AND ")
	}
	sqlStr += " ORDER BY created_at DESC"
	if f.Limit > 0 {
		sqlStr += fmt.Sprintf(" LIMIT %d", f.Limit)
	}

	rows, err := s.db.Query(sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: list events: %w", ErrStorage, err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("%w: scan event: %w", ErrStorage, err)
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: list events: %w", ErrStorage, err)
	}
	return out, nil
}

// MarkEventReverted stamps reverted_at if not already set (idempotent, matching
// db.js markEventReverted). Returns true if the row transitioned to reverted.
func (s *Store) MarkEventReverted(id string) (bool, error) {
	if err := s.requireWritable(); err != nil {
		return false, err
	}
	res, err := s.db.Exec(
		"UPDATE events SET reverted_at = ? WHERE id = ? AND reverted_at IS NULL", nowISO(), id,
	)
	if err != nil {
		return false, fmt.Errorf("%w: mark event reverted: %w", ErrStorage, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%w: mark event reverted: %w", ErrStorage, err)
	}
	return n > 0, nil
}

// ---- theme KV ---------------------------------------------------------------

// ThemeGet returns the decoded JSON value for (themeId,key), or ErrNotFound when
// the key is absent. The value is returned as raw JSON; the theme owns its shape.
func (s *Store) ThemeGet(themeID, key string) (json.RawMessage, error) {
	var value string
	err := s.db.QueryRow(
		"SELECT value FROM theme_kv WHERE theme_id = ? AND key = ?", themeID, key,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: theme get: %w", ErrStorage, err)
	}
	return json.RawMessage(value), nil
}

// ThemeGetAll returns every key for a theme as decoded JSON values, keyed by the
// KV key (matching db.js themeGetAll).
func (s *Store) ThemeGetAll(themeID string) (map[string]json.RawMessage, error) {
	rows, err := s.db.Query("SELECT key, value FROM theme_kv WHERE theme_id = ?", themeID)
	if err != nil {
		return nil, fmt.Errorf("%w: theme get all: %w", ErrStorage, err)
	}
	defer rows.Close()

	out := make(map[string]json.RawMessage)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("%w: scan theme kv: %w", ErrStorage, err)
		}
		out[k] = json.RawMessage(v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: theme get all: %w", ErrStorage, err)
	}
	return out, nil
}

// ThemeSet stores a JSON value for (themeId,key), upserting (matching db.js
// themeSet). value must be valid JSON (the theme's own encoding).
func (s *Store) ThemeSet(themeID, key string, value json.RawMessage) error {
	if err := s.requireWritable(); err != nil {
		return err
	}
	if !json.Valid(value) {
		return fmt.Errorf("%w: theme set: value is not valid JSON", ErrStorage)
	}
	_, err := s.db.Exec(`
		INSERT INTO theme_kv (theme_id, key, value) VALUES (?, ?, ?)
		ON CONFLICT(theme_id, key) DO UPDATE SET value = excluded.value`,
		themeID, key, string(value),
	)
	if err != nil {
		return fmt.Errorf("%w: theme set: %w", ErrStorage, err)
	}
	return nil
}
