package ui

import (
	tea "charm.land/bubbletea/v2"

	"matterbox/internal/update"
)

// The update notice.
//
// Two surfaces for one fact, because neither is enough on its own. A note in
// the footer is seen only by somebody looking at the footer in the four seconds
// it is there; a line printed on exit lands where the command can actually be
// typed, but a client people leave open for weeks does not exit often. Between
// them the release gets mentioned without anything being interrupted, which is
// the whole brief: this is worth a sentence, not a modal.
//
// The check itself is in internal/update — what runs here is when to ask and
// when to speak.

// updateFoundMsg carries a release newer than this build. Never sent otherwise:
// the comparison, the daily interval and the config switch are all settled
// before the message exists, so everything downstream can assume there is
// genuinely something to say.
type updateFoundMsg struct{ rel *update.Release }

// checkUpdateCmd asks whether there is a newer matterbox, off the Update
// goroutine and off the startup path — nothing waits for it, and a machine with
// no network simply never produces the message. nil when the check is off, or
// when this build has no version to compare (a plain `go build`, which is its
// own answer to "am I up to date").
func (m Model) checkUpdateCmd() tea.Cmd {
	if !m.updateCheck || buildInfo.version == "" {
		return nil
	}
	ctx, current := m.ctx, buildInfo.version
	return func() tea.Msg {
		// The error is deliberately dropped: a failed update check is not news,
		// and the one place it is worth reporting is `matterbox upgrade`, where
		// somebody asked. See update.Check.
		rel, _ := update.Check(ctx, current)
		if rel == nil {
			return nil
		}
		// Recorded for the command layer, which prints the line on exit once
		// the TUI has released the terminal.
		update.SetPending(rel)
		return updateFoundMsg{rel: rel}
	}
}

// updateNoticeText is the footer note. Short: it shares the line with whatever
// help the current pane is advertising, and the command is repeated on exit
// where there is room for it.
func updateNoticeText(rel *update.Release) string {
	return "▲ " + rel.Version + " available — matterbox upgrade"
}

// flushUpdateNotice shows the notice once a moment presents itself: the startup
// splash down, and the status slot free. Called from the Update wrapper on
// every event, so it does not matter which one that turns out to be.
//
// Waiting for a free slot rather than taking one is the point. The slot carries
// "downloading 3 files…" and "leave: user not loaded yet" — things the user
// asked for and is waiting on — and stepping on one of those to advertise a
// release would be exactly the kind of nagging this is trying not to be. If the
// moment never comes, the line on exit still does.
func (m *Model) flushUpdateNotice() tea.Cmd {
	if m.updateFound == nil || m.updateNoticed || m.splash.active || m.status != "" {
		return nil
	}
	m.updateNoticed = true
	return m.flashStatus(updateNoticeText(m.updateFound))
}
