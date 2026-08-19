package cli

import (
	"slices"
	"strings"
	"testing"

	"matterbox/internal/effects"
	"matterbox/internal/game"
	"matterbox/internal/hidden"
	"matterbox/internal/replyto"
)

// hostBody is the shape of a real host post: a visible header, an ASCII board,
// then the invisible blob.
func hostBody(st *game.State) string {
	w := st.World()
	return "🎮 **Gorillas** — a vs b · wind calm · 0–0\n" +
		game.ASCIIBoard(w, st.LiveShot(w), 64, 18) + "\n" +
		game.Encode(game.MarshalState(st))
}

func TestDecodeGameState(t *testing.T) {
	st := &game.State{
		Seed:   4242,
		Wind:   -3,
		Phase:  game.PhaseFlight,
		Turn:   1,
		Scores: [2]uint8{1, 2},
		Winner: -1,
		Joiner: "abcdefghijklmnopqrstuvwxyz",
		Shot:   &game.ShotWire{Angle: 45, Power: 60, T: 123},
		Craters: []game.Crater{
			{X: 100, Y: 200, RX: 22, RY: 16},
		},
	}

	var out strings.Builder
	if err := inspectPost(&out, hostBody(st), 32, 10, true); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	got := out.String()

	for _, want := range []string{
		"gorillas payload — 50 bytes",
		"kind     state",
		"seed     4242",
		"wind     -3 (blowing left)",
		"phase    flight",
		"turn     1 (joiner)",
		"scores   host 1 – joiner 2",
		"joiner   abcdefghijklmnopqrstuvwxyz",
		"angle 45° · power 60 · t 1.23s",
		"craters  1",
		"(100,200) rx=22 ry=16",
		"board",
		// The raw view spells the blob out where it sits: magic first, then the
		// state's version byte and its little-endian seed (4242 = 0x1092).
		"magic MBG1 (gorillas) + 50 payload bytes",
		"‹4d›‹42›‹47›‹31›‹02›‹92›‹10›",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q\n%s", want, got)
		}
	}
	// The visible half must come through with the blob gone.
	if strings.Contains(got, "︀") || !strings.Contains(got, "🎮 **Gorillas**") {
		t.Errorf("visible text not stripped/echoed correctly\n%s", got)
	}
}

// A run's offsets have to point at the real bytes of the body, since finding the
// blob is half of what the tool is for.
func TestHiddenRuns(t *testing.T) {
	blob := game.Encode([]byte{0xAA})
	body := "hi\n" + blob

	runs := hiddenRuns(body)
	if len(runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(runs))
	}
	r := runs[0]
	if body[r.byteStart:r.byteEnd] != blob {
		t.Errorf("run offsets %d–%d do not bracket the blob", r.byteStart, r.byteEnd)
	}
	if r.magic != game.Magic || string(r.bytes) != game.Magic+"\xaa" {
		t.Errorf("run carries %q (magic=%q), want the magic + 0xaa", r.bytes, r.magic)
	}
	if r.runeStart != 3 {
		t.Errorf("rune start = %d, want 3", r.runeStart)
	}
}

func TestDecodeGameInput(t *testing.T) {
	body := "🎮 _controller_\n" + game.Encode(game.MarshalInput(&game.Input{Angle: 30, Power: 70, Seq: 4}))

	var out strings.Builder
	if err := inspectPost(&out, body, 0, 0, false); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	got := out.String()
	for _, want := range []string{"kind     input", "angle    30°", "power    70", "seq      4"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q\n%s", want, got)
		}
	}
}

// A body whose invisible runes were eaten by the copy is the failure this tool
// exists to name, so it has to say so rather than report an empty game.
func TestDecodeStripped(t *testing.T) {
	st := &game.State{Seed: 1, Winner: -1}
	var out strings.Builder
	err := inspectPost(&out, hidden.Strip(hostBody(st)), 0, 0, false)
	if err == nil || !strings.Contains(err.Error(), "no invisible runes") {
		t.Fatalf("want a stripped-body error, got %v", err)
	}
}

// An emoji drags a lone U+FE0F along with it. That is invisible payload-shaped
// noise without any channel magic, and must not be mistaken for a truncated
// blob's absence of one — but it must also never decode as a payload.
func TestDecodeEmojiOnly(t *testing.T) {
	var out strings.Builder
	err := inspectPost(&out, "just a message with an emoji ☀️", 0, 0, false)
	if err == nil || !strings.Contains(err.Error(), "no recognised payload") {
		t.Fatalf("want a no-magic error, got %v", err)
	}
	// The error should name the magics it was looking for, so the reader can see
	// what was expected — the tool is generic over channels now.
	if !strings.Contains(err.Error(), game.Magic) || !strings.Contains(err.Error(), effects.MagicEffects) {
		t.Errorf("the error should list the known channel magics, got: %v", err)
	}
}

// effectsBody is a post carrying a text-effects payload the way the composer
// sends one: clean visible text, spans riding along as an invisible MBF1 blob.
func effectsBody(visible string, spans []effects.Span) string {
	return visible + hidden.Encode(effects.MagicEffects, effects.MarshalPayload(spans))
}

// spacePadding collapses each run of spaces to one, so assertions can name the
// columns without counting the %-8s padding that varies with an effect's name.
func spacePadding(s string) string {
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

// More than one feature rides the invisible transport now, and the decoder is no
// longer game-only: a text-effects post decodes into its spans and the markup
// that produced them, rather than erroring as a mangled game.
func TestDecodeEffects(t *testing.T) {
	spans := []effects.Span{
		{ID: effects.Shimmer, Start: 0, Len: 5},
		{ID: effects.Glow, Start: 6, Len: 2},
	}
	var out strings.Builder
	if err := inspectPost(&out, effectsBody("today it ships", spans), 0, 0, false); err != nil {
		t.Fatalf("an effects post should decode, got: %v", err)
	}
	got := spacePadding(out.String())
	for _, want := range []string{
		"magic MBF1 (text effects)",
		"text effects payload —",
		"kind text effects",
		"version 1",
		"spans 2",
		`shimmer runes 0–5 "today"`,
		`glow runes 6–8 "it"`,
		// The reconstructed markup is the clearest picture of what the effects do.
		`markup \shimmer{today} \glow{it}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q\n%s", want, got)
		}
	}
}

// A span whose rune offsets fall outside the current visible text is the symptom
// of a post edited from another client after the effects were written. The tool
// must flag it rather than slice out of range or drop it silently.
func TestDecodeEffectsOutOfRange(t *testing.T) {
	// The span claims runes 0–9, but "hi" is only two runes long: the text was
	// edited out from under it.
	spans := []effects.Span{{ID: effects.Rainbow, Start: 0, Len: 9}}
	var out strings.Builder
	if err := inspectPost(&out, effectsBody("hi", spans), 0, 0, false); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "out of range") {
		t.Errorf("an out-of-range span should be flagged, not sliced:\n%s", got)
	}
}

// The old name has to keep working: game-debug is muscle memory and may be baked
// into scripts, so it stays as an alias of decode.
func TestDecodeKeepsGameDebugAlias(t *testing.T) {
	cmd := newDecodeCmd()
	if cmd.Name() != "decode" {
		t.Errorf("command name = %q, want decode", cmd.Name())
	}
	if !slices.Contains(cmd.Aliases, "game-debug") {
		t.Errorf("game-debug alias missing, aliases = %v", cmd.Aliases)
	}
}

// A nested reply carrying text effects as well: the debug view has to report
// both channels, which is the whole reason hidden.Append parts their runs.
func TestDecodeNestedReplyAlongsideEffects(t *testing.T) {
	const parent = "8kq1h9wz3jby7rn5cxtd2fmp4a"
	spans := []effects.Span{{ID: effects.Shimmer, Start: 0, Len: 4}}
	body := replyto.Attach("that one"+hidden.Encode(effects.MagicEffects, effects.MarshalPayload(spans)), parent)

	var out strings.Builder
	if err := inspectPost(&out, body, 0, 0, false); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"nested reply",
		"parent   " + parent,
		"text effects",
		"shimmer",
		"that one", // the visible text, once, with neither payload in it
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("decode output is missing %q:\n%s", want, got)
		}
	}
	// Both runs must be reported as their own channel, not one merged smear.
	if n := strings.Count(got, "payload — "); n != 2 {
		t.Fatalf("reported %d payloads, want 2:\n%s", n, got)
	}
}
