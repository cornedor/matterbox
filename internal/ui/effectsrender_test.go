package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"

	"matterbox/internal/effects"
	"matterbox/internal/viewport"
)

// stripSentinels removes the effect sentinels so a test can compare against the
// plain text.
func stripSentinels(s string) string {
	return strings.Map(func(r rune) rune {
		if r > effSentinelBase && r <= effSentinelEnd {
			return -1
		}
		return r
	}, s)
}

func TestInjectEffectSentinelsBracketsSpan(t *testing.T) {
	visible := "ship it now"
	spans := []effects.Span{{ID: effects.Shimmer, Start: 5, Len: 2}} // "it"
	got := injectEffectSentinels(visible, spans)

	if stripSentinels(got) != visible {
		t.Fatalf("stripping sentinels gave %q; want %q", stripSentinels(got), visible)
	}
	want := "ship " + string(effStart(effects.Shimmer)) + "it" + string(effSentinelEnd) + " now"
	if got != want {
		t.Fatalf("got %q; want %q", got, want)
	}
}

// A span whose offsets no longer fit the text (e.g. after a foreign edit) is
// dropped, not applied to the wrong runes.
func TestInjectDropsStaleSpans(t *testing.T) {
	visible := "short"
	spans := []effects.Span{{ID: effects.Shimmer, Start: 3, Len: 99}}
	if got := injectEffectSentinels(visible, spans); got != visible {
		t.Fatalf("stale span was not dropped: %q", got)
	}
}

func TestResolveEffectsStripsSentinelsAndPreservesWidth(t *testing.T) {
	marked := "ship " + string(effStart(effects.Rainbow)) + "it" + string(effSentinelEnd) + " now"
	out := resolveEffects([]string{marked}, effectStaticPhase, 0)

	if hasEffectSentinel(out[0]) {
		t.Fatal("sentinels survived resolveEffects")
	}
	if got := lipgloss.Width(out[0]); got != lipgloss.Width("ship it now") {
		t.Fatalf("width changed: got %d, want %d", got, lipgloss.Width("ship it now"))
	}
	// The plain text (SGR stripped) is unchanged, and colour was applied.
	if plain := ansi.Strip(out[0]); plain != "ship it now" {
		t.Fatalf("plain text = %q; want %q", plain, "ship it now")
	}
	if !strings.Contains(out[0], "\x1b[38;2;") {
		t.Fatal("expected a truecolor SGR in the coloured output")
	}
}

func TestResolveEffectsNoSentinelIsNoOp(t *testing.T) {
	in := []string{"a plain line", "another \x1b[1mstyled\x1b[0m line"}
	out := resolveEffects(in, effectStaticPhase, 0)
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("line %d changed: %q -> %q", i, in[i], out[i])
		}
	}
}

// A span split across a soft-wrap keeps painting on the continuation line: the
// active effect carries over even though the start sentinel is only on line one.
func TestResolveEffectsCarriesAcrossWrap(t *testing.T) {
	line1 := "aaa " + string(effStart(effects.Glow)) + "bbb"
	line2 := "ccc" + string(effSentinelEnd) + " ddd"
	out := resolveEffects([]string{line1, line2}, effectStaticPhase, 0)
	// "ccc" on line two is inside the span, so it must be coloured.
	if !strings.Contains(out[1], "\x1b[38;2;") {
		t.Fatal("continuation line was not coloured")
	}
	if hasEffectSentinel(out[0]) || hasEffectSentinel(out[1]) {
		t.Fatal("sentinels survived")
	}
}

// End to end: the composer text a user types, through the send-side compile and
// the whole render side, must come out as clean coloured text with no sentinels.
func TestFullEffectsPipeline(t *testing.T) {
	wire := compileEffects("gg \\rainbow{well played}") // what goes on the wire

	body := renderMarkdown(effectsPreprocess(wire), nil, nil, "")
	lines := resolveEffects(appendBodyLines(nil, body, 80), effectStaticPhase, 0)
	joined := strings.Join(lines, "\n")

	if hasEffectSentinel(joined) {
		t.Fatal("sentinels leaked to the rendered output")
	}
	if plain := strings.TrimSpace(ansi.Strip(joined)); plain != "gg well played" {
		t.Fatalf("visible text = %q; want %q", plain, "gg well played")
	}
	if !strings.Contains(joined, "\x1b[38;2;") {
		t.Fatal("no colour applied to the effect span")
	}
}

// The invariant the whole animation rests on: the sentinels ride inside every
// wrapped, measured and cached artefact, so they must measure zero columns and
// survive both the viewport and the bordered box. If any of this drifts, spans
// shift wraps and the pane's border misaligns — which is exactly what the first
// choice of sentinel runes (the *unassigned* tag codepoints above U+E0000) did:
// ansi.StringWidth gave them a column each.
func TestEffectSentinelsAreZeroWidthAndSurviveRender(t *testing.T) {
	plain := "ship it now"
	marked := "ship " + string(effStart(effects.Shimmer)) + "it" + string(effSentinelEnd) + " now"

	if got, want := lipgloss.Width(marked), lipgloss.Width(plain); got != want {
		t.Errorf("lipgloss.Width: marked=%d, plain=%d — sentinels are not zero width", got, want)
	}
	if got, want := ansi.StringWidth(marked), ansi.StringWidth(plain); got != want {
		t.Errorf("ansi.StringWidth: marked=%d, plain=%d — sentinels are not zero width", got, want)
	}

	vp := viewport.New()
	vp.SetWidth(20)
	vp.SetHeight(3)
	vp.SetContentLines([]string{marked, "second", "third"})
	if out := vp.View(); !hasEffectSentinel(out) {
		t.Errorf("the viewport dropped the sentinels: %q", out)
	}

	// The bordered box must render byte-identically once the sentinels are taken
	// back out — i.e. they cost no padding and shift no border.
	box := func(s string) string {
		return lipgloss.NewStyle().Border(border).UnsetBorderTop().UnsetBorderRight().UnsetBorderBottom().
			Width(20).Height(3).Render(s)
	}
	if got, want := stripEffectSentinels(box(marked)), box(plain); got != want {
		t.Errorf("the box render drifted:\n marked (stripped) %q\n plain             %q", got, want)
	}
}

// A span still open across a soft-wrap must not paint the pane's border column
// on the continuation row — the reason paintEffects is given a chrome width.
func TestResolveEffectsLeavesChromeUnpainted(t *testing.T) {
	// Two rows of a bordered box: the span opens on the first and closes on the
	// second, so it is open as the border of row two is crossed.
	line1 := "│ aaa " + string(effStart(effects.Shimmer)) + "bbb"
	line2 := "│ ccc" + string(effSentinelEnd) + " ddd"
	out := resolveEffects([]string{line1, line2}, effectStaticPhase, msgsBoxChrome)

	// The border opens row two with no colour applied to it.
	if !strings.HasPrefix(out[1], "│") {
		t.Fatalf("the border was painted or moved: %q", out[1])
	}
	// ...while the span's text on that row still is coloured.
	if !strings.Contains(out[1], "\x1b[38;2;") {
		t.Fatalf("the continuation row lost its colour: %q", out[1])
	}
	for i, l := range out {
		if hasEffectSentinel(l) {
			t.Fatalf("sentinels survived on line %d", i)
		}
	}
}

// An OSC-8 hyperlink inside an effect span must come through whole. The painter
// walks the line rune by rune, so an escape scanner that didn't consume a string
// sequence would inject colour SGRs *inside* the sequence and spill the raw URL
// onto the screen.
func TestResolveEffectsKeepsHyperlinkIntact(t *testing.T) {
	link := "\x1b]8;;https://example.com/a?b=1\x1b\\click\x1b]8;;\x1b\\"
	marked := "  " + string(effStart(effects.Shimmer)) + "go " + link + " now" + string(effSentinelEnd)

	out := resolveEffects([]string{marked}, effectStaticPhase, 0)[0]

	if !strings.Contains(out, "\x1b]8;;https://example.com/a?b=1\x1b\\") {
		t.Fatalf("the hyperlink escape was mangled: %q", out)
	}
	if strings.Contains(ansi.Strip(out), "https://example.com") {
		t.Fatalf("the URL leaked into the visible text: %q", ansi.Strip(out))
	}
	if !strings.Contains(out, "\x1b[38;2;") {
		t.Fatal("the span was not coloured at all")
	}
}

// A Kitty image placeholder (a custom emoji) inside a span keeps the foreground
// that encodes its image id — painting over it would break the image.
func TestResolveEffectsDoesNotPaintOverImagePlaceholder(t *testing.T) {
	ph := kittyPlaceholder(0x123456, 1, 2) // id-bearing fg + placeholder cells
	marked := "  " + string(effStart(effects.Shimmer)) + "nice " + ph + " one" + string(effSentinelEnd)

	out := resolveEffects([]string{marked}, effectStaticPhase, 0)[0]

	// The id's SGR must still be immediately followed by the placeholder cell:
	// nothing of ours may sit between them.
	idFG := "\x1b[38;2;18;52;86m" // 0x12, 0x34, 0x56
	want := idFG + string(kitty.Placeholder)
	if !strings.Contains(out, want) {
		t.Fatalf("an SGR was injected between the image id and its placeholder: %q", out)
	}
}

// The sentinels must survive renderMarkdown untouched, or the whole pipeline
// breaks: the injected runes have to reach the wrap stage intact.
func TestSentinelsSurviveRenderMarkdown(t *testing.T) {
	in := "hello " + string(effStart(effects.Shimmer)) + "world" + string(effSentinelEnd)
	out := renderMarkdown(in, nil, nil, "")
	if !strings.ContainsRune(out, effStart(effects.Shimmer)) || !strings.ContainsRune(out, effSentinelEnd) {
		t.Fatalf("renderMarkdown dropped the sentinels: %q", out)
	}
}
