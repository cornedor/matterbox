package ui

import (
	"strings"
	"testing"
)

// TestBallStepStaysInBounds steps a ball through many frames from many
// random-ish starting states and asserts it never leaves the box. This
// is the core invariant: a frame that placed the ball on the border or
// outside would draw it on (or through) the wall.
func TestBallStepStaysInBounds(t *testing.T) {
	for _, dim := range [][2]int{{24, 10}, {1, 1}, {3, 7}, {30, 12}} {
		w, h := dim[0], dim[1]
		for sx := 0; sx < w; sx++ {
			for _, dxy := range [][2]int{{1, 1}, {-1, 1}, {1, -1}, {-1, -1}} {
				b := ballAnim{w: w, h: h, x: sx, y: 0, dx: dxy[0], dy: dxy[1]}
				for f := 0; f < 500; f++ {
					b.step()
					if b.x < 0 || b.x >= w || b.y < 0 || b.y >= h {
						t.Fatalf("box %dx%d from x=%d v=%v: out of bounds at frame %d: (%d,%d)",
							w, h, sx, dxy, f, b.x, b.y)
					}
				}
			}
		}
	}
}

// TestRenderBallFrame checks the frame is a fenced code block with the
// ball drawn at the requested cell and a border of the right size.
func TestRenderBallFrame(t *testing.T) {
	const w, h = 5, 3
	out := renderBallFrame(w, h, 2, 1)

	if !strings.HasPrefix(out, fence+"\n") || !strings.HasSuffix(out, "\n"+fence) {
		t.Fatalf("frame not wrapped in a code fence:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	// fence + top border + h rows + bottom border + fence
	if want := h + 4; len(lines) != want {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), want, out)
	}
	border := "+" + strings.Repeat("-", w) + "+"
	if lines[1] != border || lines[len(lines)-2] != border {
		t.Fatalf("borders wrong:\n%s", out)
	}
	// Ball at (x=2,y=1) → third interior row, char at index 1+2.
	ballRow := lines[2+1] // fence(0) border(1) row0(2) row1(3)
	if ballRow[1+2] != 'O' {
		t.Fatalf("ball not at expected cell, row=%q", ballRow)
	}
	if strings.Count(out, "O") != 1 {
		t.Fatalf("expected exactly one ball, got %d:\n%s", strings.Count(out, "O"), out)
	}
}
