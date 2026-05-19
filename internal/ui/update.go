package ui

import (
	"encoding/json"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattermost/mattermost/server/public/model"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.filter.Width = channelsWidth - 4
		m.resizeMessagesViewport()
		m.resizeInput()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case meLoadedMsg:
		m.me = msg.user
		m.status = "loading teams & channels…"
		return m, tea.Batch(
			m.fetchTeams(m.me.Id),
			m.fetchAllChannels(m.me.Id),
			m.fetchChannelMembers(m.me.Id),
		)

	case teamsLoadedMsg:
		m.teams = msg.teams
		m.teamsLoaded = true
		return m, m.maybeFetchInitialPosts()

	case channelsLoadedMsg:
		for id, name := range msg.userNames {
			m.userNames[id] = name
		}
		m.bucketChannels(msg.channels)
		m.channelsLoaded = true
		m.applyUnreadFromMembers()
		return m, m.maybeFetchInitialPosts()

	case membersLoadedMsg:
		m.members = msg.members
		m.membersLoaded = true
		m.applyUnreadFromMembers()
		return m, nil

	case postsLoadedMsg:
		vis := m.visibleChannels()
		if m.channelIdx >= len(vis) || vis[m.channelIdx].Id != msg.channelID {
			return m, nil // stale
		}
		m.posts = msg.posts
		for id, name := range msg.users {
			m.userNames[id] = name
		}
		delete(m.unread, msg.channelID)
		delete(m.mentions, msg.channelID)
		m.loading = false
		m.status = ""
		m.postIdx = len(m.posts) - 1
		m.renderMessages()
		return m, m.markChannelViewed(msg.channelID)

	case errMsg:
		m.loading = false
		m.status = "error: " + msg.err.Error()
		if isUnauthorized(msg.err) {
			m.status = "auth failed — re-run `python mm_login.py` to refresh the token"
		}
		return m, nil

	case wsConnectedMsg:
		m.ws = msg.ws
		m.wsRetry = 0
		if strings.HasPrefix(m.status, "websocket") || strings.HasPrefix(m.status, "reconnecting") {
			m.status = ""
		}
		return m, waitWSEvent(m.ws)

	case wsEventMsg:
		cmd := m.handleWSEvent(msg.ev)
		return m, tea.Batch(cmd, waitWSEvent(m.ws))

	case wsClosedMsg:
		m.ws = nil
		m.wsRetry++
		delay := wsBackoff(m.wsRetry)
		if msg.err != nil {
			m.status = "websocket: " + msg.err.Error() + "; retry in " + delay.String()
		} else {
			m.status = "websocket closed; retry in " + delay.String()
		}
		return m, tea.Tick(delay, func(_ time.Time) tea.Msg { return wsReconnectMsg{} })

	case wsReconnectMsg:
		m.status = "reconnecting…"
		return m, m.connectWS()

	case postSentMsg:
		m.status = ""
		// Refetch to replace the optimistic stub with the real post and
		// catch anything that arrived between send and now. The WS-driven
		// refetch may double this up — harmless and idempotent.
		return m, m.fetchPosts(msg.channelID)

	case threadLoadedMsg:
		if !m.threadOpen || msg.rootID != m.threadRootID {
			return m, nil // stale (closed or switched)
		}
		m.threadPosts = msg.posts
		for id, name := range msg.users {
			m.userNames[id] = name
		}
		m.threadLoading = false
		m.threadIdx = len(m.threadPosts) - 1
		if m.threadIdx < 0 {
			m.threadIdx = 0
		}
		m.renderThread()
		return m, nil

	case fileInfosLoadedMsg:
		for _, p := range m.posts {
			if p.Id != msg.postID {
				continue
			}
			if p.Metadata == nil {
				p.Metadata = &model.PostMetadata{}
			}
			p.Metadata.Files = msg.infos
			m.renderMessages()
			break
		}
		return m, nil

	case attachmentOpenedMsg:
		if msg.err != nil {
			m.status = "open " + msg.name + ": " + msg.err.Error()
		} else {
			m.status = "opened " + msg.name
		}
		return m, nil

	case mentionDebounceMsg:
		if !m.mention.active || msg.seq != m.mention.fetchSeq {
			return m, nil
		}
		// Scope autocomplete to the thread's channel/team when replying in
		// a thread (which may not be the channel currently selected in the
		// sidebar). Otherwise use the focused channel.
		var teamID, channelID string
		if m.threadOpen && m.threadChannelID != "" {
			channelID = m.threadChannelID
			teamID = m.threadTeamID()
		} else {
			vis := m.visibleChannels()
			if m.channelIdx >= len(vis) {
				return m, nil
			}
			ch := vis[m.channelIdx]
			teamID = ch.TeamId
			channelID = ch.Id
		}
		return m, m.fetchMentions(teamID, channelID, m.mention.query, msg.seq)

	case mentionUsersMsg:
		if !m.mention.active || msg.seq != m.mention.fetchSeq {
			return m, nil
		}
		if msg.err != nil {
			m.status = "mention: " + msg.err.Error()
			return m, nil
		}
		items := msg.users
		if len(items) > mentionLimit {
			items = items[:mentionLimit]
		}
		// Cache resolved usernames so future post rows label them without
		// another lookup.
		for _, u := range items {
			if u != nil && u.Username != "" {
				m.userNames[u.Id] = u.Username
			}
		}
		m.mention.items = items
		if m.mention.idx >= len(items) {
			m.mention.idx = 0
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.msgsView, cmd = m.msgsView.Update(msg)
	return m, cmd
}

// handleWSEvent reacts to a WebSocket event by parsing the embedded
// post and applying it locally — no refetch unless the payload is
// unparseable and the event concerns the current channel.
func (m *Model) handleWSEvent(ev *model.WebSocketEvent) tea.Cmd {
	switch ev.EventType() {
	case model.WebsocketEventPosted:
		return m.applyPosted(ev)
	case model.WebsocketEventPostEdited:
		return m.applyPostEdited(ev)
	case model.WebsocketEventPostDeleted:
		m.applyPostDeleted(ev)
	}
	return nil
}

// applyPosted appends a new post locally. If for a non-focused channel,
// just bumps the unread (and mention, if we're tagged) counter.
func (m *Model) applyPosted(ev *model.WebSocketEvent) tea.Cmd {
	p := parsePost(ev)
	if p == nil {
		// Fall back to refetch if we can't parse and it's the current
		// channel; also refresh the open thread so it doesn't fall
		// behind.
		var cmds []tea.Cmd
		if b := ev.GetBroadcast(); b != nil && m.isCurrentChannel(b.ChannelId) {
			cmds = append(cmds, m.fetchPosts(b.ChannelId))
		}
		if m.threadOpen {
			cmds = append(cmds, m.fetchThread(m.threadRootID))
		}
		if len(cmds) == 0 {
			return nil
		}
		return tea.Batch(cmds...)
	}
	if sn, ok := ev.GetData()["sender_name"].(string); ok && sn != "" {
		m.userNames[p.UserId] = strings.TrimPrefix(sn, "@")
	}

	var cmds []tea.Cmd

	if m.isCurrentChannel(p.ChannelId) {
		alreadyShown := false
		for _, ex := range m.posts {
			if ex.Id != "" && ex.Id == p.Id {
				alreadyShown = true
				break
			}
		}
		if !alreadyShown {
			// Drop any matching optimistic stub (own send, no Id yet).
			for i := len(m.posts) - 1; i >= 0; i-- {
				ex := m.posts[i]
				if ex.Id == "" && ex.UserId == p.UserId && ex.Message == p.Message {
					m.posts = append(m.posts[:i], m.posts[i+1:]...)
					break
				}
			}
			// If the user was viewing the last post, advance selection to the
			// new last so the incoming message comes into view. Otherwise keep
			// them where they are.
			wasAtBottom := m.postIdx >= len(m.posts)-1
			m.posts = append(m.posts, p)
			if wasAtBottom {
				m.postIdx = len(m.posts) - 1
			}
			m.renderMessages()
			cmds = append(cmds, m.markChannelViewed(p.ChannelId))
			if needsFileInfoFetch(p) {
				cmds = append(cmds, m.fetchFileInfos(p.Id))
			}
		}
	} else if !m.isThreadPost(p) {
		// Not in the focused channel and not part of the open thread →
		// it's a background channel update.
		m.unread[p.ChannelId]++
		if m.me != nil && wsMentions(ev)[m.me.Id] {
			m.mentions[p.ChannelId]++
		}
	}

	if m.isThreadPost(p) {
		m.appendThreadPost(p)
		m.renderThread()
	}

	return tea.Batch(cmds...)
}

// isThreadPost reports whether p belongs to the currently-open thread.
func (m *Model) isThreadPost(p *model.Post) bool {
	if !m.threadOpen || p == nil {
		return false
	}
	return p.Id == m.threadRootID || p.RootId == m.threadRootID
}

// appendThreadPost inserts p into m.threadPosts, deduping by Id and
// replacing matching optimistic stubs (own-send echo).
func (m *Model) appendThreadPost(p *model.Post) {
	for _, ex := range m.threadPosts {
		if ex.Id != "" && ex.Id == p.Id {
			return
		}
	}
	for i := len(m.threadPosts) - 1; i >= 0; i-- {
		ex := m.threadPosts[i]
		if ex.Id == "" && ex.UserId == p.UserId && ex.Message == p.Message {
			m.threadPosts = append(m.threadPosts[:i], m.threadPosts[i+1:]...)
			break
		}
	}
	wasAtBottom := m.threadIdx >= len(m.threadPosts)-1
	m.threadPosts = append(m.threadPosts, p)
	if wasAtBottom {
		m.threadIdx = len(m.threadPosts) - 1
	}
}

func (m *Model) applyPostEdited(ev *model.WebSocketEvent) tea.Cmd {
	p := parsePost(ev)
	if p == nil {
		return nil
	}
	var cmd tea.Cmd
	if m.isCurrentChannel(p.ChannelId) {
		for i, ex := range m.posts {
			if ex.Id == p.Id {
				m.posts[i] = p
				m.renderMessages()
				if needsFileInfoFetch(p) {
					cmd = m.fetchFileInfos(p.Id)
				}
				break
			}
		}
	}
	if m.isThreadPost(p) {
		for i, ex := range m.threadPosts {
			if ex.Id == p.Id {
				m.threadPosts[i] = p
				m.renderThread()
				break
			}
		}
	}
	return cmd
}

// needsFileInfoFetch reports whether a post claims file attachments but
// arrived without resolved FileInfo metadata.
func needsFileInfoFetch(p *model.Post) bool {
	return len(p.FileIds) > 0 && (p.Metadata == nil || len(p.Metadata.Files) == 0)
}

func (m *Model) applyPostDeleted(ev *model.WebSocketEvent) {
	p := parsePost(ev)
	if p == nil {
		return
	}
	if m.isCurrentChannel(p.ChannelId) {
		for i, ex := range m.posts {
			if ex.Id == p.Id {
				m.posts = append(m.posts[:i], m.posts[i+1:]...)
				if i < m.postIdx {
					m.postIdx--
				}
				m.renderMessages()
				break
			}
		}
	}
	if m.isThreadPost(p) {
		// If the root itself was deleted, drop the whole sidebar — there's
		// nothing left to show.
		if p.Id == m.threadRootID {
			m.closeThread()
			return
		}
		for i, ex := range m.threadPosts {
			if ex.Id == p.Id {
				m.threadPosts = append(m.threadPosts[:i], m.threadPosts[i+1:]...)
				if i < m.threadIdx {
					m.threadIdx--
				}
				m.renderThread()
				break
			}
		}
	}
}

func (m *Model) isCurrentChannel(channelID string) bool {
	vis := m.visibleChannels()
	return m.channelIdx < len(vis) && vis[m.channelIdx].Id == channelID
}

// parsePost extracts and unmarshals the JSON-encoded post embedded in
// `posted` / `post_edited` / `post_deleted` event data.
func parsePost(ev *model.WebSocketEvent) *model.Post {
	raw, ok := ev.GetData()["post"].(string)
	if !ok || raw == "" {
		return nil
	}
	var p model.Post
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil
	}
	return &p
}

// wsMentions returns the set of user IDs explicitly mentioned in the
// event (Mattermost JSON-encodes the list into data["mentions"]).
func wsMentions(ev *model.WebSocketEvent) map[string]bool {
	out := map[string]bool{}
	raw, ok := ev.GetData()["mentions"].(string)
	if !ok || raw == "" {
		return out
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return out
	}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// wsBackoff returns the backoff delay for the n-th consecutive failure
// (1 → 1s, 2 → 2s, …, capped at 32s).
func wsBackoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	shift := n - 1
	if shift > 5 {
		shift = 5
	}
	return time.Second << shift
}

func isUnauthorized(err error) bool {
	s := err.Error()
	return strings.Contains(s, "401") || strings.Contains(strings.ToLower(s), "unauthorized")
}

// maybeFetchInitialPosts kicks off the first post fetch once both teams
// and channels have arrived. Returns nil if either is still pending.
func (m *Model) maybeFetchInitialPosts() tea.Cmd {
	if !m.teamsLoaded || !m.channelsLoaded {
		return nil
	}
	m.ensureSelection()
	vis := m.visibleChannels()
	if len(vis) == 0 {
		m.loading = false
		m.status = "no channels"
		return nil
	}
	if m.posts != nil {
		return nil
	}
	m.status = "loading messages…"
	return m.fetchPosts(vis[m.channelIdx].Id)
}

// ensureSelection clamps teamIdx/channelIdx to valid values given current
// teams + channels state. Picks the first team that has channels; falls
// back to DMs if no team channels exist.
func (m *Model) ensureSelection() {
	if len(m.teams) == 0 && !m.hasDMs {
		return
	}
	if m.teamIdx > m.maxTeamIdx() {
		m.teamIdx = 0
	}
	for tries := 0; tries <= m.maxTeamIdx(); tries++ {
		if len(m.channels[m.currentTeamID()]) > 0 {
			break
		}
		m.teamIdx++
		if m.teamIdx > m.maxTeamIdx() {
			m.teamIdx = 0
			break
		}
	}
	if m.channelIdx >= len(m.visibleChannels()) {
		m.channelIdx = 0
		m.chanOff = 0
	}
}

// maxTeamIdx returns the highest valid teamIdx, accounting for the
// synthetic DM tab (when present) and the always-present Unread tab.
func (m *Model) maxTeamIdx() int {
	n := len(m.teams)
	n++ // Unread is always present
	if m.hasDMs {
		n++
	}
	n--
	if n < 0 {
		n = 0
	}
	return n
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Filter mode and input mode each own most keys while active; check
	// before the global shortcuts so plain "q" / "/" / "esc" don't leak
	// through while the user is typing.
	if m.filterMode {
		return m.handleFilterKey(msg)
	}
	if m.focus == focusInput {
		return m.handleInputKey(msg)
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "q":
		if m.focus == focusChannels && m.filterValue != "" {
			// Don't quit while a filter is applied; let user clear with esc.
			return m, nil
		}
		return m, tea.Quit

	case "tab":
		return m.cycleFocus(1)
	case "shift+tab":
		return m.cycleFocus(-1)

	case "/":
		if m.focus == focusChannels {
			m.filterMode = true
			m.filter.SetValue(m.filterValue)
			m.filter.Focus()
			return m, nil
		}
	case "esc":
		if m.filterValue != "" {
			m.filterValue = ""
			m.filter.SetValue("")
			m.channelIdx = 0
			m.chanOff = 0
			return m, nil
		}
		if m.threadOpen {
			m.closeThread()
			return m, nil
		}
	}

	switch m.focus {
	case focusChannels:
		return m.handleChannelsKey(msg)
	case focusMessages:
		return m.handleMessagesKey(msg)
	case focusThread:
		return m.handleThreadKey(msg)
	case focusTeams:
		return m.handleTeamsKey(msg)
	}
	return m, nil
}

func (m Model) handleThreadKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.closeThread()
		return m, nil
	case "up", "k":
		if m.threadIdx > 0 {
			m.threadIdx--
			m.renderThread()
		}
		return m, nil
	case "down", "j":
		if m.threadIdx < len(m.threadPosts)-1 {
			m.threadIdx++
			m.renderThread()
		}
		return m, nil
	case "home", "g":
		m.threadIdx = 0
		m.renderThread()
		return m, nil
	case "end", "G":
		m.threadIdx = len(m.threadPosts) - 1
		m.renderThread()
		return m, nil
	case "o":
		if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
			m.status = "no message selected"
			return m, nil
		}
		opens := collectOpenables(m.threadPosts[m.threadIdx])
		if len(opens) == 0 {
			m.status = "nothing to open on this message"
			return m, nil
		}
		o := opens[0]
		m.status = "opening " + o.name + "…"
		return m, m.openOpenable(o)
	}
	var cmd tea.Cmd
	m.threadView, cmd = m.threadView.Update(msg)
	return m, cmd
}

// cycleFocus advances the active focus by `step` (typically +1 / -1)
// and syncs the input's bubble-level focus so its cursor blinks only
// while focused. focusThread is skipped when the sidebar is closed.
func (m Model) cycleFocus(step int) (tea.Model, tea.Cmd) {
	for i := 0; i < numFocus; i++ {
		m.focus = focus((int(m.focus) + step + numFocus) % numFocus)
		if m.focus == focusThread && !m.threadOpen {
			continue
		}
		break
	}
	var cmd tea.Cmd
	if m.focus == focusInput {
		cmd = m.input.Focus()
	} else {
		m.input.Blur()
	}
	// Bar visibility depends on whether messages pane has focus.
	m.renderMessages()
	m.renderThread()
	return m, cmd
}

func (m Model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// When the @-mention popup is open with at least one candidate, it
	// owns navigation/accept/dismiss keys before the normal input flow.
	if m.mention.active && len(m.mention.items) > 0 {
		switch msg.String() {
		case "up", "ctrl+p":
			if m.mention.idx > 0 {
				m.mention.idx--
			}
			return m, nil
		case "down", "ctrl+n":
			if m.mention.idx < len(m.mention.items)-1 {
				m.mention.idx++
			}
			return m, nil
		case "tab", "enter":
			if m.acceptMention() {
				return m, nil
			}
		case "esc":
			m.closeMention()
			return m, nil
		}
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "tab":
		return m.cycleFocus(1)
	case "shift+tab":
		return m.cycleFocus(-1)
	case "esc":
		m.closeMention()
		m.input.Blur()
		m.focus = focusMessages
		m.renderMessages()
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		// Replying inside an open thread targets the thread's channel
		// regardless of which channel is selected in the sidebar list —
		// otherwise switching channels while the thread is up would
		// silently send to the wrong place.
		var channelID, rootID string
		if m.threadOpen {
			channelID = m.threadChannelID
			rootID = m.threadRootID
		} else {
			vis := m.visibleChannels()
			if m.channelIdx >= len(vis) {
				return m, nil
			}
			channelID = vis[m.channelIdx].Id
		}
		m.input.Reset()
		m.closeMention()
		m.appendOptimistic(channelID, rootID, text)
		if !m.threadOpen {
			m.postIdx = len(m.posts) - 1
		}
		m.renderMessages()
		m.renderThread()
		m.status = "sending…"
		return m, m.sendMessage(channelID, rootID, text)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// After the textarea has consumed the keystroke, recompute mention
	// state from the new value + cursor position.
	mentionCmd := m.updateMention()
	return m, tea.Batch(cmd, mentionCmd)
}

func (m Model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filterMode = false
		m.filterValue = ""
		m.filter.SetValue("")
		m.filter.Blur()
		m.channelIdx = 0
		m.chanOff = 0
		return m, nil
	case "enter":
		m.filterMode = false
		m.filter.Blur()
		// Keep current filter applied. Selecting the highlighted channel:
		vis := m.visibleChannels()
		if len(vis) > 0 && m.channelIdx < len(vis) {
			m.status = "loading messages…"
			m.posts = nil
			m.renderMessages()
			return m, m.fetchPosts(vis[m.channelIdx].Id)
		}
		return m, nil
	case "up", "down":
		// Allow navigating the filtered list while still typing.
		_ = msg
		return m.handleChannelsKey(msg)
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	if v := m.filter.Value(); v != m.filterValue {
		m.filterValue = v
		m.channelIdx = 0
		m.chanOff = 0
	}
	return m, cmd
}

func (m Model) handleChannelsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	vis := m.visibleChannels()
	if len(vis) == 0 {
		return m, nil
	}
	// visibleChannels can shrink under a stale channelIdx — e.g. opening
	// the last entry on the Unread tab clears that channel's unread, and
	// arrow-navigating back to Unread doesn't reset the selection.
	if m.channelIdx >= len(vis) {
		m.channelIdx = len(vis) - 1
	}
	if m.channelIdx < 0 {
		m.channelIdx = 0
	}
	switch msg.String() {
	case "up", "k":
		if m.channelIdx > 0 {
			m.channelIdx--
		}
	case "down", "j":
		if m.channelIdx < len(vis)-1 {
			m.channelIdx++
		}
	case "home", "g":
		m.channelIdx = 0
	case "end", "G":
		m.channelIdx = len(vis) - 1
	case "enter":
		ch := vis[m.channelIdx]
		// When opening from the virtual Unread tab, hop to the channel's
		// home team so isCurrentChannel keeps tracking the open channel
		// after its unread count clears and it leaves the Unread list.
		if m.currentTeamID() == unreadTeamID {
			m.switchToChannelHomeTeam(ch)
			m.filterValue = ""
			m.filter.SetValue("")
		}
		m.status = "loading messages…"
		m.posts = nil
		m.renderMessages()
		return m, m.fetchPosts(ch.Id)
	}
	return m, nil
}

func (m Model) handleMessagesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.postIdx > 0 {
			m.postIdx--
			m.renderMessages()
		}
		return m, nil
	case "down", "j":
		if m.postIdx < len(m.posts)-1 {
			m.postIdx++
			m.renderMessages()
		}
		return m, nil
	case "home", "g":
		m.postIdx = 0
		m.renderMessages()
		return m, nil
	case "end", "G":
		m.postIdx = len(m.posts) - 1
		m.renderMessages()
		return m, nil
	case "o":
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			m.status = "no message selected"
			return m, nil
		}
		opens := collectOpenables(m.posts[m.postIdx])
		if len(opens) == 0 {
			m.status = "nothing to open on this message"
			return m, nil
		}
		o := opens[0]
		m.status = "opening " + o.name + "…"
		return m, m.openOpenable(o)
	case "enter":
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			return m, nil
		}
		return m.openThreadForPost(m.posts[m.postIdx])
	}
	// Anything else (pgup/pgdn, half-page, etc.) falls through to viewport.
	var cmd tea.Cmd
	m.msgsView, cmd = m.msgsView.Update(msg)
	return m, cmd
}

// openThreadForPost figures out which thread the selected post belongs
// to (its own Id if it's a root, otherwise its RootId) and opens the
// thread sidebar. Optimistic stubs (empty Id) are ignored.
func (m Model) openThreadForPost(p *model.Post) (tea.Model, tea.Cmd) {
	rootID := p.RootId
	if rootID == "" {
		rootID = p.Id
	}
	if rootID == "" {
		return m, nil // optimistic stub, no canonical Id yet
	}
	channelID := p.ChannelId
	// Same thread already open? Just refocus the sidebar.
	if m.threadOpen && m.threadRootID == rootID {
		m.focus = focusThread
		m.input.Blur()
		m.renderMessages()
		m.renderThread()
		return m, nil
	}
	m.threadOpen = true
	m.threadRootID = rootID
	m.threadChannelID = channelID
	m.threadPosts = nil
	m.threadIdx = 0
	m.threadLoading = true
	m.focus = focusThread
	m.input.Blur()
	m.input.Prompt = "↳ "
	m.resizeMessagesViewport()
	m.resizeInput()
	m.renderMessages()
	m.renderThread()
	return m, m.fetchThread(rootID)
}

// closeThread tears down the sidebar and returns focus to the messages pane.
func (m *Model) closeThread() {
	if !m.threadOpen {
		return
	}
	m.threadOpen = false
	m.threadRootID = ""
	m.threadChannelID = ""
	m.threadPosts = nil
	m.threadIdx = 0
	m.threadLoading = false
	if m.focus == focusThread {
		m.focus = focusMessages
	}
	m.input.Prompt = "> "
	m.resizeMessagesViewport()
	m.resizeInput()
	m.renderMessages()
}

func (m Model) handleTeamsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	max := m.maxTeamIdx()
	if max == 0 && len(m.teams) == 0 && !m.hasDMs {
		return m, nil
	}
	switch msg.String() {
	case "left", "h":
		if m.teamIdx > 0 {
			m.teamIdx--
		}
	case "right", "l":
		if m.teamIdx < max {
			m.teamIdx++
		}
	case "enter":
		m.focus = focusChannels
		m.posts = nil
		m.channelIdx = 0
		m.chanOff = 0
		m.filterValue = ""
		m.filter.SetValue("")
		m.renderMessages()
		vis := m.visibleChannels()
		if len(vis) == 0 {
			if m.currentTeamID() == unreadTeamID {
				m.status = "all caught up"
			} else {
				m.status = "no channels in this team"
			}
			return m, nil
		}
		m.status = "loading messages…"
		return m, m.fetchPosts(vis[m.channelIdx].Id)
	}
	return m, nil
}
