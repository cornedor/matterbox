package ui

import tea "charm.land/bubbletea/v2"

// composerHistory is a minimal undo/redo ring for the message composer.
//
// It stores whole-draft snapshots rather than tracking deltas: the textarea is
// small (a 4000-char limit) so snapshotting the string is cheaper to reason
// about than diff/patch bookkeeping, and it matches how every programmatic
// edit already replaces the value wholesale via SetValue. Restoring a snapshot
// diffs it against the live draft and lands the cursor at the edit site (see
// applyComposerSnapshot / changeEndOffset) rather than at the end, so undoing a
// change mid-message keeps the caret where the change was.
//
// Consecutive single-character edits of the same kind (all inserts, or all
// deletes) coalesce into one undo step until a word/line boundary, so undo
// removes roughly a word at a time instead of a keystroke at a time. Bulk edits
// (paste, @-mention / :emoji / grammar replacements) are each recorded as their
// own discrete step via checkpoint.
//
// Every snapshot is scoped to a composer context (channel / thread / edit
// target). When that context changes — e.g. switching channels, where a draft
// from another channel will soon be loaded from the server — the history drops
// on the next access, so an undo can never resurrect a different channel's
// draft.
type composerHistory struct {
	key    string   // context the snapshots belong to (see composerContextKey)
	past   []string // older drafts; past[len-1] is the most recently superseded
	future []string // re-doable drafts, popped by redo
	open   bool     // a coalescing run is currently in progress
	del    bool     // the open run is deletions (vs insertions)
}

// maxComposerHistory bounds each stack so a long editing session can't grow the
// snapshots without limit. 100 whole-draft strings is plenty of reach and, at
// the composer's 4000-char ceiling, a trivial amount of memory.
const maxComposerHistory = 100

// rebase discards the history when the composer context changed out from under
// it. It returns true when the existing snapshots still belong to key.
func (h *composerHistory) rebase(key string) bool {
	if h.key == key {
		return true
	}
	h.key = key
	h.past = h.past[:0]
	h.future = h.future[:0]
	h.open = false
	return false
}

// push appends v as an undo restore point and invalidates the redo stack (a new
// edit forks history). Adjacent duplicates are collapsed.
func (h *composerHistory) push(v string) {
	if n := len(h.past); n == 0 || h.past[n-1] != v {
		h.past = append(h.past, v)
		if len(h.past) > maxComposerHistory {
			h.past = h.past[1:]
		}
	}
	h.future = h.future[:0]
}

// note records a single-keystroke change (before → after) with coalescing.
func (h *composerHistory) note(key, before, after string) {
	h.rebase(key)
	del := len([]rune(after)) < len([]rune(before))
	// Open a fresh run — committing the pre-run draft — when none is open or
	// the edit kind flipped between inserting and deleting.
	if !h.open || del != h.del {
		h.push(before)
		h.open = true
		h.del = del
	}
	// End the run at a word/line boundary so the next keystroke starts a new
	// undo step, giving word-at-a-time granularity.
	if endsAtBoundary(after) {
		h.open = false
	}
}

// checkpoint records a bulk, non-keystroke edit (paste / autocomplete accept /
// grammar fix). before is the draft prior to the edit; it becomes its own
// discrete undo step.
func (h *composerHistory) checkpoint(key, before string) {
	h.rebase(key)
	h.push(before)
	h.open = false
}

// undo returns the previous draft and moves live onto the redo stack. ok is
// false when there's nothing to undo — including immediately after a context
// switch, which is what keeps an undo from crossing channels.
func (h *composerHistory) undo(key, live string) (string, bool) {
	if !h.rebase(key) {
		return "", false
	}
	n := len(h.past)
	if n == 0 {
		return "", false
	}
	prev := h.past[n-1]
	h.past = h.past[:n-1]
	h.future = append(h.future, live)
	h.open = false
	return prev, true
}

// redo reverses the most recent undo, moving live back onto the undo stack.
func (h *composerHistory) redo(key, live string) (string, bool) {
	if !h.rebase(key) {
		return "", false
	}
	n := len(h.future)
	if n == 0 {
		return "", false
	}
	next := h.future[n-1]
	h.future = h.future[:n-1]
	h.past = append(h.past, live)
	h.open = false
	return next, true
}

// reset clears the history outright. Used when the draft is intentionally
// discarded (send / clear), so undo can't resurrect a sent or cleared message.
func (h *composerHistory) reset() {
	h.past = h.past[:0]
	h.future = h.future[:0]
	h.open = false
	h.key = ""
}

// applyComposerSnapshot replaces the draft with a restored undo/redo snapshot
// and resyncs the dependent composer state: the cursor (landed at the edit
// site, see below), input height, the @-mention / :emoji popups, and a fresh
// grammar pass (the cached matches point at the old text). It returns the
// commands to run.
func (m *Model) applyComposerSnapshot(v string) tea.Cmd {
	// Place the cursor where the restored draft diverges from the current one
	// — i.e. the text the undo/redo just changed — instead of at the end (where
	// SetValue would leave it). Computed before SetValue clobbers the value.
	cursor := changeEndOffset(m.input.Value(), v)
	m.input.SetValue(v)
	m.input.SetCursorOffset(cursor)
	m.syncInputHeight()
	mentionCmd := m.updateMention()
	m.updateEmoji()
	slashCmd := m.updateSlash()
	m.updateLang()
	cmdHlCmd := m.updateCommandHighlight()
	m.clearGrammar()
	return tea.Batch(mentionCmd, slashCmd, cmdHlCmd, m.scheduleGrammarCheck())
}

// changeEndOffset returns the rune offset, within after, at the end of the
// region where before and after differ — the natural caret position after an
// edit that turned before into after. It trims the shared prefix and suffix and
// returns the tail of what's left (len(after) minus the common suffix), so an
// undo/redo lands the cursor right after the text it restored. Identical strings
// yield len(after) (end of draft), which is harmless.
func changeEndOffset(before, after string) int {
	a := []rune(before)
	b := []rune(after)
	// Longest common prefix.
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	// Longest common suffix that doesn't overlap the shared prefix.
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	return len(b) - s
}

// composerContextKey identifies which logical buffer the composer currently
// holds, so undo history stays bound to it. Editing a post, a thread reply, and
// a channel's main draft are distinct buffers; switching channels changes the
// channel key. When the key changes the history self-drops (see rebase), which
// is what guarantees an undo can't reach back into another channel's draft.
func (m *Model) composerContextKey() string {
	switch {
	case m.editingPostID != "":
		return "edit:" + m.editingPostID
	case m.threadOpen:
		return "thread:" + m.threadChannelID + ":" + m.threadRootID
	default:
		return "chan:" + m.openChannelID
	}
}

// endsAtBoundary reports whether a coalesced run should close after producing s.
// A run ends once the edit lands on whitespace (or empties the draft), which
// makes each typed/deleted word its own undo step.
func endsAtBoundary(s string) bool {
	if s == "" {
		return true
	}
	r := []rune(s)
	switch r[len(r)-1] {
	case ' ', '\n', '\t':
		return true
	}
	return false
}
