package ui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/mattermost/mattermost/server/public/model"

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

// The semantic effects paint a single steady colour: whichever phase the loop
// happens to be at, a \whisper{} or \ok{} span looks the same, so it never drives
// the frame ticker (effectAnimated keys on exactly this). A \warn{} span, by
// contrast, breathes — its colour must differ between two phases.
func TestSemanticEffectsColour(t *testing.T) {
	paint := func(id byte, phase float64) string {
		marked := "x " + string(effStart(id)) + "hi" + string(effSentinelEnd) + " y"
		return resolveEffects([]string{marked}, phase, 0)[0]
	}
	for _, id := range []byte{effects.Ok, effects.Bad, effects.Whisper} {
		if !strings.Contains(paint(id, 0), "\x1b[38;2;") {
			t.Errorf("effect %d painted no colour", id)
		}
		if effectAnimated(id) {
			t.Errorf("effect %d is a steady colour but reports as animated", id)
		}
		if a, b := paint(id, 0), paint(id, 0.5); a != b {
			t.Errorf("effect %d changed with phase (static expected): %q vs %q", id, a, b)
		}
	}
	if !effectAnimated(effects.Warn) {
		t.Error("warn should be animated")
	}
	// 0.25 and 0.75 are the breath's peak and trough (phases 0 and 0.5 share sin=0).
	if a, b := paint(effects.Warn, 0.25), paint(effects.Warn, 0.75); a == b {
		t.Error("warn did not change between phases")
	}
}

// \underline{} composes with a colour rather than being overridden by it: a
// \bad{\underline{x}} rune is painted red AND underlined, on the same escape.
func TestUnderlineComposesWithColour(t *testing.T) {
	marked := string(effStart(effects.Bad)) + string(effStart(effects.Underline)) +
		"x" + string(effSentinelEnd) + string(effSentinelEnd)
	out := resolveEffects([]string{marked}, effectStaticPhase, 0)[0]
	if !strings.Contains(out, "\x1b[4;38;2;") {
		t.Fatalf("expected underline + colour on one rune, got %q", out)
	}
}

// \spoiler{} paints each rune's foreground and background the same colour — an
// opaque bar whatever the terminal background — but leaves the underlying text
// intact, so selecting/copying it still yields the words (reveal-by-copy).
func TestSpoilerPaintsOpaqueBlock(t *testing.T) {
	marked := string(effStart(effects.Spoiler)) + "secret" + string(effSentinelEnd)
	out := resolveEffects([]string{marked}, effectStaticPhase, 0)[0]

	r, g, b := rgb8(spoilerBlock)
	fg := fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
	bg := fmt.Sprintf("48;2;%d;%d;%d", r, g, b)
	if !strings.Contains(out, fg) || !strings.Contains(out, bg) {
		t.Fatalf("spoiler should paint fg and bg the same; got %q", out)
	}
	if plain := ansi.Strip(out); plain != "secret" {
		t.Fatalf("spoiler must keep the real text for copy; got %q", plain)
	}
	if effectAnimated(effects.Spoiler) {
		t.Error("spoiler is static and must not drive the frame ticker")
	}
}

// A \copy{} span is baked into the rendered body as a click-to-copy OSC 8 link
// whose payload decodes back to the chip's text; the effect sentinels survive
// inside it so the chip is still painted.
func TestCopyChipBecomesLink(t *testing.T) {
	body := renderMarkdownEffects(compileEffects("grab \\copy{ID-42} now"), nil, nil, "")
	want := copyURLScheme + encodeCopyPayload("ID-42")
	if !strings.Contains(body, "\x1b]8;;"+want) {
		t.Fatalf("copy span did not become a copy link; body = %q", body)
	}
	if !strings.ContainsRune(body, effStart(effects.Copy)) {
		t.Error("copy sentinel was consumed; the chip would not be painted")
	}
	if text, ok := decodeCopyPayload(encodeCopyPayload("a b\tc")); !ok || text != "a b\tc" {
		t.Errorf("copy payload did not round-trip: %q, ok=%v", text, ok)
	}
}

// The copy link must be resolvable by the same hit-test that opens ordinary
// links: a click landing on the chip's columns returns the copy URL. This is the
// reason the link is baked into the body (which the hit-test reads) and not added
// at paint time. "grab " is 5 columns, so the chip covers columns 5..9.
func TestCopyLinkIsHitTestable(t *testing.T) {
	body := renderMarkdownEffects(compileEffects("grab \\copy{ID-42} now"), nil, nil, "")
	url, ok := linkAtDisplayCol(body, 7) // a column inside "ID-42"
	if !ok || !strings.HasPrefix(url, copyURLScheme) {
		t.Fatalf("click on the chip did not resolve to a copy link: url=%q ok=%v", url, ok)
	}
	if text, _ := decodeCopyPayload(strings.TrimPrefix(url, copyURLScheme)); text != "ID-42" {
		t.Errorf("copy link payload = %q; want ID-42", text)
	}
	// A column outside the chip is not the copy link.
	if url, ok := linkAtDisplayCol(body, 1); ok && strings.HasPrefix(url, copyURLScheme) {
		t.Error("a click on plain text resolved to the copy link")
	}
}

// The copy chip renders a leading icon, and the icon is not part of what gets
// copied.
func TestCopyChipHasIcon(t *testing.T) {
	body := renderMarkdownEffects(compileEffects("\\copy{tok}"), nil, nil, "")
	if !strings.Contains(body, copyGlyph) {
		t.Error("copy chip rendered without its icon")
	}
	want := copyURLScheme + encodeCopyPayload("tok") // payload excludes the icon
	if !strings.Contains(body, want) {
		t.Errorf("copy payload changed by the icon; body=%q", body)
	}
}

// Each \spoiler{} becomes an indexed hover link but keeps its block sentinel, so
// it's hidden until the pointer lands on it.
func TestSpoilerBecomesIndexedHoverLink(t *testing.T) {
	body := renderMarkdownEffects(compileEffects("\\spoiler{one} and \\spoiler{two}"), nil, nil, "")
	for _, idx := range []string{"0", "1"} {
		if !strings.Contains(body, "\x1b]8;;"+spoilerURLScheme+idx) {
			t.Errorf("missing spoiler hover link %s; body=%q", idx, body)
		}
	}
	if n := strings.Count(body, string(effStart(effects.Spoiler))); n != 2 {
		t.Errorf("both spoilers should still carry a block sentinel, got %d", n)
	}
}

// Hovering a spoiler lifts only that one's block — its text shows, the others stay
// hidden — and moving off restores it. End to end through the paint.
func TestRevealSpoilerShowsHoveredText(t *testing.T) {
	body := renderMarkdownEffects(compileEffects("a \\spoiler{first} b \\spoiler{second} c"), nil, nil, "")
	r, g, b := rgb8(spoilerBlock)
	block := fmt.Sprintf("48;2;%d;%d;%d", r, g, b)

	paint := func(s string) string {
		return strings.Join(resolveEffects(appendBodyLines(nil, s, 80), effectStaticPhase, 0), "\n")
	}
	// Un-hovered: both are blocks.
	if out := paint(body); strings.Count(out, block) == 0 {
		t.Fatal("un-hovered spoilers should paint a block background")
	}
	// Hover the second spoiler (index 1): "second" shows, "first" stays a block.
	revealed := revealSpoiler(body, spoilerURLScheme+"1")
	out := paint(revealed)
	if !strings.Contains(ansi.Strip(out), "second") {
		t.Fatalf("hovered spoiler text not revealed: %q", ansi.Strip(out))
	}
	if !strings.Contains(out, block) {
		t.Error("the other spoiler should still be a block")
	}
	// It is precisely one block that was lifted.
	if before, after := strings.Count(body, string(effStart(effects.Spoiler))), strings.Count(revealed, string(effStart(effects.Spoiler))); before-after != 1 {
		t.Errorf("expected exactly one spoiler lifted, got %d→%d", before, after)
	}
}

// hovered() routes a spoiler hover to a reveal (not the link-highlight path).
func TestHoveredRevealsSpoiler(t *testing.T) {
	body := renderMarkdownEffects(compileEffects("x \\spoiler{secret} y"), nil, nil, "")
	m := &Model{}
	p := &model.Post{Id: "p1"}

	if got := m.hovered(body, p); got != body {
		t.Error("with nothing hovered, the body must be unchanged")
	}
	m.hoverLink = hoverLink{postID: "p1", url: spoilerURLScheme + "0"}
	if got := m.hovered(body, p); strings.ContainsRune(got, effStart(effects.Spoiler)) {
		t.Error("hovering the spoiler should have lifted its block sentinel")
	}
}

// Clicking a copy chip copies its text instead of opening anything: the copy
// scheme is intercepted before the non-web link warning modal.
func TestCopyLinkClickCopiesNotOpens(t *testing.T) {
	m := &Model{}
	_, cmd := m.activateLink(copyURLScheme + encodeCopyPayload("ID-42"))
	if m.linkConfirm.active {
		t.Fatal("a copy click raised the open-link warning modal")
	}
	if cmd == nil {
		t.Fatal("a copy click produced no clipboard command")
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

// An effect whose boundary lands inside a markdown token would split it, and
// goldmark would print the asterisks literally — the effect corrupting the very
// text it decorates. The span is dropped instead: the words render exactly as
// they would with no effect at all.
func TestEffectStraddlingMarkdownIsDropped(t *testing.T) {
	// The closing "**" is split: the span starts between the two asterisks.
	wire := compileEffects("**bold*\\shimmer{*} tail")
	got := renderMarkdownEffects(wire, nil, nil, "")
	want := renderMarkdown("**bold** tail", nil, nil, "")

	if got != want {
		t.Errorf("a straddling effect changed the rendered text:\n got  %q\n want %q", got, want)
	}
	if hasEffectSentinel(got) {
		t.Error("the dropped span still left sentinels behind")
	}
}

// The guard must not fire on the ordinary case: an effect wrapping a markdown
// token (rather than splitting one) keeps both the styling and the effect.
func TestEffectAroundMarkdownIsKept(t *testing.T) {
	wire := compileEffects("\\shimmer{**bold**} tail")
	got := renderMarkdownEffects(wire, nil, nil, "")

	if !hasEffectSentinel(got) {
		t.Fatal("the effect was dropped even though it doesn't split a token")
	}
	if want := renderMarkdown("**bold** tail", nil, nil, ""); stripEffectSentinels(got) != want {
		t.Errorf("the markdown rendered differently:\n got  %q\n want %q", stripEffectSentinels(got), want)
	}
}

// A span ending at a bare URL is the common case (`/rainbow …link`): the end
// sentinel sits immediately after the URL. The bare-URL matcher must not swallow
// that zero-width rune into the link's OSC 8 destination — if it does, the span's
// close vanishes into an escape sequence resolveEffects skips whole, so the effect
// paints every following line for the rest of the pane ("it never stops"), and the
// leftover empty style trips the guard, silently dropping the effect instead.
func TestEffectEndingAtURLClosesAndStaysClickable(t *testing.T) {
	// The real report: a whole-message rainbow whose text ends in two MR links.
	visible := "review pls? https://git.example.com/a/-/merge_requests/1715 https://git.example.com/a/-/merge_requests/1718"
	body := renderMarkdownEffects(wholeMessageEffect(effects.Rainbow, visible), nil, nil, "")

	if !hasEffectSentinel(body) {
		t.Fatal("the effect was dropped: a URL-terminated span must still render")
	}
	// The span must close: paint this post's lines followed by a plain line and
	// check the effect's truecolour does not leak onto the follow-up.
	lines := appendBodyLines(nil, body, 60)
	follow := appendBodyLines(nil, renderMarkdown("later message", nil, nil, ""), 60)
	out := resolveEffects(append(append([]string{}, lines...), follow...), effectStaticPhase, 0)
	for _, l := range out[len(lines):] {
		if strings.Contains(l, "\x1b[38;2;") {
			t.Fatalf("the effect bled past its own post onto: %q", showEsc(l))
		}
	}
	// The link stays clickable: its OSC 8 destination is the clean URL, with no
	// invisible sentinel smuggled in.
	if !strings.Contains(body, "\x1b]8;;https://git.example.com/a/-/merge_requests/1718\x1b\\") {
		t.Errorf("the URL's OSC 8 destination was altered:\n%q", showEsc(body))
	}
}

func showEsc(s string) string { return strings.ReplaceAll(s, "\x1b", "^[") }

// The sentinels must survive renderMarkdown untouched, or the whole pipeline
// breaks: the injected runes have to reach the wrap stage intact.
func TestSentinelsSurviveRenderMarkdown(t *testing.T) {
	in := "hello " + string(effStart(effects.Shimmer)) + "world" + string(effSentinelEnd)
	out := renderMarkdown(in, nil, nil, "")
	if !strings.ContainsRune(out, effStart(effects.Shimmer)) || !strings.ContainsRune(out, effSentinelEnd) {
		t.Fatalf("renderMarkdown dropped the sentinels: %q", out)
	}
}
