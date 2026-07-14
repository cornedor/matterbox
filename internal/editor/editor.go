// Package editor is matterbox's own multi-line text input, replacing
// charm.land/bubbles/v2/textarea for the composer (and the jira-comment / SQL
// editors). It exists so that wrap, scroll, cursor and inline decorations all
// live in one place: the grammar/spell underline overlay used to post-process
// the textarea's rendered View(), which desynced the moment the field scrolled
// (View() returns a scrolled window, but the overlay reconstructed absolute
// wrap geometry). Here decorations are drawn during the same wrap+scroll pass
// as the text, so they are correct by construction.
//
// The public surface deliberately mirrors the slice of textarea.Model that
// matterbox actually used, so call sites change little; the notable additions
// are CursorOffset/SetCursorOffset (rune offsets into Value), CursorRowCol, and
// SetDecorations.
package editor

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// Model is a multi-line text input. The zero value is not usable; call New.
// It is copied by value (matching the `m.field, cmd = m.field.Update(msg)`
// pattern), so its mutable state is held in slices — copies share backing
// arrays only until the next edit, which always reallocates the affected line.
type Model struct {
	// lines holds the logical lines of the buffer (the value split on '\n').
	// It is never empty: an empty buffer is [][]rune{{}}.
	lines [][]rune
	// row, col are the logical cursor position: row indexes lines, col is the
	// rune index within lines[row] in [0, len(lines[row])].
	row, col int
	// desiredVCol is the visual column vertical moves try to preserve. It is
	// refreshed on every horizontal move and reused by CursorUp/CursorDown so
	// travelling across short lines doesn't ratchet the column inward.
	desiredVCol int

	// width is the total inner width handed to SetWidth (prompt + content).
	// Content wraps at width-promptWidth.
	width int
	// height is the number of visual rows currently shown (the scroll window
	// height). With DynamicHeight it tracks content between Min/MaxHeight.
	height int
	// yOffset is the index of the topmost visual row shown — the scroll
	// position. Exposed via ScrollYOffset.
	yOffset int

	// MinHeight / MaxHeight bound the dynamic height (in visual rows).
	MinHeight int
	MaxHeight int
	// MaxContentHeight caps the number of logical lines retained, a cheap guard
	// against pathological pasted input. 0 means unbounded.
	MaxContentHeight int
	// DynamicHeight, when set, grows/shrinks height with content (clamped to
	// [MinHeight, MaxHeight]); otherwise height is whatever SetHeight set.
	DynamicHeight bool

	// CharLimit caps the buffer length in runes (0 = unlimited).
	CharLimit int
	// Placeholder is shown, dimmed, when the buffer is empty.
	Placeholder string

	focus bool

	// promptWidth is the column width reserved for the prompt; promptFunc
	// renders the prompt for a given global visual line index.
	promptWidth int
	promptFunc  func(visualLine int, focused bool) string

	// Styles controls text/placeholder/prompt/cursor rendering.
	Styles Styles
	// KeyMap binds editing actions to keys.
	KeyMap KeyMap

	// MarkdownHighlight, when set, styles inline markdown (bold/italic/
	// strikethrough/code spans and fenced code blocks) live as the user types,
	// keeping the markers visible. Off by default — callers that edit markdown
	// (the composer, jira comments) opt in. See markdown.go and Styles.Markdown.
	MarkdownHighlight bool

	// ContinueLists, when set, carries a markdown list marker onto the next line
	// when the caret is in a list item and a newline is inserted ("- x" ⏎ opens
	// "- ", "1. x" ⏎ opens "2. "); a newline on the still-empty item ends the
	// list. Off by default — an editor holding non-markdown text (the SQL tab)
	// doesn't want it. See list.go.
	ContinueLists bool

	// NativeCursor, when set, suppresses the drawn reverse-video caret in View:
	// the owner reads CursorViewPos and places the real terminal cursor instead
	// (so its blink, colour and shape follow the terminal). Off by default — an
	// editor whose owner can't compute its absolute screen position keeps the
	// drawn caret. See CursorViewPos.
	NativeCursor bool

	// decorations are inline styled spans (e.g. grammar underlines), addressed
	// by rune offset into Value(). Drawn during View, clipped to the scroll
	// window automatically.
	decorations []Decoration

	// cmdActive marks the leading slash-command token [cmdStart, cmdEnd) (rune
	// offsets into Value) as a recognised command: it is drawn bold with an
	// animated orange gradient (a skeleton-loader shimmer) over the text. The
	// owner advances cmdPhase, in [0,1), once per animation frame to slide the
	// band. The editor is content-agnostic — what counts as "a command" is the
	// caller's call (see SetCommandSpan).
	cmdActive        bool
	cmdStart, cmdEnd int
	cmdPhase         float64

	// ghost is dim virtual text drawn after the caret when it sits at the end of
	// its visual row — a slash command's argument hint ("[message]"). It is not
	// part of Value and never affects wrapping, the cursor, or the buffer; the
	// owner decides when it applies (see SetGhost).
	ghost string

	// selActive marks a live text selection. Its range is [min, max) of the
	// anchor and the cursor in rune-offset space (the CursorOffset coordinate
	// space): selAnchor is the fixed end set when the drag began, the cursor is
	// the moving end. Mouse-driven (see selection.go); a self-inserting key or
	// paste replaces it, backspace/delete removes it, and a movement key
	// collapses or clears it. A few scalars, so the value-copied Model stays cheap.
	selActive bool
	selAnchor int
	// selGran is the unit a drag snaps to: character (plain click), word
	// (double-click) or line (triple-click). selAnchorLo/Hi bound the fixed-end
	// unit — the word/line the drag began on — equal to selAnchor for character
	// granularity; a drag then grows the moving end a whole unit at a time.
	selGran                  selGran
	selAnchorLo, selAnchorHi int
}

// New returns a ready-to-use single-row input with default keys and styles.
func New() Model {
	m := Model{
		lines:         [][]rune{{}},
		width:         0,
		height:        1,
		MinHeight:     1,
		MaxHeight:     maxDefaultHeight,
		DynamicHeight: false,
		promptWidth:   0,
		Styles:        DefaultStyles(),
		KeyMap:        DefaultKeyMap(),
	}
	return m
}

const maxDefaultHeight = 99

// contentWidth is the wrap width for text (total width minus the prompt gutter).
func (m *Model) contentWidth() int {
	w := m.width - m.promptWidth
	if w < 1 {
		return 1
	}
	return w
}

// Value returns the buffer contents with '\n' between logical lines.
func (m *Model) Value() string {
	if len(m.lines) == 0 {
		return ""
	}
	var b strings.Builder
	for i, ln := range m.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(string(ln))
	}
	return b.String()
}

// SetValue replaces the buffer and parks the cursor at the end (matching the
// textarea behaviour callers rely on; they reposition afterwards when needed).
func (m *Model) SetValue(s string) {
	m.setLines(splitLines(s))
	m.ClearSelection()
	m.CursorEnd()
	m.recalc()
}

// setLines installs logical lines, enforcing the non-empty invariant and the
// content-height cap, and clamps the cursor into range.
func (m *Model) setLines(lines [][]rune) {
	if len(lines) == 0 {
		lines = [][]rune{{}}
	}
	if m.MaxContentHeight > 0 && len(lines) > m.MaxContentHeight {
		lines = lines[:m.MaxContentHeight]
	}
	m.lines = lines
	m.clampCursor()
}

// Reset empties the buffer and resets cursor and scroll.
func (m *Model) Reset() {
	m.lines = [][]rune{{}}
	m.row, m.col, m.desiredVCol = 0, 0, 0
	m.yOffset = 0
	m.decorations = nil
	m.ClearSelection()
	m.ClearCommandSpan()
	m.ClearGhost()
	m.recalc()
}

// Focus gives the field focus (drawing the cursor). It returns no command —
// the cursor is static (no blink), which keeps the per-keystroke render cheap.
func (m *Model) Focus() tea.Cmd {
	m.focus = true
	return nil
}

// Blur removes focus. It also drops any selection so a drag-selected range
// can't survive a focus change and silently get replaced by the next keystroke
// once the field is refocused.
func (m *Model) Blur() {
	m.focus = false
	m.ClearSelection()
}

// Focused reports whether the field has focus.
func (m *Model) Focused() bool { return m.focus }

// SetPromptFunc sets the prompt renderer and the column width reserved for it.
// fn is called per global visual line (0-based) with the focus state.
func (m *Model) SetPromptFunc(promptWidth int, fn func(visualLine int, focused bool) string) {
	m.promptWidth = promptWidth
	m.promptFunc = fn
	m.recalc()
}

// SetWidth sets the total inner width (prompt + content). Content wraps at
// width-promptWidth.
func (m *Model) SetWidth(w int) {
	if w < 1 {
		w = 1
	}
	m.width = w
	m.recalc()
}

// Width returns the total inner width.
func (m *Model) Width() int { return m.width }

// Height returns the current number of visual rows shown.
func (m *Model) Height() int { return m.height }

// SetHeight sets the visual-row height. With DynamicHeight on, this is an
// initial value that subsequent edits may override within [Min,Max]Height.
func (m *Model) SetHeight(h int) {
	m.height = clamp(h, 1, m.maxHeight())
	m.clampScroll()
}

func (m *Model) maxHeight() int {
	if m.MaxHeight > 0 {
		return m.MaxHeight
	}
	return maxDefaultHeight
}

// ScrollYOffset returns the index of the topmost visible visual row.
func (m *Model) ScrollYOffset() int { return m.yOffset }

// length returns the buffer length in runes (newlines counted as one each),
// matching CursorOffset's coordinate space.
func (m *Model) length() int {
	n := 0
	for i, ln := range m.lines {
		if i > 0 {
			n++ // newline
		}
		n += len(ln)
	}
	return n
}

// recalc recomputes dynamic height and re-clamps the scroll window. Call after
// any change to content, width, or cursor.
func (m *Model) recalc() {
	if m.DynamicHeight {
		// Count the cursor-aware layout so a caret parked past a perfectly full
		// line (which lands on a reserved trailing row) still gets a row to
		// live in, rather than scrolling the real content out of view.
		total := len(m.layout(true))
		m.height = clamp(total, m.MinHeight, m.maxHeight())
	} else if m.height < 1 {
		m.height = 1
	}
	m.clampScroll()
}

// splitLines splits a string into logical lines of runes, normalising CRLF/CR
// to LF first. The result always has at least one (possibly empty) line.
func splitLines(s string) [][]rune {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	parts := strings.Split(s, "\n")
	out := make([][]rune, len(parts))
	for i, p := range parts {
		out[i] = []rune(p)
	}
	return out
}

// sanitize cleans runes coming from a keystroke or paste: CRLF/CR become LF,
// tabs become a single space, and other control runes (except LF) are dropped.
func sanitize(rs []rune) []rune {
	out := make([]rune, 0, len(rs))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\r':
			// Collapse CRLF / lone CR to a single LF.
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			out = append(out, '\n')
		case r == '\n':
			out = append(out, '\n')
		case r == '\t':
			out = append(out, ' ')
		case unicode.IsControl(r):
			// drop
		default:
			out = append(out, r)
		}
	}
	return out
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
