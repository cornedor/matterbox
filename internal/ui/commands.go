package ui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
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
			name:           "Index this channel until X days ago",
			desc:           "fetch & cache history back N days into the local DB",
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
	}
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

// commandResults returns commands matching the current query, in their
// registered order. Empty query lists everything.
func (m Model) commandResults() []switcherCommand {
	all := builtinCommands()
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
	vis := m.visibleChannels()
	if m.channelIdx < 0 || m.channelIdx >= len(vis) {
		return "", ""
	}
	c := vis[m.channelIdx]
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
