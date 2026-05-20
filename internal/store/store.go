// Package store owns the on-disk SQLite database that backs matterbox's
// persistent message cache. Two callers share it: the UI, which reads
// the most recent N posts per channel to repaint on reopen and writes
// every post that arrives via API or WebSocket, and (later) a local
// search command that queries the FTS5 index.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Store wraps the message database. Methods are safe for concurrent use
// by multiple goroutines because database/sql serialises access.
type Store struct {
	db *sql.DB
}

// DefaultPath returns ~/.config/matterbox/messages.db, mirroring the
// path convention used by channel_stats.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "matterbox", "messages.db"), nil
}

// Open opens (and creates if needed) the SQLite database at path and
// runs idempotent migrations. WAL keeps readers and writers from
// blocking each other; busy_timeout absorbs transient contention.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	// One writer is enough; SQLite serialises writes anyway, and capping
	// connections avoids "database is locked" surprises.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
