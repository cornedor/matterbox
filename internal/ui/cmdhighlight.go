package ui

import (
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// When the composer's first line is a recognised "/command", its trigger token
// (the slash plus the command word) is drawn bold with an animated orange
// gradient that loops across it — a skeleton-loader shimmer that reads as "this
// is a live command, not literal text". The editor draws the span (see
// editor.SetCommandSpan); here we recognise the command and drive the frame
// loop. It is a sibling of the "/" autocomplete popup (updateSlash) and gated
// the same way: line 0, a leading "/", and never while editing a post (whose
// leading slash is literal).

// cmdShimmerState drives the gradient animation. active gates the frame loop so
// each recognised keystroke doesn't stack ticks; phase, in [0,1), slides the
// band and is pushed to the editor each frame.
type cmdShimmerState struct {
	active bool
	phase  float64
}

// cmdShimmerInterval is the gap between gradient frames — a calm, smooth pulse
// rather than a frantic flicker.
const cmdShimmerInterval = 90 * time.Millisecond

// cmdShimmerStep advances the phase per frame; the band loops in roughly
// cmdShimmerInterval / cmdShimmerStep ≈ 1.8s.
const cmdShimmerStep = 0.05

// cmdShimmerTickMsg advances the gradient by one frame.
type cmdShimmerTickMsg struct{}

// updateCommandHighlight recomputes the composer's command shimmer after the
// input changes: when line 0 is a recognised command the trigger token is
// marked on the editor (bold + animated) and the frame loop is armed; otherwise
// the highlight is cleared. It mirrors updateSlash and must be called from the
// same composer-edit points. Returns a Cmd that starts the animation loop when
// it isn't already running, or nil.
func (m *Model) updateCommandHighlight() tea.Cmd {
	start, end, ok := m.recognisedCommandSpan()
	if !ok {
		// Leave m.cmdShimmer.active alone: a running loop notices the cleared
		// span on its next tick and stops itself (see applyCmdShimmerTick). This
		// avoids stacking a second loop when a command is cleared and re-typed
		// before the pending tick fires.
		m.input.ClearCommandSpan()
		m.input.ClearGhost()
		return nil
	}
	m.input.SetCommandSpan(start, end)
	m.input.SetCommandPhase(m.cmdShimmer.phase)
	m.input.SetGhost(m.commandHintAfter(end))
	return m.maybeStartCmdShimmer()
}

// commandHintAfter returns the argument-usage hint to trail as dim ghost text
// after a recognised command whose trigger token is [0, end) (rune offsets), or
// "" when none should show. The hint advances as the user fills arguments — for
// `/poll "[Question]" "[Answer 1]" "[Answer 2]"...` it shows the full template
// at first, then drops the placeholders already typed, so once the question is
// entered only the answer placeholders trail. It returns "" when the composer
// spans more than the command line, the command advertises no hint, or every
// placeholder is filled. The editor only draws it while the caret rests at the
// line's end (see editor.SetGhost), so it reads as a prompt for what's next.
func (m *Model) commandHintAfter(end int) string {
	val := m.input.Value()
	if strings.ContainsRune(val, '\n') {
		return "" // past the first line; the command word is committed
	}
	runes := []rune(val)
	hint := m.commandHint(strings.ToLower(string(runes[1:end])))
	if hint == "" {
		return ""
	}
	return remainingHint(hint, string(runes[end:]))
}

// remainingHint trims the leading placeholders of a command's usage hint that
// the user has already supplied. It tokenises both the hint and the typed
// arguments quote-aware (so `"[Answer 1]"` and a typed `"two words"` each count
// as one slot), then drops as many hint slots as there are typed tokens. A
// trailing "…"/"..." slot repeats, so it keeps trailing once every listed slot
// is filled (e.g. /poll's answers). Returns "" when nothing remains.
func remainingHint(hint, typed string) string {
	slots := argTokens(hint)
	if len(slots) == 0 {
		return ""
	}
	start := len(argTokens(typed))
	if start < len(slots) {
		return strings.Join(slots[start:], " ")
	}
	if last := slots[len(slots)-1]; strings.HasSuffix(last, "...") || strings.HasSuffix(last, "…") {
		return last // a repeating final slot keeps offering itself
	}
	return ""
}

// argTokens splits s into whitespace-separated tokens, keeping a double-quoted
// run (spaces and all) as a single token, so `"[Answer 1]"` is one token and a
// typed `"two words"` counts as one argument. An unclosed quote runs to the end
// (the user is mid-typing it). Leading/trailing whitespace is ignored.
func argTokens(s string) []string {
	var tokens []string
	var cur strings.Builder
	inQuote, hasTok := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			hasTok = true
			cur.WriteRune(r)
		case !inQuote && unicode.IsSpace(r):
			if hasTok {
				tokens = append(tokens, cur.String())
				cur.Reset()
				hasTok = false
			}
		default:
			hasTok = true
			cur.WriteRune(r)
		}
	}
	if hasTok {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

// commandHint returns the argument-usage hint for a known command name (no
// leading "/", already lower-cased) — the built-in's args field or the active
// team's cached server hint — or "" when the command advertises none. Mirrors
// the union isKnownCommand checks.
func (m *Model) commandHint(name string) string {
	if c, ok := lookupSlash(name); ok {
		return c.args
	}
	ch, _ := m.composerTarget()
	for _, s := range m.serverCmds[m.commandTeamID(ch)] {
		if s.trigger == name {
			return s.hint
		}
	}
	return ""
}

// recognisedCommandSpan returns the rune span [start,end) of the leading
// "/command" token in the composer when its command word is recognised — a
// built-in (slashRegistry, including aliases) or a cached server command for the
// active team — and ok=false otherwise. The token is the slash plus the word up
// to the first whitespace, so "/me waves" highlights just "/me".
func (m *Model) recognisedCommandSpan() (start, end int, ok bool) {
	if m.editingPostID != "" {
		return 0, 0, false // a leading "/" in an edited post is literal text
	}
	val := m.input.Value()
	if len(val) < 2 || val[0] != '/' {
		return 0, 0, false
	}
	// Commands live on the first line (parseSlash requires the very start).
	line0 := val
	if i := strings.IndexByte(val, '\n'); i >= 0 {
		line0 = val[:i]
	}
	runes := []rune(line0)
	wend := 1
	for wend < len(runes) && !unicode.IsSpace(runes[wend]) {
		wend++
	}
	if wend == 1 {
		return 0, 0, false // bare "/"
	}
	if !m.isKnownCommand(strings.ToLower(string(runes[1:wend]))) {
		return 0, 0, false
	}
	// On line 0 the rune index is the offset into Value(): "/" at 0, the word at
	// [1, wend), so the trigger token is [0, wend).
	return 0, wend, true
}

// isKnownCommand reports whether name (already lower-cased, no leading "/") is a
// command matterbox or the active team's server recognises — the same union the
// "/" popup ranks.
func (m *Model) isKnownCommand(name string) bool {
	if _, ok := lookupSlash(name); ok {
		return true
	}
	ch, _ := m.composerTarget()
	for _, s := range m.serverCmds[m.commandTeamID(ch)] {
		if s.trigger == name {
			return true
		}
	}
	return false
}

// maybeStartCmdShimmer arms the frame loop if it isn't already running.
// Idempotent so each recognised keystroke doesn't stack ticks.
func (m *Model) maybeStartCmdShimmer() tea.Cmd {
	if m.cmdShimmer.active {
		return nil
	}
	m.cmdShimmer.active = true
	return cmdShimmerTickCmd()
}

// applyCmdShimmerTick advances the gradient one frame and reschedules, stopping
// the loop once the composer no longer holds a recognised command or focus has
// left the input. The tick Msg itself drives the re-render that animates the
// band (and the one that clears it when it stops).
func (m *Model) applyCmdShimmerTick() tea.Cmd {
	if !m.cmdShimmer.active {
		return nil
	}
	if m.focus != focusInput {
		m.cmdShimmer.active = false
		return nil
	}
	if _, _, ok := m.input.CommandSpan(); !ok {
		m.cmdShimmer.active = false
		return nil
	}
	m.cmdShimmer.phase += cmdShimmerStep
	if m.cmdShimmer.phase >= 1 {
		m.cmdShimmer.phase -= 1
	}
	m.input.SetCommandPhase(m.cmdShimmer.phase)
	return cmdShimmerTickCmd()
}

// cmdShimmerTickCmd schedules the next gradient frame.
func cmdShimmerTickCmd() tea.Cmd {
	return tea.Tick(cmdShimmerInterval, func(time.Time) tea.Msg {
		return cmdShimmerTickMsg{}
	})
}
