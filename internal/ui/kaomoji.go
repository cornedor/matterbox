package ui

import (
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// kaomojiItem is one picker entry: a short name to read the list by, and the
// text that gets inserted.
type kaomojiItem struct {
	name string
	text string
}

// kaomojiPickerState is the /kaomoji picker: a list of the built-in set plus
// the user's kaomoji_options, most-used first, inserted into the composer at
// the cursor on enter.
type kaomojiPickerState struct {
	active bool
	items  []kaomojiItem
	idx    int
}

var defaultKaomoji = []kaomojiItem{
	{name: "shrug", text: `¯\_(ツ)_/¯`},
	{name: "tableflip", text: `(╯°□°）╯︵ ┻━┻`},
	{name: "unflip", text: `┬─┬ ノ( ゜-゜ノ)`},
	{name: "lenny", text: `( ͡° ͜ʖ ͡°)`},
	{name: "happy", text: `ヽ(•‿•)ノ`},
	{name: "wave", text: `( ﾟ▽ﾟ)/`},
	{name: "cheers", text: `♪(┌・。・)┌`},
	{name: "confused", text: `¯\_(:/)_/¯`},
	{name: "cry", text: `(ಥ﹏ಥ)`},
	{name: "sparkle", text: `｡^‿^｡`},
	{name: "dealwithit", text: `(⌐■_■)`},
	{name: "love", text: `(づ｡◕‿‿◕｡)づ`},
	{name: "blush", text: `(˶ᵔ ᵕ ᵔ˶)`},
	{name: "excited", text: `٩(ˊᗜˋ*)و`},
	{name: "sleepy", text: `(-_-) zzz`},
	{name: "cat", text: `(=^･ω･^=)`},
	{name: "bear", text: `ʕ•ᴥ•ʔ`},
	{name: "fightme", text: `༼ つ ◕_◕ ༽つ`},
	{name: "disapprove", text: `ಠ_ಠ`},
	{name: "celebrate", text: `＼(＾▽＾)／`},
	{name: "bow", text: `m(_ _)m`},
	{name: "running", text: "ε=ε=ε=┌(;*´Д`)ﾉ"},
	{name: "music", text: `~(˘▾˘~)`},
	{name: "scared", text: `Σ(°△°|||)︴`},
	{name: "hello", text: "ヾ(•ω•`)o"},
	{name: "thinking", text: `( -_・)?`},
}

// kaomojiCommand returns the palette entry that raises the picker, plus
// whether it applies: the pick is inserted into the composer, so — like the
// Templates sheet — it's only offered while a channel tab shows one.
func (m *Model) kaomojiCommand() (switcherCommand, bool) {
	if !m.composerShown() {
		return switcherCommand{}, false
	}
	return switcherCommand{
		name: "Kaomoji",
		desc: "insert a kaomoji at the cursor (enter picks); also /kaomoji <name> to send one",
		run:  runKaomojiPicker,
	}, true
}

func runKaomojiPicker(m *Model, _ string) tea.Cmd {
	m.openKaomojiPicker()
	return nil
}

// openKaomojiPicker raises the modal list over the composer.
func (m *Model) openKaomojiPicker() {
	m.kaomojiPicker = kaomojiPickerState{active: true, items: m.kaomojiItems()}
}

// kaomojiItems is the pick list — built-ins then the configured extras —
// sorted by how often each has been picked (ties by name) so favourites float
// to the top, like the emoji picker. Shared by the picker and the "/kaomoji "
// argument autocomplete.
func (m *Model) kaomojiItems() []kaomojiItem {
	items := append([]kaomojiItem(nil), defaultKaomoji...)
	for _, text := range m.kaomojiOptions {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		items = append(items, kaomojiItem{name: text, text: text})
	}
	sort.SliceStable(items, func(i, j int) bool {
		ui, uj := m.kaomojiUsage[items[i].text], m.kaomojiUsage[items[j].text]
		if ui != uj {
			return ui > uj
		}
		return items[i].name < items[j].name
	})
	return items
}

// kaomojiArgs offers every kaomoji as an argument row for "/kaomoji ",
// favourites first, with the kaomoji itself as the hint beside its name. (A
// configured extra is its own name, so it needs no hint.)
func kaomojiArgs(m *Model) []slashArg {
	items := m.kaomojiItems()
	out := make([]slashArg, 0, len(items))
	for _, it := range items {
		a := slashArg{value: it.name}
		if it.text != it.name {
			a.desc = it.text
		}
		out = append(out, a)
	}
	return out
}

// findKaomoji looks one up by its picker name (case-insensitive) — what
// "/kaomoji <name>" takes and what its autocomplete fills in.
func (m *Model) findKaomoji(name string) (kaomojiItem, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, it := range m.kaomojiItems() {
		if strings.ToLower(it.name) == name {
			return it, true
		}
	}
	return kaomojiItem{}, false
}

func (m *Model) closeKaomojiPicker() {
	m.kaomojiPicker = kaomojiPickerState{}
}

// bumpKaomojiStat counts a pick (keyed by the inserted text, which is stable
// across renames) and persists the picker stats.
func (m *Model) bumpKaomojiStat(text string) tea.Cmd {
	if text == "" {
		return nil
	}
	if m.kaomojiUsage == nil {
		m.kaomojiUsage = map[string]int{}
	}
	m.kaomojiUsage[text]++
	return m.persistPickerStats()
}

// insertKaomoji puts item's text into the composer at the cursor (an undo
// checkpoint first, like the emoji picker) and closes the picker.
func (m *Model) insertKaomoji(item kaomojiItem) tea.Cmd {
	m.history.checkpoint(m.composerContextKey(), m.input.Value())
	m.input.InsertString(item.text)
	m.syncInputHeight()
	m.focusComposerIfShown()
	m.closeKaomojiPicker()
	m.status = "inserted " + item.name
	return tea.Batch(m.scheduleGrammarCheck(), m.bumpKaomojiStat(item.text))
}

// handleKaomojiPickerKey owns every keystroke while the picker is open:
// esc/q close, ↑/↓ move, enter inserts.
func (m Model) handleKaomojiPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.closeKaomojiPicker()
		return m, nil
	}
	if m.listNav(msg, &m.kaomojiPicker.idx, len(m.kaomojiPicker.items)) {
		return m, nil
	}
	if key.Matches(msg, m.keys.OpenChannel) && m.kaomojiPicker.idx < len(m.kaomojiPicker.items) {
		return m, m.insertKaomoji(m.kaomojiPicker.items[m.kaomojiPicker.idx])
	}
	return m, nil
}

func (m *Model) renderKaomojiPicker() string {
	if !m.kaomojiPicker.active {
		return ""
	}
	items := m.kaomojiPicker.items
	row := func(i int) string {
		if items[i].name == items[i].text {
			return items[i].text
		}
		return items[i].name + "  " + items[i].text
	}
	return m.renderListModal("Kaomoji", helpKey(m.keys.OpenChannel)+" inserts · esc closes", "", len(items), m.kaomojiPicker.idx, row)
}
