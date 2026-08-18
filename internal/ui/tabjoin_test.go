package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"
)

// The tab strip and the panes below it are drawn as separate boxes that have to
// read as one frame: wherever a pane border starts on the body's first row, the
// strip's bottom rule must hand off to it with a down arm (┬ / ┼ / ┤ / ┐ / │),
// and nowhere else. These tests pin that both ways — a rule that runs straight
// past the sidebar divider or stops short of the right edge is the frame coming
// apart, which is exactly what regressed before.

// tabJoinModel builds a model on a real team tab with a second team, so the
// strip carries the sticky DMs/Feed/Search tabs plus two team tabs.
func tabJoinModel() Model {
	m := mouseModel([]*model.Post{p("a", 100)})
	m.width = 100
	m.height = 30
	m.teams = []*model.Team{
		{Id: "t1", DisplayName: "Emico"},
		{Id: "t2", DisplayName: "Justbrands"},
	}
	m.channels["t2"] = []*model.Channel{{Id: "d", TeamId: "t2", Type: model.ChannelTypeOpen, DisplayName: "gen"}}
	m.hasDMs = true
	m.teamIdx = m.firstTeamTabIdx() + 1
	return m
}

// assertTabsJoinBody renders the frame and checks the strip's last rule row
// against the first body row, column by column.
func assertTabsJoinBody(t *testing.T, m Model) {
	t.Helper()
	m.layoutPanes()
	lines := strings.Split(m.renderViewContent(), "\n")
	if len(lines) <= tabsHeight {
		t.Fatalf("frame too short: %d lines", len(lines))
	}
	rule := []rune(ansi.Strip(lines[tabsHeight-1]))
	body := []rune(ansi.Strip(lines[tabsHeight]))
	for c := 0; c < len(body) && c < len(rule) && c < m.width; c++ {
		wantDown := body[c] == '│'
		gotDown := glyphArms(rule[c])&armDown != 0
		if wantDown != gotDown {
			t.Errorf("col %d: rule %q, body %q — down arm %v, want %v\n%s\n%s",
				c, string(rule[c]), string(body[c]), gotDown, wantDown,
				string(rule), string(body))
		}
	}
}

func TestTabsJoinPanes(t *testing.T) {
	assertTabsJoinBody(t, tabJoinModel())
}

func TestTabsJoinThreadPane(t *testing.T) {
	m := tabJoinModel()
	m.threadOpen = true
	m.threadPosts = []*model.Post{p("a", 100)}
	assertTabsJoinBody(t, m)
}

// A full-width tab (Search / Feed / SQL) owns the whole body: only the two
// outer borders join, and the rule must close on the right edge.
func TestTabsJoinFullWidthTabs(t *testing.T) {
	for _, idx := range []int{1, 2} { // Feed, Search
		m := tabJoinModel()
		m.teamIdx = idx
		assertTabsJoinBody(t, m)
	}
}

// The divider can land under the active tab, whose bottom is open: the border
// continues up into it as a plain │ rather than growing a rule that isn't there.
func TestTabsJoinUnderActiveTab(t *testing.T) {
	m := tabJoinModel()
	m.teams[0].DisplayName = "AVeryLongTeamNameHere"
	m.teamIdx = m.firstTeamTabIdx()
	assertTabsJoinBody(t, m)
}

func TestJoinGlyph(t *testing.T) {
	cases := []struct {
		name       string
		glyph      rune
		col, width int
		want       rune
	}{
		{"fill rule gains a stem", '─', 10, 100, '┬'},
		{"tab seam becomes a cross", '┴', 10, 100, '┼'},
		{"active tab's left corner", '┘', 10, 100, '┤'},
		{"active tab's right corner", '└', 10, 100, '├'},
		{"active tab's open bottom", ' ', 10, 100, '│'},
		{"already joined stays put", '┼', 10, 100, '┼'},
		{"first column drops its left arm", '┴', 0, 100, '├'},
		{"first column under the active tab", '┘', 0, 100, '│'},
		{"last column closes the strip", '─', 99, 100, '┐'},
		{"last column on a tab seam", '┴', 99, 100, '┤'},
		{"last column under the active tab", '└', 99, 100, '│'},
	}
	for _, c := range cases {
		if got := joinGlyph(c.glyph, c.col, c.width); got != c.want {
			t.Errorf("%s: joinGlyph(%q, %d, %d) = %q, want %q", c.name, string(c.glyph), c.col, c.width, string(got), string(c.want))
		}
	}
}
