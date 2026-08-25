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
// matterbox is GPL-3.0-or-later, which makes all four free configurations fine:
// LGPL is GPL-compatible, and every GPL configuration FFmpeg reports is "or
// later", so it upgrades to v3. That was not true while matterbox was
// Apache-2.0 — a GPL FFmpeg then produced a binary nobody could hand out under
// the project's own license, and since almost every distribution builds FFmpeg
// with --enable-gpl, that was most builds. It is why this package exists.
//
// --enable-nonfree is the one remaining dead end: such a build may not be
// conveyed on any terms, by us or by anyone. So the question is now narrow, but
// it still has to be asked, and only the linked library can answer it
// (avutil_license). `matterbox version` prints the answer, and
// scripts/third-party-licenses refuses to build a license bundle for a build
// that cannot be conveyed at all.
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
	// ClassPermissive: LGPL. GPL-compatible, so it rides along inside a
	// GPL-3.0-or-later binary; its notice and source offer go in the bundle.
	ClassPermissive
	// ClassCopyleft: GPL. Compatible with matterbox's own GPL-3.0-or-later,
	// because every GPL license FFmpeg reports is an "or later" one.
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
// under matterbox's GPL-3.0-or-later license. Unknown counts as no.
//
// Listed positively rather than as "not forbidden": a class added later should
// have to be admitted deliberately, not let through by a negation.
func (c Class) Distributable() bool {
	return c == ClassNone || c == ClassPermissive || c == ClassCopyleft
}

// Class classifies the license string FFmpeg reports. The strings come from
// libavutil's own version.c and have been stable for many major versions:
// "LGPL version 2.1 or later", "GPL version 2 or later", "nonfree and
// unredistributable", each optionally with "(v3)"-style variants.
//
// Order matters. "nonfree" is checked first because such a build is out of
// bounds regardless of what else the string says, and GPL is checked before
// LGPL because "LGPL" contains "GPL" — a substring test in the other order
// would report a GPL build as LGPL. Nothing now hangs on telling those two
// apart for distribution, but the bundle names the library's own license, and
// naming the wrong one is still wrong.
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
	switch i.Class() {
	case ClassForbidden:
		s += " — nonfree: this binary may NOT be distributed at all"
	case ClassUnknown:
		s += " — unrecognised license: treat this binary as not distributable"
	}
	return s
}
