// Command probe prints what libav this build links, for scripts that need to
// decide whether a binary may be redistributed. It is deliberately not behind
// the `video` build tag: run tag-free it reports class=none, which is the
// correct answer for a build that links no ffmpeg at all, and keeps
// `go build ./...` and `go vet ./...` working in both configurations.
//
//	go run -tags video ./internal/libav/probe
//	linked=1
//	version=8.1.2
//	license=GPL version 3 or later
//	class=gpl
//	distributable=1
//
// distributable is about matterbox's own GPL-3.0-or-later: a GPL ffmpeg is
// compatible with it, so only an --enable-nonfree build (or one whose license
// string we don't recognise) reports 0.
//
// scripts/third-party-licenses parses `class` and `distributable`. It always
// exits 0 — a probe that cannot answer is a different failure from a probe that
// answers "not distributable", and the caller needs to tell them apart.
package main

import (
	"fmt"

	"matterbox/internal/libav"
)

func main() {
	i := libav.Linked()
	linked := 0
	if i.Linked {
		linked = 1
	}
	distributable := 0
	if i.Class().Distributable() {
		distributable = 1
	}
	fmt.Printf("linked=%d\n", linked)
	fmt.Printf("version=%s\n", i.Version)
	fmt.Printf("license=%s\n", i.License)
	fmt.Printf("class=%s\n", i.Class())
	fmt.Printf("distributable=%d\n", distributable)
}
