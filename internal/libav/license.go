// Package libav reports which FFmpeg libraries a matterbox binary has linked
// and what their license means for handing that binary to someone.
//
// This exists because the `video` build tag raises a licensing question that
// go.mod cannot answer. The Go side of video decoding is
// github.com/asticode/go-astiav, which is MIT — but astiav is only cgo bindings,
// and the actual code in the binary comes from the system's libav* shared
// libraries. FFmpeg's license depends on how whoever built it configured it:
//
//	default                        LGPL-2.1-or-later
//	--enable-version3              LGPL-3.0-or-later
//	--enable-gpl                   GPL-2.0-or-later
//	--enable-gpl --enable-version3 GPL-3.0-or-later
//	--enable-nonfree               not redistributable at all
//
// Dynamically linking LGPL FFmpeg from an Apache-2.0 program is fine: LGPL-2.1
// section 4 lets you distribute a work that merely uses the library under terms
// of your choice as long as the user can relink it against their own copy, which
// linking a .so satisfies. GPL FFmpeg is a different matter — a binary linking
// it is a combined work that has to be conveyed under the GPL, so it cannot be
// handed out under matterbox's Apache-2.0.
//
// So the same source, built on two machines, can produce one binary that is
// distributable and one that is not, and nothing in the source records which.
// FFmpeg knows the answer at runtime (avutil_license), so ask it: `matterbox
// version` prints it, and scripts/third-party-licenses refuses to build a
// license bundle for a copyleft-contaminated build.
package libav

import "strings"

// Info is what the linked libav reports about itself. The zero value is a build
// without the `video` tag, which links no libav at all.
type Info struct {
	Linked  bool   // built with -tags video, so libav is in the binary
	License string // avutil_license(), e.g. "LGPL version 2.1 or later"
	Version string // av_version_info(), e.g. "8.1.2"
}

// Class is what a license means for redistributing the binary.
type Class int

const (
	// ClassNone: no libav linked, so it constrains nothing.
	ClassNone Class = iota
	// ClassPermissive: LGPL. Dynamically linked, an Apache-2.0 binary may ship.
	ClassPermissive
	// ClassCopyleft: GPL. The whole binary would have to be conveyed under the
	// GPL, so not under Apache-2.0.
	ClassCopyleft
	// ClassForbidden: built --enable-nonfree. Not redistributable on any terms.
	ClassForbidden
	// ClassUnknown: libav is linked but its license string is unrecognised.
	// Treated as unsafe, because guessing in the permissive direction is the
	// one error with legal consequences.
	ClassUnknown
)

func (c Class) String() string {
	switch c {
	case ClassNone:
		return "none"
	case ClassPermissive:
		return "lgpl"
	case ClassCopyleft:
		return "gpl"
	case ClassForbidden:
		return "nonfree"
	default:
		return "unknown"
	}
}

// Distributable reports whether a binary carrying this libav may be handed out
// under matterbox's Apache-2.0 license. Unknown counts as no.
func (c Class) Distributable() bool {
	return c == ClassNone || c == ClassPermissive
}

// Class classifies the license string FFmpeg reports. The strings come from
// libavutil's own version.c and have been stable for many major versions:
// "LGPL version 2.1 or later", "GPL version 2 or later", "nonfree and
// unredistributable", each optionally with "(v3)"-style variants.
//
// Order matters. "nonfree" is checked first because such a build is out of
// bounds regardless of what else the string says, and GPL is checked before
// LGPL because "LGPL" contains "GPL" — a substring test in the other order
// would wave a GPL build straight through.
func (i Info) Class() Class {
	if !i.Linked {
		return ClassNone
	}
	l := strings.ToLower(i.License)
	switch {
	case l == "":
		return ClassUnknown
	case strings.Contains(l, "nonfree"), strings.Contains(l, "unredistributable"):
		return ClassForbidden
	case strings.Contains(l, "lgpl"):
		return ClassPermissive
	case strings.Contains(l, "gpl"):
		return ClassCopyleft
	default:
		return ClassUnknown
	}
}

// Summary is the one-line description `matterbox version` prints. Empty when no
// libav is linked, so the caller can omit the line entirely.
func (i Info) Summary() string {
	if !i.Linked {
		return ""
	}
	lic := i.License
	if lic == "" {
		lic = "license unknown"
	}
	s := lic
	if i.Version != "" {
		s = i.Version + ", " + lic
	}
	if !i.Class().Distributable() {
		s += " — this binary is NOT distributable under Apache-2.0"
	}
	return s
}
