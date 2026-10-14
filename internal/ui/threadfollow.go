package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Thread follow state is the per-user membership Mattermost's collapsed reply
// threads notify on (see mm.ThreadFollowing). The thread pane shows it in its
// title and the palette toggles it; nothing is cached beyond the open thread.

// threadFollowMsg carries the open thread's follow state: a lookup's answer, or
// a toggle's outcome. err on a toggle reverts the optimistic flip.
type threadFollowMsg struct {
	rootID    string
	following bool
	toggled   bool
	err       error
}

// fetchThreadFollow looks up whether the reader follows the open thread. A
// failed lookup leaves the state unknown, which draws no mark at all — a wrong
// "following" is worse than none.
func (m *Model) fetchThreadFollow() tea.Cmd {
	if !m.threadOpen || m.threadRootID == "" || m.client == nil {
		return nil
	}
	client, ctx := m.client, m.ctx
	teamID, rootID := m.threadTeamID(), m.threadRootID
	return func() tea.Msg {
		ok, err := client.ThreadFollowing(ctx, teamID, rootID)
		if err != nil {
			return nil
		}
		return threadFollowMsg{rootID: rootID, following: ok}
	}
}

func (m *Model) applyThreadFollow(msg threadFollowMsg) {
	if !m.threadOpen || msg.rootID != m.threadRootID {
		return // closed or switched since
	}
	if msg.err != nil {
		m.threadFollowing = !msg.following
		m.status = oneLine(msg.err.Error())
		return
	}
	m.threadFollowing, m.threadFollowKnown = msg.following, true
	if msg.toggled {
		if msg.following {
			m.status = "following thread"
		} else {
			m.status = "unfollowed thread"
		}
	}
}

// applyThreadFollowChanged picks up a follow or unfollow made on another client
// (or the echo of our own).
func (m *Model) applyThreadFollowChanged(ev *model.WebSocketEvent) {
	root, _ := ev.GetData()["thread_id"].(string)
	if !m.threadOpen || root == "" || root != m.threadRootID {
		return
	}
	m.threadFollowing, _ = ev.GetData()["state"].(bool)
	m.threadFollowKnown = true
}

// followThreadCommand is the palette's follow/unfollow toggle for the open
// thread, listed once its state is known; the label follows that state.
func (m *Model) followThreadCommand() (switcherCommand, bool) {
	if !m.threadOpen || !m.threadFollowKnown {
		return switcherCommand{}, false
	}
	if m.threadFollowing {
		return switcherCommand{
			name: "Unfollow thread",
			tid:  "thread_unfollow",
			desc: "stop notifications for replies in the open thread",
			run:  runToggleThreadFollow,
		}, true
	}
	return switcherCommand{
		name: "Follow thread",
		tid:  "thread_follow",
		desc: "get notified about replies in the open thread",
		run:  runToggleThreadFollow,
	}, true
}

// runToggleThreadFollow flips the open thread's follow state optimistically so
// the title mark updates at once; the server call follows, and its error
// reverts the flip.
func runToggleThreadFollow(m *Model, _ string) tea.Cmd {
	if !m.threadOpen || !m.threadFollowKnown {
		return nil
	}
	follow := !m.threadFollowing
	m.threadFollowing = follow
	client, ctx := m.client, m.ctx
	teamID, rootID := m.threadTeamID(), m.threadRootID
	return func() tea.Msg {
		err := client.SetThreadFollowing(ctx, teamID, rootID, follow)
		return threadFollowMsg{rootID: rootID, following: follow, toggled: true, err: err}
	}
}
