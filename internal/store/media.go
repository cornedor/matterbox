package store

import (
	"encoding/json"
	"fmt"

	"github.com/mattermost/mattermost/server/public/model"
)

// channelFilesSQL walks a channel's posts newest-first and expands the file
// attachments each one carries. The (channel_id, create_at) index makes the
// walk stop as soon as LIMIT posts with files have been seen, so a busy
// channel costs a few milliseconds rather than a full scan.
//
// mini_preview is a base64 JPEG thumbnail we never read; json_remove drops it
// before the row reaches json.Unmarshal.
//
// Ordering is by the *post's* create_at, not the file's, so the listing reads
// in message order and a post's attachments stay together in upload order. A
// file's own create_at is its upload time, which trails its post's by seconds
// to a couple of minutes — near enough to display, but not a sort key.
//
// Soft-deleted posts need no explicit guard beyond delete_at: a tombstone has
// its metadata cleared (see StripTombstoneContent), so json_array_length is
// NULL and the row falls out of the WHERE either way.
const channelFilesSQL = `
WITH with_files AS (
    SELECT create_at, json_extract(raw_json, '$.metadata.files') AS files
    FROM posts
    WHERE channel_id = ?1 AND delete_at = 0
      AND json_array_length(json_extract(raw_json, '$.metadata.files')) > 0
    ORDER BY create_at DESC
    LIMIT ?2
)
SELECT json_remove(f.value, '$.mini_preview')
FROM with_files w, json_each(w.files) f
ORDER BY w.create_at DESC, f.key ASC`

// ChannelFiles returns the file attachments cached for a channel, newest post
// first and in attachment order within a post. It scans at most limit posts,
// so the result can hold more than limit files.
//
// The cache is the only source: files on posts that were never fetched aren't
// listed. That's the tradeoff for a listing that renders instantly and works
// offline — Mattermost has no channel-files endpoint to reconcile against.
func (s *Store) ChannelFiles(channelID string, limit int) ([]*model.FileInfo, error) {
	if s == nil || channelID == "" || limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(channelFilesSQL, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("query channel files: %w", err)
	}
	defer rows.Close()

	var out []*model.FileInfo
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var f model.FileInfo
		if err := json.Unmarshal(raw, &f); err != nil {
			continue
		}
		if f.Id == "" {
			continue
		}
		out = append(out, &f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan channel files: %w", err)
	}
	return out, nil
}
