package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// QueryResult is the outcome of a RawQuery: the selected column names and up
// to maxRows result rows. Each cell holds the raw driver value (int64,
// float64, string, []byte, or nil). Truncated is true when the result set had
// more rows than maxRows, so callers can tell the user the view is clipped.
type QueryResult struct {
	Columns   []string
	Rows      [][]any
	Truncated bool
}

// readonlyDB lazily opens (and caches) a second handle to the same database
// file with PRAGMA query_only(true). That handle is opened read-write at the
// OS level — so it reads the WAL/SHM normally — but SQLite rejects any
// statement that would modify the database ("attempt to write a readonly
// database"). This keeps the SQL tab from ever corrupting the message cache,
// regardless of what the user types, without parsing their SQL.
func (s *Store) readonlyDB() (*sql.DB, error) {
	s.roMu.Lock()
	defer s.roMu.Unlock()
	if s.roDB != nil {
		return s.roDB, nil
	}
	if s.path == "" {
		return nil, errors.New("store has no path")
	}
	dsn := "file:" + s.path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=query_only(true)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open read-only: %w", err)
	}
	// A handful of connections is plenty; the SQL tab issues one query at a time.
	db.SetMaxOpenConns(2)
	s.roDB = db
	return db, nil
}

// writeVerbs are the leading keywords we reject up front with a friendly
// message. The query_only handle is the real safety net (it also stops
// WITH … DELETE and the like); this is purely so the common "DELETE FROM …"
// mistake gets a clear explanation instead of a raw SQLite error.
var writeVerbs = map[string]bool{
	"insert": true, "update": true, "delete": true, "replace": true,
	"drop": true, "alter": true, "create": true, "truncate": true,
	"attach": true, "detach": true, "vacuum": true, "reindex": true,
}

// RawQuery runs an arbitrary read-only query against the message cache and
// returns its columns and up to maxRows rows. It executes on a query_only
// handle, so writes and DDL fail rather than touching the database. The query
// runs under ctx, so a caller-imposed timeout (or app shutdown) interrupts a
// runaway scan. Intended for the SQL tab — a power-user feature.
func (s *Store) RawQuery(ctx context.Context, query string, maxRows int) (*QueryResult, error) {
	if s == nil {
		return nil, errors.New("store unavailable")
	}
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil, errors.New("empty query")
	}
	if verb := strings.ToLower(strings.TrimFunc(firstWord(trimmed), isPunct)); writeVerbs[verb] {
		return nil, fmt.Errorf("%s is a write — the SQL tab is read-only (SELECT/EXPLAIN/PRAGMA/WITH)", strings.ToUpper(verb))
	}

	db, err := s.readonlyDB()
	if err != nil {
		return nil, err
	}
	rs, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rs.Close()

	cols, err := rs.Columns()
	if err != nil {
		return nil, err
	}
	res := &QueryResult{Columns: cols}
	for rs.Next() {
		if maxRows > 0 && len(res.Rows) >= maxRows {
			res.Truncated = true
			break
		}
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return res, err
		}
		res.Rows = append(res.Rows, cells)
	}
	if err := rs.Err(); err != nil {
		return res, err
	}
	return res, nil
}

// firstWord returns the first whitespace-delimited token of s.
func firstWord(s string) string {
	if i := strings.IndexFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }); i >= 0 {
		return s[:i]
	}
	return s
}

func isPunct(r rune) bool {
	return r == '(' || r == ';' || r == ',' || r == '`' || r == '"' || r == '\''
}
