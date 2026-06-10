package ui

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// ctrlKey builds a ctrl+<code> KeyPressMsg. code is either a special key
// (tea.KeyDown) or a printable rune ('j').
func ctrlKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl})
}

// navModel builds a renderable Model sitting on a real team tab (t1) with two
// channels, plus a second team (t2) with one channel, so the global team/
// channel navigation can be driven through the real handleKey dispatch.
func navModel() Model {
	vp := viewport.New()
	vp.SoftWrap = true
	vp.SetWidth(80)
	vp.SetHeight(40)
	ta := textarea.New()
	ta.SetWidth(40)
	fi := textinput.New()

	m := Model{
		teams: []*model.Team{
			{Id: "t1", DisplayName: "Engineering", Name: "eng"},
			{Id: "t2", DisplayName: "Design", Name: "design"},
		},
		channels: map[string][]*model.Channel{
			"t1": {
				{Id: "c1", TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: "general"},
				{Id: "c2", TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: "random"},
			},
			"t2": {
				{Id: "c3", TeamId: "t2", Type: model.ChannelTypeOpen, DisplayName: "ideas"},
			},
		},
		userNames:     map[string]string{},
		keys:          newKeyMap(true),
		focus:         focusMessages,
		width:         100,
		height:        44,
		msgsView:      vp,
		input:         ta,
		filter:        fi,
		search:        newSearchState(false),
		feed:          newFeedState(),
		openChannelID: "c1",
		channelIdx:    0,
	}
	m.teamIdx = m.firstTeamTabIdx() // land on t1
	return m
}

// TestNavChannelOpensImmediately: ctrl+↓ (and the ctrl+j vim alias) move the
// sidebar selection and open the next channel without leaving the messages
// pane — focus stays on focusMessages.
func TestNavChannelOpensImmediately(t *testing.T) {
	for _, code := range []rune{tea.KeyDown, 'j'} {
		m := navModel()
		out, _ := m.handleKey(ctrlKey(code))
		got := out.(Model)
		if got.openChannelID != "c2" {
			t.Fatalf("ctrl+%q: openChannelID = %q, want c2", code, got.openChannelID)
		}
		if got.channelIdx != 1 {
			t.Fatalf("ctrl+%q: channelIdx = %d, want 1", code, got.channelIdx)
		}
		if got.focus != focusMessages {
			t.Fatalf("ctrl+%q: focus = %v, want focusMessages (kept reading)", code, got.focus)
		}
	}
}

// TestNavChannelPrevClampsAtTop: ctrl+↑ on the first channel is a no-op (no
// wrap, nothing reopened).
func TestNavChannelPrevClampsAtTop(t *testing.T) {
	m := navModel()
	out, _ := m.handleKey(ctrlKey(tea.KeyUp))
	got := out.(Model)
	if got.channelIdx != 0 || got.openChannelID != "c1" {
		t.Fatalf("ctrl+↑ at top moved: idx=%d open=%q, want 0/c1", got.channelIdx, got.openChannelID)
	}
}

// TestNavTeamOpensAndKeepsReading: ctrl+→ (and the ctrl+l vim alias) switch to
// the next team, open its preferred channel, and keep focus on the messages
// pane rather than yanking it to the sidebar.
func TestNavTeamOpensAndKeepsReading(t *testing.T) {
	for _, code := range []rune{tea.KeyRight, 'l'} {
		m := navModel()
		startTeam := m.teamIdx
		out, _ := m.handleKey(ctrlKey(code))
		got := out.(Model)
		if got.teamIdx != startTeam+1 {
			t.Fatalf("ctrl+%q: teamIdx = %d, want %d", code, got.teamIdx, startTeam+1)
		}
		if got.currentTeamID() != "t2" {
			t.Fatalf("ctrl+%q: currentTeamID = %q, want t2", code, got.currentTeamID())
		}
		if got.openChannelID != "c3" {
			t.Fatalf("ctrl+%q: openChannelID = %q, want c3", code, got.openChannelID)
		}
		if got.focus != focusMessages {
			t.Fatalf("ctrl+%q: focus = %v, want focusMessages (kept reading)", code, got.focus)
		}
	}
}

// TestNavTeamNextClampsAtLastTeam: ctrl+→ on the last team tab is a no-op —
// the tab strip clamps at maxTeamIdx with no wrap.
func TestNavTeamNextClampsAtLastTeam(t *testing.T) {
	m := navModel()
	m.teamIdx = m.maxTeamIdx() // last team (t2)
	m.openChannelID = "c3"
	out, _ := m.handleKey(ctrlKey(tea.KeyRight))
	got := out.(Model)
	if got.teamIdx != m.teamIdx {
		t.Fatalf("ctrl+→ at last team moved teamIdx %d → %d, want clamp", m.teamIdx, got.teamIdx)
	}
}

// TestNavTeamPrevEntersSyntheticTab: the synthetic Unread/Feed/Search tabs sit
// left of the teams in the strip, so ctrl+← from the first team steps onto the
// Search tab and focuses its body — mirroring the plain ← behavior exactly.
func TestNavTeamPrevEntersSyntheticTab(t *testing.T) {
	m := navModel()
	out, _ := m.handleKey(ctrlKey(tea.KeyLeft))
	got := out.(Model)
	if !got.onSearchTab() {
		t.Fatalf("ctrl+← from first team: currentTeamID = %q, want the Search tab", got.currentTeamID())
	}
	if got.focus != focusSearch {
		t.Fatalf("ctrl+← onto Search tab: focus = %v, want focusSearch", got.focus)
	}
}

// TestCycleFocusSkipsSidebar: the channel sidebar is no longer a Tab stop, so
// cycling focus from the messages pane never lands on focusChannels (or the
// team strip on a normal tab) — it bounces between the content panes only.
func TestCycleFocusSkipsSidebar(t *testing.T) {
	m := navModel() // focusMessages, no thread, no attachments, normal tab
	seen := map[focus]bool{}
	cur := m
	for i := 0; i < 6; i++ {
		out, _ := cur.cycleFocus(1)
		cur = out.(Model)
		seen[cur.focus] = true
		if cur.focus == focusChannels || cur.focus == focusTeams {
			t.Fatalf("Tab landed on the sidebar focus %v, want it skipped", cur.focus)
		}
	}
	if !seen[focusInput] || !seen[focusMessages] {
		t.Fatalf("Tab cycle should visit messages+input, saw %v", seen)
	}
}

// TestFilterOpensFromMessages: with the sidebar non-focusable, f opens the
// channel filter from the messages pane (no need to focus a sidebar first).
func TestFilterOpensFromMessages(t *testing.T) {
	m := navModel()
	out, _ := m.handleKey(keyPress('f'))
	got := out.(Model)
	if !got.filterMode {
		t.Fatalf("f from messages did not open the filter (filterMode=%v)", got.filterMode)
	}
}

// TestFilterApplyOpensAndClears: enter in the filter opens the highlighted
// channel into the messages pane and drops the filter (one-shot finder).
func TestFilterApplyOpensAndClears(t *testing.T) {
	m := navModel()
	m.filterMode = true
	m.channelIdx = 1 // c2
	out, _ := m.handleKey(keyPress(tea.KeyEnter))
	got := out.(Model)
	if got.openChannelID != "c2" {
		t.Fatalf("filter+enter opened %q, want c2", got.openChannelID)
	}
	if got.filterMode || got.filterValue != "" {
		t.Fatalf("filter not cleared after open: mode=%v value=%q", got.filterMode, got.filterValue)
	}
	if got.focus != focusMessages {
		t.Fatalf("filter+enter focus = %v, want focusMessages", got.focus)
	}
}

// TestCtrlArrowNavDisabled: with the ctrl+arrow aliases off, ctrl+↓ no longer
// navigates (it's free for the composer's word-jump), but the ctrl+vim key
// ctrl+j still switches channel.
func TestCtrlArrowNavDisabled(t *testing.T) {
	m := navModel()
	m.keys = newKeyMap(false)

	out, _ := m.handleKey(ctrlKey(tea.KeyDown))
	got := out.(Model)
	if got.openChannelID != "c1" || got.channelIdx != 0 {
		t.Fatalf("ctrl+↓ should be inert when ctrl-arrow nav is off; opened %q idx %d", got.openChannelID, got.channelIdx)
	}

	out2, _ := m.handleKey(ctrlKey('j'))
	got2 := out2.(Model)
	if got2.openChannelID != "c2" {
		t.Fatalf("ctrl+j should still navigate with arrows off; opened %q want c2", got2.openChannelID)
	}
}

// TestNavWorksWhileComposing: ctrl-nav fires even when the composer is focused
// (it's checked before the typing guards), switching channel without discarding
// the in-progress draft and leaving focus in the composer so typing continues
// against the newly-opened channel.
func TestNavWorksWhileComposing(t *testing.T) {
	m := navModel()
	m.focus = focusInput
	m.input.Focus()
	m.input.SetValue("draft")

	out, _ := m.handleKey(ctrlKey('j'))
	got := out.(Model)
	if got.openChannelID != "c2" {
		t.Fatalf("ctrl+j in composer: openChannelID = %q, want c2 (nav not blocked)", got.openChannelID)
	}
	if got.input.Value() != "draft" {
		t.Fatalf("ctrl+j in composer discarded draft: input = %q, want \"draft\"", got.input.Value())
	}
	if got.focus != focusInput {
		t.Fatalf("ctrl+j in composer changed focus to %v, want focusInput (keep composing)", got.focus)
	}
}
