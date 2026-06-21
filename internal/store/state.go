package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// GetState reads a value from the rule_state ledger. ok is false when the key
// is absent.
func (s *Store) GetState(key string) (value string, ok bool, err error) {
	if s == nil {
		return "", false, nil
	}
	err = s.db.QueryRow(`SELECT value FROM rule_state WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get state %s: %w", key, err)
	}
	return value, true, nil
}

// SetState upserts a value into the rule_state ledger.
func (s *Store) SetState(key, value string) error {
	if s == nil {
		return nil
	}
	if _, err := s.db.Exec(
		`INSERT INTO rule_state(key, value, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UnixMilli(),
	); err != nil {
		return fmt.Errorf("set state %s: %w", key, err)
	}
	return nil
}

// IncrState atomically adds delta to the integer value of key (treating a
// missing or non-numeric value as 0 via SQLite's CAST) and returns the new
// value. delta may be negative to decrement. The arithmetic happens inside a
// single UPSERT so concurrent increments from different posts can't lose a
// write.
func (s *Store) IncrState(key string, delta int64) (int64, error) {
	if s == nil {
		return 0, nil
	}
	var n int64
	err := s.db.QueryRow(
		`INSERT INTO rule_state(key, value, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
		     value = CAST(CAST(value AS INTEGER) + ? AS TEXT),
		     updated_at = excluded.updated_at
		 RETURNING CAST(value AS INTEGER)`,
		key, strconv.FormatInt(delta, 10), time.Now().UnixMilli(), delta,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("incr state %s: %w", key, err)
	}
	return n, nil
}

// DeleteState removes a key from the rule_state ledger. Deleting an absent key
// is not an error.
func (s *Store) DeleteState(key string) error {
	if s == nil {
		return nil
	}
	if _, err := s.db.Exec(`DELETE FROM rule_state WHERE key = ?`, key); err != nil {
		return fmt.Errorf("delete state %s: %w", key, err)
	}
	return nil
}

// AllState returns the entire rule_state ledger as a map, so a rule action can
// expose every stored value to a template or a child process in one read. The
// table is small (it holds only what rules write), so a full scan is cheap.
func (s *Store) AllState() (map[string]string, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT key, value FROM rule_state`)
	if err != nil {
		return nil, fmt.Errorf("all state: %w", err)
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan state: %w", err)
		}
		m[k] = v
	}
	return m, rows.Err()
}
