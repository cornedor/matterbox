package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"matterbox/internal/effects"
)

// The "\" effect picker. Text effects are invisible by construction — the markup
// compiles away and nothing in a channel advertises that `\shimmer{...}` is a
// thing — so without a picker the feature is undiscoverable, and a typo
// (`\shimer{}`) silently stays literal text with no hint as to why. Typing a
// backslash lists what can follow it; accepting one writes `\name{}` and puts
// the cursor inside the braces.
//
// It mirrors the "/" command popup (updateSlash / acceptSlash / renderSlashPopup)
// and shares its keys, with one difference: a command lives at the start of the
// composer, while a directive can open anywhere in a sentence, so the trigger is
// found by scanning back from the cursor rather than by looking at column 0.

// effectPopupState tracks an in-progress "\" completion. start is the rune offset
// of the backslash within the cursor's line; query is the letters between it and
// the cursor, lower-cased.
type effectPopupState struct {
	active bool
	query  string
	start  int
	items  []effects.Effect
	idx    int
}

// updateEffectPopup recomputes the "\" picker after the composer has processed a
// key. It opens only while the effect *name* is being typed: a backslash, then
// nothing but ASCII letters up to the cursor. An escaped backslash (`\\`) is a
// literal one and never opens it. Unlike the "/" picker this stays available
// while editing a post, because an edit shows the markup and may well be adding
// an effect to it.
func (m *Model) updateEffectPopup() {
	row, col := m.input.CursorRowCol()
	lines := strings.Split(m.input.Value(), "\n")
	if row < 0 || row >= len(lines) {
		m.closeEffectPopup()
		return
	}
	runes := []rune(lines[row])
	if col > len(runes) {
		col = len(runes)
	}
	// Scan back over the name to the backslash that opened it.
	i := col - 1
	for i >= 0 && isEffectNameRune(runes[i]) {
		i--
	}
	if i < 0 || runes[i] != '\\' {
		m.closeEffectPopup()
		return
	}
	if i > 0 && runes[i-1] == '\\' {
		m.closeEffectPopup() // "\\" is an escaped backslash, not a directive
		return
	}
	query := strings.ToLower(string(runes[i+1 : col]))
	if m.effectPopup.active && m.effectPopup.query == query && m.effectPopup.start == i {
		return
	}
	items := effectMatches(query)
	if len(items) == 0 {
		m.closeEffectPopup()
		return
	}
	m.effectPopup = effectPopupState{active: true, query: query, start: i, items: items}
}

// isEffectNameRune matches the ASCII letters an effect name is made of — the
// same rule the parser uses (effects.directiveAt).
func isEffectNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

// effectMatches ranks the effects whose name starts with query. A bare backslash
// (empty query) lists them all. Prefix-only: the set is four items, so fuzzy
// matching would offer nothing but false hits on ordinary prose — every `\` in a
// code snippet would otherwise pop a menu.
func effectMatches(query string) []effects.Effect {
	var out []effects.Effect
	for _, e := range effects.All() {
		if strings.HasPrefix(e.Name, query) {
			out = append(out, e)
		}
	}
	return out
}

// closeEffectPopup clears the picker.
func (m *Model) closeEffectPopup() {
	if !m.effectPopup.active {
		return
	}
	m.effectPopup = effectPopupState{}
}

// acceptEffectPopup replaces the typed `\name` fragment with the full
// `\name{}` and drops the cursor between the braces, ready for the text the
// effect will wrap. Returns ok=false when there's nothing to accept, so the
// caller falls through to the default key handling.
func (m *Model) acceptEffectPopup() bool {
	if !m.effectPopup.active || m.effectPopup.idx < 0 || m.effectPopup.idx >= len(m.effectPopup.items) {
		return false
	}
	name := m.effectPopup.items[m.effectPopup.idx].Name
	row, col := m.input.CursorRowCol()
	lines := strings.Split(m.input.Value(), "\n")
	if row < 0 || row >= len(lines) {
		return false
	}
	runes := []rune(lines[row])
	if col > len(runes) {
		col = len(runes)
	}
	start := m.effectPopup.start
	if start < 0 || start > col {
		return false
	}
	open := "\\" + name + "{"
	lines[row] = string(runes[:start]) + open + "}" + string(runes[col:])

	m.history.checkpoint(m.composerContextKey(), m.input.Value())
	m.input.SetValue(strings.Join(lines, "\n"))
	// The cursor goes inside the braces: everything on the lines above, plus this
	// line up to the '}' we just wrote.
	m.input.SetCursorOffset(lineRuneOffset(lines, row) + start + len([]rune(open)))
	m.syncInputHeight()
	m.closeEffectPopup()
	return true
}

// lineRuneOffset is the rune offset at which line `row` begins, newlines counted
// — the coordinate SetCursorOffset speaks.
func lineRuneOffset(lines []string, row int) int {
	off := 0
	for i := 0; i < row && i < len(lines); i++ {
		off += len([]rune(lines[i])) + 1 // +1 for the newline
	}
	return off
}

// effectPopupStyle reuses the mention/emoji/slash dropdown frame.
var effectPopupStyle = lipgloss.NewStyle().
	Border(border).
	BorderForeground(focusedColor).
	Padding(0, 1)

// renderEffectPopup returns the "\" effect dropdown, or "" when it shouldn't
// show. Each row previews its own effect: the name is drawn in the colour that
// effect paints with, so the list shows what it does rather than describing it.
func (m *Model) renderEffectPopup(width int) string {
	if !m.effectPopup.active || len(m.effectPopup.items) == 0 {
		return ""
	}
	maxw := width - 6
	if maxw < 12 {
		maxw = 12
	}
	dim := lipgloss.NewStyle().Foreground(dimColor)
	rows := make([]string, 0, len(m.effectPopup.items))
	for i, e := range m.effectPopup.items {
		name := "\\" + e.Name + "{…}"
		if i == m.effectPopup.idx {
			line := name + "  " + e.Desc
			rows = append(rows, selectedRow.Render(ansi.Truncate(line, maxw, "…")))
			continue
		}
		rows = append(rows, ansi.Truncate(effectSample(e.ID, name)+"  "+dim.Render(e.Desc), maxw, "…"))
	}
	return effectPopupStyle.Render(strings.Join(rows, "\n"))
}

// effectSample paints s with its own effect, at the resting phase — the picker
// row shows the effect rather than naming it.
func effectSample(id byte, s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		b.WriteString(effectHintVisual(id, i, len(rs), effectStaticPhase).ansi())
		b.WriteRune(r)
	}
	b.WriteString("\x1b[0m")
	return b.String()
}
