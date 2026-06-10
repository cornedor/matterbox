package ui

import "charm.land/bubbles/v2/key"

// keyMap holds every user-facing keybinding. Bindings are reused by both
// the help bubble (for rendering) and the focused handlers (for matching),
// so the rendered shortcuts and the actual behaviour can't drift apart.
type keyMap struct {
	// Pane navigation
	Tab      key.Binding
	ShiftTab key.Binding

	// List movement
	Up    key.Binding
	Down  key.Binding
	Left  key.Binding
	Right key.Binding
	Home  key.Binding
	End   key.Binding

	// Global sidebar navigation (works from any reading pane). ctrl+arrows
	// and ctrl+vim keys switch team/channel and open the target immediately.
	NavTeamPrev key.Binding
	NavTeamNext key.Binding
	NavChanPrev key.Binding
	NavChanNext key.Binding

	// Channels
	Filter      key.Binding
	ClearFilter key.Binding
	OpenChannel key.Binding

	// Search-result match cycling (messages pane)
	NextHit key.Binding
	PrevHit key.Binding

	// Message paging
	PageDown key.Binding
	PageUp   key.Binding

	// Messages / thread
	OpenThread    key.Binding
	ReplyInThread key.Binding
	OpenAttach    key.Binding
	CopyMD        key.Binding
	ShowHistory   key.Binding
	EditPost      key.Binding
	DeletePost    key.Binding
	React         key.Binding
	CloseThread   key.Binding

	// Feed
	MarkRead key.Binding
	Refresh  key.Binding

	// Attachments (input + chip strip)
	Paste        key.Binding
	AttachRemove key.Binding

	// Teams
	SwitchTeam    key.Binding
	LoadTeam      key.Binding
	MoveTeamLeft  key.Binding
	MoveTeamRight key.Binding

	// Input / filter modes
	Compose    key.Binding
	Send       key.Binding
	NewLine    key.Binding
	LeaveInput key.Binding
	ApplyOpen  key.Binding
	CancelEdit key.Binding

	// Global
	Switcher   key.Binding
	Search     key.Binding
	SearchHere key.Binding
	Leader     key.Binding
	Unread     key.Binding
	Feed       key.Binding
	Help       key.Binding
	Quit       key.Binding
}

// newKeyMap builds the keymap. ctrlArrowNav toggles the ctrl+arrow aliases for
// sidebar navigation: when false, only the ctrl+vim keys (ctrl+h/j/k/l) move
// teams/channels, leaving ctrl+arrows free for the composer's word-jump.
func newKeyMap(ctrlArrowNav bool) keyMap {
	// Sidebar-nav keys: ctrl+vim letters are always bound; the ctrl+arrow
	// aliases are prepended (and shown in help) only when enabled.
	teamPrev, teamNext := []string{"ctrl+h"}, []string{"ctrl+l"}
	chanPrev, chanNext := []string{"ctrl+k"}, []string{"ctrl+j"}
	teamPrevHelp, teamNextHelp := "ctrl+h", "ctrl+l"
	chanPrevHelp, chanNextHelp := "ctrl+k", "ctrl+j"
	if ctrlArrowNav {
		teamPrev = append([]string{"ctrl+left"}, teamPrev...)
		teamNext = append([]string{"ctrl+right"}, teamNext...)
		chanPrev = append([]string{"ctrl+up"}, chanPrev...)
		chanNext = append([]string{"ctrl+down"}, chanNext...)
		teamPrevHelp, teamNextHelp = "ctrl+←/h", "ctrl+→/l"
		chanPrevHelp, chanNextHelp = "ctrl+↑/k", "ctrl+↓/j"
	}
	return keyMap{
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "focus next"),
		),
		ShiftTab: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("shift+tab", "focus prev"),
		),

		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "down"),
		),
		Left: key.NewBinding(
			key.WithKeys("left", "h"),
			key.WithHelp("←/h", "left"),
		),
		Right: key.NewBinding(
			key.WithKeys("right", "l"),
			key.WithHelp("→/l", "right"),
		),
		Home: key.NewBinding(
			key.WithKeys("home", "g"),
			key.WithHelp("g", "top"),
		),
		End: key.NewBinding(
			key.WithKeys("end", "G"),
			key.WithHelp("G", "bottom"),
		),

		NavTeamPrev: key.NewBinding(
			key.WithKeys(teamPrev...),
			key.WithHelp(teamPrevHelp, "prev team"),
		),
		NavTeamNext: key.NewBinding(
			key.WithKeys(teamNext...),
			key.WithHelp(teamNextHelp, "next team"),
		),
		NavChanPrev: key.NewBinding(
			key.WithKeys(chanPrev...),
			key.WithHelp(chanPrevHelp, "prev channel"),
		),
		NavChanNext: key.NewBinding(
			key.WithKeys(chanNext...),
			key.WithHelp(chanNextHelp, "next channel"),
		),

		Filter: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "filter"),
		),
		ClearFilter: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "clear filter"),
		),
		OpenChannel: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("↵", "open"),
		),

		NextHit: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "next match"),
		),
		PrevHit: key.NewBinding(
			key.WithKeys("N"),
			key.WithHelp("N", "prev match"),
		),

		PageDown: key.NewBinding(
			key.WithKeys("pgdown", "ctrl+d"),
			key.WithHelp("ctrl+d", "page down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup", "ctrl+u"),
			key.WithHelp("ctrl+u", "page up"),
		),

		OpenThread: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("↵", "open thread"),
		),
		ReplyInThread: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "reply in thread"),
		),
		OpenAttach: key.NewBinding(
			key.WithKeys("o"),
			key.WithHelp("o", "open attachment/link"),
		),
		CopyMD: key.NewBinding(
			key.WithKeys("y"),
			key.WithHelp("y", "copy markdown"),
		),
		ShowHistory: key.NewBinding(
			key.WithKeys("alt+e"),
			key.WithHelp("alt+e", "edit history"),
		),
		EditPost: key.NewBinding(
			key.WithKeys("e"),
			key.WithHelp("e", "edit message"),
		),
		DeletePost: key.NewBinding(
			key.WithKeys("D"),
			key.WithHelp("D", "delete message"),
		),
		React: key.NewBinding(
			key.WithKeys("R"),
			key.WithHelp("R", "react"),
		),
		CloseThread: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "close thread"),
		),

		MarkRead: key.NewBinding(
			key.WithKeys("m"),
			key.WithHelp("m", "mark read"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "refresh"),
		),

		Paste: key.NewBinding(
			key.WithKeys("ctrl+v"),
			key.WithHelp("ctrl+v", "paste"),
		),
		AttachRemove: key.NewBinding(
			key.WithKeys("d", "x"),
			key.WithHelp("d/x", "remove"),
		),

		SwitchTeam: key.NewBinding(
			key.WithKeys("left", "right", "h", "l"),
			key.WithHelp("←/→", "switch"),
		),
		LoadTeam: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("↵", "load"),
		),
		MoveTeamLeft: key.NewBinding(
			key.WithKeys("<"),
			key.WithHelp("<", "move team left"),
		),
		MoveTeamRight: key.NewBinding(
			key.WithKeys(">"),
			key.WithHelp(">", "move team right"),
		),

		Compose: key.NewBinding(
			key.WithKeys("i"),
			key.WithHelp("i", "compose"),
		),
		Send: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("↵", "send"),
		),
		NewLine: key.NewBinding(
			key.WithKeys("alt+enter", "ctrl+j", "shift+enter"),
			key.WithHelp("alt+↵", "newline"),
		),
		LeaveInput: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "leave"),
		),
		ApplyOpen: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("↵", "apply + open"),
		),
		CancelEdit: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),

		Switcher: key.NewBinding(
			key.WithKeys("ctrl+p"),
			key.WithHelp("ctrl+p", "switch channel"),
		),
		Search: key.NewBinding(
			key.WithKeys("F"),
			key.WithHelp("F", "search all"),
		),
		SearchHere: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "search channel"),
		),
		Leader: key.NewBinding(
			key.WithKeys(",", "ctrl+w"),
			key.WithHelp(",", "go to…"),
		),
		Unread: key.NewBinding(
			key.WithKeys("u"),
			key.WithHelp("u", "jump to unread"),
		),
		Feed: key.NewBinding(
			key.WithKeys("U"),
			key.WithHelp("U", "unread feed"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}
