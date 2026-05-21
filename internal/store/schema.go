package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ftsTokenizer is the FTS5 tokenizer for the message index. The leading
// `porter` wraps `unicode61` with the Porter stemmer so morphological variants
// collapse to a shared root in BOTH directions ("deployment" finds "deployed",
// "running" finds "run") — something the forward-only prefix matching in
// ftsQuery/ftsTerm could not do on its own. It is the single source of truth:
// schemaSQL bakes it into new databases and ensureFTSTokenizer rebuilds older
// ones when it changes.
const ftsTokenizer = "porter unicode61 remove_diacritics 2"

// ftsCreateSQL creates the FTS5 index over posts.message. Kept separate from
// schemaSQL (and reused verbatim by ensureFTSTokenizer) so the tokenizer is
// defined in exactly one place.
const ftsCreateSQL = `
CREATE VIRTUAL TABLE IF NOT EXISTS posts_fts USING fts5(
    message,
    content='posts',
    content_rowid='rowid',
    tokenize='` + ftsTokenizer + `'
);
`

// postsSchemaSQL is the base table the FTS index and triggers depend on, so it
// must run before ftsCreateSQL and triggersSQL.
const postsSchemaSQL = `
CREATE TABLE IF NOT EXISTS posts (
    id          TEXT PRIMARY KEY,
    channel_id  TEXT NOT NULL,
    user_id     TEXT NOT NULL DEFAULT '',
    root_id     TEXT NOT NULL DEFAULT '',
    create_at   INTEGER NOT NULL,
    update_at   INTEGER NOT NULL DEFAULT 0,
    edit_at     INTEGER NOT NULL DEFAULT 0,
    delete_at   INTEGER NOT NULL DEFAULT 0,
    message     TEXT NOT NULL DEFAULT '',
    raw_json    BLOB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_posts_channel_createat ON posts(channel_id, create_at);
CREATE INDEX IF NOT EXISTS idx_posts_root              ON posts(root_id);
`

// triggersSQL keeps posts_fts in sync with posts and archives prior versions of
// edited posts. Runs after posts_fts exists.
const triggersSQL = `
CREATE TRIGGER IF NOT EXISTS posts_ai AFTER INSERT ON posts BEGIN
    INSERT INTO posts_fts(rowid, message) VALUES (new.rowid, new.message);
END;

CREATE TRIGGER IF NOT EXISTS posts_ad AFTER DELETE ON posts BEGIN
    INSERT INTO posts_fts(posts_fts, rowid, message) VALUES('delete', old.rowid, old.message);
END;

CREATE TRIGGER IF NOT EXISTS posts_au AFTER UPDATE ON posts BEGIN
    INSERT INTO posts_fts(posts_fts, rowid, message) VALUES('delete', old.rowid, old.message);
    INSERT INTO posts_fts(rowid, message) VALUES (new.rowid, new.message);
END;

-- Archive of prior versions of edited posts. Mattermost's API doesn't
-- expose edit history, so this table only contains versions matterbox
-- itself observed (either via WS PostEdited events or by upserting a
-- post whose edit_at advanced between fetches).
CREATE TABLE IF NOT EXISTS post_revisions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id     TEXT NOT NULL,
    channel_id  TEXT NOT NULL,
    user_id     TEXT NOT NULL DEFAULT '',
    edit_at     INTEGER NOT NULL,    -- EditAt of the archived version
    update_at   INTEGER NOT NULL,
    captured_at INTEGER NOT NULL,    -- unix-ms when matterbox archived this
    message     TEXT NOT NULL,
    raw_json    BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_revisions_post ON post_revisions(post_id, edit_at);

-- Capture the OLD row whenever a posts UPDATE actually changes message
-- or edit_at. Re-persisting the same content (e.g. fileInfo metadata
-- fill-in, or a refetch of an unchanged post) won't trigger because
-- both columns are unchanged.
CREATE TRIGGER IF NOT EXISTS posts_capture_revision
AFTER UPDATE ON posts
WHEN new.message != old.message OR new.edit_at != old.edit_at
BEGIN
    INSERT INTO post_revisions
        (post_id, channel_id, user_id, edit_at, update_at, captured_at, message, raw_json)
    VALUES
        (old.id, old.channel_id, old.user_id, old.edit_at, old.update_at,
         CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER),
         old.message, old.raw_json);
END;
`

// schemaSQL is run once per Open. Every statement is idempotent so a
// migration is just "execute the latest schema" — no version table
// needed for the current shape. Order matters: posts → posts_fts → triggers.
const schemaSQL = postsSchemaSQL + ftsCreateSQL + triggersSQL

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return err
	}
	return s.ensureFTSTokenizer()
}

// ensureFTSTokenizer rebuilds posts_fts when its on-disk tokenizer no longer
// matches ftsTokenizer. `CREATE VIRTUAL TABLE IF NOT EXISTS` is a no-op on an
// existing index, so changing the tokenizer (e.g. adding the Porter stemmer)
// doesn't re-tokenize old content on its own — the index has to be dropped and
// rebuilt from the content table. Detected by substring on the stored DDL, so
// it runs exactly once per tokenizer change and is a no-op afterward.
func (s *Store) ensureFTSTokenizer() error {
	var ddl string
	err := s.db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='posts_fts'`,
	).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // not created yet (migrate just ran schemaSQL with the new one)
	}
	if err != nil {
		return fmt.Errorf("inspect posts_fts: %w", err)
	}
	if strings.Contains(ddl, ftsTokenizer) {
		return nil // already on the desired tokenizer
	}
	// 'rebuild' repopulates the index from the content table (posts.message)
	// using the new tokenizer; the sync triggers reference posts_fts by name
	// and keep working across the drop/recreate.
	for _, q := range []string{
		`DROP TABLE posts_fts;`,
		ftsCreateSQL,
		`INSERT INTO posts_fts(posts_fts) VALUES('rebuild');`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("rebuild posts_fts: %w", err)
		}
	}
	return nil
}
