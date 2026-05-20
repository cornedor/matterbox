package ui

import (
	"strconv"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

// postLineCacheCap bounds the rendered-line cache. The cache is keyed by
// post id; over the cap we just clear it (a chat-style workload reaches
// the cap mostly during fast channel-switching, when the previously
// cached lines aren't going to be revisited soon).
const postLineCacheCap = 1024

type postLineCacheEntry struct {
	fp    string
	lines []string
}

// postLineFingerprint encodes every input renderPostLines /
// renderThreadPostLines reads from a post (and the relevant Model state).
// If two calls produce the same fingerprint, their output is identical and
// the cached []string can be returned verbatim. Polls embed selection
// state in their render, so callers skip the cache for poll posts.
func (m *Model) postLineFingerprint(p *model.Post, width int, isThread, isRoot bool) string {
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
	b.WriteByte('|')
	b.WriteString(m.userNames[p.UserId])
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

// cachedPostLines returns previously-rendered lines if the fingerprint
// matches, or (nil, false) on a miss. Polls return (nil, false)
// unconditionally — their output depends on the current selection.
func (m *Model) cachedPostLines(p *model.Post, fp string) ([]string, bool) {
	if m.postLineCache == nil {
		return nil, false
	}
	e, ok := m.postLineCache[p.Id]
	if !ok || e.fp != fp {
		return nil, false
	}
	return e.lines, true
}

func (m *Model) putPostLines(postID, fp string, lines []string) {
	if m.postLineCache == nil {
		m.postLineCache = make(map[string]postLineCacheEntry, 128)
	}
	if len(m.postLineCache) >= postLineCacheCap {
		// Cheap eviction: drop everything. The active viewport will
		// repopulate on the next render pass; older entries weren't
		// going to be visited soon anyway.
		m.postLineCache = make(map[string]postLineCacheEntry, 128)
	}
	m.postLineCache[postID] = postLineCacheEntry{fp: fp, lines: lines}
}

// invalidatePostLines clears the cache entry for one post. Used by WS
// event handlers (edit / delete / reaction add/remove) so the next
// render observes the change even if UpdateAt didn't move.
func (m *Model) invalidatePostLines(postID string) {
	if m.postLineCache == nil || postID == "" {
		return
	}
	delete(m.postLineCache, postID)
}
