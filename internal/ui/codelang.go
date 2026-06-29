package ui

import (
	"sort"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// langLimit caps the code-fence language picker so the popup stays a few rows
// tall regardless of how many lexers match.
const langLimit = 8

// langState drives the ```-fence language picker, mirroring emojiState: it
// opens on the info string of an *opening* code fence and offers the chroma
// lexers our message pane can syntax-highlight.
type langState struct {
	active bool
	line   int
	start  int // rune index where the language token begins (just past the ```)
	query  string
	items  []string
	idx    int
}

// langPopularity floats the languages people actually paste in chat to the top
// of ties, so an empty or single-letter query surfaces the obvious pick first
// instead of whatever sorts alphabetically. Anything absent ranks at 0.
var langPopularity = map[string]int{
	"go": 100, "python": 95, "javascript": 90, "typescript": 88,
	"json": 85, "bash": 84, "shell": 83, "sh": 82, "sql": 80,
	"yaml": 78, "html": 76, "css": 74, "java": 72, "c": 70,
	"cpp": 69, "c++": 68, "rust": 66, "ruby": 64, "php": 62,
	"kotlin": 60, "swift": 58, "csharp": 56, "c#": 55, "markdown": 54,
	"dockerfile": 52, "diff": 50, "xml": 48, "toml": 46, "ini": 44,
	"lua": 42, "perl": 40, "scala": 38, "haskell": 36, "elixir": 34,
	"dart": 32, "r": 30, "powershell": 28, "text": 26, "plaintext": 25,
}

// langNames is the lower-cased, de-duplicated set of every lexer name and alias
// chroma knows, kept only where lexers.Get resolves it — so every offered tag
// actually highlights (or is harmlessly ignored) in the message pane. Built
// once; sorting up front keeps prefix/substring ranking deterministic.
var (
	langOnce  sync.Once
	langNames []string
)

func langIndex() []string {
	langOnce.Do(func() {
		seen := map[string]struct{}{}
		for _, n := range lexers.Names(true) {
			l := strings.ToLower(n)
			if _, dup := seen[l]; dup {
				continue
			}
			// Keep only tags chroma can resolve back to a lexer; a lower-cased
			// display name with no matching alias wouldn't highlight, so drop it.
			if lexers.Get(l) == nil {
				continue
			}
			seen[l] = struct{}{}
			langNames = append(langNames, l)
		}
		sort.Strings(langNames)
	})
	return langNames
}

// updateLang recomputes the language picker after the editor has processed a
// key. Like emoji it never fetches, so it returns nothing. The picker opens
// only on the info string of an *opening* ``` fence — the line where a language
// tag belongs — so it never fights the closing fence or content of a block.
func (m *Model) updateLang() {
	row, col := m.input.CursorRowCol()

	lines := strings.Split(m.input.Value(), "\n")
	if row < 0 || row >= len(lines) {
		m.closeLang()
		return
	}
	runes := []rune(lines[row])
	if col > len(runes) {
		col = len(runes)
	}

	// Parse the line as a backtick fence: up to three leading spaces (four is
	// indented code, not a fence), then a run of three or more backticks.
	i := 0
	for i < len(runes) && runes[i] == ' ' {
		i++
	}
	if i >= 4 || i >= len(runes) || runes[i] != '`' {
		m.closeLang()
		return
	}
	n := 0
	for i < len(runes) && runes[i] == '`' {
		i++
		n++
	}
	if n < 3 {
		m.closeLang()
		return
	}
	start := i // first rune of the info string

	// Only an opening fence carries a language tag. If the caret line is
	// already inside a block, this ``` closes it — stay quiet so Enter sends.
	if m.input.InCodeBlock() {
		m.closeLang()
		return
	}

	// The caret must sit at the end of the (single-token) info string: after
	// the backticks, and with nothing but trailing whitespace beyond it.
	if col < start {
		m.closeLang()
		return
	}
	for j := col; j < len(runes); j++ {
		if runes[j] != ' ' && runes[j] != '\t' {
			m.closeLang()
			return
		}
	}
	query := strings.ToLower(string(runes[start:col]))
	// Require at least one character of the tag before opening. A bare ``` stays
	// quiet so Enter still sends (or starts a plain, unlabelled block) without
	// the picker hijacking it; the moment a language letter is typed it opens.
	if query == "" || !isLangQuery(query) {
		m.closeLang()
		return
	}

	if m.lang.active && m.lang.line == row && m.lang.start == start && m.lang.query == query {
		return
	}
	m.lang.active = true
	m.lang.line = row
	m.lang.start = start
	m.lang.query = query
	m.lang.items = langMatches(query)
	m.lang.idx = 0
	if len(m.lang.items) == 0 {
		m.closeLang()
	}
}

// isLangQuery reports whether s holds only the characters a language tag can
// contain ([a-z0-9_+#.-]). A space or other punctuation means it's not a bare
// language tag — bail so the picker stays closed and Enter still sends. (The
// empty string passes the charset check; updateLang gates it out separately.)
func isLangQuery(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '+' || r == '-' || r == '#' || r == '.':
		default:
			return false
		}
	}
	return true
}

// closeLang clears the picker.
func (m *Model) closeLang() {
	if !m.lang.active {
		return
	}
	m.lang = langState{}
}

// langMatches returns up to langLimit language tags ranked by, in order: match
// quality (exact > prefix > substring > fuzzy subsequence), then popularity
// (common chat languages float up within a tier), then match position, then
// name.
func langMatches(query string) []string {
	type cand struct {
		name  string
		band  int
		score int
	}
	var cands []cand
	for _, name := range langIndex() {
		band, score, ok := fuzzyScore(name, query)
		if !ok {
			continue
		}
		cands = append(cands, cand{name: name, band: band, score: score})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.band != b.band {
			return a.band < b.band
		}
		if pa, pb := langPopularity[a.name], langPopularity[b.name]; pa != pb {
			return pa > pb
		}
		if a.score != b.score {
			return a.score < b.score
		}
		return a.name < b.name
	})
	if len(cands) > langLimit {
		cands = cands[:langLimit]
	}
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.name
	}
	return out
}

// acceptLang replaces the typed language fragment with the selected tag at the
// captured fence position and parks the caret just past it, ready for the code
// (no trailing space — a fence's info string is a bare tag). Returns
// (cmd, true) on success or (nil, false) when there's nothing usable, so the
// caller falls through to the default key handler.
func (m *Model) acceptLang() (tea.Cmd, bool) {
	if !m.lang.active || m.lang.idx < 0 || m.lang.idx >= len(m.lang.items) {
		return nil, false
	}
	lang := m.lang.items[m.lang.idx]
	if lang == "" {
		return nil, false
	}
	lines := strings.Split(m.input.Value(), "\n")
	if m.lang.line < 0 || m.lang.line >= len(lines) {
		return nil, false
	}
	runes := []rune(lines[m.lang.line])
	_, col := m.input.CursorRowCol()
	if col > len(runes) {
		col = len(runes)
	}
	if m.lang.start > col {
		return nil, false
	}
	lines[m.lang.line] = string(runes[:m.lang.start]) + lang + string(runes[col:])
	m.history.checkpoint(m.composerContextKey(), m.input.Value())
	m.input.SetValue(strings.Join(lines, "\n"))
	// Park the caret right after the inserted tag rather than at the buffer end
	// (where SetValue leaves it), so a multi-line block keeps typing in place.
	off := 0
	for i := 0; i < m.lang.line; i++ {
		off += len([]rune(lines[i])) + 1 // +1 for the newline
	}
	off += m.lang.start + len([]rune(lang))
	m.input.SetCursorOffset(off)
	m.syncInputHeight()
	m.closeLang()
	return nil, true
}

// langPopupStyle reuses the mention/emoji dropdown frame vocabulary.
var langPopupStyle = lipgloss.NewStyle().
	Border(border).
	BorderForeground(focusedColor).
	Padding(0, 1)

// renderLangPopup returns the dropdown or "" if it shouldn't show.
func (m *Model) renderLangPopup() string {
	if !m.lang.active || len(m.lang.items) == 0 {
		return ""
	}
	dim := lipgloss.NewStyle().Foreground(dimColor)
	rows := make([]string, 0, len(m.lang.items))
	for i, name := range m.lang.items {
		if i == m.lang.idx {
			rows = append(rows, selectedRow.Render(name))
			continue
		}
		rows = append(rows, dim.Render(name))
	}
	return langPopupStyle.Render(strings.Join(rows, "\n"))
}
