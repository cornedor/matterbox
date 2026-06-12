package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// keysSheetMaxWidth caps the cheatsheet popup's outer width — wide enough for
// two columns (keys + description), capped so it stays readable on a big
// terminal. Mirrors historyPopupMaxWidth.
const keysSheetMaxWidth = 96

// keysSheetSections lists, in display order, the context layers shown in the
// cheatsheet under a friendly heading. The rows come from each named context's
// claims() (looked up in keyContexts), so the sheet reflects the *live* keymap:
// any bindings override, the nav modifier, and vim_nav all flow through. A
// section with no bound keys is dropped, so e.g. arrow-nav disabled or a layer
// emptied by overrides simply doesn't appear.
var keysSheetSections = []struct {
	title    string
	contexts []string                      // rows pulled from these contexts' claims
	rows     func(m *Model) []keysSheetRow // alternative source (used when set)
}{
	{title: "Global", contexts: []string{"global:reading", "global:switcher-chord"}},
	// Sidebar nav comes straight from the routes so each direction is one row
	// merging its arrow alias + vim key, honouring vim_nav (the global:nav
	// context splits them and its bindings carry no descriptions).
	{title: "Sidebar navigation", rows: (*Model).navSheetRows},
	{title: "Messages", contexts: []string{"focus:messages"}},
	{title: "Thread", contexts: []string{"focus:thread"}},
	// The preview modal's own keys are hardwired in handlePreviewKey (not bound
	// through the registry), so list them from a synthetic source (rows func),
	// like navSheetRows above.
	{title: "Image preview", rows: (*Model).previewSheetRows},
	{title: "Compose", contexts: []string{"focus:input"}},
	{title: "Attachments", contexts: []string{"focus:attachments"}},
	{title: "Teams", contexts: []string{"focus:teams"}},
	{title: "Unread feed", contexts: []string{"focus:feed"}},
	{title: "Search", contexts: []string{"focus:search"}},
	{title: "Channel filter", contexts: []string{"mode:filter"}},
	{title: "Channel switcher", contexts: []string{"modal:switcher"}},
	{title: "Delete dialog", contexts: []string{"modal:delete-confirm"}},
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

// openKeysSheet opens the keyboard cheatsheet popup (switcher "> Keys"). The
// switcher has already closed itself before the command runs, so there's no
// overlap. Content is rebuilt here and on resize.
func (m *Model) openKeysSheet() {
	m.keysSheetMode = true
	m.sizeKeysSheetView()
	m.renderKeysSheet()
	m.keysSheetView.GotoTop()
}

func (m *Model) closeKeysSheet() {
	m.keysSheetMode = false
}

// keysSheetDims returns the popup's outer width and content (inner) height,
// mirroring historyDims: min(80% of terminal, cap), clamped to a floor, with
// a few rows of vertical margin and room for the title + separator.
func (m *Model) keysSheetDims() (outerW, innerH int) {
	outerW = m.width * 4 / 5
	if outerW > keysSheetMaxWidth {
		outerW = keysSheetMaxWidth
	}
	if outerW < 30 {
		outerW = 30
	}
	if outerW > m.width-2 {
		outerW = m.width - 2
	}
	if outerW < 1 {
		outerW = 1
	}
	bodyH := m.height - 4
	if bodyH < 6 {
		bodyH = 6
	}
	innerH = bodyH - 4 // border (2) + title (1) + separator (1)
	if innerH < 3 {
		innerH = 3
	}
	return outerW, innerH
}

// sizeKeysSheetView keeps the popup viewport sized to the terminal. Call
// before rendering and on resize.
func (m *Model) sizeKeysSheetView() {
	w, h := m.keysSheetDims()
	inner := w - 4 // border (2) + padding (1) left/right
	if inner < 1 {
		inner = 1
	}
	m.keysSheetView.SetWidth(inner)
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
			seen := map[string]bool{}
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
					label := prettyKeysAll(keys)
					desc := b.Help().Desc
					k := label + "\x00" + desc
					if seen[k] {
						continue
					}
					seen[k] = true
					rows = append(rows, keysSheetRow{keys: label, desc: desc})
				}
			}
		}
		if len(rows) > 0 {
			groups = append(groups, keysSheetGroup{title: sec.title, rows: rows})
		}
	}
	return groups
}

// renderKeysSheet populates the popup viewport: a bold heading per group, then
// aligned "keys  description" rows. The key column is sized to the widest
// label (capped at half the popup) so descriptions line up.
func (m *Model) renderKeysSheet() {
	groups := m.keysSheetGroups()
	w, _ := m.keysSheetDims()
	inner := w - 4
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
	m.keysSheetView, cmd = m.keysSheetView.Update(msg)
	return m, cmd
}

// renderKeysSheetPopup composes the bordered cheatsheet popup. The viewport's
// SoftWrap re-wraps long rows to the popup width as the user scrolls.
func (m *Model) renderKeysSheetPopup() string {
	if !m.keysSheetMode {
		return ""
	}
	outerW, _ := m.keysSheetDims()
	inner := outerW - 4
	if inner < 1 {
		inner = 1
	}
	title := titleStyle.Render("Keyboard shortcuts") + "  " +
		lipgloss.NewStyle().Foreground(dimColor).Render("esc/q close · ↑/↓ scroll")
	dim := lipgloss.NewStyle().Foreground(dimColor)
	rule := dim.Render(strings.Repeat("─", inner))
	rows := []string{title, rule, m.keysSheetView.View()}
	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(focusedColor).
		Padding(0, 1).
		Width(outerW).
		Render(strings.Join(rows, "\n"))
}
