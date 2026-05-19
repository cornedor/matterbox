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

	// Channels
	Filter      key.Binding
	ClearFilter key.Binding
	OpenChannel key.Binding

	// Messages / thread
	OpenThread  key.Binding
	OpenAttach  key.Binding
	CopyMD      key.Binding
	CloseThread key.Binding

	// Attachments (input + chip strip)
	Paste        key.Binding
	AttachRemove key.Binding

	// Teams
	SwitchTeam key.Binding
	LoadTeam   key.Binding

	// Input / filter modes
	Send       key.Binding
	NewLine    key.Binding
	LeaveInput key.Binding
	ApplyOpen  key.Binding
	CancelEdit key.Binding

	// Global
	Switcher key.Binding
	Unread   key.Binding
	Help     key.Binding
	Quit     key.Binding
}

func newKeyMap() keyMap {
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

		Filter: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "filter"),
		),
		ClearFilter: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "clear filter"),
		),
		OpenChannel: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("↵", "open"),
		),

		OpenThread: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("↵", "open thread"),
		),
		OpenAttach: key.NewBinding(
			key.WithKeys("o", "enter"),
			key.WithHelp("o/↵", "open attachment"),
		),
		CopyMD: key.NewBinding(
			key.WithKeys("y"),
			key.WithHelp("y", "copy markdown"),
		),
		CloseThread: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "close thread"),
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
			key.WithKeys("ctrl+k"),
			key.WithHelp("ctrl+k", "switch channel"),
		),
		Unread: key.NewBinding(
			key.WithKeys("u"),
			key.WithHelp("u", "jump to unread"),
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
