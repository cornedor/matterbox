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
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// defaultRecencyHalfLife is the age at which a search match's relevance weight
// is halved when no override is configured. See rankByRelevanceAndAge; 90 days
// keeps recent discussion on top while still surfacing a strong older match.
const defaultRecencyHalfLife = 90 * 24 * time.Hour

// Store wraps the message database. Methods are safe for concurrent use
// by multiple goroutines because database/sql serialises access.
type Store struct {
	db   *sql.DB
	path string // the database file, kept so RawQuery can open a read-only handle
	// recencyHalfLife tunes how fast ranked search results decay with age.
	recencyHalfLife time.Duration

	// roMu guards lazy creation of roDB, a separate read-only (query_only)
	// handle used by RawQuery so a power-user query on the SQL tab can never
	// mutate the message cache. See query.go.
	roMu sync.Mutex
	roDB *sql.DB
}

// Option configures a Store at Open time.
type Option func(*Store)

// WithRecencyHalfLife sets the age half-life used to down-weight older matches
// in ranked search (the Search tab and AI search). A non-positive value falls
// back to defaultRecencyHalfLife.
func WithRecencyHalfLife(d time.Duration) Option {
	return func(s *Store) {
		if d > 0 {
			s.recencyHalfLife = d
		}
	}
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
func Open(path string, opts ...Option) (*Store, error) {
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
	s := &Store{db: db, path: path, recencyHalfLife: defaultRecencyHalfLife}
	for _, opt := range opts {
		opt(s)
	}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle (and the lazily-opened read-only handle).
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.roMu.Lock()
	if s.roDB != nil {
		s.roDB.Close()
		s.roDB = nil
	}
	s.roMu.Unlock()
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}
