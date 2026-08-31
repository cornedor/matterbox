package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"
)

// feedButtonModel is a Feed tab with two unread channels on it and a third
// unread channel the feed is not showing (a muted one), laid out like the real
// View path so the button's screen rect is armed.
func feedButtonModel(t *testing.T) Model {
	t.Helper()
	m := newFeedReplyModel()
	m.vcache = &viewCache{}
	m.mouseEnabled = true
	m.channels["t1"] = append(m.channels["t1"],
		&model.Channel{Id: "c3", TeamId: "t1", DisplayName: "third", Type: model.ChannelTypeOpen},
		&model.Channel{Id: "c4", TeamId: "t1", DisplayName: "muted", Type: model.ChannelTypeOpen})
	m.unread = map[string]int{"c2": 2, "c3": 1, "c4": 5}
	m.mentions = map[string]int{"c3": 1}

	fs := newFeedState(false, 0)
	fs.built = true
	fs.entries = []feedEntry{
		{channelID: "c2", unread: []*model.Post{{Id: "p1", ChannelId: "c2", Message: "hey"}}},
		{channelID: "c3", unread: []*model.Post{{Id: "p2", ChannelId: "c3", Message: "ping"}}, mention: true},
	}
	m.feed = fs
	gotoTab(&m, tabFeed)
	m.focus = focusFeed
	m.layoutPanes()
	m.renderFeedResults()
	m.viewContent() // arms vcache.feedBtnZone, as the real View path does
	return m
}

// TestMarkAllFeedRead: the action clears every bubble and its local unread /
// mention counters in one go, and leaves the unread channels the feed is not
// showing alone — the button can only speak for the list it sits above.
func TestMarkAllFeedRead(t *testing.T) {
	m := feedButtonModel(t)
	cmd := m.markAllFeedRead()
	if cmd == nil {
		t.Fatal("expected the ViewChannel batch command")
	}
	if len(m.feed.entries) != 0 || m.feed.idx != 0 {
		t.Fatalf("feed left with %d entries (idx %d), want none", len(m.feed.entries), m.feed.idx)
	}
	for _, id := range []string{"c2", "c3"} {
		if m.unread[id] != 0 || m.mentions[id] != 0 {
			t.Errorf("%s still unread=%d mentions=%d after mark all", id, m.unread[id], m.mentions[id])
		}
	}
	if m.unread["c4"] != 5 {
		t.Errorf("c4 unread = %d, want 5 — a channel the feed isn't showing must keep its count", m.unread["c4"])
	}
	if !strings.Contains(m.status, "2 channels") {
		t.Errorf("status = %q, want it to name the two channels marked", m.status)
	}
	// An empty feed has nothing to mark: no command, no status churn.
	m.status = ""
	if cmd := m.markAllFeedRead(); cmd != nil || m.status != "" {
		t.Errorf("empty feed: cmd=%v status=%q, want a no-op", cmd != nil, m.status)
	}
}

// TestFeedMarkAllKey: A on the feed runs the same action.
func TestFeedMarkAllKey(t *testing.T) {
	m := feedButtonModel(t)
	next, cmd := m.handleFeedKey(tea.KeyPressMsg{Code: 'A', Text: "A", Mod: tea.ModShift})
	got := next.(Model)
	if len(got.feed.entries) != 0 {
		t.Fatalf("A left %d bubbles, want the feed cleared", len(got.feed.entries))
	}
	if cmd == nil {
		t.Fatal("A: expected the ViewChannel batch command")
	}
}

// TestFeedMarkAllButtonRenders: the button is on the pane's title row while
// there are bubbles, states the action's configured key, and disappears (with
// its click target) once the feed is empty.
func TestFeedMarkAllButtonRenders(t *testing.T) {
	m := feedButtonModel(t)
	want := "Mark all read (" + m.keys.MarkAllRead.Help().Key + ")"
	frame := ansi.Strip(m.viewContent())
	if !strings.Contains(frame, want) {
		t.Fatalf("frame missing %q:\n%s", want, frame)
	}
	rows := strings.Split(frame, "\n")
	if len(rows) <= tabsHeight || !strings.Contains(rows[tabsHeight], want) {
		t.Errorf("button not on the pane's title row (row %d): %q", tabsHeight, rows[tabsHeight])
	}
	if !m.vcache.feedBtnZone.active {
		t.Fatal("feedBtnZone not armed")
	}

	m.feed.entries = nil
	m.renderFeedResults()
	m.vcache.viewValid = false
	if frame := ansi.Strip(m.viewContent()); strings.Contains(frame, want) {
		t.Error("empty feed: the button is still drawn")
	}
	if m.vcache.feedBtnZone.active {
		t.Error("empty feed: feedBtnZone left armed")
	}
}

// TestFeedMarkAllButtonZone: the recorded rect covers exactly the cells the
// title row painted the label on, and the frame's rows keep their width — the
// button replaces the metadata's trailing columns rather than widening the row
// (which would push the pane's right border out of column).
func TestFeedMarkAllButtonZone(t *testing.T) {
	m := feedButtonModel(t)
	z := m.vcache.feedBtnZone
	if !z.active {
		t.Fatal("feedBtnZone not armed")
	}
	if z.y != tabsHeight {
		t.Errorf("button row = %d, want the body's first row %d", z.y, tabsHeight)
	}
	if z.x1 != m.width-1 {
		t.Errorf("button ends at column %d, want the pane's inner right edge %d", z.x1, m.width-1)
	}
	rows := strings.Split(ansi.Strip(m.viewContent()), "\n")
	for i, r := range rows {
		if w := lipgloss.Width(r); w != m.width {
			t.Fatalf("row %d is %d columns wide, want %d: %q", i, w, m.width, r)
		}
	}
	// Columns, not bytes: the metadata to the left of the button is full of
	// multi-byte separators.
	label := ansi.Strip(m.feedMarkAllText())
	cells := ansi.Truncate(ansi.TruncateLeft(rows[z.y], z.x0, ""), z.x1-z.x0, "")
	if cells != label {
		t.Errorf("cells [%d,%d) = %q, want the label %q", z.x0, z.x1, cells, label)
	}
}

// TestFeedMarkAllButtonClick: a click on the button marks everything read, and
// the pointer over it hovers (a click anywhere else on the title row doesn't).
func TestFeedMarkAllButtonClick(t *testing.T) {
	m := feedButtonModel(t)
	z := m.vcache.feedBtnZone
	x, y := (z.x0+z.x1)/2, z.y

	if h := m.hitTest(x, y); h.zone != hitFeedMarkAll {
		t.Fatalf("hitTest on the button = %v, want hitFeedMarkAll", h.zone)
	}
	if h := m.hitTest(1, y); h.zone == hitFeedMarkAll {
		t.Error("the title's own columns must not be part of the button")
	}
	if hv := m.hoverAt(x, y); hv.zone != hitFeedMarkAll {
		t.Errorf("hoverAt on the button = %v, want hitFeedMarkAll", hv.zone)
	}

	next, cmd := m.handleMouseClick(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	got := next.(Model)
	if len(got.feed.entries) != 0 {
		t.Fatalf("click left %d bubbles, want the feed cleared", len(got.feed.entries))
	}
	if got.hover.zone != hitNone {
		t.Error("hover should be dropped — the button is gone from under the pointer")
	}
	if cmd == nil {
		t.Fatal("click: expected the ViewChannel batch command")
	}
}

// TestFeedTitleRowFits: the title row never outgrows the pane's inner width —
// a wrapped row would push the bubbles down and put every mouse row one off —
// and a pane too narrow to hold the button drops it rather than the title.
func TestFeedTitleRowFits(t *testing.T) {
	m := feedButtonModel(t)
	left := "Unread Feed  2 channels  ·  enter open · R reply · m mark read · r refresh"
	for _, contentW := range []int{8, 20, 30, 40, 98, 200} {
		btn := m.feedMarkAllButton(contentW)
		row := feedTitleRow(left, contentW, btn)
		if w := lipgloss.Width(row); w > contentW {
			t.Errorf("contentW=%d: row is %d columns wide", contentW, w)
		}
		if btn.active && btn.col0+lipgloss.Width(btn.text) != contentW {
			t.Errorf("contentW=%d: button not flush with the inner right edge", contentW)
		}
		wantBtn := contentW >= lipgloss.Width(m.feedMarkAllText())+feedTitleMinLeft
		if btn.active != wantBtn {
			t.Errorf("contentW=%d: button active=%v, want %v", contentW, btn.active, wantBtn)
		}
		if !btn.active && !strings.HasPrefix(ansi.Strip(row), "Unread") {
			t.Errorf("contentW=%d: narrow row dropped the title instead of the button: %q", contentW, row)
		}
	}
}
