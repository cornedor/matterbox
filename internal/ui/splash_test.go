package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"
)

// splashRows returns the non-blank, unstyled rows of the rendered splash.
func splashRows(t *testing.T, m *Model) []string {
	t.Helper()
	var rows []string
	for _, line := range strings.Split(m.renderSplash(), "\n") {
		if s := strings.TrimRight(ansi.Strip(line), " "); strings.TrimSpace(s) != "" {
			rows = append(rows, s)
		}
	}
	return rows
}

func TestSplashCoversTheStartupLayout(t *testing.T) {
	m := New(nil, nil)
	m.width, m.height = 100, 24
	if !m.splash.active {
		t.Fatal("New() did not arm the startup splash")
	}
	screen := ansi.Strip(m.viewContent())
	// The half-drawn real layout is what the splash exists to hide.
	for _, unwanted := range []string{"(no teams)", "Unread Feed", "│", "─"} {
		if strings.Contains(screen, unwanted) {
			t.Errorf("splash screen leaks the real layout (%q):\n%s", unwanted, screen)
		}
	}
	if !strings.Contains(screen, "connecting") {
		t.Errorf("splash does not show the first step:\n%s", screen)
	}
}

func TestSplashCentersCurrentStepAndStacksFinishedAbove(t *testing.T) {
	m := New(nil, nil)
	m.width, m.height = 100, 25
	m.splash.finish(splashConnect)
	m.splash.finish(splashAccount)

	lines := strings.Split(m.renderSplash(), "\n")
	if len(lines) != m.height {
		t.Fatalf("splash is %d rows; want %d", len(lines), m.height)
	}
	center := (m.height - 1) / 2
	cur := strings.TrimSpace(ansi.Strip(lines[center]))
	if !strings.HasSuffix(cur, "loading teams") {
		t.Errorf("centre row = %q; want the current step", cur)
	}
	// The current step is horizontally centred too.
	full := ansi.Strip(lines[center])
	lead := len(full) - len(strings.TrimLeft(full, " "))
	trail := len(strings.TrimRight(full, " "))
	if got := m.width - trail - lead; got < -1 || got > 1 {
		t.Errorf("centre row is off-centre by %d cells: %q", got, full)
	}
	if got := strings.TrimSpace(ansi.Strip(lines[center-1])); got != "✓ signing in" {
		t.Errorf("row above centre = %q; want the last finished step", got)
	}
	if got := strings.TrimSpace(ansi.Strip(lines[center-2])); !strings.HasPrefix(got, "✓ connecting") {
		t.Errorf("second row above centre = %q; want the first finished step", got)
	}
	// Nothing below the current step.
	for i := center + 1; i < len(lines); i++ {
		if strings.TrimSpace(ansi.Strip(lines[i])) != "" {
			t.Errorf("row %d below the current step is not blank: %q", i, lines[i])
		}
	}
}

// Finished steps stack in the order they *land*, not in step order — the
// startup fetches run concurrently and finish out of order, and reordering
// them would make the lines already on screen jump around.
func TestSplashStacksInCompletionOrder(t *testing.T) {
	m := New(nil, nil)
	m.width, m.height = 100, 25
	m.splash.finish(splashTeams)
	m.splash.finish(splashConnect)

	rows := splashRows(t, &m)
	want := []string{"✓ loading teams", "✓ connecting"}
	for i, w := range want {
		if got := strings.TrimSpace(rows[i]); got != w {
			t.Errorf("row %d = %q; want %q", i, got, w)
		}
	}
	if cur := strings.TrimSpace(rows[len(rows)-1]); !strings.HasSuffix(cur, "signing in") {
		t.Errorf("current step = %q; want the first still-pending step", cur)
	}
}

func TestSplashClearsWhenTheFirstTranscriptLands(t *testing.T) {
	m := New(nil, nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	got := nm.(Model)

	steps := []tea.Msg{
		wsConnectedMsg{},
		meLoadedMsg{user: &model.User{Id: "u1", Username: "me"}},
		teamsLoadedMsg{teams: []*model.Team{{Id: "t1", Name: "team"}}},
		channelsLoadedMsg{channels: []*model.Channel{
			{Id: "c1", TeamId: "t1", Name: "general", DisplayName: "General", Type: model.ChannelTypeOpen},
		}},
		membersLoadedMsg{},
	}
	for _, msg := range steps {
		n, _ := got.Update(msg)
		got = n.(Model)
		if !got.splash.active {
			t.Fatalf("splash cleared early, on %T", msg)
		}
	}
	// The last step names the channel it is waiting on.
	if cur, _ := got.splash.current(); cur != "opening #General" {
		t.Errorf("current step = %q; want the channel being opened", cur)
	}
	n, _ := got.Update(postsLoadedMsg{channelID: "c1", posts: []*model.Post{{Id: "p1", Message: "hi"}}})
	got = n.(Model)
	if got.splash.active {
		t.Error("splash still up after the first transcript loaded")
	}
	if got.splash.steps != nil || got.splash.done != nil {
		t.Error("stopped splash still holds its step state")
	}
}

// An unreachable server must not trap the user behind the splash.
func TestSplashClearsOnErrorAndKeypress(t *testing.T) {
	m := New(nil, nil)
	nm, _ := m.Update(errMsg{err: errors.New("dial tcp: connection refused")})
	if nm.(Model).splash.active {
		t.Error("splash survived a startup error")
	}

	m = New(nil, nil)
	nm, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	nm, _ = nm.(Model).Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if nm.(Model).splash.active {
		t.Error("splash survived a keypress")
	}

	m = New(nil, nil)
	nm, _ = m.Update(splashTimeoutMsg{})
	if nm.(Model).splash.active {
		t.Error("splash survived its timeout")
	}
}

func TestSplashHostFromServerURL(t *testing.T) {
	cases := map[string]string{
		"https://chat.emico.io":        "chat.emico.io",
		"https://chat.emico.io/":       "chat.emico.io",
		"http://localhost:8065/api/v4": "localhost",
		"chat.emico.io":                "chat.emico.io",
		"":                             "",
	}
	for in, want := range cases {
		if got := splashHost(in); got != want {
			t.Errorf("splashHost(%q) = %q; want %q", in, got, want)
		}
	}
}

// A terminal too short for the finished stack still draws the current step.
func TestSplashFitsAShortTerminal(t *testing.T) {
	m := New(nil, nil)
	m.width, m.height = 40, 3
	m.splash.finish(splashConnect)
	m.splash.finish(splashAccount)
	m.splash.finish(splashTeams)

	lines := strings.Split(m.renderSplash(), "\n")
	if len(lines) != m.height {
		t.Fatalf("splash is %d rows; want %d", len(lines), m.height)
	}
	if got := strings.TrimSpace(ansi.Strip(lines[1])); !strings.HasSuffix(got, "loading channels") {
		t.Errorf("centre row = %q; want the current step", got)
	}
}
