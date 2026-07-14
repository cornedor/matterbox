package cli

import (
	"strings"
	"testing"

	"matterbox/internal/game"
)

// hostBody is the shape of a real host post: a visible header, an ASCII board,
// then the invisible blob.
func hostBody(st *game.State) string {
	w := st.World()
	return "🎮 **Gorillas** — a vs b · wind calm · 0–0\n" +
		game.ASCIIBoard(w, st.LiveShot(w), 64, 18) + "\n" +
		game.Encode(game.MarshalState(st))
}

func TestGameDebugState(t *testing.T) {
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
	if err := inspectGamePost(&out, hostBody(st), 32, 10, true); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	got := out.String()

	for _, want := range []string{
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
		"magic MBG1 + 50 payload bytes",
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
	if !r.magic || string(r.bytes) != game.Magic+"\xaa" {
		t.Errorf("run carries %q (magic=%v), want the magic + 0xaa", r.bytes, r.magic)
	}
	if r.runeStart != 3 {
		t.Errorf("rune start = %d, want 3", r.runeStart)
	}
}

func TestGameDebugInput(t *testing.T) {
	body := "🎮 _controller_\n" + game.Encode(game.MarshalInput(&game.Input{Angle: 30, Power: 70, Seq: 4}))

	var out strings.Builder
	if err := inspectGamePost(&out, body, 0, 0, false); err != nil {
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
func TestGameDebugStripped(t *testing.T) {
	st := &game.State{Seed: 1, Winner: -1}
	var out strings.Builder
	err := inspectGamePost(&out, game.Strip(hostBody(st)), 0, 0, false)
	if err == nil || !strings.Contains(err.Error(), "no invisible runes") {
		t.Fatalf("want a stripped-body error, got %v", err)
	}
}

// An emoji drags a lone U+FE0F along with it. That is invisible payload-shaped
// noise without the magic, and must not be mistaken for a truncated blob's
// absence of one — but it must also never decode as a game.
func TestGameDebugEmojiOnly(t *testing.T) {
	var out strings.Builder
	err := inspectGamePost(&out, "just a message with an emoji ☀️", 0, 0, false)
	if err == nil || !strings.Contains(err.Error(), "MBG1 magic") {
		t.Fatalf("want a no-magic error, got %v", err)
	}
}
