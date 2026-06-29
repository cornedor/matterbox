package ui

import (
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Following a permalink in-app: a Mattermost message permalink looks like
// {serverURL}/{team}/pl/{postID}. When such a link is clicked we resolve it to
// the target message and jump there inside the app — switching team/channel and
// centring the messages pane on the post — instead of handing the URL to the OS
// and bouncing the user out to the web client. Anything we can't resolve (a
// deleted post, a channel we're not in, no server configured) falls back to the
// browser so the click still does something. See activateLink in linkclick.go.

// parsePermalinkPostID returns the post id of a Mattermost message permalink on
// our own server, or ok=false for anything else. The path is /<team>/pl/<postID>
// (optionally under the server's URL subpath), with the host matching serverURL.
func (m *Model) parsePermalinkPostID(raw string) (string, bool) {
	if m.serverURL == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	base, err := url.Parse(m.serverURL)
	if err != nil || !strings.EqualFold(u.Host, base.Host) {
		return "", false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(segs); i++ {
		if segs[i] == "pl" {
			if id := segs[i+1]; model.IsValidId(id) {
				return id, true
			}
			return "", false
		}
	}
	return "", false
}

// followPermalinkMsg asks the update loop to follow a clicked/opened permalink.
// openTarget emits it so the mouse-click and keyboard `o` / picker paths share
// one navigation entry (openTarget can only return a command, not a new model).
type followPermalinkMsg struct {
	postID string
	url    string
}

// followPermalink navigates to the message a clicked permalink points at.
// fallbackURL is the original link, opened in the browser if the post can't be
// reached in-app. The channel is resolved from the loaded posts or the local
// cache when possible; otherwise the post is fetched from the server first.
func (m Model) followPermalink(postID, fallbackURL string) (tea.Model, tea.Cmd) {
	// Already loaded in the open channel: reposition without any lookup.
	for _, p := range m.posts {
		if p.Id == postID {
			return m.jumpToChannelPost(m.openChannelID, postID)
		}
	}
	// Cached elsewhere: resolve the channel from the local store (no API hit).
	if m.store != nil {
		if chID, ok, _ := m.store.ChannelOfPost(postID); ok {
			if ch := m.findChannel(chID); ch != nil {
				return m.openChannelAtPost(ch, postID)
			}
			return m, m.openOpenable(openable{name: fallbackURL, url: fallbackURL})
		}
	}
	// Not local: ask the server which channel the post is in, then navigate.
	m.status = "opening message…"
	return m, m.resolvePermalinkCmd(postID, fallbackURL)
}

// openChannelAtPost switches to ch (hopping to its team) and centres the
// messages pane on postID. A context window is pulled straight from the cache
// so a permalink to an old message lands with surrounding posts; when the cache
// can't satisfy it, the standard channel load runs with a queued jump. ch must
// be a channel the user is a member of. Mirrors openHitChannel (search.go) but
// follows the mainstream channel-open read-dwell instead of clearing the badge.
func (m Model) openChannelAtPost(ch *model.Channel, postID string) (tea.Model, tea.Cmd) {
	// Same channel already open: just reposition the selection within it.
	if ch.Id == m.openChannelID {
		return m.jumpToChannelPost(ch.Id, postID)
	}
	if m.infoOpen && ch.Id != m.infoChannelID {
		m.closeInfo()
	}
	m.switchToChannelHomeTeam(ch)
	m.filterValue = ""
	m.filter.SetValue("")
	m.focus = focusMessages
	saveCmd := m.bumpChannelStat(ch.Id)

	// Fast path: a context window from the cache, so a permalink to a message
	// older than the recent page still lands on it (openChannelLoadCmd only
	// loads the latest page; jumpToPendingPost can't reach further back).
	if m.store != nil {
		if around, err := m.store.PostsAround(ch.Id, postID, 30, 30); err == nil && len(around) > 0 {
			// We bypass openChannelLoadCmd here, so replicate the channel-open
			// bookkeeping it does: stash the outgoing draft / restore the incoming
			// one, repoint openChannelID, drop the stale unread divider and start a
			// fresh mark-read dwell.
			draftCmd := m.swapChannelDraft(ch.Id)
			m.openChannelID = ch.Id
			m.unreadBoundary = 0
			m.unreadDividerID = ""
			m.unreadDividerResolved = false
			m.viewGen++
			m.viewSettled = false
			m.posts = around
			m.postIdx = len(around) - 1
			for i, p := range around {
				if p.Id == postID {
					m.postIdx = i
					break
				}
			}
			m.pendingJumpPostID = ""
			m.msgScrollFree = false
			m.status = ""
			m.loading = false
			m.renderMessages()
			// Gap-fill forward from the newest cached post so the user can scroll
			// down to live without an extra step.
			var gapCmd tea.Cmd
			if gapID, _ := m.store.LatestPostID(ch.Id); gapID != "" {
				gapCmd = m.fetchPostsAfter(ch.Id, gapID)
			}
			return m, tea.Batch(draftCmd, saveCmd, gapCmd, m.scheduleMarkViewed(ch.Id))
		}
	}

	// Fallback: the standard load (which handles the draft swap, openChannelID,
	// the unread divider and viewGen itself) with a queued jump applied once the
	// page arrives.
	m.pendingJumpPostID = postID
	return m, tea.Batch(m.openChannelLoadCmd(ch.Id), saveCmd)
}

// permalinkResolvedMsg carries the channel a permalinked post belongs to, looked
// up from the server when it wasn't local. channelID is empty on failure
// (deleted post / no access), in which case fallbackURL opens in the browser.
type permalinkResolvedMsg struct {
	postID      string
	channelID   string
	fallbackURL string
	err         error
}

// resolvePermalinkCmd fetches the post to learn its channel, off the UI thread.
func (m Model) resolvePermalinkCmd(postID, fallbackURL string) tea.Cmd {
	return func() tea.Msg {
		p, err := m.client.Post(m.ctx, postID)
		if err != nil || p == nil {
			return permalinkResolvedMsg{postID: postID, fallbackURL: fallbackURL, err: err}
		}
		return permalinkResolvedMsg{postID: postID, channelID: p.ChannelId, fallbackURL: fallbackURL}
	}
}

// handlePermalinkResolved navigates to the resolved post, or opens the original
// link in the browser when it can't be reached in-app (post gone, or its channel
// isn't one we're a member of).
func (m Model) handlePermalinkResolved(msg permalinkResolvedMsg) (tea.Model, tea.Cmd) {
	if msg.channelID == "" {
		m.status = ""
		return m, m.openOpenable(openable{name: msg.fallbackURL, url: msg.fallbackURL})
	}
	ch := m.findChannel(msg.channelID)
	if ch == nil {
		m.status = ""
		return m, m.openOpenable(openable{name: msg.fallbackURL, url: msg.fallbackURL})
	}
	return m.openChannelAtPost(ch, msg.postID)
}
