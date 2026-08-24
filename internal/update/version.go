package update

import "regexp"

// Version comparison, deliberately coarser than semver.
//
// The strings being compared are not clean tags. A release build is stamped
// with `git describe`, so a build three commits past v1.1.0 calls itself
// "v1.1.0-3-gabc1234", and versionName may append the commit and a "-dirty"
// suffix on top of that. Read as semver, every one of those sorts *below* the
// plain v1.1.0 it is actually ahead of — which would tell the person running
// the newest code on the machine that they are behind.
//
// So only the numeric triple is compared and everything after it is discarded.
// The rule that falls out is the safe one: somebody ahead of a release is never
// told to go back to it. The cost is that a v1.2.0-rc1 is not offered the final
// v1.2.0 — nobody is installing release candidates of a chat client, and the
// endpoint does not serve prereleases as latest anyway.
var versionRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// Newer reports whether latest names a strictly higher release than current.
// Anything that is not a version — "dev", a bare commit, an empty string — is
// not comparable, and not comparable means no.
func Newer(current, latest string) bool {
	c, ok := triple(current)
	if !ok {
		return false
	}
	l, ok := triple(latest)
	if !ok {
		return false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// Comparable reports whether a version string names a release this package can
// reason about. False for a plain `go build` (which calls itself by its commit)
// and for "dev" — for which "are you behind?" has no answer, but "install the
// latest" still does. See runUpgrade.
func Comparable(version string) bool {
	_, ok := triple(version)
	return ok
}

// triple pulls the leading major.minor.patch out of a version string, ignoring
// whatever git describe, the build stamp or a "v" prefix put around it.
func triple(s string) ([3]int, bool) {
	m := versionRE.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		// The regexp matched \d+, so every group is digits; the only way to
		// overflow is a version number nobody will ever cut.
		n := 0
		for _, r := range m[i+1] {
			n = n*10 + int(r-'0')
			if n > 1<<30 {
				return [3]int{}, false
			}
		}
		out[i] = n
	}
	return out, true
}
