package store

// schemaSQL is run once per Open. Every statement is idempotent so a
// migration is just "execute the latest schema" — no version table
// needed for the current shape.
const schemaSQL = `
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

CREATE VIRTUAL TABLE IF NOT EXISTS posts_fts USING fts5(
    message,
    content='posts',
    content_rowid='rowid',
    tokenize='unicode61 remove_diacritics 2'
);

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

func (s *Store) migrate() error {
	_, err := s.db.Exec(schemaSQL)
	return err
}
