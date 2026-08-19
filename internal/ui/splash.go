package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mattermost/mattermost/server/public/model"
)

// Startup splash.
//
// Until the first transcript is on screen there is nothing for the real layout
// to draw: no team tabs, an empty sidebar, an empty messages pane, a footer
// full of keys that don't do anything yet. It paints as a half-drawn frame
// that pops into shape one fetch at a time, which reads as a broken layout
// rather than as loading. The splash covers exactly that window with a blank
// screen and a short progress list — the step being waited on dead centre, the
// finished ones stacked above it and fading out — and hands over to the real
// UI the moment the first channel is ready.
//
// It is startup-only: the zero splashState is inactive, so a Model built as a
// literal (every test that does) never sees it.

// splashKey identifies a startup step. The order of the constants is the order
// the steps are shown in; the current step is the first one still pending.
type splashKey int

const (
	splashConnect splashKey = iota
	splashAccount
	splashTeams
	splashChannels
	splashUnread
	splashOpen
)

const (
	// splashFrameInterval paces the spinner on the current step. The tick chain
	// stops as soon as the splash does, so it costs nothing after startup.
	splashFrameInterval = 100 * time.Millisecond
	// splashTimeout gives up on the splash even if a fetch never lands, so an
	// unreachable server shows the normal UI (and its status line) rather than
	// spinning forever.
	splashTimeout = 20 * time.Second
	// splashMaxDone caps how many finished steps stay on screen above the
	// current one. There are fewer steps than that today; the cap is what keeps
	// the stack off the top edge if more are ever added.
	splashMaxDone = 6
)

var splashFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var (
	// splashCurrentStyle is the step being waited on: the only line at full
	// contrast.
	splashCurrentStyle = lipgloss.NewStyle().Foreground(adaptiveColor{
		light: lipgloss.Color("236"), dark: lipgloss.Color("252"),
	})
	// splashDoneStyles walk a finished step further into the background the
	// longer ago it finished; the last entry holds for anything older. The
	// range is deliberately shallow — the oldest line should read as settled,
	// not as unreadable.
	splashDoneStyles = []lipgloss.Style{
		lipgloss.NewStyle().Foreground(adaptiveColor{light: lipgloss.Color("244"), dark: lipgloss.Color("246")}),
		lipgloss.NewStyle().Foreground(adaptiveColor{light: lipgloss.Color("246"), dark: lipgloss.Color("243")}),
		lipgloss.NewStyle().Foreground(adaptiveColor{light: lipgloss.Color("248"), dark: lipgloss.Color("241")}),
		lipgloss.NewStyle().Foreground(adaptiveColor{light: lipgloss.Color("249"), dark: lipgloss.Color("240")}),
	}
)

type splashTickMsg struct{}
type splashTimeoutMsg struct{}

type splashStep struct {
	key   splashKey
	label string
	done  bool
}

// splashState is the startup progress list. done records the steps in the
// order they *finished* rather than in step order, so the stack above the
// current line only ever grows — the fetches run concurrently and land out of
// order, and re-sorting them would make finished lines jump around.
type splashState struct {
	active bool
	frame  int
	steps  []splashStep
	done   []string
}

// newSplashState builds the startup step list. server is the configured
// Mattermost URL, shown by host so the first line says which server is being
// waited on; it may be empty.
func newSplashState(server string) splashState {
	connect := "connecting"
	if h := splashHost(server); h != "" {
		connect = "connecting to " + h
	}
	return splashState{
		active: true,
		steps: []splashStep{
			{key: splashConnect, label: connect},
			{key: splashAccount, label: "signing in"},
			{key: splashTeams, label: "loading teams"},
			{key: splashChannels, label: "loading channels"},
			{key: splashUnread, label: "reading unread state"},
			{key: splashOpen, label: "opening the last channel"},
		},
	}
}

// splashHost reduces a configured server URL to its host, the only part worth
// a line on screen. Deliberately string surgery rather than url.Parse: a
// half-typed config value should degrade to "no host" instead of an error.
func splashHost(server string) string {
	s := strings.TrimSpace(server)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.TrimSuffix(s, "/")
	if i := strings.IndexAny(s, "/:"); i >= 0 {
		s = s[:i]
	}
	return s
}

// finish marks a step done and moves it onto the finished stack. Finishing the
// last pending step ends the splash.
func (s *splashState) finish(key splashKey) {
	if !s.active {
		return
	}
	pending := false
	for i := range s.steps {
		if s.steps[i].key == key && !s.steps[i].done {
			s.steps[i].done = true
			s.done = append(s.done, s.steps[i].label)
		}
		pending = pending || !s.steps[i].done
	}
	if !pending {
		s.stop()
	}
}

// relabel rewrites a pending step's text — the open step only learns which
// channel it is opening once the channel list has arrived.
func (s *splashState) relabel(key splashKey, label string) {
	if !s.active {
		return
	}
	for i := range s.steps {
		if s.steps[i].key == key && !s.steps[i].done {
			s.steps[i].label = label
			return
		}
	}
}

// stop tears the splash down and releases its state, whether the startup
// finished, failed, or timed out.
func (s *splashState) stop() {
	s.active = false
	s.steps = nil
	s.done = nil
}

// current returns the label of the step being waited on.
func (s *splashState) current() (string, bool) {
	for i := range s.steps {
		if !s.steps[i].done {
			return s.steps[i].label, true
		}
	}
	return "", false
}

// splashTickCmd advances the spinner on the current step.
func splashTickCmd() tea.Cmd {
	return tea.Tick(splashFrameInterval, func(time.Time) tea.Msg { return splashTickMsg{} })
}

// splashTimeoutCmd arms the give-up backstop (see splashTimeout).
func splashTimeoutCmd() tea.Cmd {
	return tea.Tick(splashTimeout, func(time.Time) tea.Msg { return splashTimeoutMsg{} })
}

// splashSettle ends the splash once the first transcript is on screen. m.loading
// is the one signal every open path clears — the warm cache render, the server
// fetch, "no channels", and the error path all go through it — so watching it
// from the Update wrapper saves each of them from remembering.
func (m *Model) splashSettle() {
	if m.splash.active && !m.loading {
		m.splash.stop()
	}
}

// splashOpening names the channel the startup open is waiting on, so the last
// line reads "opening #general" instead of "opening the last channel".
func (m *Model) splashOpening(c *model.Channel) {
	if !m.splash.active || c == nil {
		return
	}
	m.splash.relabel(splashOpen, "opening "+m.channelLabel(c))
}

// renderSplash paints the whole screen: the current step centred, the finished
// ones stacked above it, everything else blank.
func (m *Model) renderSplash() string {
	cur, ok := m.splash.current()
	if !ok {
		return ""
	}
	// The current step sits on the centre row; the stack above it can only be
	// as tall as the rows there actually are.
	center := (m.height - 1) / 2
	if center < 0 {
		center = 0
	}
	done := m.splash.done
	if len(done) > splashMaxDone {
		done = done[len(done)-splashMaxDone:]
	}
	if len(done) > center {
		done = done[len(done)-center:]
	}

	rows := make([]string, 0, m.height)
	for i := center - len(done); i > 0; i-- {
		rows = append(rows, "")
	}
	for i, label := range done {
		age := len(done) - 1 - i // 0 = most recently finished
		if age >= len(splashDoneStyles) {
			age = len(splashDoneStyles) - 1
		}
		rows = append(rows, m.splashLine(splashDoneStyles[age], "✓ "+label))
	}
	rows = append(rows, m.splashLine(splashCurrentStyle, splashFrames[m.splash.frame%len(splashFrames)]+" "+cur))
	for len(rows) < m.height {
		rows = append(rows, "")
	}
	return strings.Join(rows[:m.height], "\n")
}

func (m *Model) splashLine(style lipgloss.Style, text string) string {
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, style.Render(text))
}
