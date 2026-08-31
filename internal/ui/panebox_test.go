package ui

import (
	"image/color"
	"math/rand"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestPaneBoxMatchesLipgloss holds the fast path to the letter: every box it
// agrees to draw is byte-identical to the lipgloss style it stands in for.
// The pane boxes go on to be post-processed (joinRuleRows rewrites border
// glyphs by index) and diffed in golden tests, so "looks the same" is not
// enough — the bytes have to match.
func TestPaneBoxMatchesLipgloss(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	ramp := []string{" ", ".", ":", "-", "=", "+", "*", "#"}
	contents := []string{
		"ab\ncdef",
		"",
		"one",
		"a\nb\nc\nd\ne\nf\ng\nh",
		"\x1b[38;5;4mstyled\x1b[m\ny",
		"\x1b[38;5;4munterminated",
		"日本語\nx",
		"shipped 🚀\nflag 🇳🇱",
		"├───┤\n███",
	}
	for range 30 {
		var rows []string
		for r := 0; r < 1+rng.Intn(8); r++ {
			var b strings.Builder
			for c := 0; c < rng.Intn(14); c++ {
				g := ramp[rng.Intn(len(ramp))]
				if rng.Intn(2) == 0 {
					b.WriteString(feedBlobStyles[rng.Intn(len(feedBlobStyles))].Render(g))
				} else {
					b.WriteString(g)
				}
			}
			rows = append(rows, b.String())
		}
		contents = append(contents, strings.Join(rows, "\n"))
	}

	checked := 0
	for _, content := range contents {
		for _, w := range []int{4, 10, 16} {
			for _, h := range []int{2, 5, 9} {
				for _, c := range []color.Color{dimColor, focusedColor} {
					got, ok := renderPaneBox(content, w, h, c)
					if !ok {
						continue
					}
					want := lipgloss.NewStyle().
						Border(border).UnsetBorderTop().
						Width(w).Height(h).BorderForeground(c).
						Render(content)
					checked++
					if got != want {
						t.Errorf("renderPaneBox(%q, %d, %d):\n got %q\nwant %q", content, w, h, got, want)
					}
				}
			}
		}
	}
	if checked < 200 {
		t.Fatalf("only %d boxes took the fast path — the comparison is vacuous", checked)
	}
}

// TestJoinVerticalLeftMatchesLipgloss: same contract as the pane box — when
// the fast join takes a stack, its bytes are lipgloss's.
func TestJoinVerticalLeftMatchesLipgloss(t *testing.T) {
	stacks := [][]string{
		{"abc", "de"},
		{"", ""},
		{"a\nbb\nccc", "dddd", "e"},
		{"\x1b[38;5;4mstyled\x1b[m", "plain longer line"},
		{"日本語\nx", "yy"},
		{"🚀 ship", "🇳🇱 flag\nmore"},
		{"├───┤", "███\n█"},
		{"trailing \n line", "x"},
	}
	checked := 0
	for _, stack := range stacks {
		got, ok := joinVerticalLeft(stack...)
		if !ok {
			continue
		}
		checked++
		if want := lipgloss.JoinVertical(lipgloss.Left, stack...); got != want {
			t.Errorf("joinVerticalLeft(%q):\n got %q\nwant %q", stack, got, want)
		}
	}
	if checked < len(stacks) {
		t.Fatalf("only %d of %d stacks took the fast path", checked, len(stacks))
	}
}
