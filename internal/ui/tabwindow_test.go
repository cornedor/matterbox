package ui

import "testing"

func TestTeamTabWindow(t *testing.T) {
	tens := []int{10, 10, 10, 10, 10, 10} // six 10-wide team tabs

	cases := []struct {
		name                        string
		widths                      []int
		activePos, avail            int
		wantStart, wantEnd          int
		wantLeftClip, wantRightClip bool
	}{
		{
			name: "everything fits", widths: tens, activePos: 2, avail: 100,
			wantStart: 0, wantEnd: 6, wantLeftClip: false, wantRightClip: false,
		},
		{
			// budget = 50-2 = 48 → five 10-wide tabs fit. Active at the start
			// pages from the left; only the right side is clipped.
			name: "active at start, overflow", widths: tens, activePos: 0, avail: 50,
			wantStart: 0, wantEnd: 4, wantLeftClip: false, wantRightClip: true,
		},
		{
			// Active at the far end is pinned right; the left side clips.
			name: "active at end, overflow", widths: tens, activePos: 5, avail: 50,
			wantStart: 2, wantEnd: 6, wantLeftClip: true, wantRightClip: false,
		},
		{
			// Active in the middle pins toward the right edge, clipping both.
			name: "active mid, overflow", widths: tens, activePos: 4, avail: 50,
			wantStart: 1, wantEnd: 5, wantLeftClip: true, wantRightClip: true,
		},
		{
			// A sticky tab is active (-1): teams page from the very start.
			name: "sticky active", widths: tens, activePos: -1, avail: 50,
			wantStart: 0, wantEnd: 4, wantLeftClip: false, wantRightClip: true,
		},
		{
			name: "no room", widths: tens, activePos: 3, avail: 0,
			wantStart: 0, wantEnd: 0, wantLeftClip: false, wantRightClip: false,
		},
		{
			name: "no teams", widths: nil, activePos: -1, avail: 80,
			wantStart: 0, wantEnd: 0, wantLeftClip: false, wantRightClip: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end, left, right := teamTabWindow(tc.widths, tc.activePos, tc.avail)
			if start != tc.wantStart || end != tc.wantEnd {
				t.Errorf("window = [%d,%d); want [%d,%d)", start, end, tc.wantStart, tc.wantEnd)
			}
			if left != tc.wantLeftClip || right != tc.wantRightClip {
				t.Errorf("clips = (left %v, right %v); want (left %v, right %v)",
					left, right, tc.wantLeftClip, tc.wantRightClip)
			}
			// The active team must always remain inside the window when it is a
			// real team and there is room to show anything.
			if tc.activePos >= 0 && end > start {
				if tc.activePos < start || tc.activePos >= end {
					t.Errorf("active %d outside window [%d,%d)", tc.activePos, start, end)
				}
			}
		})
	}
}
