package choresdb

import (
	"database/sql"
	"errors"
	"fmt"
)

// MetaGet returns the raw value stored under key in the module's meta table,
// or ErrNotFound when absent. Read-only: the module owns every meta key (e.g.
// "schedule_activations", the per-(chore,user) window-open timestamps that
// node_helper.js writes for morning/nightly and calendar schedules).
func (s *Store) MetaGet(key string) (string, error) {
	var value sql.NullString
	err := s.db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("%w: meta get: %w", ErrStorage, err)
	}
	return value.String, nil
}
