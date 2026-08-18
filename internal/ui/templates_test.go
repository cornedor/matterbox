package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/config"
	"matterbox/internal/editor"
)

// insertTemplate lands the body at the cursor — the middle of a draft stays a
// draft around it — and a bare name (what /tmpl with no argument passes)
// opens the picker instead of erroring.
func TestTemplateInsertAtCursor(t *testing.T) {
	m := Model{templates: map[string]string{"standup": "Yesterday\nToday"}, teams: []*model.Team{{Id: "t1"}}}
	m.teamIdx = m.firstTeamTabIdx() // a team tab: the composer is on screen
	m.input = editor.New()
	m.input.SetWidth(60)
	m.input.SetValue("before after")
	m.input.SetCursorOffset(len("before "))
	_ = m.insertTemplate("Standup") // names are matched case-insensitively
	if got, want := m.input.Value(), "before Yesterday\nTodayafter"; got != want {
		t.Fatalf("insertTemplate composer = %q, want %q", got, want)
	}
	if m.focus != focusInput {
		t.Fatalf("focus = %v after insert, want the composer so typing continues", m.focus)
	}
	if got := m.status; got != "inserted template standup" {
		t.Errorf("status = %q", got)
	}

	m.input.SetValue("")
	_ = m.insertTemplate("nope")
	if m.input.Value() != "" || m.status != "template not found: nope" {
		t.Fatalf("unknown template: composer %q status %q", m.input.Value(), m.status)
	}

	_ = m.insertTemplate("  ")
	if !m.templatePicker.active {
		t.Fatal("insertTemplate with no name should open the picker")
	}
}

// /tmpl with no name opens the picker (through the slash registry, like any
// other command); with a name it inserts, consuming the typed command first.
func TestSlashTemplate(t *testing.T) {
	m := newSlashTestModel("/tmpl")
	m.templates = map[string]string{"standup": "Yesterday\nToday"}
	next, _ := m.runSlashCommand("tmpl", "")
	got := next.(Model)
	if !got.templatePicker.active {
		t.Fatal("/tmpl without a name should open the Templates picker")
	}
	if got.input.Value() != "" {
		t.Fatalf("/tmpl should consume the composer, left %q", got.input.Value())
	}

	m = newSlashTestModel("/tmpl standup")
	m.templates = map[string]string{"standup": "Yesterday\nToday"}
	next, _ = m.runSlashCommand("tmpl", "standup")
	got = next.(Model)
	if got.templatePicker.active {
		t.Fatal("/tmpl <name> should insert, not open the picker")
	}
	if got.input.Value() != "Yesterday\nToday" {
		t.Fatalf("/tmpl standup composer = %q", got.input.Value())
	}
}

// The picker: enter inserts the selected template and closes; d deletes it
// (persisting), keeping the cursor on a valid row; esc closes.
func TestTemplatePickerKeys(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	m := Model{keys: newKeyMap("ctrl"), templates: map[string]string{"a": "alpha", "b": "bravo"}}
	m.input = editor.New()
	m.input.SetWidth(60)
	m.openTemplatePicker()
	if !m.templatePicker.active {
		t.Fatal("picker should be open")
	}
	if got := m.templatePicker.names; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("picker names = %v, want sorted a, b", got)
	}

	// ↓ to b, enter inserts b and closes.
	next, _ := m.handleTemplatePickerKey(tea.KeyPressMsg{Code: tea.KeyDown})
	next, _ = next.(Model).handleTemplatePickerKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Model)
	if got.templatePicker.active {
		t.Fatal("enter should close the picker")
	}
	if got.input.Value() != "bravo" {
		t.Fatalf("enter inserted %q, want bravo", got.input.Value())
	}

	// Reopen on the last row and delete it: the cursor moves up to the new
	// last row and the deletion is written through to templates.json.
	got.openTemplatePicker()
	got.templatePicker.idx = 1
	next, cmd := got.handleTemplatePickerKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	got = next.(Model)
	if _, ok := got.templates["b"]; ok {
		t.Fatal("d should delete the selected template")
	}
	if len(got.templatePicker.names) != 1 || got.templatePicker.names[0] != "a" || got.templatePicker.idx != 0 {
		t.Fatalf("after delete: names %v idx %d, want [a] 0", got.templatePicker.names, got.templatePicker.idx)
	}
	if cmd == nil {
		t.Fatal("delete should return the persist command")
	}
	cmd() // run the write synchronously
	if disk := loadTemplates(); len(disk) != 1 || disk["a"] != "alpha" {
		t.Fatalf("templates.json after delete = %v, want {a: alpha}", disk)
	}

	next, _ = got.handleTemplatePickerKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if next.(Model).templatePicker.active {
		t.Fatal("esc should close the picker")
	}
}

// saveTemplate keeps the composer text (verbatim, multi-line) under the
// lower-cased name, and refuses an empty name or an empty composer.
func TestSaveTemplate(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	m := Model{}
	m.input = editor.New()
	m.input.SetWidth(60)
	if cmd := m.saveTemplate("Standup"); cmd != nil || m.status != "template: composer is empty" {
		t.Fatalf("empty composer: cmd=%v status=%q", cmd != nil, m.status)
	}
	m.input.SetValue("Yesterday\nToday")
	if cmd := m.saveTemplate("  "); cmd != nil || m.status != "template: enter a name" {
		t.Fatalf("empty name: cmd=%v status=%q", cmd != nil, m.status)
	}
	cmd := m.saveTemplate("Standup")
	if cmd == nil {
		t.Fatal("saveTemplate should return the persist command")
	}
	cmd()
	if got := loadTemplates(); got["standup"] != "Yesterday\nToday" {
		t.Fatalf("templates.json = %v, want standup → multi-line body", got)
	}
}

// The Templates commands are listed only where the composer they act on is
// on screen: on a channel tab, not on the Feed / Search / SQL tabs, where an
// insert would silently edit the hidden channel's draft.
func TestTemplateCommandsNeedTheComposer(t *testing.T) {
	m := Model{teams: []*model.Team{{Id: "t1"}}}
	m.teamIdx = m.firstTeamTabIdx()
	cmds, ok := m.templateCommands()
	if !ok || len(cmds) != 2 || cmds[0].name != "Templates" {
		t.Fatalf("channel tab: %v ok=%v", cmdNames(cmds), ok)
	}
	gotoTab(&m, tabFeed)
	if _, ok := m.templateCommands(); ok {
		t.Fatal("feed tab: Templates should not be offered")
	}
	gotoTab(&m, tabSearch)
	if _, ok := m.templateCommands(); ok {
		t.Fatal("search tab: Templates should not be offered")
	}
}
