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
	// The hovered post renders one link with a background highlight, so its wrapped
	// lines must not share a cache entry with its un-hovered self (markdownBody).
	if m.hoverLink.url != "" && m.hoverLink.postID == p.Id {
		b.WriteString("|H:")
		b.WriteString(m.hoverLink.url)
	}
	// A long post folds to a preview unless the user expanded it; key the cache
	// on that so toggling expand/collapse re-renders. Only matters while
	// collapsing is enabled (collapseRows > 0). Width — which the fold decision
	// depends on — is already encoded above.
	if m.collapseRows > 0 && m.expandedPosts[p.Id] {
		b.WriteString("|X")
	}
	// Collapsing the post's thumbnails changes both the rows it draws and the
	// chevron on its image indicators, and is independent of the body fold above —
	// it works even with collapsing disabled, since it hides an image rather than
	// text.
	if m.thumbsCollapsed[p.Id] {
		b.WriteString("|Z")
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

// markdownBody returns p's styled (but unwrapped) body as the *transcript* draws
// it: every body-image indicator carrying the post's thumbnail chevron (see
// imgChevrons), because the transcript is where that thumbnail is drawn. Anywhere
// a post's text appears without its images — the SQL tab, the info pane, a /me
// emote — use markdownBodyPlain, which leaves the indicator bare.
func (m *Model) markdownBody(p *model.Post) string {
	return m.hovered(m.imgChevrons(m.markdownBodyMarked(p), p), p)
}

// markdownBodyPlain is the body with no thumbnail chevrons — for the consumers that
// show a post's text but draw none of its images.
func (m *Model) markdownBodyPlain(p *model.Post) string {
	return m.hovered(m.markdownBodyRaw(p), p)
}

// hovered paints the hovered link's background on the post that owns it. Done on
// the unwrapped body (where the link is contiguous and the cached normal body is
// reused), so the highlight costs one string scan and never pollutes the cache.
// postLineFingerprint carries the same hover bit so the wrapped-line cache serves
// the highlighted version. See linkclick.go.
func (m *Model) hovered(body string, p *model.Post) string {
	if m.hoverLink.url == "" || m.hoverLink.postID != p.Id {
		return body
	}
	// A hovered \spoiler{} reveals rather than highlights: its block is lifted so
	// the text shows while the pointer rests on it (see revealSpoiler). Copy chips
	// and ordinary links take the usual hover background.
	if strings.HasPrefix(m.hoverLink.url, spoilerURLScheme) {
		return revealSpoiler(body, m.hoverLink.url)
	}
	return highlightLink(body, m.hoverLink.url, mdLinkHoverStyle)
}

// markdownBodyRaw is the styled body without the hover highlight or any chevrons —
// the plain text of the message (see the cache notes on markdownBodyMarked).
func (m *Model) markdownBodyRaw(p *model.Post) string {
	return stripImgMarks(m.markdownBodyMarked(p))
}

// markdownBodyMarked renders p's styled (but unwrapped) body via renderMarkdown on
// a miss and memoizes the result. The cache is width-independent, so it stays warm
// across resizes: the costly styling runs once per message version and a resize only
// re-wraps. The first render of a post (a miss) still calls renderMarkdown, whose
// inline() records emoji sightings, so deferring later styling doesn't drop the
// fetch trigger.
//
// What is cached carries the raw body-image markers (imgIndicatorMark), not the
// chevrons they become: the chevron depends on a collapse state the message text
// knows nothing about, and baking it in would mean re-running the whole markdown
// pass on every z press — and holding a separate entry for each consumer. Callers
// resolve or strip the markers on the way out; none of them may hand this string on
// unprocessed.
func (m *Model) markdownBodyMarked(p *model.Post) string {
	self := ""
	if m.me != nil {
		self = m.me.Username
	}
	mr := m.buildMRInlineFn(p.Id)
	if p.Id == "" {
		return renderMarkdownEffects(p.Message, m.emojiImg, mr, self)
	}
	fp := markdownFingerprint(p)
	if e, ok := m.postMarkdownCache[p.Id]; ok && e.fp == fp {
		return e.body
	}
	body := renderMarkdownEffects(p.Message, m.emojiImg, mr, self)
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
