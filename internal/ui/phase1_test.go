package ui

import (
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// keyStr builds a KeyPressMsg from a plain string token like "esc" / "y".
// Single-rune tokens become a rune Code; multi-char tokens (special keys)
// set Text so msg.String() round-trips. For the cases below we only need
// the few specials that have a tea.Key* constant, so map those explicitly.
func keyStr(s string) tea.KeyPressMsg {
	switch s {
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	default:
		r := []rune(s)
		return tea.KeyPressMsg(tea.Key{Code: r[0], Text: s})
	}
}

// TestComposerCtrlJNeverInsertsNewline: ctrl+j is the global "next channel"
// nav even while composing — it must switch channels and leave the draft
// byte-for-byte intact (no newline slips into the textarea).
func TestComposerCtrlJNeverInsertsNewline(t *testing.T) {
	m := navModel()
	m.focus = focusInput
	m.input.Focus()
	m.input.SetValue("hello")

	out, _ := m.handleKey(ctrlKey('j'))
	got := out.(Model)
	if got.input.Value() != "hello" {
		t.Fatalf("ctrl+j touched the draft: input = %q, want \"hello\" (no newline)", got.input.Value())
	}
	if got.openChannelID != "c2" {
		t.Fatalf("ctrl+j did not navigate: openChannelID = %q, want c2", got.openChannelID)
	}
}

// TestDeleteDialogEnterDoesNotConfirm: enter is no longer a confirm key on the
// delete dialog — the dialog stays open and nothing is deleted.
func TestDeleteDialogEnterDoesNotConfirm(t *testing.T) {
	m := navModel()
	m.openDeleteConfirm(&model.Post{Id: "p1"})

	out, cmd := m.handleKey(keyStr("enter"))
	got := out.(Model)
	if got.deleteConfirmPostID != "p1" {
		t.Fatalf("enter closed the delete dialog: deleteConfirmPostID = %q, want it still open", got.deleteConfirmPostID)
	}
	if cmd != nil {
		t.Fatalf("enter on the delete dialog fired a command; want none")
	}
}

// TestDeleteDialogYConfirms: y still confirms, closing the dialog and firing
// the delete.
func TestDeleteDialogYConfirms(t *testing.T) {
	m := navModel()
	m.openDeleteConfirm(&model.Post{Id: "p1"})

	out, cmd := m.handleKey(keyStr("y"))
	got := out.(Model)
	if got.deleteConfirmPostID != "" {
		t.Fatalf("y did not close the delete dialog: deleteConfirmPostID = %q", got.deleteConfirmPostID)
	}
	if cmd == nil {
		t.Fatalf("y did not fire the delete command")
	}
}

// TestEscClosesThreadBeforeFilter: with both a thread open and a sidebar
// filter active, esc dismisses the thread first (the visually dominant thing),
// leaving the filter for a second esc.
func TestEscClosesThreadBeforeFilter(t *testing.T) {
	m := navModel()
	m.threadOpen = true
	m.threadRootID = "r"
	m.threadChannelID = "c1"
	m.filterValue = "gen"

	out, _ := m.handleKey(keyStr("esc"))
	got := out.(Model)
	if got.threadOpen {
		t.Fatalf("esc did not close the thread first")
	}
	if got.filterValue != "gen" {
		t.Fatalf("esc cleared the filter too: filterValue = %q, want it kept", got.filterValue)
	}
}

// TestLeaderUnknownKeyFlashes: an unbound second key flashes a hint rather
// than vanishing silently.
func TestLeaderUnknownKeyFlashes(t *testing.T) {
	m := navModel()
	m.leaderPending = true

	out, _ := m.handleLeaderKey(keyStr("z"))
	got := out.(Model)
	if got.leaderPending {
		t.Fatalf("leader chord not cleared after second key")
	}
	if got.status == "" {
		t.Fatalf("unbound leader key gave no feedback")
	}
}

// TestLeaderEscCancelsSilently: esc still cancels the chord with no noise.
func TestLeaderEscCancelsSilently(t *testing.T) {
	m := navModel()
	m.leaderPending = true

	out, _ := m.handleLeaderKey(keyStr("esc"))
	got := out.(Model)
	if got.status != "" {
		t.Fatalf("esc on a leader chord flashed %q, want silent cancel", got.status)
	}
}

// TestLeaderMessagesNoOpOnSearchTabFlashes: ",m" has no messages pane on the
// Search tab, so it explains why instead of doing nothing.
func TestLeaderMessagesNoOpOnSearchTabFlashes(t *testing.T) {
	m := navModel()
	m.openSearchTab() // move onto the synthetic Search tab
	m.leaderPending = true

	out, _ := m.handleLeaderKey(keyStr("m"))
	got := out.(Model)
	if got.status == "" {
		t.Fatalf(",m on the Search tab gave no feedback")
	}
}

// TestComposerEscWithDraftFlashes: esc with a non-empty draft keeps focus in
// the composer and explains why, rather than silently doing nothing.
func TestComposerEscWithDraftFlashes(t *testing.T) {
	m := navModel()
	m.focus = focusInput
	m.input.Focus()
	m.input.SetValue("half-typed")

	out, _ := m.handleInputKey(keyStr("esc"))
	got := out.(Model)
	if got.focus != focusInput {
		t.Fatalf("esc with a draft left the composer: focus = %v, want focusInput", got.focus)
	}
	if got.status == "" {
		t.Fatalf("esc with a draft gave no feedback")
	}
}

// TestSwitcherCtrlPTogglesClosed: ctrl+p opens the switcher, and pressing it
// again toggles it closed (it used to be a dead list-up arm / ctrl+k).
func TestSwitcherCtrlPTogglesClosed(t *testing.T) {
	m := navModel()
	m.switcher = textinput.New()

	out, _ := m.openSwitcher()
	m = out.(Model)
	if !m.switcherMode {
		t.Fatalf("openSwitcher did not enter switcher mode")
	}

	out, _ = m.handleKey(ctrlKey('p'))
	got := out.(Model)
	if got.switcherMode {
		t.Fatalf("ctrl+p did not toggle the switcher closed")
	}
}
