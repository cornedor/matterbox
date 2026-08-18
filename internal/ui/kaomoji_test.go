package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/editor"
)

// The picker lists the built-in set plus kaomoji_options, most-picked first
// (ties by name), so a favourite floats to the top and a configured extra is
// reachable at all.
func TestKaomojiPickerOrder(t *testing.T) {
	m := Model{
		kaomojiOptions: []string{"  ", "ʕノ•ᴥ•ʔノ ︵ ┻━┻"},
		kaomojiUsage:   map[string]int{`ಠ_ಠ`: 5, `ʕノ•ᴥ•ʔノ ︵ ┻━┻`: 2},
	}
	m.openKaomojiPicker()
	items := m.kaomojiPicker.items
	if len(items) != len(defaultKaomoji)+1 {
		t.Fatalf("%d items, want %d built-ins + 1 configured (blank option dropped)", len(items), len(defaultKaomoji)+1)
	}
	if items[0].name != "disapprove" || items[1].text != "ʕノ•ᴥ•ʔノ ︵ ┻━┻" {
		t.Fatalf("order = %q, %q; want the two used entries first, most used on top", items[0].name, items[1].text)
	}
	if items[2].name != "bear" { // first by name among the never-used
		t.Fatalf("items[2] = %q, want alphabetical after the used ones", items[2].name)
	}
}

// Enter inserts the selection at the composer cursor, counts the pick and
// closes; esc just closes.
func TestKaomojiPickerInsert(t *testing.T) {
	m := Model{keys: newKeyMap("ctrl")}
	m.input = editor.New()
	m.input.SetWidth(60)
	m.input.SetValue("hi  there")
	m.input.SetCursorOffset(3)
	m.openKaomojiPicker()
	m.kaomojiPicker.idx = 1
	want := m.kaomojiPicker.items[1]

	next, cmd := m.handleKaomojiPickerKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Model)
	if got.kaomojiPicker.active {
		t.Fatal("enter should close the picker")
	}
	if v := got.input.Value(); v != "hi "+want.text+" there" {
		t.Fatalf("composer = %q, want the pick at the cursor", v)
	}
	if got.kaomojiUsage[want.text] != 1 {
		t.Fatalf("usage[%q] = %d, want 1", want.text, got.kaomojiUsage[want.text])
	}
	if cmd == nil {
		t.Fatal("expected the persist/grammar batch command")
	}

	got.openKaomojiPicker()
	next, _ = got.handleKaomojiPickerKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if next.(Model).kaomojiPicker.active {
		t.Fatal("esc should close the picker")
	}
}
