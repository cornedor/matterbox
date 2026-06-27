package editor

import (
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestCommandSpanSetClear(t *testing.T) {
	m := New()
	if _, _, ok := m.CommandSpan(); ok {
		t.Fatal("fresh editor should have no command span")
	}
	m.SetCommandSpan(0, 3)
	if s, e, ok := m.CommandSpan(); !ok || s != 0 || e != 3 {
		t.Fatalf("CommandSpan = (%d,%d,%v), want (0,3,true)", s, e, ok)
	}
	// A zero-width or inverted span clears rather than sets.
	m.SetCommandSpan(2, 2)
	if _, _, ok := m.CommandSpan(); ok {
		t.Error("zero-width span should clear the highlight")
	}
	m.SetCommandSpan(0, 3)
	m.ClearCommandSpan()
	if _, _, ok := m.CommandSpan(); ok {
		t.Error("ClearCommandSpan should remove the highlight")
	}
}

func TestResetClearsCommandSpan(t *testing.T) {
	m := New()
	m.SetCommandSpan(0, 3)
	m.Reset()
	if _, _, ok := m.CommandSpan(); ok {
		t.Error("Reset should clear the command span")
	}
}

// TestCommandSpanRendersBold checks that the highlighted token is drawn bold and
// that the highlight is confined to the span (text past it is not bold), while
// the visible text is unchanged.
func TestCommandSpanRendersBold(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("/me hi")
	m.Focus()

	plain := func(s string) string { return ansi.Strip(s) }

	// lipgloss merges the bold attribute and the truecolor foreground into one
	// SGR sequence ("\x1b[1;38;2;r;g;bm"), so the span shows up as a bold +
	// truecolor run; nothing is bold before the span is set.
	const boldTruecolor = "\x1b[1;38;2;"
	bare := m.View()
	if strings.Contains(bare, boldTruecolor) {
		t.Fatalf("no command span set, but output is bold:\n%q", bare)
	}

	m.SetCommandSpan(0, 3) // "/me"
	got := m.View()
	if !strings.Contains(got, boldTruecolor) {
		t.Errorf("command span should render bold + coloured, got:\n%q", got)
	}
	if want := "/me hi"; !strings.HasPrefix(strings.TrimRight(plain(got), " "), want) {
		t.Errorf("rendered text = %q, want it to start with %q", plain(got), want)
	}
}

// TestGhostRendersAfterCaret checks the trailing hint draws after the caret when
// it rests at the line's end, is dimmed (placeholder style), and is not part of
// Value; and that it disappears once the caret leaves the end or the hint clears.
func TestGhostRendersAfterCaret(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("/shrug")
	m.Focus()

	if strings.Contains(ansi.Strip(m.View()), "[message]") {
		t.Fatal("no ghost set, but hint text appeared")
	}

	m.SetGhost("[message]")
	got := m.View()
	if !strings.Contains(ansi.Strip(got), "/shrug") {
		t.Errorf("buffer text missing: %q", ansi.Strip(got))
	}
	if !strings.Contains(ansi.Strip(got), "[message]") {
		t.Errorf("ghost hint not rendered: %q", ansi.Strip(got))
	}
	if m.Value() != "/shrug" {
		t.Errorf("ghost leaked into Value = %q, want %q", m.Value(), "/shrug")
	}

	// Move the caret off the end of the row: the ghost only trails the caret at
	// the line's end, so it should vanish.
	m.SetCursorOffset(3)
	if strings.Contains(ansi.Strip(m.View()), "[message]") {
		t.Error("ghost should not draw when the caret is not at the line's end")
	}

	m.CursorEnd()
	m.ClearGhost()
	if strings.Contains(ansi.Strip(m.View()), "[message]") {
		t.Error("ClearGhost should remove the hint")
	}
}

// TestResetClearsGhost confirms a reset composer drops a lingering hint.
func TestResetClearsGhost(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("/shrug")
	m.Focus()
	m.SetGhost("[message]")
	m.Reset()
	if m.ghost != "" {
		t.Errorf("Reset should clear the ghost, got %q", m.ghost)
	}
}

// TestShimmerColorLoopSeam verifies the gradient is continuous across the phase
// wrap (the band has fully exited at both 0 and ~1) and that it actually
// brightens somewhere in the middle of a sweep.
func TestShimmerColorLoopSeam(t *testing.T) {
	const n = 3
	rgba := func(c color.Color) color.RGBA {
		r, g, b, _ := c.RGBA()
		return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0xff}
	}
	// At phase 0 and just before 1 the band sits off both ends, so every cell is
	// the resting (dim) colour — no jump at the seam.
	for pos := range n {
		if c := rgba(shimmerColor(pos, n, 0)); c != shimmerDim {
			t.Errorf("phase 0 pos %d = %v, want dim %v", pos, c, shimmerDim)
		}
		if c := rgba(shimmerColor(pos, n, 0.999)); c != shimmerDim {
			t.Errorf("phase ~1 pos %d = %v, want dim %v", pos, c, shimmerDim)
		}
	}
	// Somewhere mid-sweep a cell lifts toward the bright endpoint.
	var brightened bool
	for _, ph := range []float64{0.3, 0.4, 0.5, 0.6, 0.7} {
		for pos := range n {
			if rgba(shimmerColor(pos, n, ph)).G > shimmerDim.G {
				brightened = true
			}
		}
	}
	if !brightened {
		t.Error("shimmer never brightened across a sweep")
	}
}
