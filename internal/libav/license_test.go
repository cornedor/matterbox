package libav

import (
	"strings"
	"testing"
)

// TestClass pins the classification, which is the part with legal consequences:
// mistaking a GPL build for an LGPL one produces a binary that gets handed out
// under a license that does not cover it. The strings are the real ones
// libavutil reports (see its version.c), plus the substring traps.
func TestClass(t *testing.T) {
	for _, tc := range []struct {
		name    string
		info    Info
		want    Class
		distrib bool
	}{
		{"not linked", Info{}, ClassNone, true},
		{"lgpl 2.1", Info{Linked: true, License: "LGPL version 2.1 or later"}, ClassPermissive, true},
		{"lgpl 3", Info{Linked: true, License: "LGPL version 3 or later"}, ClassPermissive, true},
		{"gpl 2", Info{Linked: true, License: "GPL version 2 or later"}, ClassCopyleft, false},
		{"gpl 3", Info{Linked: true, License: "GPL version 3 or later"}, ClassCopyleft, false},
		{"nonfree", Info{Linked: true, License: "nonfree and unredistributable"}, ClassForbidden, false},
		// A linked build that won't say what it is gets the unsafe answer: the
		// only classification error that can cause harm is calling something
		// distributable when it isn't.
		{"empty license", Info{Linked: true}, ClassUnknown, false},
		{"gibberish", Info{Linked: true, License: "something new"}, ClassUnknown, false},
		// Case shouldn't matter, and "LGPL" must not be read as GPL by a
		// substring test — the ordering trap this whole switch is built around.
		{"lowercase lgpl", Info{Linked: true, License: "lgpl version 2.1 or later"}, ClassPermissive, true},
		{"mixed case gpl", Info{Linked: true, License: "Gpl Version 2 Or Later"}, ClassCopyleft, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.Class(); got != tc.want {
				t.Errorf("Class() = %v (%s), want %v (%s)", got, got, tc.want, tc.want)
			}
			if got := tc.info.Class().Distributable(); got != tc.distrib {
				t.Errorf("Distributable() = %v, want %v", got, tc.distrib)
			}
		})
	}
}

// TestSummaryFlagsNonDistributable checks the line `matterbox version` prints:
// silent when nothing is linked, and carrying the warning when the build cannot
// be handed out under Apache-2.0.
func TestSummaryFlagsNonDistributable(t *testing.T) {
	if s := (Info{}).Summary(); s != "" {
		t.Errorf("unlinked Summary() = %q, want empty so the line is omitted", s)
	}

	gpl := Info{Linked: true, License: "GPL version 3 or later", Version: "8.1.2"}
	s := gpl.Summary()
	for _, want := range []string{"8.1.2", "GPL version 3 or later", "NOT distributable"} {
		if !strings.Contains(s, want) {
			t.Errorf("Summary() = %q, want it to mention %q", s, want)
		}
	}

	lgpl := Info{Linked: true, License: "LGPL version 2.1 or later", Version: "8.1.2"}
	if s := lgpl.Summary(); strings.Contains(s, "NOT distributable") {
		t.Errorf("LGPL Summary() = %q, should carry no warning", s)
	}
}
