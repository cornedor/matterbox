package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Ephemeral posts: messages the server shows to one user and never stores.
// Plugins and integrations answer with them (a Jira lookup, a bot's "you can't
// do that"), and until this existed matterbox dropped every one on the floor —
// the user typed something and got silence back.
//
// Built-in slash commands are unaffected either way: their reply comes back on
// the REST call and lands in the footer (see slashExecMsg).

// ephemeralNote marks the post as nobody else's business. The webapp shows a
// "(Only visible to you)" badge next to the timestamp; matterbox has no room
// for chrome per message, so the note joins the body as dim italics — the
// message is synthetic anyway, and this keeps the whole feature out of the
// render hot path.
const ephemeralNote = "\n\n*(only visible to you)*"

// applyEphemeralPost shows one, if it belongs to what's on screen.
//
// It is deliberately less than applyPosted does: nothing is persisted (the post
// does not exist server-side, so caching it would resurrect it in search and in
// the warm-open transcript, where no refetch could ever remove it), nothing is
// counted unread (there is no read state to reconcile against), and a channel
// that isn't open drops it (matterbox runs commands in the open channel, so the
// reply belongs there or nowhere). Reloading the conversation is what clears
// it, exactly as the server intends.
func (m *Model) applyEphemeralPost(ev *model.WebSocketEvent) tea.Cmd {
	p := parsePost(ev)
	if p == nil {
		return nil
	}
	if p.ChannelId == "" {
		// The broadcast knows the channel even when the payload skipped it.
		if b := ev.GetBroadcast(); b != nil {
			p.ChannelId = b.ChannelId
		}
	}
	p.Message = strings.TrimRight(p.Message, "\n") + ephemeralNote

	if m.isThreadPost(p) {
		m.appendThreadPost(p)
		m.renderThread()
	}
	if !m.isCurrentChannel(p.ChannelId) {
		return nil
	}
	wasAtBottom := m.postIdx >= len(m.posts)-1
	m.posts = append(m.posts, p)
	if wasAtBottom {
		m.postIdx = len(m.posts) - 1
		m.trimPostWindowHead()
		m.anchorMsgSelBottom = true
	}
	m.renderMessages()
	return nil
}

// isEphemeral reports whether a post is one of these — server-side type, set by
// SendEphemeralPost. The persistence guards read it so an ephemeral that
// reaches a shared path (a thread append, a re-render) still never hits disk.
func isEphemeral(p *model.Post) bool {
	return p != nil && p.Type == model.PostTypeEphemeral
}
