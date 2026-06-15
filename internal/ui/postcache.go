package ui

import (
	"strconv"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

// postLineCacheCap bounds the rendered-line cache (entries keyed by post
// id). Sized comfortably above the largest realistic working set — the
// rendered message window (maxLoadedPosts) plus an open thread plus a few
// channel switches — so steady-state scrolling never evicts. On overflow
// (heavy channel hopping) evictPostLines drops a fraction rather than
// clearing the whole map.
const postLineCacheCap = 2048

type postLineCacheEntry struct {
	fp    string
	lines []string
	// rows is the number of soft-wrapped visual rows lines occupies at the
	// fingerprinted width. Cached alongside the lines so renderMessages can
	// sum prefix heights to locate the selection instead of re-measuring
	// every visible line's width on each keystroke.
	rows int
}

// postLineFingerprint encodes every input renderPostLines /
// renderThreadPostLines reads from a post (and the relevant Model state).
// If two calls produce the same fingerprint, their output is identical and
// the cached []string can be returned verbatim. Polls embed selection
// state in their render, so callers skip the cache for poll posts.
// postAuthorName returns the name to show for a post. A webhook/bot post
// carries its own display name in Props["override_username"]; honour that
// per-post rather than the per-user cache, since the same UserId can post
// under multiple identities (a human and a bot running on their account).
// Falls back to the cached username, then a truncated UserId.
func (m *Model) postAuthorName(p *model.Post) string {
	if ov, ok := p.GetProp("override_username").(string); ok && ov != "" {
		return ov
	}
	name := m.userNames[p.UserId]
	if name == "" {
		name = p.UserId
		if len(name) > 8 {
			name = name[:8]
		}
	}
	return name
}

func (m *Model) postLineFingerprint(p *model.Post, width int, isThread, isRoot, grouped bool) string {
	var b strings.Builder
	b.Grow(96)
	b.WriteString(strconv.FormatInt(p.UpdateAt, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(p.EditAt, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(p.DeleteAt, 10))
	b.WriteByte('|')
	b.WriteString(p.RootId)
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(p.ReplyCount, 10))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(width))
	b.WriteByte('|')
	if isThread {
		b.WriteByte('T')
		if isRoot {
			b.WriteByte('R')
		}
	} else {
		b.WriteByte('M')
	}
	// Whether the name/time header is suppressed (a continuation line) depends
	// on the post above this one, so it must key the cache: the same post can
	// render headed or grouped as its neighbours change.
	if grouped {
		b.WriteByte('g')
	}
	b.WriteByte('|')
	b.WriteString(m.postAuthorName(p))
	b.WriteByte('|')
	if p.Metadata != nil {
		for _, r := range p.Metadata.Reactions {
			if r == nil {
				continue
			}
			b.WriteString(r.EmojiName)
			b.WriteByte(':')
			b.WriteString(r.UserId)
			b.WriteByte(',')
		}
	}
	return b.String()
}

// cachedPostLines returns previously-rendered lines and their visual-row
// count if the fingerprint matches, or (nil, 0, false) on a miss. Polls
// return false unconditionally — their output depends on the current
// selection.
func (m *Model) cachedPostLines(p *model.Post, fp string) ([]string, int, bool) {
	if m.postLineCache == nil {
		return nil, 0, false
	}
	e, ok := m.postLineCache[p.Id]
	if !ok || e.fp != fp {
		return nil, 0, false
	}
	return e.lines, e.rows, true
}

func (m *Model) putPostLines(postID, fp string, lines []string, rows int) {
	if m.postLineCache == nil {
		m.postLineCache = make(map[string]postLineCacheEntry, 128)
	}
	if len(m.postLineCache) >= postLineCacheCap {
		m.evictPostLines()
	}
	m.postLineCache[postID] = postLineCacheEntry{fp: fp, lines: lines, rows: rows}
}

// evictPostLines drops roughly the oldest quarter of the cache when it
// fills up. Go randomises map iteration order, so without per-entry access
// timestamps we delete an arbitrary subset rather than the whole map: that
// keeps ~75% of the working set warm instead of going fully cold on every
// overflow. (The old "clear everything" behaviour made deep scroll-back
// re-render every visible post's markdown on each keystroke once the
// loaded history exceeded the cap.)
func (m *Model) evictPostLines() {
	target := postLineCacheCap * 3 / 4
	for id := range m.postLineCache {
		if len(m.postLineCache) <= target {
			break
		}
		delete(m.postLineCache, id)
	}
}

// invalidatePostLines clears the cache entries for one post. Used by WS
// event handlers (edit / delete / reaction add/remove) so the next render
// observes the change even if UpdateAt didn't move — and by the emoji-ready
// path, which is why the width-independent markdown cache is dropped here too:
// a just-readied emoji changes renderMarkdown's output without touching any
// post field markdownFingerprint reads. delete on a nil map is a no-op.
func (m *Model) invalidatePostLines(postID string) {
	if postID == "" {
		return
	}
	delete(m.postLineCache, postID)
	delete(m.postMarkdownCache, postID)
}

// postMarkdownCacheEntry caches the width-INDEPENDENT styled body that
// renderMarkdown produces for a post. fp is markdownFingerprint(p); when it
// still matches, the body can be re-wrapped at any width without re-styling.
type postMarkdownCacheEntry struct {
	fp   string
	body string
}

// markdownFingerprint captures everything renderMarkdown reads from a post:
// just its message text, tracked via the edit timestamps (UpdateAt moves on
// any content change, EditAt/DeleteAt on edit/delete). Emoji readiness also
// affects the output but is not a post field — invalidatePostLines is called
// explicitly when an emoji image loads (invalidatePostsForEmoji), dropping
// the stale entry.
func markdownFingerprint(p *model.Post) string {
	var b strings.Builder
	b.Grow(40)
	b.WriteString(strconv.FormatInt(p.UpdateAt, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(p.EditAt, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(p.DeleteAt, 10))
	return b.String()
}

// markdownBody returns p's styled (but unwrapped) body, rendering it via
// renderMarkdown on a miss and memoizing the result. The cache is
// width-independent, so it stays warm across resizes: the costly styling runs
// once per message version and a resize only re-wraps. The first render of a
// post (a miss) still calls renderMarkdown, whose inline() records emoji
// sightings, so deferring later styling doesn't drop the fetch trigger.
func (m *Model) markdownBody(p *model.Post) string {
	mr := m.buildMRInlineFn(p.Id)
	if p.Id == "" {
		return renderMarkdown(p.Message, m.emojiImg, mr)
	}
	fp := markdownFingerprint(p)
	if e, ok := m.postMarkdownCache[p.Id]; ok && e.fp == fp {
		return e.body
	}
	body := renderMarkdown(p.Message, m.emojiImg, mr)
	if m.postMarkdownCache == nil {
		m.postMarkdownCache = make(map[string]postMarkdownCacheEntry, 128)
	}
	if len(m.postMarkdownCache) >= postLineCacheCap {
		m.evictMarkdown()
	}
	m.postMarkdownCache[p.Id] = postMarkdownCacheEntry{fp: fp, body: body}
	return body
}

// evictMarkdown drops roughly the oldest quarter of the markdown cache on
// overflow, mirroring evictPostLines — keep ~75% of the working set warm
// rather than clearing it whole.
func (m *Model) evictMarkdown() {
	target := postLineCacheCap * 3 / 4
	for id := range m.postMarkdownCache {
		if len(m.postMarkdownCache) <= target {
			break
		}
		delete(m.postMarkdownCache, id)
	}
}
