package ui

import "testing"

// The width-independent markdown cache must survive a width change — the whole
// point is that a resize re-wraps cached bodies instead of re-styling them —
// and must drop on a content edit or on explicit invalidation (the emoji-ready
// path).
func TestMarkdownCacheSurvivesWidthChangeAndInvalidates(t *testing.T) {
	m := newScrollBenchModel(1)
	p := m.posts[0]
	p.Message = "hello **world** :smile:"

	body1 := m.markdownBody(p)
	if _, ok := m.postMarkdownCache[p.Id]; !ok {
		t.Fatal("markdownBody did not populate the cache")
	}

	// A width change drops postLineCache but must NOT drop the markdown cache.
	m.postLineCache = nil
	body2 := m.markdownBody(p)
	if body2 != body1 {
		t.Errorf("markdown body changed across width change:\n %q\n %q", body1, body2)
	}
	if _, ok := m.postMarkdownCache[p.Id]; !ok {
		t.Error("markdown cache was dropped by a width change")
	}

	// An edit (UpdateAt moves) must invalidate via the fingerprint.
	p.Message = "edited"
	p.UpdateAt++
	if got := m.markdownBody(p); got == body1 {
		t.Error("edited post returned the stale cached body")
	}

	// Explicit invalidation (the emoji-ready path runs through here) drops both
	// caches so a just-readied placeholder is picked up.
	m.invalidatePostLines(p.Id)
	if _, ok := m.postMarkdownCache[p.Id]; ok {
		t.Error("invalidatePostLines did not drop the markdown entry")
	}
}

// A resize drag schedules one settle tick per frame, each carrying the
// generation at schedule time; only the latest may run the (expensive) content
// re-render. msgsContentVer bumps on every renderMessages, so it's the signal
// for "a re-render happened".
func TestResizeSettleGenGating(t *testing.T) {
	m := newScrollBenchModel(10)
	m.renderMessages() // establish a baseline content version

	// Two drag frames advanced the generation; gen 2 is current.
	m.resizeGen = 2
	before := m.msgsContentVer

	// A stale settle (an earlier frame's tick) must be a no-op.
	out, _ := m.update(resizeSettleMsg{gen: 1})
	m = out.(Model)
	if m.msgsContentVer != before {
		t.Errorf("stale settle re-rendered (content ver %d -> %d)", before, m.msgsContentVer)
	}

	// The current settle re-renders once.
	out, _ = m.update(resizeSettleMsg{gen: 2})
	m = out.(Model)
	if m.msgsContentVer == before {
		t.Error("current settle did not re-render")
	}
}
