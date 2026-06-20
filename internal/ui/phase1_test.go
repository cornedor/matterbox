package ui

import (
	"strings"
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
// nav even while composing — it must switch channels without slipping a
// literal newline into the textarea. With per-channel drafts the half-typed
// text isn't carried into the new channel but stashed under the old one, so
// the composer shows the target channel's (empty) draft instead.
func TestComposerCtrlJNeverInsertsNewline(t *testing.T) {
	m := navModel()
	m.focus = focusInput
	m.input.Focus()
	m.input.SetValue("hello")

	out, _ := m.handleKey(ctrlKey('j'))
	got := out.(Model)
	if strings.Contains(got.input.Value(), "\n") {
		t.Fatalf("ctrl+j slipped a newline into the composer: input = %q", got.input.Value())
	}
	if got.openChannelID != "c2" {
		t.Fatalf("ctrl+j did not navigate: openChannelID = %q, want c2", got.openChannelID)
	}
	if got.input.Value() != "" {
		t.Fatalf("ctrl+j did not show c2's empty draft: input = %q, want \"\"", got.input.Value())
	}
	if got.drafts["c1"] != "hello" {
		t.Fatalf("ctrl+j discarded c1's draft: drafts[c1] = %q, want \"hello\"", got.drafts["c1"])
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

// altKey builds an alt+<rune> KeyPressMsg (e.g. the alt+d jump), matching how
// Ghostty delivers it under the Kitty protocol.
func altKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModAlt})
}

// TestAltDigitSwitchesTeam: alt+<n> jumps straight to the n-th real team,
// replacing the old ",n" leader chord.
func TestAltDigitSwitchesTeam(t *testing.T) {
	m := navModel() // sits on team t1
	out, _ := m.handleKey(altKey('2'))
	got := out.(Model)
	if kind, id, _ := got.tabAt(got.teamIdx); kind != tabTeam || id != "t2" {
		t.Fatalf("alt+2 landed on tab kind=%v id=%q, want the 2nd team t2", kind, id)
	}
}

// TestAltDigitSwitchesTeamWhileComposing: alt+<n> is global — it jumps teams
// even from the composer (no alt+digit is a textarea edit key), so fast
// switching works mid-draft like the ctrl+arrow nav.
func TestAltDigitSwitchesTeamWhileComposing(t *testing.T) {
	m := navModel()
	m.focus = focusInput
	m.input.Focus()
	m.input.SetValue("draft")

	out, _ := m.handleKey(altKey('2'))
	got := out.(Model)
	if kind, id, _ := got.tabAt(got.teamIdx); kind != tabTeam || id != "t2" {
		t.Fatalf("alt+2 while composing landed on kind=%v id=%q, want team t2", kind, id)
	}
}

// TestAltDJumpsToDMs: alt+d jumps to the DM tab (old ",d").
func TestAltDJumpsToDMs(t *testing.T) {
	m := navModel()
	m.hasDMs = true
	out, _ := m.handleKey(altKey('d'))
	got := out.(Model)
	if kind, _, _ := got.tabAt(got.teamIdx); kind != tabDM {
		t.Fatalf("alt+d landed on tab kind=%v, want the DM tab", kind)
	}
}

// TestAltUJumpsToFeed: alt+u jumps to the Feed tab (old ",u").
func TestAltUJumpsToFeed(t *testing.T) {
	m := navModel()
	out, _ := m.handleKey(altKey('u'))
	if got := out.(Model); !got.onFeedTab() {
		t.Fatalf("alt+u did not open the Feed tab (teamIdx=%d)", got.teamIdx)
	}
}

// TestCtrlShiftFOpensSearchAll: ctrl+shift+f opens the all-channel search tab,
// the ctrl twin of F.
func TestCtrlShiftFOpensSearchAll(t *testing.T) {
	m := navModel()
	out, _ := m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'f', Mod: tea.ModCtrl | tea.ModShift}))
	if got := out.(Model); !got.onSearchTab() {
		t.Fatalf("ctrl+shift+f did not open the Search tab (teamIdx=%d)", got.teamIdx)
	}
}

// TestAltJumpInertWhileComposing: the alt-jumps are reading-pane only, so a
// jump key pressed in the composer stays with the textarea (alt+u =
// uppercase-word) and does NOT switch tabs.
func TestAltJumpInertWhileComposing(t *testing.T) {
	m := navModel()
	m.focus = focusInput
	m.input.Focus()
	m.input.SetValue("hi")
	startTab := m.teamIdx

	out, _ := m.handleKey(altKey('u'))
	got := out.(Model)
	if got.focus != focusInput {
		t.Fatalf("alt+u in the composer changed focus to %v, want focusInput", got.focus)
	}
	if got.teamIdx != startTab || got.onFeedTab() {
		t.Fatalf("alt+u in the composer switched tabs (teamIdx %d→%d); jumps must be reading-pane only", startTab, got.teamIdx)
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
