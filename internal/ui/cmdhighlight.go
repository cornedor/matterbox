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
		return nil
	}
	m.input.SetCommandSpan(start, end)
	m.input.SetCommandPhase(m.cmdShimmer.phase)
	return m.maybeStartCmdShimmer()
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
