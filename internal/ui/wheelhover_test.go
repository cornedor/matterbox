package ui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/mattermost/mattermost/server/public/model"
	"matterbox/internal/viewport"
)

// wheelHoverModel is composing (focus on the input) with a sidebar and a
// transcript both taller than their panes.
func wheelHoverModel() Model {
	chans := make([]*model.Channel, 80)
	for i := range chans {
		chans[i] = &model.Channel{Id: fmt.Sprintf("c%d", i), TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: fmt.Sprintf("Chan %d", i)}
	}
	msgs := viewport.New()
	msgs.SetWidth(60)
	msgs.SetHeight(10)
	msgs.SetContent(longBody())
	return Model{
		keys:         newKeyMap("ctrl"),
		mouseEnabled: true,
		focus:        focusInput,
		teamIdx:      2, // Feed(0), Search(1), team(2)
		width:        100,
		height:       44,
		teams:        []*model.Team{{Id: "t1", Name: "eng", DisplayName: "Engineering"}},
		channels:     map[string][]*model.Channel{"t1": chans},
		msgsView:     msgs,
	}
}

func wheelAt(m Model, btn tea.MouseButton, x, y int) Model {
	out, _ := m.handleMouseWheel(tea.MouseWheelMsg(tea.Mouse{Button: btn, X: x, Y: y}))
	out, _ = out.(Model).update(wheelFlushMsg{})
	return out.(Model)
}

// TestWheelScrollsHoveredSidebar: the wheel over the sidebar scrolls its list
// without moving the cursor, and the offset survives render until the cursor
// moves.
func TestWheelScrollsHoveredSidebar(t *testing.T) {
	m := wheelAt(wheelHoverModel(), tea.MouseWheelDown, 2, tabsHeight+3)
	if m.channelIdx != 0 {
		t.Fatalf("cursor moved to %d", m.channelIdx)
	}
	listH := m.channelListH()
	if got := m.channelScrollOff(len(m.visibleChannels()), listH); got != 3 {
		t.Fatalf("sidebar offset = %d, want 3", got)
	}
	if m.msgsView.YOffset() != 0 {
		t.Errorf("transcript scrolled to %d", m.msgsView.YOffset())
	}
	m.channelIdx = 1
	if got := m.channelScrollOff(len(m.visibleChannels()), listH); got != 1 {
		t.Errorf("after cursor move offset = %d, want 1 (cursor back on screen)", got)
	}
}

// TestWheelSidebarClampsAtEnd: scrolling past the last row stops with the last
// channel on the bottom row.
func TestWheelSidebarClampsAtEnd(t *testing.T) {
	m := wheelHoverModel()
	for range 50 {
		m = wheelAt(m, tea.MouseWheelDown, 2, tabsHeight+3)
	}
	want := len(m.visibleChannels()) - m.channelListH()
	if m.chanOff != want {
		t.Errorf("chanOff = %d, want %d", m.chanOff, want)
	}
}

// TestWheelScrollsHoveredTranscriptWhileComposing: focus stays in the
// composer, yet the wheel over the transcript scrolls it.
func TestWheelScrollsHoveredTranscriptWhileComposing(t *testing.T) {
	m := wheelAt(wheelHoverModel(), tea.MouseWheelDown, channelsWidth+5, tabsHeight+3)
	if m.msgsView.YOffset() != m.msgsView.MouseWheelDelta {
		t.Errorf("transcript YOffset = %d, want %d", m.msgsView.YOffset(), m.msgsView.MouseWheelDelta)
	}
	if m.focus != focusInput {
		t.Errorf("focus changed to %v", m.focus)
	}
}
