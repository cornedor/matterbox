package listen

import (
	"context"
	"strconv"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// syncedKey is the meta-table key for the cache watermark: the time up to which
// the local cache holds everything the server had.
const syncedKey = "listen.cache_synced_ms"

// syncSlack is taken off the watermark whenever it's set: a half-open socket
// can sit dead for ~65s before the ping watchdog notices, and posts carry the
// server's clock, not ours.
const syncSlack = 5 * time.Minute

// postsSinceCap is the server's row limit on GetPostsSince. A result that big
// may be missing an arbitrary part of the window.
const postsSinceCap = 1000

// backfillCache fills the cache with what changed while the daemon was away.
// The websocket only delivers going forward and a resume spans a blip at best,
// so a restart or a longer outage leaves a hole that offline search, digest and
// the feed would silently miss. Runs after every connect, before live events,
// whatever the notification settings.
//
// Only channels with a post after the watermark are fetched. PostsSince keys on
// update_at, so edits and deletions in those channels come along; an edit in a
// channel with no new post is not seen.
func (e *Engine) backfillCache(ctx context.Context) {
	e.cacheSynced = false
	if e.store == nil || e.me == nil {
		return
	}
	start := time.Now()
	v, ok, err := e.store.GetMeta(syncedKey)
	if err != nil {
		e.log.Printf("cache backfill: %v", err)
		return
	}
	since, perr := strconv.ParseInt(v, 10, 64)
	if !ok || perr != nil {
		// First run: there's no hole to fill yet, just start tracking.
		e.markSynced(start)
		return
	}
	channels, err := e.client.AllChannels(ctx, e.me.Id)
	if err != nil {
		e.log.Printf("cache backfill: %v", err)
		return
	}
	n, complete := 0, true
	for _, ch := range channels {
		if ch.LastPostAt < since {
			continue
		}
		got, err := e.backfillChannel(ctx, ch.Id, since)
		if err != nil {
			// Leave the watermark where it is so the next try covers this one.
			e.log.Printf("cache backfill %s: %v", ch.Id, err)
			complete = false
			continue
		}
		n += got
	}
	if n > 0 {
		e.log.Printf("cache backfill: %d post(s) since %s", n, time.UnixMilli(since).Format(time.RFC3339))
	}
	if complete {
		e.markSynced(start)
	}
}

// backfillChannel stores every post in the channel created, edited or deleted
// since the watermark and returns how many it saw.
func (e *Engine) backfillChannel(ctx context.Context, channelID string, since int64) (int, error) {
	pl, err := e.client.PostsSince(ctx, channelID, since)
	if err != nil {
		return 0, err
	}
	if len(pl.Posts) >= postsSinceCap {
		full, err := e.client.PostsCreatedSince(ctx, channelID, since)
		if err != nil {
			return 0, err
		}
		for id, p := range full.Posts {
			pl.Posts[id] = p
		}
	}
	live := make([]*model.Post, 0, len(pl.Posts))
	for _, p := range pl.Posts {
		if p == nil || p.Id == "" {
			continue
		}
		if p.DeleteAt != 0 {
			if err := e.store.Delete(p); err != nil {
				return 0, err
			}
			continue
		}
		live = append(live, p)
	}
	return len(pl.Posts), e.store.UpsertMany(live)
}

// keepSynced runs while connected: live events keep the cache current, so the
// watermark can follow the clock. A connect whose backfill failed retries here
// instead, so the hole isn't buried under a newer watermark.
func (e *Engine) keepSynced(ctx context.Context) {
	if e.cacheSynced {
		e.markSynced(time.Now())
		return
	}
	e.backfillCache(ctx)
}

// markSynced records that the cache is complete up to t (less syncSlack). It
// never moves the watermark back.
func (e *Engine) markSynced(t time.Time) {
	e.cacheSynced = true
	ms := t.Add(-syncSlack).UnixMilli()
	if v, ok, err := e.store.GetMeta(syncedKey); err == nil && ok {
		if cur, perr := strconv.ParseInt(v, 10, 64); perr == nil && cur >= ms {
			return
		}
	}
	if err := e.store.SetMeta(syncedKey, strconv.FormatInt(ms, 10)); err != nil {
		e.log.Printf("persist cache watermark: %v", err)
	}
}
