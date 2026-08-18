package listen

import (
	"time"

	"matterbox/internal/control"
)

// tuiStatusTTL is how long one answer from the TUI is reused. A burst of posts
// (or several rules over one post) then costs a single socket round-trip, and
// the staleness it buys is far shorter than the time it takes a human to look
// away and back.
const tuiStatusTTL = time.Second

// tuiStatus reports what the local TUI is showing, for the `viewing` match
// condition and the notify gate. It answers the zero Status — "no TUI, viewing
// nothing" — whenever it can't ask: no socket path, no TUI running, a stale
// socket file, or a ruleset the answer can't affect (needsTUIStatus false, so a
// pure cache-warmer never dials at all).
//
// Failing towards "not viewing" is deliberate: the cost of a wrong answer here
// is either a notification you didn't need (harmless) or one you never got
// (the bug this exists to avoid).
func (e *Engine) tuiStatus() control.Status {
	if !e.asksTUI() || e.tuiSocket == "" {
		return control.Status{}
	}
	now := e.clock()
	e.tuiMu.Lock()
	defer e.tuiMu.Unlock()
	if !e.tuiAt.IsZero() && now.Sub(e.tuiAt) < e.tuiTTL {
		return e.tuiCached
	}
	s, ok := control.Query(e.tuiSocket, control.QueryTimeout)
	if !ok {
		s = control.Status{}
	}
	e.tuiCached, e.tuiAt = s, now
	return s
}

// TUISocketPath is the control socket the daemon asks about the on-screen
// conversation, for the startup log. Empty means it won't ask at all.
func (e *Engine) TUISocketPath() string {
	if !e.asksTUI() {
		return ""
	}
	return e.tuiSocket
}

// rulesUseViewing reports whether any rule (including nested not: blocks)
// carries a viewing condition. The engine ORs it with "can any rule notify"
// into needsTUIStatus — the notify gate consults the TUI too — so a config that
// can't act on the answer pays no socket round-trip per post.
func rulesUseViewing(rules []Rule) bool {
	for _, r := range rules {
		if matchUsesViewing(r.Match) {
			return true
		}
	}
	return false
}

func matchUsesViewing(m Match) bool {
	if m.viewing != nil {
		return true
	}
	return m.not != nil && matchUsesViewing(*m.not)
}
