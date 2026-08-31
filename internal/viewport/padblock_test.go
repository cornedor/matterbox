package viewport

import (
	"math/rand"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestPadBlockMatchesLipgloss is the contract the fast path lives by: whenever
// padBlock accepts a block, its bytes are exactly what lipgloss would have
// produced. A divergence here is a render bug that would only show up as a
// smeared pane on someone's terminal.
func TestPadBlockMatchesLipgloss(t *testing.T) {
	cases := [][]string{
		{"ab", "cdef"},
		{""},
		{},
		{"12345678"},
		{"a", "b", "c", "d", "e", "f", "g"},
		{"\x1b[38;5;4mstyled\x1b[m", "y"},
		{"\x1b[38;5;4mopen"},
		{"日本語", "x"},
		{"emoji 🚀", "flag 🇳🇱"},
		{"box ├───┤", "block ███"},
		{strings.Repeat("=", 8)},
	}
	// Plus random ramp-style rows, the shape the feed's blob field emits.
	rng := rand.New(rand.NewSource(1))
	ramp := []string{" ", ".", ":", "-", "=", "+", "*", "#"}
	for range 40 {
		var rows []string
		for r := 0; r < 1+rng.Intn(6); r++ {
			var b strings.Builder
			for c := 0; c < rng.Intn(9); c++ {
				g := ramp[rng.Intn(len(ramp))]
				if rng.Intn(2) == 0 {
					b.WriteString("\x1b[38;5;241m" + g + "\x1b[m")
				} else {
					b.WriteString(g)
				}
			}
			rows = append(rows, b.String())
		}
		cases = append(cases, rows)
	}

	checked := 0
	for _, lines := range cases {
		for _, w := range []int{1, 3, 8, 12} {
			for _, h := range []int{1, 3, 6} {
				got, ok := padBlock(lines, w, h)
				if !ok {
					continue
				}
				want := lipgloss.NewStyle().Width(w).Height(h).Render(strings.Join(lines, "\n"))
				checked++
				if got != want {
					t.Errorf("padBlock(%q, %d, %d):\n got %q\nwant %q", lines, w, h, got, want)
				}
			}
		}
	}
	if checked < 200 {
		t.Fatalf("only %d blocks took the fast path — the comparison is vacuous", checked)
	}
}
