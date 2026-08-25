package libav

import (
	"strings"
	"testing"
)

// TestClass pins the classification, which is the part with legal consequences.
// Under matterbox's GPL-3.0-or-later the distribution answer is yes for every
// free FFmpeg configuration — what still has to be right is naming the license
// the bundle reproduces, and catching the one build that may not be conveyed at
// all. The strings are the real ones libavutil reports (see its version.c),
// plus the substring traps.
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
		// GPL FFmpeg is what nearly every distribution ships, and it is
		// compatible with our own license — this pair is the whole point of the
		// relicense, so it is the pair most worth pinning.
		{"gpl 2", Info{Linked: true, License: "GPL version 2 or later"}, ClassCopyleft, true},
		{"gpl 3", Info{Linked: true, License: "GPL version 3 or later"}, ClassCopyleft, true},
		{"nonfree", Info{Linked: true, License: "nonfree and unredistributable"}, ClassForbidden, false},
		// A linked build that won't say what it is gets the unsafe answer: the
		// only classification error that can cause harm is calling something
		// distributable when it isn't.
		{"empty license", Info{Linked: true}, ClassUnknown, false},
		{"gibberish", Info{Linked: true, License: "something new"}, ClassUnknown, false},
		// Case shouldn't matter, and "LGPL" must not be read as GPL by a
		// substring test — the ordering trap this whole switch is built around.
		{"lowercase lgpl", Info{Linked: true, License: "lgpl version 2.1 or later"}, ClassPermissive, true},
		{"mixed case gpl", Info{Linked: true, License: "Gpl Version 2 Or Later"}, ClassCopyleft, true},
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
// silent when nothing is linked, quiet about a license that is merely copyleft,
// and loud about the one build that may not be handed to anyone.
func TestSummaryFlagsNonDistributable(t *testing.T) {
	if s := (Info{}).Summary(); s != "" {
		t.Errorf("unlinked Summary() = %q, want empty so the line is omitted", s)
	}

	for _, lic := range []string{"GPL version 3 or later", "GPL version 2 or later", "LGPL version 2.1 or later"} {
		s := Info{Linked: true, License: lic, Version: "8.1.2"}.Summary()
		if !strings.Contains(s, "8.1.2") || !strings.Contains(s, lic) {
			t.Errorf("Summary() = %q, want it to name the version and %q", s, lic)
		}
		if strings.Contains(s, "NOT") || strings.Contains(s, "not distributable") {
			t.Errorf("Summary() = %q, should carry no warning: %s is compatible with GPL-3.0-or-later", s, lic)
		}
	}

	nonfree := Info{Linked: true, License: "nonfree and unredistributable", Version: "8.1.2"}
	if s := nonfree.Summary(); !strings.Contains(s, "NOT be distributed") {
		t.Errorf("nonfree Summary() = %q, want it to refuse distribution", s)
	}

	unknown := Info{Linked: true, License: "something new", Version: "8.1.2"}
	if s := unknown.Summary(); !strings.Contains(s, "not distributable") {
		t.Errorf("unknown-license Summary() = %q, want it to warn", s)
	}
}
