package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/editor"
	"matterbox/internal/viewport"
)

// keyPress builds a KeyPressMsg for a named special key ("up" / "down").
func keyPress(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}

// composerModel extends pagingModel with a real (empty) composer textarea so
// the ↑/↓ composer-jump handlers can read the cursor row and focus the input.
// It sits on a real team's channel tab — the only place an open channel has a
// composer — so the ↓-into-composer drop isn't suppressed (focusComposer is a
// no-op on the synthetic Feed/Search tabs).
func composerModel(posts []*model.Post, postIdx int) Model {
	m := pagingModel(posts, postIdx)
	m.keys = newKeyMap("ctrl")
	m.teams = []*model.Team{{Id: "t1", Name: "eng", DisplayName: "Engineering"}}
	m.channels = map[string][]*model.Channel{"t1": {{Id: "c", TeamId: "t1", Type: model.ChannelTypeOpen}}}
	m.teamIdx = m.firstTeamTabIdx() // land on the channel tab, not a synthetic tab
	ta := editor.New()
	ta.SetWidth(40)
	m.input = ta
	return m
}

// TestComposerArrowRoundTrip: ↓ on the absolute-last message drops into the
// composer, and ↑ on the first row of the composer jumps back to it.
func TestComposerArrowRoundTrip(t *testing.T) {
	m := composerModel([]*model.Post{p("a", 100), p("b", 200), p("c", 300)}, 2)

	out, _ := m.handleMessagesKey(keyPress(tea.KeyDown))
	m = out.(Model)
	if m.focus != focusInput {
		t.Fatalf("↓ on last message: focus = %v, want focusInput", m.focus)
	}

	out, _ = m.handleInputKey(keyPress(tea.KeyUp))
	m = out.(Model)
	if m.focus != focusMessages {
		t.Fatalf("↑ in composer: focus = %v, want focusMessages", m.focus)
	}
	if got := m.posts[m.postIdx].Id; got != "c" {
		t.Fatalf("↑ in composer selected %q, want absolute-last c", got)
	}
}

// TestComposerDownNotOnLast: ↓ while a non-last message is selected just moves
// the selection down — it doesn't escape to the composer.
func TestComposerDownNotOnLast(t *testing.T) {
	m := composerModel([]*model.Post{p("a", 100), p("b", 200), p("c", 300)}, 0)

	out, _ := m.handleMessagesKey(keyPress(tea.KeyDown))
	m = out.(Model)
	if m.focus != focusMessages {
		t.Fatalf("↓ off the last message: focus = %v, want focusMessages", m.focus)
	}
	if got := m.posts[m.postIdx].Id; got != "b" {
		t.Fatalf("↓ moved selection to %q, want b", got)
	}
}

// TestComposerUpMultiRowStaysInInput: ↑ while the cursor is below the first
// row of a multi-line draft moves the cursor within the text, not to the
// transcript.
func TestComposerUpMultiRowStaysInInput(t *testing.T) {
	m := composerModel([]*model.Post{p("a", 100), p("b", 200)}, 1)
	m.focus = focusInput
	m.input.SetValue("line one\nline two") // cursor lands on the last row
	if r, _ := m.input.CursorRowCol(); r == 0 {
		t.Fatalf("precondition: cursor expected below row 0, got row 0")
	}

	out, _ := m.handleInputKey(keyPress(tea.KeyUp))
	m = out.(Model)
	if m.focus != focusInput {
		t.Fatalf("↑ on a lower row escaped the composer: focus = %v, want focusInput", m.focus)
	}
}

// TestThreadComposerArrowRoundTrip: the same ↓-into-composer / ↑-back-to-last
// bounce works inside the thread sidebar.
func TestThreadComposerArrowRoundTrip(t *testing.T) {
	m := composerModel([]*model.Post{p("a", 100)}, 0)
	tv := viewport.New()
	tv.SoftWrap = true
	tv.SetWidth(60)
	tv.SetHeight(20)
	m.threadView = tv
	m.threadOpen = true
	m.threadRootID = "a"
	m.threadChannelID = "c"
	m.threadPosts = []*model.Post{p("a", 100), p("r1", 150), p("r2", 200)}
	m.threadIdx = 2
	m.focus = focusThread

	out, _ := m.handleThreadKey(keyPress(tea.KeyDown))
	m = out.(Model)
	if m.focus != focusInput {
		t.Fatalf("↓ on last thread reply: focus = %v, want focusInput", m.focus)
	}

	out, _ = m.handleInputKey(keyPress(tea.KeyUp))
	m = out.(Model)
	if m.focus != focusThread {
		t.Fatalf("↑ in thread composer: focus = %v, want focusThread", m.focus)
	}
	if m.threadIdx != len(m.threadPosts)-1 {
		t.Fatalf("↑ in thread composer selected idx %d, want last %d", m.threadIdx, len(m.threadPosts)-1)
	}
}

// TestComposerUpWhileEditingStaysInInput: ↑ is inert while editing an existing
// post, so an in-progress edit isn't abandoned by a stray arrow.
func TestComposerUpWhileEditingStaysInInput(t *testing.T) {
	m := composerModel([]*model.Post{p("a", 100), p("b", 200)}, 1)
	m.focus = focusInput
	m.editingPostID = "b"

	out, _ := m.handleInputKey(keyPress(tea.KeyUp))
	m = out.(Model)
	if m.focus != focusInput {
		t.Fatalf("↑ while editing escaped the composer: focus = %v, want focusInput", m.focus)
	}
}
