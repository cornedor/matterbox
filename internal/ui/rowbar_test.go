package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"
)

// bgAt reports the SGR state still open where sub starts in row — what the
// terminal is actually painting the first cell of sub with.
func bgAt(t *testing.T, row, sub string) string {
	t.Helper()
	i := strings.Index(row, sub)
	if i < 0 {
		t.Fatalf("%q not found in %q", sub, row)
	}
	return sgrState(row[:i])
}

// sidebarRow renders the DM sidebar and returns the row whose visible text
// contains want.
func sidebarRow(t *testing.T, m *Model, want string) string {
	t.Helper()
	for _, r := range strings.Split(m.renderChannelsPane(12), "\n") {
		if strings.Contains(ansi.Strip(r), want) {
			return r
		}
	}
	t.Fatalf("no sidebar row shows %q", want)
	return ""
}

// A DM row's coloured presence dot ends its own styling with a reset, which used
// to close the hover bar with it: the highlight covered the dot and the trailing
// padding but not the username in between (the row's most visible part). The bar
// must reach the name, and the dot must keep its presence colour.
func TestSidebarHoverBarCoversDMName(t *testing.T) {
	m, vis := benchSidebarModel(3)
	m.hasDMs = true
	m.channels = map[string][]*model.Channel{dmTeamID: vis}
	m.unread = map[string]int{}
	m.channelIdx = 0
	m.hover = hoverState{zone: hitChannel, idx: 1}

	hoverBG := leadingSGR(hoverRowStyle.Render("x"))
	dotFG := leadingSGR(statusOnlineStyle.Render("x"))
	row := sidebarRow(t, m, "@user1")

	if at := bgAt(t, row, "@user1"); !strings.Contains(at, hoverBG) {
		t.Errorf("hover background not open at the username: state %q, row %q", at, row)
	}
	if at := bgAt(t, row, statusDot); !strings.Contains(at, dotFG) {
		t.Errorf("presence dot lost its colour: state %q, row %q", at, row)
	}
	if at := bgAt(t, row, "@user1"); strings.Contains(at, dotFG) {
		t.Errorf("the dot's colour bled onto the username: state %q, row %q", at, row)
	}

	// An unhovered row keeps its plain background.
	if at := bgAt(t, sidebarRow(t, m, "@user2"), "@user2"); strings.Contains(at, hoverBG) {
		t.Errorf("unhovered row is painted with the hover background: %q", at)
	}
}

// Same defect on the selected row: an unread label resets before the badge, so
// the selection bar has to be re-opened for the badge to sit inside it.
func TestSidebarSelectedBarCoversUnreadBadge(t *testing.T) {
	m, vis := benchSidebarModel(3)
	m.hasDMs = true
	m.channels = map[string][]*model.Channel{dmTeamID: vis}
	m.unread = map[string]int{vis[0].Id: 4}
	m.channelIdx = 0
	m.hover = hoverState{}

	selBG := leadingSGR(selectedRow.Render("x"))
	row := sidebarRow(t, m, "@user0")
	badge := " 4\x1b[m" // the badge text, disambiguated from digits inside SGR params
	if at := bgAt(t, row, badge); !strings.Contains(at, selBG) {
		t.Errorf("selection bar not open at the unread badge: state %q, row %q", at, row)
	}
}

// rowBar leaves the row reset-terminated: an unclosed background would paint the
// sidebar's border column beside it.
func TestRowBarEndsClosed(t *testing.T) {
	dot := lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render(statusDot)
	got := rowBar(hoverRowStyle, 20, dot+" @sbernards")
	if sgrState(got) != "" {
		t.Errorf("row ends with styling still open: %q", got)
	}
	if w := lipgloss.Width(got); w != 20 {
		t.Errorf("row width = %d, want 20", w)
	}
}
