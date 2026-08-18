package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"
)

// The panes, the tab strip above them and the footer below are drawn as
// separate boxes that have to read as one frame. Three promises hold that
// together, and each one has been wrong at some point:
//
//   - the panes draw no bottom border — the footer is the frame's bottom edge,
//     so every body row is content and the body fills the terminal;
//   - the sidebar and the messages pane share one divider column, not two;
//   - a full-width rule inside a pane (the composer separator, the Search /
//     Feed / SQL section rules) meets the frame as ├ ┤ instead of floating
//     between two untouched walls.
//
// The geometry the mouse layer reproduces has to agree with all three, so
// these check the rendered frame, not the intent.

// frameLines renders one frame and returns its rows with styling stripped.
func frameLines(t *testing.T, m Model) []string {
	t.Helper()
	m.layoutPanes()
	m.renderAllPanes()
	lines := strings.Split(m.renderViewContent(), "\n")
	for i, ln := range lines {
		lines[i] = ansi.Strip(ln)
	}
	return lines
}

// bodyRows returns just the pane rows: everything between the tab strip and
// the footer.
func bodyRows(t *testing.T, m Model) []string {
	t.Helper()
	lines := frameLines(t, m)
	footerH := lipgloss.Height(m.renderFooter())
	return lines[tabsHeight : len(lines)-footerH]
}

func TestFrameFillsTerminalHeight(t *testing.T) {
	for _, h := range []int{12, 20, 40} {
		m := tabJoinModel()
		m.height = h
		if got := len(frameLines(t, m)); got != h {
			t.Errorf("height %d: frame is %d rows, want %d", h, got, h)
		}
	}
}

// No pane draws a bottom border, so the body's last row is content — a corner
// or a tee there means a pane is spending a row on chrome the footer provides.
func TestPanesHaveNoBottomBorder(t *testing.T) {
	layouts := map[string]func(*Model){
		"panes":  func(*Model) {},
		"thread": func(m *Model) { m.threadOpen = true; m.threadPosts = []*model.Post{p("a", 100)} },
		"search": func(m *Model) { m.teamIdx = 2 },
		"feed":   func(m *Model) { m.teamIdx = 1 },
	}
	for name, setup := range layouts {
		m := tabJoinModel()
		setup(&m)
		rows := bodyRows(t, m)
		last := rows[len(rows)-1]
		if strings.ContainsAny(last, "└┘┴") {
			t.Errorf("%s: body's last row carries a bottom border: %q", name, last)
		}
	}
}

// The sidebar gave up its right border so the messages pane's left border is
// the single divider. Two adjacent verticals there means it grew back.
func TestSidebarSharesOneDivider(t *testing.T) {
	m := tabJoinModel()
	for i, row := range bodyRows(t, m) {
		r := []rune(row)
		if r[0] != '│' {
			t.Fatalf("row %d: body doesn't start on a left border: %q", i, row)
		}
		// The divider is a plain │, or a ├ where the composer rule meets it.
		if r[channelsWidth] != '│' && r[channelsWidth] != '├' {
			t.Fatalf("row %d: no divider at col %d: %q", i, channelsWidth, row)
		}
		if r[channelsWidth-1] == '│' {
			t.Errorf("row %d: doubled divider at cols %d-%d: %q", i, channelsWidth-1, channelsWidth, row)
		}
	}
}

// ruleRowIn returns the index of the pane row that is a joined section rule,
// searching from the bottom (the composer separator is the lowest one).
func ruleRowIn(rows []string) int {
	for i := len(rows) - 1; i >= 0; i-- {
		if strings.Contains(rows[i], "├──") {
			return i
		}
	}
	return -1
}

func TestComposerRuleJoinsTheFrame(t *testing.T) {
	for _, threadOpen := range []bool{false, true} {
		m := tabJoinModel()
		if threadOpen {
			m.threadOpen = true
			m.threadPosts = []*model.Post{p("a", 100)}
		}
		rows := bodyRows(t, m)
		r := ruleRowIn(rows)
		if r < 0 {
			t.Fatalf("threadOpen=%v: no composer rule in the body", threadOpen)
		}
		rule := []rune(rows[r])
		// The rule belongs to whichever pane holds the composer, so it ends on
		// that pane's right edge — the frame's, since it's the last pane.
		if rule[len(rule)-1] != '┤' {
			t.Errorf("threadOpen=%v: composer rule doesn't meet the right edge: %q", threadOpen, rows[r])
		}
		if !strings.Contains(rows[r], "├──") {
			t.Errorf("threadOpen=%v: composer rule doesn't meet a left border: %q", threadOpen, rows[r])
		}
	}
}

// Search / Feed / SQL own the whole body, so their section rules span it and
// must meet both outer borders.
func TestFullWidthPaneRulesJoinTheFrame(t *testing.T) {
	for _, idx := range []int{1, 2} { // Feed, Search
		m := tabJoinModel()
		m.teamIdx = idx
		joined := 0
		for _, row := range bodyRows(t, m) {
			r := []rune(row)
			if !strings.Contains(row, "──") || r[0] == '│' && r[len(r)-1] == '│' && !strings.Contains(row, "├") {
				continue
			}
			if r[0] == '├' {
				joined++
				if r[len(r)-1] != '┤' {
					t.Errorf("tab %d: rule meets the left edge but not the right: %q", idx, row)
				}
			}
		}
		if joined == 0 {
			t.Errorf("tab %d: no section rule joined to the frame", idx)
		}
	}
}

// composerGeom is what a click resolves against; it has to land on the rows the
// composer actually occupies — directly under its rule, ending on the body's
// last row.
func TestComposerGeomMatchesRender(t *testing.T) {
	m := tabJoinModel()
	m.vcache = &viewCache{}
	rows := bodyRows(t, m)
	m.vcache.bodyH = len(rows)
	r := ruleRowIn(rows)
	_, top, _, height, _ := m.composerGeom()
	if want := tabsHeight + r + 1; top != want {
		t.Errorf("composer top = %d, want %d (rule on body row %d)", top, want, r)
	}
	if got, want := top+height-1, tabsHeight+len(rows)-1; got != want {
		t.Errorf("composer ends on screen row %d, want the body's last row %d", got, want)
	}
}

func TestJoinRuleLine(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"both borders", "│────│", "├────┤"},
		{"left border only", "│────", "├────"},
		{"no border", "────", "────"},
	}
	for _, c := range cases {
		if got := joinRuleLine(c.in); got != c.want {
			t.Errorf("%s: joinRuleLine(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
