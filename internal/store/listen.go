package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// notifTargetCap bounds the notif_targets table (newest kept), so the daemon's
// notification→thread map doesn't grow without limit on a long-running host.
const notifTargetCap = 2000

// GetMeta reads a value from the key/value meta table. ok is false when the key
// is absent.
func (s *Store) GetMeta(key string) (value string, ok bool, err error) {
	if s == nil {
		return "", false, nil
	}
	err = s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get meta %s: %w", key, err)
	}
	return value, true, nil
}

// SetMeta upserts a value into the meta table.
func (s *Store) SetMeta(key, value string) error {
	if s == nil {
		return nil
	}
	_, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("set meta %s: %w", key, err)
	}
	return nil
}

// PutNotifTarget records which Mattermost message a sent Telegram notification
// (tgMsgID) is about, so a later reply/reaction can act on it across restarts.
// The table is pruned to the newest notifTargetCap rows on each insert.
func (s *Store) PutNotifTarget(tgMsgID int, channelID, rootID, postID string) error {
	if s == nil {
		return nil
	}
	if _, err := s.db.Exec(
		`INSERT INTO notif_targets(tg_msg_id, channel_id, root_id, post_id, created_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(tg_msg_id) DO UPDATE SET
		     channel_id = excluded.channel_id,
		     root_id    = excluded.root_id,
		     post_id    = excluded.post_id,
		     created_at = excluded.created_at`,
		tgMsgID, channelID, rootID, postID, time.Now().UnixMilli(),
	); err != nil {
		return fmt.Errorf("put notif target: %w", err)
	}
	// Keep only the newest notifTargetCap rows.
	if _, err := s.db.Exec(
		`DELETE FROM notif_targets WHERE tg_msg_id NOT IN (
		     SELECT tg_msg_id FROM notif_targets ORDER BY created_at DESC LIMIT ?
		 )`, notifTargetCap,
	); err != nil {
		return fmt.Errorf("prune notif targets: %w", err)
	}
	return nil
}

// GetNotifTarget resolves a Telegram notification message id to the Mattermost
// message it referenced. ok is false when the id is unknown (e.g. evicted).
func (s *Store) GetNotifTarget(tgMsgID int) (channelID, rootID, postID string, ok bool, err error) {
	if s == nil {
		return "", "", "", false, nil
	}
	err = s.db.QueryRow(
		`SELECT channel_id, root_id, post_id FROM notif_targets WHERE tg_msg_id = ?`,
		tgMsgID,
	).Scan(&channelID, &rootID, &postID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, fmt.Errorf("get notif target: %w", err)
	}
	return channelID, rootID, postID, true, nil
}
