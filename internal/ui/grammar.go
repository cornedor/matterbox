package ui

import (
	"context"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"matterbox/internal/editor"
	"matterbox/internal/languagetool"
)

// Grammar/spell check for the composer. When enabled (config.language_tool),
// the draft is sent to a LanguageTool server a short beat after typing stops;
// findings are underlined in place — pushed to the input as inline curly
// underlines (see syncGrammarDecorations), which the editor draws during its
// own wrap+scroll pass so they always line up — and alt+g opens a popup of
// suggestions for the mistake under the cursor.

const (
	// grammarDebounce is the quiet period after the last keystroke before a
	// check fires, so a fast typist sends one request per pause, not per key.
	grammarDebounce = 600 * time.Millisecond
	// grammarTimeout bounds a single check; a slow/down server just leaves the
	// previous underlines in place.
	grammarTimeout = 5 * time.Second
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
	// Draft changed and no result is ready yet: drop the now-stale underlines
	// until the pending check returns (mirrors the old overlay, which only drew
	// while checkedText == the live value). Rebuilt rather than cleared, so this
	// doesn't take the effect previews down with them.
	m.syncComposerDecorations()
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
		// Deliberately silent in the UI (see above), which is exactly why it is
		// worth reporting: a LanguageTool server that stopped answering looks
		// to the user like a feature that simply does nothing. Once per
		// session — a check fires on every debounce tick.
		if m.firstTime("grammar_check/error") {
			m.recordFeature("grammar_check", "auto", noLatency, 0, msg.err)
		}
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
	m.syncComposerDecorations()
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
	m.syncComposerDecorations() // the findings are gone; any effect preview is not
}

// ---- cursor → match -------------------------------------------------------

// matchAtCursor returns the index of the match the cursor sits in (or just
// past the end of), or -1. Findings are tied to checkedText, so it only reports
// while that still equals the live draft.
func (m *Model) matchAtCursor() int {
	if len(m.grammar.matches) == 0 || m.grammar.checkedText != m.input.Value() {
		return -1
	}
	off := m.input.CursorOffset()
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
	m.history.checkpoint(m.composerContextKey(), val)
	m.input.SetValue(newVal)
	// Land the cursor just after the inserted correction rather than at the end
	// of the draft (where SetValue would leave it), so fixing a word mid-message
	// doesn't fling the caret away from where the user was working.
	m.input.SetCursorOffset(mt.Offset + len([]rune(mt.Replacements[i])))
	m.syncInputHeight()
	m.closeGrammarPopup()
	m.clearGrammar()
	// A suggestion accepted is the feature paying off — the checks themselves
	// run automatically and counting those would measure typing, not use.
	m.recordFeature("grammar_check", "key", noLatency, len(mt.Replacements), nil)
	// The app just rewrote the draft; an undo in the next few seconds is a
	// verdict on the suggestion (see noteUndo).
	m.noteAutoComposerEdit()
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

// ---- underline decorations ------------------------------------------------

// grammarDecorations renders the current findings as inline curly underlines, or
// nil when they no longer apply. The editor draws decorations during its own
// wrap+scroll pass, so offsets always line up with what's on screen — even when
// the composer has scrolled.
func (m *Model) grammarDecorations() []editor.Decoration {
	if !m.grammarEnabled() {
		return nil
	}
	// Findings are addressed by rune offset into the checked draft; only paint
	// while that still equals the live value, so a half-typed change never
	// shows stale squiggles at the wrong place.
	if len(m.grammar.matches) == 0 || m.grammar.checkedText != m.input.Value() {
		return nil
	}
	decos := make([]editor.Decoration, 0, len(m.grammar.matches))
	for _, mt := range m.grammar.matches {
		if mt.Length <= 0 {
			continue
		}
		decos = append(decos, editor.Decoration{
			Start: mt.Offset,
			End:   mt.Offset + mt.Length,
			Style: lipgloss.NewStyle().
				UnderlineStyle(lipgloss.UnderlineCurly).
				UnderlineColor(grammarColor(mt.IssueType)),
		})
	}
	return decos
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
