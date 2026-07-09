package ui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
)

// switcherCommand is a > command listed inside the ctrl+k modal when
// the user types ">" as the first character of the query. Each command
// optionally prompts for a single argument before running.
type switcherCommand struct {
	name           string
	desc           string
	argPrompt      string // non-empty → switcher swaps to a captive arg input
	argPlaceholder string
	// run is invoked after the argument is collected. arg == "" when the
	// command has no argPrompt. The command may mutate the model via the
	// pointer and return a Cmd for any async work.
	run func(m *Model, arg string) tea.Cmd
}

// builtinCommands returns the registered > commands in display order.
func builtinCommands() []switcherCommand {
	return []switcherCommand{
		{
			name: "Summarize recent messages",
			desc: "summarize this channel (or the unread feed) with a local LLM",
			// No argPrompt: the command opens its own duration picker (or, on
			// the Feed tab, summarizes all unread messages straight away).
			run: runSummarize,
		},
		{
			name: "Create channel",
			desc: "make a new public or private channel on one of your teams",
			// No argPrompt: the command opens its own form modal.
			run: runCreateChannel,
		},
		{
			name:           "Start group DM",
			desc:           "open (creating if new) a group DM with the people you name",
			argPrompt:      "users: ",
			argPlaceholder: "@alice, @bob  (2–7 people)",
			run:            runStartGroupDM,
		},
		{
			name: "Keys",
			desc: "keyboard shortcuts cheatsheet (your effective bindings)",
			run:  runShowKeys,
		},
		{
			name: "Status: online",
			desc: "set your presence to online",
			run:  runSetPresence(model.StatusOnline),
		},
		{
			name: "Status: away",
			desc: "set your presence to away",
			run:  runSetPresence(model.StatusAway),
		},
		{
			name: "Status: dnd (do not disturb)",
			desc: "set your presence to do not disturb",
			run:  runSetPresence(model.StatusDnd),
		},
		{
			name: "Status: offline",
			desc: "appear offline to others",
			run:  runSetPresence(model.StatusOffline),
		},
		{
			name:           "Set custom status",
			desc:           "emoji + text shown next to your name",
			argPrompt:      "custom status: ",
			argPlaceholder: ":palm_tree: on vacation  (empty clears)",
			run:            runSetCustomStatus,
		},
		{
			name: "Clear custom status",
			desc: "remove your custom status emoji + text",
			run:  runClearCustomStatus,
		},
		{
			name:           "Index channel",
			desc:           "cache N days of history to the local DB",
			argPrompt:      "days: ",
			argPlaceholder: "30",
			run:            runIndexChannel,
		},
		{
			name:           "Typing animation",
			desc:           "fake live-typing a message via a stream of edits (socket test)",
			argPrompt:      "message: ",
			argPlaceholder: "hello world",
			run:            runTypingAnimation,
		},
		{
			name:           "Bouncing ball",
			desc:           "animate a ball inside a code-block box (socket test)",
			argPrompt:      "duration(s) fps: ",
			argPlaceholder: "8 30",
			run:            runBouncingBall,
		},
		{
			name: "Debug: key inspector",
			desc: "echo the raw key events the terminal sends (diagnose option/ctrl/shift+arrow)",
			run:  runKeyDebug,
		},
		{
			name: "Debug: Copy message ID",
			desc: "copy the ID of the currently selected message to the clipboard",
			run:  runCopyMessageID,
		},
		{
			name: "Debug: Copy channel ID",
			desc: "copy the ID of the currently open channel to the clipboard",
			run:  runCopyChannelID,
		},
	}
}

// runKeyDebug opens the key-inspector popup (see keydebug.go), which echoes the
// decoded key events the terminal delivers — useful for diagnosing why a
// modifier+arrow binding (e.g. nav_modifier: alt) never fires: you can see
// whether option+arrow arrives as "alt+up", as a bare "up", or not at all.
func runKeyDebug(m *Model, _ string) tea.Cmd {
	m.openKeyDebug()
	return nil
}

// runCopyMessageID copies the ID of the currently selected post to the
// system clipboard. It respects focus: a selected thread reply takes
// precedence over the main message list.
func runCopyMessageID(m *Model, _ string) tea.Cmd {
	var p *model.Post
	switch m.focus {
	case focusThread:
		if m.threadIdx >= 0 && m.threadIdx < len(m.threadPosts) {
			p = m.threadPosts[m.threadIdx]
		}
	case focusMessages:
		if m.postIdx >= 0 && m.postIdx < len(m.posts) {
			p = m.posts[m.postIdx]
		}
	}
	if p == nil {
		m.status = "no message selected"
		return nil
	}
	return m.copyText(p.Id, "message ID")
}

// runCopyChannelID copies the ID of the currently open channel to the
// system clipboard. If no channel is open (e.g. Feed/Search tab), it shows
// a status message instead.
func runCopyChannelID(m *Model, _ string) tea.Cmd {
	if m.openChannelID == "" {
		m.status = "no channel open"
		return nil
	}
	return m.copyText(m.openChannelID, "channel ID")
}

// runShowKeys opens the keyboard cheatsheet popup (see cheatsheet.go). The
// switcher has already closed itself, so this just raises the popup.
func runShowKeys(m *Model, _ string) tea.Cmd {
	m.openKeysSheet()
	return nil
}

// groupDMResolvedMsg carries the result of resolving a user list into a DM /
// group-DM channel (ch is nil when err is set). message, when non-empty, is sent
// to the channel after it's opened — used by the /dm slash command's trailing
// text; the ">" palette and info-panel callers leave it empty.
type groupDMResolvedMsg struct {
	ch      *model.Channel
	err     error
	message string
}

// runStartGroupDM resolves the comma-separated @usernames the user typed into
// a DM (one other) or group-DM (2–7 others) channel, creating it on the
// server if it doesn't exist yet. Resolution touches the network, so it runs
// in the returned Cmd; applyGroupDMResolved then switches to the channel.
func runStartGroupDM(m *Model, arg string) tea.Cmd {
	spec := strings.TrimSpace(arg)
	if spec == "" {
		m.status = "group DM: name at least one user (e.g. @alice, @bob)"
		return nil
	}
	if m.me == nil {
		m.status = "group DM: user not loaded yet"
		return nil
	}
	m.status = "opening group DM…"
	client, ctx, meID := m.client, m.ctx, m.me.Id
	return func() tea.Msg {
		ch, err := mm.ResolveRecipients(ctx, client, meID, spec)
		return groupDMResolvedMsg{ch: ch, err: err}
	}
}

// applyGroupDMResolved switches to the channel produced by "Start group DM".
// A freshly-created group DM isn't in the sidebar yet (matterbox has no
// WebSocket handler for channel-added events), so it's inserted into the DM
// bucket before the jump so the row is selectable. The composer takes focus,
// matching the switcher's "jump there and start typing" behaviour.
func (m Model) applyGroupDMResolved(msg groupDMResolvedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.status = "group DM: " + msg.err.Error()
		return m, nil
	}
	ch := msg.ch
	if m.findChannel(ch.Id) == nil {
		m.channels[dmTeamID] = append(m.channels[dmTeamID], ch)
		m.hasDMs = true
		m.sortDMBucket()
	}
	m.switchToChannelHomeTeam(ch)
	m.filterValue = ""
	m.filter.SetValue("")
	m.focus = focusInput
	m.status = ""
	// openChannelLoadCmd sets m.openChannelID synchronously, so a trailing /dm
	// message targets the freshly-opened channel.
	cmds := []tea.Cmd{m.input.Focus(), m.openChannelLoadCmd(ch.Id), m.bumpChannelStat(ch.Id)}
	if msg.message != "" {
		cmds = append(cmds, m.sendMessage(ch.Id, "", msg.message, nil))
	}
	return m, tea.Batch(cmds...)
}

func runIndexChannel(m *Model, arg string) tea.Cmd {
	days, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || days <= 0 {
		m.status = "indexer: needs a positive number of days"
		return nil
	}
	channelID, label := m.indexTargetChannel()
	if channelID == "" {
		m.status = "indexer: no channel selected"
		return nil
	}
	return m.startIndexer(channelID, label, days)
}

// runSetPresence returns a command runner that sets the user's own
// presence. The footer dot is updated optimistically; the server's
// status_change WS event confirms (or corrects) shortly after.
func runSetPresence(status string) func(*Model, string) tea.Cmd {
	return func(m *Model, _ string) tea.Cmd {
		if m.me == nil {
			m.status = "status: user not loaded yet"
			return nil
		}
		m.statuses[m.me.Id] = status
		m.status = "presence → " + status
		id := m.me.Id
		client, ctx := m.client, m.ctx
		return func() tea.Msg {
			if err := client.UpdateStatus(ctx, id, status); err != nil {
				return errMsg{err}
			}
			return nil
		}
	}
}

// parseCustomStatusArg splits an optional leading :shortcode: emoji off
// the custom-status text. No emoji prefix falls back to speech_balloon,
// matching the web app's default.
func parseCustomStatusArg(arg string) (emojiName, text string) {
	if strings.HasPrefix(arg, ":") {
		if end := strings.Index(arg[1:], ":"); end > 0 {
			return arg[1 : end+1], strings.TrimSpace(arg[end+2:])
		}
	}
	return "speech_balloon", arg
}

func runSetCustomStatus(m *Model, arg string) tea.Cmd {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return runClearCustomStatus(m, "")
	}
	if m.me == nil {
		m.status = "status: user not loaded yet"
		return nil
	}
	emojiName, text := parseCustomStatusArg(arg)
	cs := model.CustomStatus{Emoji: emojiName, Text: text}
	m.customStatuses[m.me.Id] = cs
	m.status = "custom status: " + strings.TrimSpace(m.renderEmojiGlyph(emojiName)+" "+text)
	id := m.me.Id
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		if err := client.UpdateCustomStatus(ctx, id, &cs); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

func runClearCustomStatus(m *Model, _ string) tea.Cmd {
	if m.me == nil {
		m.status = "status: user not loaded yet"
		return nil
	}
	delete(m.customStatuses, m.me.Id)
	m.status = "custom status cleared"
	id := m.me.Id
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		if err := client.ClearCustomStatus(ctx, id); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// muteCommand returns the mute/unmute toggle for the channel that was open
// when the switcher was raised, plus whether one applies (there's nothing to
// mute on the Feed/Search tabs). The label and action flip with the channel's
// current mute state.
func (m Model) muteCommand() (switcherCommand, bool) {
	c := m.findChannel(m.openChannelID)
	if c == nil {
		return switcherCommand{}, false
	}
	label := m.channelLabel(c)
	if m.channelMuted(c.Id) {
		return switcherCommand{
			name: "Unmute " + label,
			desc: "restore notifications and let it back into the unread feed",
			run:  runSetMuted(c.Id, false),
		}, true
	}
	return switcherCommand{
		name: "Mute " + label,
		desc: "silence notifications and hide it from the unread feed",
		run:  runSetMuted(c.Id, true),
	}, true
}

// runSetMuted returns a runner that mutes/unmutes the given channel for the
// current user. The cached member flips optimistically so the feed filter and
// the command label update immediately; the server patch follows async.
func runSetMuted(channelID string, muted bool) func(*Model, string) tea.Cmd {
	return func(m *Model, _ string) tea.Cmd {
		if m.me == nil {
			m.status = "mute: user not loaded yet"
			return nil
		}
		m.setChannelMutedLocal(channelID, muted)
		label := channelID
		if c := m.findChannel(channelID); c != nil {
			label = m.channelLabel(c)
		}
		if muted {
			m.status = "muted " + label
		} else {
			m.status = "unmuted " + label
		}
		userID := m.me.Id
		client, ctx := m.client, m.ctx
		return func() tea.Msg {
			if err := client.SetChannelMuted(ctx, userID, channelID, muted); err != nil {
				return errMsg{err}
			}
			return nil
		}
	}
}

// setChannelMutedLocal flips the cached member's mute state so the feed filter
// (channelMuted) and the command label reflect the change before the server
// confirms. No-op when the channel has no cached member row.
func (m *Model) setChannelMutedLocal(channelID string, muted bool) {
	level := model.ChannelMarkUnreadAll
	if muted {
		level = model.ChannelMarkUnreadMention
	}
	for i := range m.members {
		if m.members[i].ChannelId != channelID {
			continue
		}
		if m.members[i].NotifyProps == nil {
			m.members[i].NotifyProps = model.StringMap{}
		}
		m.members[i].NotifyProps[model.MarkUnreadNotifyProp] = level
		// Keep the derived muted set (channelMuted's O(1) source) in step with
		// the member we just flipped.
		if m.mutedChannels == nil {
			m.mutedChannels = map[string]bool{}
		}
		if muted {
			m.mutedChannels[channelID] = true
		} else {
			delete(m.mutedChannels, channelID)
		}
		return
	}
}

// inCommandMode reports whether the switcher value has the > prefix.
func (m Model) inCommandMode() bool {
	return strings.HasPrefix(m.switcher.Value(), ">")
}

// inCommandArgMode reports whether the switcher is awaiting a captive
// argument for a previously-selected command.
func (m Model) inCommandArgMode() bool {
	return m.switcherCmdPending != nil
}

// commandQuery returns the filter substring after ">" (trimmed of
// surrounding whitespace).
func (m Model) commandQuery() string {
	v := m.switcher.Value()
	if !strings.HasPrefix(v, ">") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(v, ">"))
}

// allCommands is the full ordered command list for the current context: the
// static builtins plus any channel-specific commands (the mute toggle, adding
// members) that only apply when a channel is open. Those are clustered next to
// Summarize at the top, since they all act on the currently-open channel.
func (m Model) allCommands() []switcherCommand {
	base := builtinCommands()
	var contextual []switcherCommand
	if mute, ok := m.muteCommand(); ok {
		contextual = append(contextual, mute)
	}
	if add, ok := m.addMembersCommand(); ok {
		contextual = append(contextual, add)
	}
	if len(contextual) == 0 {
		return base
	}
	out := make([]switcherCommand, 0, len(base)+len(contextual))
	if len(base) > 0 {
		out = append(out, base[0]) // Summarize stays first
	}
	out = append(out, contextual...)
	if len(base) > 1 {
		out = append(out, base[1:]...)
	}
	return out
}

// commandResults returns commands matching the current query, in their
// registered order. Empty query lists everything.
func (m Model) commandResults() []switcherCommand {
	all := m.allCommands()
	q := strings.ToLower(m.commandQuery())
	if q == "" {
		return all
	}
	out := make([]switcherCommand, 0, len(all))
	for _, c := range all {
		if strings.Contains(strings.ToLower(c.name), q) {
			out = append(out, c)
		}
	}
	return out
}

// indexTargetChannel returns the channel ID and label of the currently-
// focused channel (i.e. the one open when ctrl+k was pressed).
func (m Model) indexTargetChannel() (string, string) {
	c := m.findChannel(m.openChannelID)
	if c == nil {
		return "", ""
	}
	return c.Id, m.channelLabel(c)
}

// enterCommandArgMode transitions the switcher from the command list to
// a captive arg-prompt for the selected command. Caller is responsible
// for resetting switcherIdx.
func (m *Model) enterCommandArgMode(cmd switcherCommand) {
	cp := cmd // capture local copy so the pointer stays stable
	m.switcherCmdPending = &cp
	m.switcher.SetValue("")
	m.switcher.Prompt = cmd.argPrompt
	m.switcher.Placeholder = cmd.argPlaceholder
	m.switcherIdx = 0
}

// leaveCommandArgMode returns from the arg-prompt back to the command
// list, restoring the ">" prefix so the same modal shows the commands.
func (m *Model) leaveCommandArgMode() {
	m.switcherCmdPending = nil
	m.switcher.Prompt = ""
	m.switcher.Placeholder = "switch to channel or > for commands…"
	m.switcher.SetValue(">")
	m.switcherIdx = 0
}

// syncSwitcherPrompt keeps the textinput's prompt in sync with the
// current mode so the visible characters are exactly what the user
// typed (no double-"> " when in command mode).
func (m *Model) syncSwitcherPrompt() {
	if m.switcherCmdPending != nil {
		return // captive arg-prompt owns the prompt
	}
	if strings.HasPrefix(m.switcher.Value(), ">") {
		m.switcher.Prompt = ""
	} else {
		m.switcher.Prompt = "> "
	}
}
