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

// openKaomojiPicker builds the list — built-ins then the configured extras —
// sorted by how often each has been picked (ties by name) so favourites float
// to the top, like the emoji picker.
func (m *Model) openKaomojiPicker() {
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
	m.kaomojiPicker = kaomojiPickerState{active: true, items: items}
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
	return m.renderListModal("Kaomoji", "enter inserts · esc closes", "", len(items), m.kaomojiPicker.idx, row)
}
