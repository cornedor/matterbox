package update

import "testing"

// TestNewer covers the comparison the whole feature turns on, and in particular
// the shapes `git describe` and versionName actually produce — which is where a
// naive semver comparison gets it exactly backwards.
func TestNewer(t *testing.T) {
	cases := []struct {
		name            string
		current, latest string
		want            bool
	}{
		{"a newer patch", "v1.1.0", "v1.1.1", true},
		{"a newer minor", "v1.1.0", "v1.2.0", true},
		{"a newer major", "v1.9.9", "v2.0.0", true},
		{"the same release", "v1.1.0", "v1.1.0", false},
		{"an older release", "v1.2.0", "v1.1.0", false},

		// The build stamp is `git describe --tags --always --dirty`, and
		// versionName appends the commit when the stamp does not carry it. Read
		// as semver every one of these sorts below the plain tag, which would
		// tell the person on the newest code that they are behind.
		{"commits past the tag", "v1.1.0-3-gabc1234", "v1.1.0", false},
		{"commits past the tag, dirty", "v1.1.0-3-gabc1234-dirty", "v1.1.0", false},
		{"versionName's commit suffix", "v1.1.0 (abc1234def56)", "v1.1.0", false},
		{"commits past the tag, real release out", "v1.1.0-3-gabc1234", "v1.2.0", true},

		// Nothing to compare means no. An unstamped `go build` calls itself by
		// its commit, and "you are behind v1.2.0" would be a guess.
		{"an unstamped build", "abc1234", "v1.2.0", false},
		{"a dev build", "dev", "v1.2.0", false},
		{"no current version", "", "v1.2.0", false},
		{"no latest version", "v1.1.0", "", false},
		{"a junk answer", "v1.1.0", "not-a-version", false},

		// Ahead of the published latest: a prerelease is never told to go back.
		{"a prerelease ahead of latest", "v1.2.0-rc1", "v1.1.0", false},

		// The "v" is conventional, not required, on either side.
		{"no v prefix", "1.1.0", "1.2.0", true},

		// Digit-wise, not lexical: 10 is above 9.
		{"double-digit minor", "v1.9.0", "v1.10.0", true},
		{"double-digit minor, reversed", "v1.10.0", "v1.9.0", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Newer(c.current, c.latest); got != c.want {
				t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
			}
		})
	}
}

func TestTripleRejectsNonsense(t *testing.T) {
	for _, s := range []string{"", "v", "v1", "v1.2", "vx.y.z", "1.2.x", " v1.2.3", "release-1.2.3"} {
		if _, ok := triple(s); ok {
			t.Errorf("triple(%q) parsed, want rejected", s)
		}
	}
	for _, s := range []string{"v1.2.3", "1.2.3", "v1.2.3-rc1", "v0.0.0"} {
		if _, ok := triple(s); !ok {
			t.Errorf("triple(%q) rejected, want parsed", s)
		}
	}
}
