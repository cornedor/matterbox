package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"matterbox/internal/effects"
)

// pad right-pads a rendered content string with spaces to an exact visual width,
// the way the viewport pads every line before the paint passes see it.
func pad(s string, w int) string {
	if g := ansi.StringWidth(s); g < w {
		return s + strings.Repeat(" ", w-g)
	}
	return s
}

func scrollStartRune() string { return string(effStart(effects.Scroll)) }
func endRune() string         { return string(effSentinelEnd) }

// A width-preservation check every geometry frame must satisfy: no row the paint
// pass returns may change its visual width, or the pane's border drifts.
func assertWidths(t *testing.T, in, out []string) {
	t.Helper()
	if len(in) != len(out) {
		t.Fatalf("line count changed: %d -> %d", len(in), len(out))
	}
	for i := range in {
		if a, b := ansi.StringWidth(in[i]), ansi.StringWidth(out[i]); a != b {
			t.Errorf("line %d width changed: %d -> %d (%q -> %q)", i, a, b, in[i], out[i])
		}
	}
}

// The scroll marquee rotates the span's cells (text slides and wraps) while
// preserving width, and the visible text stays a rotation of the original.
func TestResolveGeometryScrollRotates(t *testing.T) {
	const w = 12
	line := pad(scrollStartRune()+"news"+endRune(), w)
	in := []string{line}

	// phase 0: shift = floor(0*4) = 0 → unrotated.
	out0 := resolveGeometry(in, 0.0, 0)
	assertWidths(t, in, out0)
	if got := strings.TrimRight(ansi.Strip(out0[0]), " "); got != "news" {
		t.Errorf("phase 0 scroll = %q, want %q", got, "news")
	}

	// phase 0.5: shift = floor(0.5*4) = 2 → "news" -> "wsne".
	out1 := resolveGeometry(in, 0.5, 0)
	assertWidths(t, in, out1)
	got := strings.TrimRight(ansi.Strip(out1[0]), " ")
	if got != "wsne" {
		t.Errorf("phase 0.5 scroll = %q, want %q", got, "wsne")
	}
	if sortStr(got) != sortStr("news") {
		t.Errorf("scroll dropped/added a letter: %q", got)
	}
	for _, l := range out1 {
		if hasEffectSentinel(l) {
			t.Errorf("scroll left a sentinel behind: %q", l)
		}
	}
}

// A box with no geometric sentinels is returned untouched — the cheap fast path.
func TestResolveGeometryNoOp(t *testing.T) {
	in := []string{"  plain line one", "  plain line two"}
	out := resolveGeometry(in, 0.37, 1)
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("line %d altered with no effects present: %q -> %q", i, in[i], out[i])
		}
	}
}

// TestScrollPipelineEndToEnd drives a real wire message — composed exactly as the
// composer sends it (compileEffects), decoded as the render path decodes it
// (effectsPreprocess), wrapped, and painted through both passes — and checks the
// whole chain never loses or duplicates a character and never changes width. This
// is the integration guard on top of the unit tests.
func TestScrollPipelineEndToEnd(t *testing.T) {
	raw := compileEffects(`hi \scroll{news today} bye`)
	if raw == `hi \scroll{news today} bye` {
		t.Fatal("compileEffects produced no payload")
	}
	marked := effectsPreprocess(raw) // decode payload + inject sentinels, as the render path does
	if marked == raw {
		t.Fatal("effectsPreprocess did not decode the payload into sentinels")
	}

	const width = 40
	lines := appendBodyLines(nil, "  "+marked, width) // body lines carry the two-space indent
	// The viewport pads every visible line to full width before the paint passes
	// run; mirror that here.
	for i := range lines {
		lines[i] = pad(lines[i], width)
	}
	painted := resolveGeometry(resolveEffects(lines, 0.5, 0), 0.5, 0)

	assertWidths(t, lines, painted)

	joined := ansi.Strip(strings.Join(painted, ""))
	if hasEffectSentinel(joined) {
		t.Error("a sentinel survived the paint passes")
	}
	// No character is lost or duplicated across the painted line.
	want := "hinewstodaybye"
	if got := onlyLetters(joined); sortStr(got) != sortStr(want) {
		t.Errorf("letters across the painted line = %q, want the letters of %q", got, want)
	}
}

// onlyLetters keeps a-z/A-Z, dropping spaces and control bytes — for asserting
// which letters ended up somewhere in the output.
func onlyLetters(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sortStr(s string) string {
	r := []rune(s)
	for i := range r {
		for j := i + 1; j < len(r); j++ {
			if r[j] < r[i] {
				r[i], r[j] = r[j], r[i]
			}
		}
	}
	return string(r)
}
