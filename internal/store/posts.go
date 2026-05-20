package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

// SearchHit is one row of a Search result: the matched post plus a
// short window of surrounding posts from the same channel (oldest →
// newest). Before/After never include the match itself.
type SearchHit struct {
	Match  *model.Post
	Before []*model.Post
	After  []*model.Post
}

// ftsQuery turns a free-form user query into an FTS5 expression. Each
// whitespace-separated token becomes a prefix-matched quoted term, so
// `hello wor` matches `hello world`. Embedded double quotes in user
// input are escaped per FTS5 string rules. Returns "" for empty input.
func ftsQuery(input string) string {
	fields := strings.Fields(strings.TrimSpace(input))
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ReplaceAll(f, `"`, `""`)
		parts = append(parts, `"`+f+`"*`)
	}
	return strings.Join(parts, " ")
}

// Search runs an FTS5 query against the persisted message corpus and
// returns up to limit hits, most-recent first. For each hit, contextN
// posts before and after (in the same channel, ordered oldest→newest)
// are included so the caller can render the match in context. Returns
// nil with no error for an empty or all-whitespace query.
//
// channelIDs is an optional scope: nil = search everywhere; non-nil
// empty slice = "filter active but resolved to no channels" = no hits;
// non-empty = restrict to channel_id IN (...). The caller (UI layer)
// uses this to implement team:/in: modifiers, which it resolves against
// its local channel metadata before issuing the query.
func (s *Store) Search(query string, channelIDs []string, limit, contextN int) ([]SearchHit, error) {
	if s == nil || limit <= 0 {
		return nil, nil
	}
	fts := ftsQuery(query)
	if fts == "" {
		return nil, nil
	}
	if channelIDs != nil && len(channelIDs) == 0 {
		// Scope resolved to zero channels — short-circuit before SQLite.
		return nil, nil
	}
	var q string
	args := []any{fts}
	if len(channelIDs) > 0 {
		placeholders := strings.Repeat("?,", len(channelIDs))
		placeholders = placeholders[:len(placeholders)-1]
		q = `
SELECT p.raw_json
FROM posts_fts f
JOIN posts p ON p.rowid = f.rowid
WHERE posts_fts MATCH ?
  AND p.delete_at = 0
  AND p.channel_id IN (` + placeholders + `)
ORDER BY p.create_at DESC
LIMIT ?`
		for _, id := range channelIDs {
			args = append(args, id)
		}
	} else {
		q = `
SELECT p.raw_json
FROM posts_fts f
JOIN posts p ON p.rowid = f.rowid
WHERE posts_fts MATCH ?
  AND p.delete_at = 0
ORDER BY p.create_at DESC
LIMIT ?`
	}
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query search: %w", err)
	}
	defer rows.Close()
	var matches []*model.Post
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan search: %w", err)
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		matches = append(matches, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	out := make([]SearchHit, 0, len(matches))
	for _, mp := range matches {
		before, after, _ := s.contextWindow(mp.ChannelId, mp.CreateAt, mp.Id, contextN)
		out = append(out, SearchHit{Match: mp, Before: before, After: after})
	}
	return out, nil
}

// contextWindow returns the limit newest posts older than the pivot
// (oldest→newest) and the limit oldest posts newer than the pivot
// (oldest→newest), both in the same channel. It's a one-statement
// equivalent of two contextPosts calls, halving the per-search-hit
// statement count — which is the hot path during interactive search.
func (s *Store) contextWindow(channelID string, createAt int64, postID string, limit int) (before, after []*model.Post, err error) {
	if s == nil || limit <= 0 || channelID == "" {
		return nil, nil, nil
	}
	const q = `
SELECT raw_json, side FROM (
    SELECT raw_json, create_at, id, 'B' AS side
    FROM posts
    WHERE channel_id = ? AND delete_at = 0 AND id != ?
      AND (create_at < ? OR (create_at = ? AND id < ?))
    ORDER BY create_at DESC, id DESC
    LIMIT ?
)
UNION ALL
SELECT raw_json, side FROM (
    SELECT raw_json, create_at, id, 'A' AS side
    FROM posts
    WHERE channel_id = ? AND delete_at = 0 AND id != ?
      AND (create_at > ? OR (create_at = ? AND id > ?))
    ORDER BY create_at ASC, id ASC
    LIMIT ?
)`
	rows, err := s.db.Query(q,
		channelID, postID, createAt, createAt, postID, limit,
		channelID, postID, createAt, createAt, postID, limit,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("query context window: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var side string
		if err := rows.Scan(&raw, &side); err != nil {
			continue
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		if side == "B" {
			before = append(before, &p)
		} else {
			after = append(after, &p)
		}
	}
	// The Before subquery is sorted newest→oldest so SQLite can apply
	// LIMIT against the index in DESC order; reverse here so the caller
	// sees oldest→newest, matching contextPosts(before=true) semantics.
	for i, j := 0, len(before)-1; i < j; i, j = i+1, j-1 {
		before[i], before[j] = before[j], before[i]
	}
	return before, after, nil
}

// PostsAround returns up to beforeN posts older than pivotPostID plus
// the pivot itself plus up to afterN posts newer, all from the same
// channel, ordered oldest→newest. Returns nil if the pivot post isn't
// in the cache (so the caller can fall back to a normal channel open).
func (s *Store) PostsAround(channelID, pivotPostID string, beforeN, afterN int) ([]*model.Post, error) {
	if s == nil || channelID == "" || pivotPostID == "" {
		return nil, nil
	}
	pivot, err := s.lookupPost(pivotPostID)
	if err != nil {
		return nil, err
	}
	if pivot == nil {
		return nil, nil
	}
	before, err := s.contextPosts(channelID, pivot.CreateAt, pivot.Id, beforeN, true)
	if err != nil {
		return nil, err
	}
	after, err := s.contextPosts(channelID, pivot.CreateAt, pivot.Id, afterN, false)
	if err != nil {
		return nil, err
	}
	out := make([]*model.Post, 0, len(before)+1+len(after))
	out = append(out, before...)
	out = append(out, pivot)
	out = append(out, after...)
	return out, nil
}

// lookupPost fetches a single post by Id. Returns (nil, nil) when the
// id isn't in the cache.
func (s *Store) lookupPost(id string) (*model.Post, error) {
	if s == nil || id == "" {
		return nil, nil
	}
	var raw []byte
	err := s.db.QueryRow(`SELECT raw_json FROM posts WHERE id = ?`, id).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup post: %w", err)
	}
	var p model.Post
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("unmarshal post: %w", err)
	}
	return &p, nil
}

// contextPosts returns up to limit posts adjacent to (channelID, createAt,
// postID) in the same channel, ordered oldest→newest. When before==true
// the window is the limit newest posts strictly older than the pivot;
// when before==false it's the limit oldest posts strictly newer than the
// pivot. The (create_at, id) tuple breaks ties when two posts share the
// same create_at.
func (s *Store) contextPosts(channelID string, createAt int64, postID string, limit int, before bool) ([]*model.Post, error) {
	if s == nil || limit <= 0 || channelID == "" {
		return nil, nil
	}
	var q string
	if before {
		q = `
SELECT raw_json FROM (
    SELECT raw_json, create_at, id
    FROM posts
    WHERE channel_id = ? AND delete_at = 0 AND id != ?
      AND (create_at < ? OR (create_at = ? AND id < ?))
    ORDER BY create_at DESC, id DESC
    LIMIT ?
) ORDER BY create_at ASC, id ASC`
	} else {
		q = `
SELECT raw_json
FROM posts
WHERE channel_id = ? AND delete_at = 0 AND id != ?
  AND (create_at > ? OR (create_at = ? AND id > ?))
ORDER BY create_at ASC, id ASC
LIMIT ?`
	}
	rows, err := s.db.Query(q, channelID, postID, createAt, createAt, postID, limit)
	if err != nil {
		return nil, fmt.Errorf("query context: %w", err)
	}
	defer rows.Close()
	var out []*model.Post
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		out = append(out, &p)
	}
	return out, nil
}

const upsertSQL = `
INSERT INTO posts (id, channel_id, user_id, root_id, create_at, update_at, edit_at, delete_at, message, raw_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    channel_id = excluded.channel_id,
    user_id    = excluded.user_id,
    root_id    = excluded.root_id,
    create_at  = excluded.create_at,
    update_at  = excluded.update_at,
    edit_at    = excluded.edit_at,
    delete_at  = excluded.delete_at,
    message    = excluded.message,
    raw_json   = excluded.raw_json
`

// Upsert inserts or updates a single post by Id. Posts with an empty Id
// (i.e. optimistic UI stubs) are silently skipped.
func (s *Store) Upsert(p *model.Post) error {
	if s == nil || p == nil || p.Id == "" {
		return nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal post: %w", err)
	}
	_, err = s.db.Exec(upsertSQL,
		p.Id, p.ChannelId, p.UserId, p.RootId,
		p.CreateAt, p.UpdateAt, p.EditAt, p.DeleteAt,
		p.Message, raw,
	)
	if err != nil {
		return fmt.Errorf("upsert post: %w", err)
	}
	return nil
}

// UpsertMany runs every Upsert inside a single transaction. Posts with
// empty Ids are skipped (see Upsert).
func (s *Store) UpsertMany(posts []*model.Post) error {
	if s == nil || len(posts) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	stmt, err := tx.Prepare(upsertSQL)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()
	for _, p := range posts {
		if p == nil || p.Id == "" {
			continue
		}
		raw, err := json.Marshal(p)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("marshal post %s: %w", p.Id, err)
		}
		if _, err := stmt.Exec(
			p.Id, p.ChannelId, p.UserId, p.RootId,
			p.CreateAt, p.UpdateAt, p.EditAt, p.DeleteAt,
			p.Message, raw,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("upsert post %s: %w", p.Id, err)
		}
	}
	return tx.Commit()
}

// Delete removes a post by Id. The FTS trigger drops the matching
// shadow row. Deleting a non-existent Id is a no-op.
func (s *Store) Delete(id string) error {
	if s == nil || id == "" {
		return nil
	}
	if _, err := s.db.Exec(`DELETE FROM posts WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete post: %w", err)
	}
	return nil
}

// RecentForChannel returns up to limit most-recent non-deleted posts
// for the channel, ordered oldest→newest (i.e. ready to assign to the
// UI's m.posts slice without reversal).
func (s *Store) RecentForChannel(channelID string, limit int) ([]*model.Post, error) {
	if s == nil || channelID == "" || limit <= 0 {
		return nil, nil
	}
	// Two-step sort: pick the newest `limit` by create_at DESC, then
	// re-sort the result ascending so callers can append directly.
	const q = `
SELECT raw_json FROM (
    SELECT rowid, raw_json, create_at
    FROM posts
    WHERE channel_id = ? AND delete_at = 0
    ORDER BY create_at DESC
    LIMIT ?
) ORDER BY create_at ASC`
	rows, err := s.db.Query(q, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent: %w", err)
	}
	defer rows.Close()
	var out []*model.Post
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			// One bad row shouldn't poison the rest — skip and continue.
			continue
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// AfterInChannel returns up to limit non-deleted posts in the channel
// strictly newer than afterCreateAt, ordered oldest→newest. Mirror of
// BeforeInChannel — used to page forward into a channel's history when
// the user scrolls past the last currently-rendered post (e.g. after
// opening a search hit centred on an older message).
func (s *Store) AfterInChannel(channelID string, afterCreateAt int64, limit int) ([]*model.Post, error) {
	if s == nil || channelID == "" || limit <= 0 {
		return nil, nil
	}
	const q = `
SELECT raw_json FROM posts
WHERE channel_id = ? AND delete_at = 0 AND create_at > ?
ORDER BY create_at ASC
LIMIT ?`
	rows, err := s.db.Query(q, channelID, afterCreateAt, limit)
	if err != nil {
		return nil, fmt.Errorf("query after: %w", err)
	}
	defer rows.Close()
	var out []*model.Post
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// BeforeInChannel returns up to limit non-deleted posts in the channel
// strictly older than beforeCreateAt, ordered oldest→newest. Used to
// page further back into a channel's history when the user scrolls past
// the top of what's currently rendered.
func (s *Store) BeforeInChannel(channelID string, beforeCreateAt int64, limit int) ([]*model.Post, error) {
	if s == nil || channelID == "" || limit <= 0 {
		return nil, nil
	}
	const q = `
SELECT raw_json FROM (
    SELECT raw_json, create_at
    FROM posts
    WHERE channel_id = ? AND delete_at = 0 AND create_at < ?
    ORDER BY create_at DESC
    LIMIT ?
) ORDER BY create_at ASC`
	rows, err := s.db.Query(q, channelID, beforeCreateAt, limit)
	if err != nil {
		return nil, fmt.Errorf("query before: %w", err)
	}
	defer rows.Close()
	var out []*model.Post
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// Revisions returns the archived prior versions of a post, oldest→newest
// by edit_at. Returns nil if the post has no recorded edit history (or
// has only ever been seen in its current form). Note: only versions
// observed by this matterbox install are present — Mattermost's server
// API does not expose edit history.
func (s *Store) Revisions(postID string) ([]*model.Post, error) {
	if s == nil || postID == "" {
		return nil, nil
	}
	const q = `
SELECT raw_json FROM post_revisions
WHERE post_id = ?
ORDER BY edit_at ASC, id ASC`
	rows, err := s.db.Query(q, postID)
	if err != nil {
		return nil, fmt.Errorf("query revisions: %w", err)
	}
	defer rows.Close()
	var out []*model.Post
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan revision: %w", err)
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// LatestPostID returns the most recent (by create_at) non-deleted post
// id stored for the channel, or "" if none. Used as the cursor for
// Client.PostsAfter when filling the gap on channel reopen.
func (s *Store) LatestPostID(channelID string) (string, error) {
	if s == nil || channelID == "" {
		return "", nil
	}
	const q = `
SELECT id FROM posts
WHERE channel_id = ? AND delete_at = 0
ORDER BY create_at DESC
LIMIT 1`
	var id string
	err := s.db.QueryRow(q, channelID).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("latest id: %w", err)
	}
	return id, nil
}

// OldestPost returns the oldest (by create_at) non-deleted post id +
// its create_at (unix-ms) stored for the channel, or ("", 0) if none.
// Used as the cursor for Client.PostsBefore when extending a channel's
// cached history backward.
func (s *Store) OldestPost(channelID string) (string, int64, error) {
	if s == nil || channelID == "" {
		return "", 0, nil
	}
	const q = `
SELECT id, create_at FROM posts
WHERE channel_id = ? AND delete_at = 0
ORDER BY create_at ASC
LIMIT 1`
	var id string
	var createAt int64
	err := s.db.QueryRow(q, channelID).Scan(&id, &createAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, nil
		}
		return "", 0, fmt.Errorf("oldest post: %w", err)
	}
	return id, createAt, nil
}
