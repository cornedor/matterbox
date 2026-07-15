package ui

import (
	"strings"
	"testing"

	"matterbox/internal/editor"
	"matterbox/internal/effects"
	"matterbox/internal/languagetool"
)

// composerAt builds a Model whose composer holds text with the cursor at rune
// offset cur.
func composerAt(t *testing.T, text string, cur int) *Model {
	t.Helper()
	m := &Model{}
	m.input = editor.New()
	m.input.SetWidth(80)
	m.input.SetValue(text)
	m.input.SetCursorOffset(cur)
	return m
}

func TestEffectPopupOpensOnBackslash(t *testing.T) {
	m := composerAt(t, "nice \\shim", len("nice \\shim"))
	m.updateEffectPopup()

	if !m.effectPopup.active {
		t.Fatal("the picker did not open on \\shim")
	}
	if len(m.effectPopup.items) != 1 || m.effectPopup.items[0].Name != "shimmer" {
		t.Fatalf("items = %+v; want just shimmer", m.effectPopup.items)
	}
	if m.effectPopup.start != 5 {
		t.Errorf("start = %d; want the backslash at rune 5", m.effectPopup.start)
	}
}

// A bare backslash offers everything — that is how the feature is discovered.
func TestEffectPopupBareBackslashListsAll(t *testing.T) {
	m := composerAt(t, "\\", 1)
	m.updateEffectPopup()

	if !m.effectPopup.active || len(m.effectPopup.items) != len(effects.All()) {
		t.Fatalf("a bare backslash should list every effect, got %+v", m.effectPopup.items)
	}
}

// The picker must stay out of the way of ordinary text. These are the cases that
// would make it pop open while someone is writing code or prose.
func TestEffectPopupStaysClosed(t *testing.T) {
	tests := []struct {
		name string
		text string
		cur  int
	}{
		{"no backslash at all", "just typing", 11},
		{"an escaped backslash", "a \\\\ literal", 4},
		{"a name that matches nothing", "\\wobble", 7},
		{"a windows path", "C:\\temp", 7},
		{"the cursor is past the braces", "\\shimmer{x}", 11},
		{"a space breaks the name", "\\shimmer x", 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := composerAt(t, tt.text, tt.cur)
			m.updateEffectPopup()
			if m.effectPopup.active {
				t.Errorf("the picker opened on %q (items %+v)", tt.text, m.effectPopup.items)
			}
		})
	}
}

// Accepting writes the full directive and leaves the cursor between the braces,
// so the next thing typed is the text the effect wraps.
func TestAcceptEffectPopupWritesDirective(t *testing.T) {
	m := composerAt(t, "gg \\rain", len("gg \\rain"))
	m.updateEffectPopup()
	if !m.effectPopup.active {
		t.Fatal("the picker did not open")
	}
	if !m.acceptEffectPopup() {
		t.Fatal("accept failed")
	}

	if got, want := m.input.Value(), "gg \\rainbow{}"; got != want {
		t.Fatalf("value = %q; want %q", got, want)
	}
	if got, want := m.input.CursorOffset(), len([]rune("gg \\rainbow{")); got != want {
		t.Errorf("cursor at %d; want %d — inside the braces", got, want)
	}
	if m.effectPopup.active {
		t.Error("the picker stayed open after accepting")
	}
}

// Accepting mid-sentence keeps what follows the cursor on the line.
func TestAcceptEffectPopupKeepsTrailingText(t *testing.T) {
	text := "say \\shim loudly"
	m := composerAt(t, text, len("say \\shim"))
	m.updateEffectPopup()
	if !m.acceptEffectPopup() {
		t.Fatal("accept failed")
	}
	if got, want := m.input.Value(), "say \\shimmer{} loudly"; got != want {
		t.Fatalf("value = %q; want %q", got, want)
	}
}

// Accepting on a later line has to place the cursor in whole-document
// coordinates, not line-local ones.
func TestAcceptEffectPopupOnSecondLine(t *testing.T) {
	text := "first line\nthen \\glo"
	m := composerAt(t, text, len([]rune(text)))
	m.updateEffectPopup()
	if !m.acceptEffectPopup() {
		t.Fatal("accept failed")
	}
	if got, want := m.input.Value(), "first line\nthen \\glow{}"; got != want {
		t.Fatalf("value = %q; want %q", got, want)
	}
	want := len([]rune("first line\nthen \\glow{"))
	if got := m.input.CursorOffset(); got != want {
		t.Errorf("cursor at %d; want %d — inside the braces on line two", got, want)
	}
}

// What the picker writes must be what the parser recognises — the two would
// otherwise drift apart silently, offering an effect that does nothing.
func TestAcceptedDirectiveActuallyCompiles(t *testing.T) {
	m := composerAt(t, "\\shim", 5)
	m.updateEffectPopup()
	if !m.acceptEffectPopup() {
		t.Fatal("accept failed")
	}
	// Fill the empty body the way a user would, then send it.
	body := strings.Replace(m.input.Value(), "{}", "{today}", 1)
	if !hasEffectPayload(compileEffects(body)) {
		t.Errorf("the accepted directive %q did not compile to an effect", body)
	}
}

// The composer previews a recognised directive: the syntax dims, the body takes
// the effect's colour. This is the only feedback that a directive is live — the
// gate that keeps prose safe also makes a typo fail silently.
func TestComposerPreviewsRecognisedDirective(t *testing.T) {
	m := composerAt(t, "go \\shimmer{now}", 0)
	m.syncComposerDecorations()

	decos := m.input.Decorations()
	if len(decos) != 3 {
		t.Fatalf("want 3 decorations (open, body, close), got %d: %+v", len(decos), decos)
	}
	// The body is the middle region and carries the effect colour, not the dim.
	body := decos[1]
	if body.Start != 12 || body.End != 15 {
		t.Errorf("body decoration = [%d,%d); want the runes of \"now\"", body.Start, body.End)
	}
	if got, want := body.Style.GetForeground(), effectPreviewColor(effects.Shimmer); got != want {
		t.Errorf("body colour = %v; want the shimmer preview colour %v", got, want)
	}
}

// A typo isn't a directive, so nothing lights up — which is exactly how you see
// that it won't work.
func TestComposerDoesNotPreviewTypo(t *testing.T) {
	m := composerAt(t, "go \\shimer{now}", 0)
	m.syncComposerDecorations()
	if got := m.input.Decorations(); len(got) != 0 {
		t.Errorf("an unknown effect was previewed: %+v", got)
	}
}

// Effects and grammar both want the editor's single decoration slice. Whoever
// wrote last used to erase the other; they must now coexist.
func TestComposerDecorationsDoNotClobberGrammar(t *testing.T) {
	m := composerAt(t, "go \\shimmer{now}", 0)
	m.ltClient = &languagetool.Client{} // grammar "enabled"
	m.grammar.checkedText = m.input.Value()
	m.grammar.matches = []languagetool.Match{{Offset: 0, Length: 2}} // "go"

	m.syncComposerDecorations()

	decos := m.input.Decorations()
	if len(decos) != 4 {
		t.Fatalf("want 3 effect + 1 grammar decoration, got %d: %+v", len(decos), decos)
	}
	// The grammar underline survived, after the effect regions.
	last := decos[len(decos)-1]
	if last.Start != 0 || last.End != 2 {
		t.Errorf("the grammar underline was lost: %+v", decos)
	}
}
