package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"matterbox/internal/editor"
)

// keysSheetSections lists, in display order, the context layers shown in the
// cheatsheet under a friendly heading. The rows come from each named context's
// claims() (looked up in keyContexts), so the sheet reflects the *live* keymap:
// any bindings override, the nav modifier, and vim_nav all flow through. A
// section with no bound keys is dropped, so e.g. arrow-nav disabled or a layer
// emptied by overrides simply doesn't appear.
var keysSheetSections = []struct {
	title string
	// contexts names the layers this section documents. Their claims are the
	// section's rows — unless rows is set, in which case the names only record
	// which layers the section covers (keysSheetCoversEveryContext checks that
	// every layer in the ladder is documented somewhere).
	contexts []string
	rows     func(m *Model) []keysSheetRow // alternative row source
}{
	{title: "Global", contexts: []string{"global:reading", "global:switcher-chord", "global:command-picker", "global:team-jump"}},
	// Sidebar nav comes straight from the routes so each direction is one row
	// merging its arrow alias + vim key, honouring vim_nav (the global:nav
	// context splits them and its bindings carry no descriptions).
	{title: "Sidebar navigation", contexts: []string{"global:nav"}, rows: (*Model).navSheetRows},
	{title: "Messages", contexts: []string{"focus:messages"}},
	{title: "Thread", contexts: []string{"focus:thread"}},
	{title: "Reference (Jira / GitLab)", contexts: []string{"focus:ref"}},
	{title: "Jira editors", contexts: []string{"modal:jira-picker", "modal:jira-points", "modal:jira-comment"}},
	{title: "Channel info / media", contexts: []string{"focus:info", "focus:info-media"}},
	// The preview modal's dismiss keys are hardwired in handlePreviewKey, so the
	// rows are built from a synthetic source (rows func) that merges them with
	// the bound toggle, like navSheetRows above.
	{title: "Image preview", contexts: []string{"modal:image-preview"}, rows: (*Model).previewSheetRows},
	{title: "Compose", contexts: []string{"focus:input"}},
	// The editing keys live in internal/editor's own keymap, below every
	// context (they are what a text input does with the keys nothing above it
	// claimed), so they come from the live editor rather than a context.
	{title: "Text editing (composer · SQL · comments)", rows: (*Model).editorSheetRows},
	{title: "Attachments", contexts: []string{"focus:attachments"}},
	{title: "Teams", contexts: []string{"focus:teams"}},
	{title: "Unread feed", contexts: []string{"focus:feed"}},
	{title: "Search", contexts: []string{"focus:search"}},
	{title: "SQL tab", contexts: []string{"focus:sql", "focus:sqlresults"}},
	{title: "Channel filter", contexts: []string{"mode:filter"}},
	{title: "Channel switcher", contexts: []string{"modal:switcher"}},
	{title: "Saved messages · templates · kaomoji", contexts: []string{"modal:saved-posts", "modal:template-picker", "modal:kaomoji-picker"}},
	{title: "Reaction picker", contexts: []string{"modal:reaction-picker"}},
	{title: "Open-target / code-block picker", contexts: []string{"modal:open-picker", "modal:code-picker"}},
	{title: "Edit history", contexts: []string{"modal:history"}},
	{title: "Channel summary", contexts: []string{"modal:summary"}},
	{title: "Poll dialog", contexts: []string{"modal:poll-dialog"}},
	{title: "Channel forms (create / edit / join)", contexts: []string{"modal:channel-form"}},
	{title: "Delete dialog", contexts: []string{"modal:delete-confirm"}},
	{title: "Confirm dialogs (approve / merge / archive / link)", contexts: []string{"modal:confirm"}},
	{title: "Sheets, popups & games", contexts: []string{"modal:keys-sheet", "modal:text-popup", "modal:key-debug", "modal:game"}},
}

// navSheetRows builds one cheatsheet row per sidebar-nav direction, merging the
// arrow alias (when arrow-nav is enabled) with the vim key (when vim_nav isn't
// off) so the row reflects exactly the keys that navigate under the user's
// config. A direction with no active key is dropped.
func (m *Model) navSheetRows() []keysSheetRow {
	var rows []keysSheetRow
	for _, r := range m.keys.navRoutes {
		keys := append([]string(nil), r.arrow.Keys()...)
		if m.vimNav != vimNavOff {
			keys = append(keys, r.vim.Keys()...)
		}
		if len(keys) == 0 {
			continue
		}
		rows = append(rows, keysSheetRow{keys: prettyKeysAll(keys), desc: r.desc})
	}
	return rows
}

// previewSheetRows lists the image-preview modal's keys, built from the live
// bindings so a rebind of the preview / left / right actions is reflected here.
// esc/q are the conventional modal dismiss (hardwired), shown alongside the
// configurable toggle key.
func (m *Model) previewSheetRows() []keysSheetRow {
	var rows []keysSheetRow
	closeKeys := append(append([]string(nil), m.keys.Preview.Keys()...), "esc", "q")
	rows = append(rows, keysSheetRow{keys: prettyKeysAll(closeKeys), desc: "open / close preview"})

	cycleKeys := append(append([]string(nil), m.keys.Left.Keys()...), m.keys.Right.Keys()...)
	if len(cycleKeys) > 0 {
		rows = append(rows, keysSheetRow{keys: prettyKeysAll(cycleKeys), desc: "previous / next image"})
	}
	return rows
}

// editorSheetRows lists the composer's editing keys — the emacs-style motions
// and kills internal/editor handles once no layer above it claimed the key.
// They're read off the live keymap, so the ctrl+←/→ word-jump that appears when
// the sidebar nav isn't on ctrl shows up here too. A Model built without an
// editor (tests) falls back to the defaults rather than dropping the section.
func (m *Model) editorSheetRows() []keysSheetRow {
	km := m.input.KeyMap
	if len(km.LineStart.Keys()) == 0 {
		km = editor.DefaultKeyMap()
	}
	pairs := []struct {
		desc string
		bs   []key.Binding
	}{
		{"character left / right", []key.Binding{km.CharacterBackward, km.CharacterForward}},
		{"word left / right", []key.Binding{km.WordBackward, km.WordForward}},
		{"start / end of line", []key.Binding{km.LineStart, km.LineEnd}},
		{"start / end of the draft", []key.Binding{km.InputBegin, km.InputEnd}},
		{"delete character back / forward", []key.Binding{km.DeleteCharacterBackward, km.DeleteCharacterForward}},
		{"delete word back / forward", []key.Binding{km.DeleteWordBackward, km.DeleteWordForward}},
		{"kill to end of line", []key.Binding{km.DeleteAfterCursor}},
		{"kill to start of line", []key.Binding{km.DeleteBeforeCursor}},
		{"next / previous cell (inside a table)", []key.Binding{km.NextTableCell, km.PrevTableCell}},
	}
	// A key a global layer takes while composing never reaches the editor:
	// ctrl+p always belongs to the switcher, and under vim_nav=global so do
	// ctrl+h/j/k/l — which is exactly why "reading" exists as an option. Don't
	// advertise what won't happen; a row left with no keys drops out.
	stolen := keySet([]key.Binding{m.keys.Switcher})
	for _, r := range m.keys.navRoutes {
		for _, k := range r.arrow.Keys() {
			stolen[k] = true
		}
		if m.vimNav == vimNavGlobal {
			for _, k := range r.vim.Keys() {
				stolen[k] = true
			}
		}
	}

	var rows []keysSheetRow
	for _, p := range pairs {
		var keys []string
		for _, b := range p.bs {
			for _, k := range b.Keys() {
				if !stolen[k] && !inList(keys, k) {
					keys = append(keys, k)
				}
			}
		}
		if len(keys) == 0 {
			continue
		}
		rows = append(rows, keysSheetRow{keys: prettyKeysAll(keys), desc: p.desc})
	}
	return rows
}

// openKeysSheet opens the keyboard cheatsheet popup (switcher "> Keys"). The
// switcher has already closed itself before the command runs, so there's no
// overlap. Content is rebuilt here and on resize.
func (m *Model) openKeysSheet() {
	m.keysSheetMode = true
	m.helpSheet = false
	m.sizeKeysSheetView()
	m.renderKeysSheet()
	m.keysSheetView.GotoTop()
}

// openHelpSheet raises the same scrollable popup but listing the "/" slash
// commands (the /help command). It shares all the cheatsheet's sizing, key
// handling and view wiring; only the content and title differ.
func (m *Model) openHelpSheet() {
	m.keysSheetMode = true
	m.helpSheet = true
	m.sizeKeysSheetView()
	m.renderKeysSheet()
	m.keysSheetView.GotoTop()
}

func (m *Model) closeKeysSheet() {
	m.keysSheetMode = false
	m.helpSheet = false
}

// sizeKeysSheetView keeps the popup viewport sized to the terminal. Call
// before rendering and on resize.
func (m *Model) sizeKeysSheetView() {
	_, h := m.modalDims()
	m.keysSheetView.SetWidth(m.modalInnerWidth())
	m.keysSheetView.SetHeight(h)
}

type keysSheetRow struct {
	keys string
	desc string
}

type keysSheetGroup struct {
	title string
	rows  []keysSheetRow
}

// keysSheetGroups builds the cheatsheet's grouped rows from the context table.
// Each section pulls the bound keys + descriptions out of its context(s)'
// claims, deduping rows that repeat (e.g. a key shared by two merged
// contexts). Unbound actions (empty override) are skipped.
func (m *Model) keysSheetGroups() []keysSheetGroup {
	byName := make(map[string]keyContext, len(keyContexts))
	for _, c := range keyContexts {
		byName[c.name] = c
	}

	var groups []keysSheetGroup
	for _, sec := range keysSheetSections {
		var rows []keysSheetRow
		if sec.rows != nil {
			rows = sec.rows(m)
		} else {
			// One row per description: a layer often answers to two key sets for
			// the same action (↑/k in a list, ↑/ctrl+p when it hangs off an
			// input), and two merged layers repeat each other's rows. Union the
			// keys instead of printing "up" twice.
			byDesc := map[string][]string{}
			for _, cname := range sec.contexts {
				c, ok := byName[cname]
				if !ok {
					continue
				}
				for _, b := range c.claims(m) {
					keys := b.Keys()
					if len(keys) == 0 {
						continue
					}
					desc := b.Help().Desc
					if _, ok := byDesc[desc]; !ok {
						rows = append(rows, keysSheetRow{desc: desc})
					}
					byDesc[desc] = appendNewKeys(byDesc[desc], keys)
				}
			}
			for i := range rows {
				rows[i].keys = prettyKeysAll(byDesc[rows[i].desc])
			}
		}
		if len(rows) > 0 {
			groups = append(groups, keysSheetGroup{title: sec.title, rows: rows})
		}
	}
	return groups
}

// appendNewKeys appends the keys of add that dst doesn't already carry,
// preserving the order they were declared in.
func appendNewKeys(dst, add []string) []string {
	for _, k := range add {
		if !inList(dst, k) {
			dst = append(dst, k)
		}
	}
	return dst
}

// renderKeysSheet populates the popup viewport: a bold heading per group, then
// aligned "keys  description" rows. The key column is sized to the widest
// label (capped at half the popup) so descriptions line up.
func (m *Model) renderKeysSheet() {
	groups := m.keysSheetGroups()
	if m.helpSheet {
		groups = []keysSheetGroup{{title: "Slash commands", rows: slashHelpRows()}}
	}
	inner := m.modalInnerWidth()
	if inner < 10 {
		inner = 10
	}

	keyW := 0
	for _, g := range groups {
		for _, r := range g.rows {
			if x := lipgloss.Width(r.keys); x > keyW {
				keyW = x
			}
		}
	}
	if maxKeyW := inner / 2; keyW > maxKeyW {
		keyW = maxKeyW
	}

	headStyle := lipgloss.NewStyle().Foreground(focusedColor).Bold(true)
	keyStyle := lipgloss.NewStyle().Bold(true)
	dim := lipgloss.NewStyle().Foreground(dimColor)

	var b strings.Builder
	for gi, g := range groups {
		if gi > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(headStyle.Render(g.title))
		b.WriteByte('\n')
		for _, r := range g.rows {
			keys := truncate(r.keys, keyW)
			pad := keyW - lipgloss.Width(keys)
			if pad < 0 {
				pad = 0
			}
			b.WriteString("  " + keyStyle.Render(keys) + strings.Repeat(" ", pad) + "  " + dim.Render(r.desc))
			b.WriteByte('\n')
		}
	}
	m.keysSheetView.SetContent(strings.TrimRight(b.String(), "\n"))
}

// handleKeysSheetKey owns every keystroke while the cheatsheet is open: esc/q
// close it (ctrl+c still quits the app); everything else scrolls the viewport.
func (m Model) handleKeysSheetKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.closeKeysSheet()
		return m, nil
	}
	var cmd tea.Cmd
	*m.keysSheetView, cmd = m.keysSheetView.Update(msg)
	return m, cmd
}

// renderKeysSheetPopup composes the cheatsheet popup in the shared sheet
// modal frame. The viewport's SoftWrap re-wraps long rows to the popup width
// as the user scrolls.
func (m *Model) renderKeysSheetPopup() string {
	if !m.keysSheetMode {
		return ""
	}
	heading := "Keyboard shortcuts"
	if m.helpSheet {
		heading = "Slash commands"
	}
	return m.renderModal(heading, "esc/q close · ↑/↓ scroll", m.keysSheetView.View())
}
