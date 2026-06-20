package ui

import (
	"context"
	"image/color"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"matterbox/internal/languagetool"
)

// Grammar/spell check for the composer. When enabled (config.language_tool),
// the draft is sent to a LanguageTool server a short beat after typing stops;
// findings are underlined in place (computed from the textarea's own rendered
// output, see grammarOverlay) and alt+g opens a popup of suggestions for the
// mistake under the cursor.

const (
	// grammarDebounce is the quiet period after the last keystroke before a
	// check fires, so a fast typist sends one request per pause, not per key.
	grammarDebounce = 600 * time.Millisecond
	// grammarTimeout bounds a single check; a slow/down server just leaves the
	// previous underlines in place.
	grammarTimeout = 5 * time.Second
	// grammarPromptWidth is the composer prompt width ("> " / "↳ " / "✎ "), the
	// column offset of the text within each rendered line. Matches the
	// SetPromptFunc(2, …) in Model.New.
	grammarPromptWidth = 2
	// grammarCacheCap bounds the per-draft result cache.
	grammarCacheCap = 64
)

// grammarState holds the live grammar-check state for the shared composer.
type grammarState struct {
	// seq guards against stale debounce ticks / responses: only the latest
	// matters once the draft changes again.
	seq int
	// checkedText is the exact draft the current matches correspond to.
	// Underlines only render while it equals the live input value, so a
	// half-typed change never paints stale spans at the wrong offsets.
	checkedText string
	matches     []languagetool.Match
	// cache memoises results by draft text so re-deriving an earlier draft
	// (e.g. after deleting back to it) skips the round-trip.
	cache map[string][]languagetool.Match
	// popup is the suggestion dropdown; popupIdx points at the match it shows.
	popup    bool
	popupIdx int
}

func newGrammarState() grammarState {
	return grammarState{cache: map[string][]languagetool.Match{}}
}

// grammarEnabled reports whether the feature is configured/on.
func (m *Model) grammarEnabled() bool { return m.ltClient != nil }

type grammarDebounceMsg struct{ seq int }

type grammarResultMsg struct {
	seq     int
	text    string
	matches []languagetool.Match
	err     error
}

// scheduleGrammarCheck is called after the draft changes. It clears state on an
// empty draft, applies a cached result immediately, or arms a debounced check.
func (m *Model) scheduleGrammarCheck() tea.Cmd {
	if !m.grammarEnabled() {
		return nil
	}
	text := m.input.Value()
	if strings.TrimSpace(text) == "" {
		m.clearGrammar()
		return nil
	}
	if m.grammar.checkedText == text {
		return nil // already have matches for this exact draft
	}
	if cached, ok := m.grammar.cache[text]; ok {
		m.setGrammarMatches(text, cached)
		return nil
	}
	m.grammar.seq++
	seq := m.grammar.seq
	return tea.Tick(grammarDebounce, func(time.Time) tea.Msg {
		return grammarDebounceMsg{seq: seq}
	})
}

// applyGrammarDebounce fires when a debounce tick matures: if it's still the
// latest and the draft is unchecked, it kicks off the HTTP check.
func (m *Model) applyGrammarDebounce(msg grammarDebounceMsg) tea.Cmd {
	if !m.grammarEnabled() || msg.seq != m.grammar.seq {
		return nil
	}
	text := m.input.Value()
	if strings.TrimSpace(text) == "" {
		m.clearGrammar()
		return nil
	}
	if m.grammar.checkedText == text {
		return nil
	}
	if cached, ok := m.grammar.cache[text]; ok {
		m.setGrammarMatches(text, cached)
		return nil
	}
	return m.runGrammarCheck(msg.seq, text)
}

// runGrammarCheck performs the LanguageTool call off the UI goroutine.
func (m *Model) runGrammarCheck(seq int, text string) tea.Cmd {
	client := m.ltClient
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), grammarTimeout)
		defer cancel()
		matches, err := client.Check(ctx, text)
		return grammarResultMsg{seq: seq, text: text, matches: matches, err: err}
	}
}

// applyGrammarResult stores a finished check. Results are cached regardless of
// staleness, but only displayed if they still match the live draft. Errors stay
// silent — a flaky local server shouldn't spam the status line every keystroke.
func (m *Model) applyGrammarResult(msg grammarResultMsg) tea.Cmd {
	if msg.err != nil {
		return nil
	}
	m.cacheGrammar(msg.text, msg.matches)
	if m.grammarEnabled() && msg.text == m.input.Value() {
		m.setGrammarMatches(msg.text, msg.matches)
	}
	return nil
}

func (m *Model) setGrammarMatches(text string, matches []languagetool.Match) {
	m.grammar.checkedText = text
	m.grammar.matches = matches
	if m.grammar.popupIdx >= len(matches) {
		m.grammar.popupIdx = 0
	}
	if len(matches) == 0 {
		m.grammar.popup = false
	}
}

func (m *Model) cacheGrammar(text string, matches []languagetool.Match) {
	if m.grammar.cache == nil {
		m.grammar.cache = map[string][]languagetool.Match{}
	}
	if _, ok := m.grammar.cache[text]; !ok && len(m.grammar.cache) >= grammarCacheCap {
		// Cheap bound: drop an arbitrary entry. The working set is one draft.
		for k := range m.grammar.cache {
			delete(m.grammar.cache, k)
			break
		}
	}
	m.grammar.cache[text] = matches
}

// clearGrammar drops the current findings (e.g. the draft was sent or cleared).
// The cache is kept — the same text may come back.
func (m *Model) clearGrammar() {
	m.grammar.checkedText = ""
	m.grammar.matches = nil
	m.grammar.popup = false
	m.grammar.popupIdx = 0
}

// ---- cursor → match -------------------------------------------------------

// inputCursorOffset returns the cursor's rune offset within the whole draft,
// counting each newline as one rune (matching LanguageTool's offsets).
func (m *Model) inputCursorOffset() int {
	row := m.input.Line()
	li := m.input.LineInfo()
	col := li.StartColumn + li.ColumnOffset
	lines := strings.Split(m.input.Value(), "\n")
	off := 0
	for i := 0; i < row && i < len(lines); i++ {
		off += len([]rune(lines[i])) + 1
	}
	return off + col
}

// matchAtCursor returns the index of the match the cursor sits in (or just
// past the end of), or -1. Findings are tied to checkedText, so it only reports
// while that still equals the live draft.
func (m *Model) matchAtCursor() int {
	if len(m.grammar.matches) == 0 || m.grammar.checkedText != m.input.Value() {
		return -1
	}
	off := m.inputCursorOffset()
	for i, mt := range m.grammar.matches {
		if off >= mt.Offset && off <= mt.Offset+mt.Length {
			return i
		}
	}
	return -1
}

// ---- popup control --------------------------------------------------------

// openOrCycleGrammarPopup opens the suggestion popup on the mistake at the
// cursor (or the first one), or — if already open — advances to the next one.
func (m *Model) openOrCycleGrammarPopup() {
	if len(m.grammar.matches) == 0 {
		return
	}
	if m.grammar.popup {
		m.cycleGrammarPopup(1)
		return
	}
	idx := m.matchAtCursor()
	if idx < 0 {
		idx = 0
	}
	m.grammar.popupIdx = idx
	m.grammar.popup = true
}

func (m *Model) cycleGrammarPopup(dir int) {
	n := len(m.grammar.matches)
	if n == 0 {
		m.grammar.popup = false
		return
	}
	m.grammar.popupIdx = (m.grammar.popupIdx + dir + n) % n
}

func (m *Model) closeGrammarPopup() { m.grammar.popup = false }

// applyGrammarSuggestion replaces the targeted mistake with its i-th
// suggestion, then re-checks. An out-of-range i (a digit with no suggestion) is
// ignored so the popup stays put.
func (m *Model) applyGrammarSuggestion(i int) tea.Cmd {
	if m.grammar.popupIdx < 0 || m.grammar.popupIdx >= len(m.grammar.matches) {
		m.closeGrammarPopup()
		return nil
	}
	mt := m.grammar.matches[m.grammar.popupIdx]
	if i < 0 || i >= len(mt.Replacements) {
		return nil
	}
	val := m.input.Value()
	if m.grammar.checkedText != val { // draft moved under us; bail safely
		m.closeGrammarPopup()
		return nil
	}
	runes := []rune(val)
	if mt.Offset < 0 || mt.Offset+mt.Length > len(runes) {
		m.closeGrammarPopup()
		return nil
	}
	newVal := string(runes[:mt.Offset]) + mt.Replacements[i] + string(runes[mt.Offset+mt.Length:])
	m.input.SetValue(newVal)
	m.syncInputHeight()
	m.closeGrammarPopup()
	m.clearGrammar()
	return m.scheduleGrammarCheck()
}

// matchText is the flagged substring of the current draft for match mt.
func (m *Model) matchText(mt languagetool.Match) string {
	runes := []rune(m.grammar.checkedText)
	if mt.Offset < 0 || mt.Offset+mt.Length > len(runes) {
		return ""
	}
	return string(runes[mt.Offset : mt.Offset+mt.Length])
}

// ---- styling --------------------------------------------------------------

// grammarColor maps a LanguageTool issue type to an underline colour: red for
// spelling, amber for grammar, blue for everything else (style, typography…).
func grammarColor(issueType string) color.Color {
	switch issueType {
	case "misspelling":
		return lipgloss.Color("#ff5555")
	case "grammar":
		return lipgloss.Color("#e5a50a")
	default:
		return lipgloss.Color("#5599ff")
	}
}

// grammarLabel is the human category shown in the popup title / footer hint.
func grammarLabel(mt languagetool.Match) string {
	switch mt.IssueType {
	case "misspelling":
		return "Spelling"
	case "grammar":
		return "Grammar"
	case "style":
		return "Style"
	case "typographical":
		return "Typography"
	}
	if mt.Category != "" {
		// Title-case a category id like "CONFUSED_WORDS" → "Confused words".
		s := strings.ToLower(strings.ReplaceAll(mt.Category, "_", " "))
		return strings.ToUpper(s[:1]) + s[1:]
	}
	return "Issue"
}

// grammarTextStyle is the textarea's own text style (bg/fg), which the
// underline overlay inherits so a restyled word keeps the composer's colours.
func (m *Model) grammarTextStyle() lipgloss.Style {
	st := m.input.Styles()
	state := st.Blurred
	if m.input.Focused() {
		state = st.Focused
	}
	return state.Text.Inherit(state.Base).Inline(true)
}

// ---- underline overlay ----------------------------------------------------

// grammarOverlay paints curly underlines onto the textarea's rendered output.
// It only acts when the findings still match the live draft, so offsets always
// line up with what's on screen.
func (m *Model) grammarOverlay(view string) string {
	if !m.grammarEnabled() || len(m.grammar.matches) == 0 {
		return view
	}
	value := m.input.Value()
	if value == "" || m.grammar.checkedText != value {
		return view
	}
	lines := strings.Split(view, "\n")
	if len(lines) == 0 {
		return view
	}
	// Derive the content wrap width from the rendered first line (prompt +
	// content padded to the inner width) rather than re-deriving the textarea's
	// reserved-width arithmetic — this stays correct if that math changes.
	contentWidth := ansi.StringWidth(lines[0]) - grammarPromptWidth
	if contentWidth < 1 {
		return view
	}
	// Skip underlining the word the cursor sits inside: we substitute flagged
	// cells, which would otherwise paint over the (inline) cursor block. The
	// squiggle returns the moment the cursor leaves the word.
	matches := m.grammar.matches
	if m.input.Focused() {
		off := m.inputCursorOffset()
		kept := make([]languagetool.Match, 0, len(matches))
		for _, mt := range matches {
			if off >= mt.Offset && off < mt.Offset+mt.Length {
				continue
			}
			kept = append(kept, mt)
		}
		matches = kept
	}
	ranges := computeUnderlineRanges(value, contentWidth, grammarPromptWidth, matches)
	if len(ranges) == 0 {
		return view
	}
	return applyUnderlineOverlay(lines, ranges, m.grammarTextStyle())
}

// cellPos is a source rune's position once its logical line is soft-wrapped:
// which visual sub-line it lands on and the visible column where it starts.
type cellPos struct{ sub, col int }

// underlineRange is one contiguous span to underline on a single rendered line.
type underlineRange struct {
	line      int    // index into the rendered visual lines
	c0, c1    int    // visible column range, prompt offset already included
	plain     string // the source text of the span (re-styled cleanly)
	issueType string
}

// computeUnderlineRanges maps LanguageTool match offsets (rune offsets into the
// draft) to visible-column spans on the textarea's wrapped, prompt-prefixed
// visual lines.
func computeUnderlineRanges(value string, width, promptWidth int, matches []languagetool.Match) []underlineRange {
	if len(matches) == 0 || width <= 0 {
		return nil
	}
	logical := strings.Split(value, "\n")
	type lineInfo struct {
		startOff int
		runes    []rune
		pos      []cellPos
		baseVis  int
	}
	infos := make([]lineInfo, len(logical))
	off, vis := 0, 0
	for i, l := range logical {
		rs := []rune(l)
		pos, subs := wrapCellPositions(rs, width)
		infos[i] = lineInfo{startOff: off, runes: rs, pos: pos, baseVis: vis}
		off += len(rs) + 1 // +1 for the newline
		vis += subs
	}

	var out []underlineRange
	for _, mt := range matches {
		if mt.Length <= 0 {
			continue
		}
		start, end := mt.Offset, mt.Offset+mt.Length
		for i := range infos {
			li := &infos[i]
			lineStart := li.startOff
			lineEnd := li.startOff + len(li.runes) // exclusive of the newline
			if start < lineStart || start > lineEnd {
				continue
			}
			a := start - lineStart
			b := end - lineStart
			if b > len(li.runes) {
				b = len(li.runes) // clamp a match that spills past this line
			}
			if a < 0 || a >= b || a >= len(li.pos) {
				break
			}
			// Walk the covered runes, emitting one range per visual sub-line.
			curSub, segStart := -1, a
			var c0, c1 int
			emit := func(end int) {
				out = append(out, underlineRange{
					line:      li.baseVis + curSub,
					c0:        c0 + promptWidth,
					c1:        c1 + promptWidth,
					plain:     string(li.runes[segStart:end]),
					issueType: mt.IssueType,
				})
			}
			for k := a; k < b && k < len(li.pos); k++ {
				cp := li.pos[k]
				w := ansi.StringWidth(string(li.runes[k]))
				switch {
				case curSub == -1:
					curSub, segStart, c0, c1 = cp.sub, k, cp.col, cp.col+w
				case cp.sub != curSub:
					emit(k)
					curSub, segStart, c0, c1 = cp.sub, k, cp.col, cp.col+w
				default:
					c1 = cp.col + w
				}
			}
			if curSub != -1 {
				emit(b)
			}
			break
		}
	}
	return out
}

// applyUnderlineOverlay rewrites the given visual lines, replacing each flagged
// span with the same plain text re-styled with a coloured curly underline. Each
// span is substituted (not bracketed) so no stray reset inside the original
// styled run can cancel the underline. The line is rebuilt in one left-to-right
// pass, cutting the gaps between spans from the *original* line via ansi.Cut
// (which re-asserts the pen state at each cut), so the escape volume stays
// O(spans) — important on this per-keystroke render path.
func applyUnderlineOverlay(lines []string, ranges []underlineRange, base lipgloss.Style) string {
	byLine := map[int][]underlineRange{}
	for _, r := range ranges {
		if r.line < 0 || r.line >= len(lines) || r.c1 <= r.c0 {
			continue
		}
		byLine[r.line] = append(byLine[r.line], r)
	}
	for ln, rs := range byLine {
		sort.Slice(rs, func(i, j int) bool { return rs[i].c0 < rs[j].c0 })
		orig := lines[ln]
		full := ansi.StringWidth(orig) + 8
		var b strings.Builder
		prev := 0
		for _, r := range rs {
			if r.c0 < prev {
				continue // overlapping span (rare); skip to avoid corruption
			}
			b.WriteString(ansi.Cut(orig, prev, r.c0))
			b.WriteString(base.
				UnderlineStyle(lipgloss.UnderlineCurly).
				UnderlineColor(grammarColor(r.issueType)).
				Render(r.plain))
			prev = r.c1
		}
		b.WriteString(ansi.Cut(orig, prev, full))
		lines[ln] = b.String()
	}
	return strings.Join(lines, "\n")
}

// wrapCellPositions soft-wraps a logical line the way charm's textarea does and
// returns, for each source rune, the (sub-line, column) it renders at, plus the
// number of sub-lines. Mirrors textarea.wrap so our columns match the screen.
func wrapCellPositions(runes []rune, width int) ([]cellPos, int) {
	sublines := taWrap(runes, width)
	pos := make([]cellPos, len(runes))
	src := 0
	for s, sub := range sublines {
		n := len(sub)
		if s == len(sublines)-1 {
			n-- // the last sub-line carries one synthetic trailing space
		}
		col := 0
		for k := 0; k < n && src < len(runes); k++ {
			pos[src] = cellPos{sub: s, col: col}
			col += ansi.StringWidth(string(sub[k]))
			src++
		}
	}
	for src < len(runes) { // defensive: shouldn't trigger
		pos[src] = cellPos{sub: len(sublines) - 1}
		src++
	}
	return pos, len(sublines)
}

// taWrap is a faithful port of charm.land/bubbles/v2 textarea's unexported
// wrap(): a greedy word-wrap that appends a synthetic trailing space to the
// final row. Kept in lock-step via grammar_test.go's comparison against a real
// textarea's rendered breaks.
func taWrap(runes []rune, width int) [][]rune {
	if width <= 0 {
		return [][]rune{append([]rune(nil), runes...)}
	}
	w := func(rs []rune) int { return ansi.StringWidth(string(rs)) }
	rwid := func(r rune) int { return ansi.StringWidth(string(r)) }
	var (
		lines  = [][]rune{{}}
		word   []rune
		row    int
		spaces int
	)
	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}
		if spaces > 0 {
			if w(lines[row])+w(word)+spaces > width {
				row++
				lines = append(lines, []rune{})
			}
			lines[row] = append(lines[row], word...)
			lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
			spaces = 0
			word = nil
		} else if len(word) > 0 {
			if w(word)+rwid(word[len(word)-1]) > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}
	if w(lines[row])+w(word)+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		spaces++
		lines[row+1] = append(lines[row+1], []rune(strings.Repeat(" ", spaces))...)
	} else {
		lines[row] = append(lines[row], word...)
		spaces++
		lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
	}
	return lines
}

// ---- footer hint + popup --------------------------------------------------

var grammarPopupStyle = lipgloss.NewStyle().
	Border(border).
	BorderForeground(focusedColor).
	Padding(0, 1)

// grammarHint is the one-line footer cue shown when the cursor rests on a
// mistake and the popup is closed: "✗ Spelling: sentnce → sentence (alt+g)".
func (m *Model) grammarHint() string {
	if !m.grammarEnabled() || m.grammar.popup || m.focus != focusInput {
		return ""
	}
	idx := m.matchAtCursor()
	if idx < 0 {
		return ""
	}
	mt := m.grammar.matches[idx]
	hint := "✗ " + grammarLabel(mt)
	cue := " (alt+g)"
	if len(mt.Replacements) > 0 {
		hint += ": " + truncate(m.matchText(mt), 18) + " → " + truncate(mt.Replacements[0], 18)
		cue = " (tab · alt+g)" // tab applies the top suggestion
	}
	hint += cue
	st := lipgloss.NewStyle().Foreground(grammarColor(mt.IssueType))
	return st.Render(truncate(hint, 64))
}

// renderGrammarPopup is the suggestion dropdown shown above the composer. It
// shares the mention/emoji popup slot, so it returns "" unless it's open.
func (m *Model) renderGrammarPopup() string {
	if !m.grammar.popup || m.grammar.popupIdx >= len(m.grammar.matches) {
		return ""
	}
	mt := m.grammar.matches[m.grammar.popupIdx]
	dim := lipgloss.NewStyle().Foreground(dimColor)
	head := lipgloss.NewStyle().Foreground(grammarColor(mt.IssueType)).Render(grammarLabel(mt))
	if len(m.grammar.matches) > 1 {
		head += dim.Render("  " + itoa(m.grammar.popupIdx+1) + "/" + itoa(len(m.grammar.matches)))
	}
	rows := []string{head}
	if flagged := m.matchText(mt); flagged != "" {
		rows = append(rows, lipgloss.NewStyle().Strikethrough(true).Render(flagged))
	}
	if len(mt.Replacements) == 0 {
		rows = append(rows, dim.Render(truncate(mt.Message, 48)))
	} else {
		for i, rep := range mt.Replacements {
			rows = append(rows, dim.Render(itoa(i+1)+" ")+rep)
		}
	}
	foot := "esc close"
	if len(mt.Replacements) > 0 {
		foot = "1-9 fix · " + foot
	}
	if len(m.grammar.matches) > 1 {
		foot = "tab next · " + foot
	}
	rows = append(rows, dim.Render(foot))
	return grammarPopupStyle.Render(strings.Join(rows, "\n"))
}
