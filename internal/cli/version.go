package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	"matterbox/internal/libav"
	"matterbox/internal/telemetry"
	"matterbox/internal/ui"
)

// version is the release name, stamped at link time by the Makefile:
//
//	go build -ldflags "-X matterbox/internal/cli.version=v1.0.0" .
//
// It is deliberately empty in a plain `go build`, so versionName() can fall
// back to the VCS revision the toolchain records in every binary built from a
// git checkout. That way even an unstamped build can name itself in a bug
// report instead of claiming to be "dev".
var version string

// buildStamp is what the toolchain recorded about this binary: the commit it
// came from, whether that tree was dirty, and the build tags it was compiled
// with. Reading it costs a map walk over debug.BuildInfo, so it happens once
// per process, when the command tree is built.
type buildStamp struct {
	revision string // git commit, short
	modified bool   // working tree was dirty at build time
	time     string // build (or commit) time, RFC3339
	tags     string // -tags value, e.g. "demoaudio,video"
}

func readBuildStamp() buildStamp {
	var s buildStamp
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return s
	}
	for _, kv := range bi.Settings {
		switch kv.Key {
		case "vcs.revision":
			s.revision = kv.Value
			if len(s.revision) > 12 {
				s.revision = s.revision[:12]
			}
		case "vcs.modified":
			s.modified = kv.Value == "true"
		case "vcs.time":
			s.time = kv.Value
		case "-tags":
			s.tags = kv.Value
		}
	}
	return s
}

// versionName is this build's name: the stamped release when there is one,
// otherwise the commit, otherwise "dev".
func versionName(s buildStamp) string {
	if version == "" {
		v := s.revision
		if v == "" {
			v = "dev"
		}
		if s.modified {
			v += "-dirty"
		}
		return v
	}
	// A stamped version usually comes from `git describe`, which already
	// carries the commit and a -dirty suffix; only add what it's missing.
	v := version
	if rev := s.revision; rev != "" && !strings.Contains(v, shortRev(rev)) {
		v += " (" + rev + ")"
	}
	if s.modified && !strings.Contains(v, "dirty") {
		v += "-dirty"
	}
	return v
}

// shortRev is the 7-character commit prefix `git describe` uses, so a stamped
// version and the toolchain's own revision can be compared.
func shortRev(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// versionBlock is everything `matterbox --version` prints, and everything a bug
// report wants to know: which build this is, which optional features were
// compiled into it (`make` picks those per machine, so two people's binaries
// differ), and the toolchain/platform. The first line stays a bare
// "matterbox <version>" so a script can still take just the head of it.
func versionBlock() string {
	s := readBuildStamp()
	var b strings.Builder
	fmt.Fprintf(&b, "matterbox %s\n", versionName(s))
	if s.time != "" {
		fmt.Fprintf(&b, "built:     %s\n", s.time)
	}
	tags := s.tags
	if tags == "" {
		tags = "(none)"
	}
	fmt.Fprintf(&b, "tags:      %s\n", tags)
	// A `video` build links the system's ffmpeg, whose license depends on how
	// that ffmpeg was configured — so two binaries from the same commit can
	// differ on whether they may be handed to anyone. Only the binary itself
	// knows, so it says. Omitted entirely when no libav is linked, which is
	// every release build.
	if sum := libav.Linked().Summary(); sum != "" {
		fmt.Fprintf(&b, "ffmpeg:    %s\n", sum)
		// Which image formats that ffmpeg unlocks is not the same question as
		// whether it is linked. HEIC and AVIF ride on decoders every ffmpeg has;
		// JPEG XL needs an optional --enable-libjxl, so it has to be asked. This
		// line is the only way a user finds out why a .jxl shows a paperclip.
		fmt.Fprintf(&b, "images:    heic, avif%s\n", jxlNote())
	}
	fmt.Fprintf(&b, "go:        %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "telemetry: %s\n", telemetryState())
	return b.String()
}

// jxlNote completes the "images:" line with JPEG XL's availability, which depends
// on the linked ffmpeg rather than on the build.
func jxlNote() string {
	if ui.JPEGXLDecodable() {
		return ", jxl"
	}
	return " (no jxl — this ffmpeg lacks libjxl)"
}

// telemetryState is the "telemetry:" line: whether this binary would report
// anything, and why not when it wouldn't.
//
// Worth printing next to the build facts because it is one, partly: three things
// have to line up — a project key compiled into the binary, a config on disk, and
// an explicit yes in it — and no two of them live in the same place. "Is this
// build sending anything?" is otherwise a question you answer by reading source,
// which is the wrong answer to give someone asking about their own machine.
//
// Reads the config only if one already exists. config.Load writes a default file
// when there isn't one, and printing a version must not leave a file behind —
// the same reason loadConfigIfPresent exists, which this uses.
func telemetryState() string {
	state, why := "off", "never asked"
	if cfg := loadConfigIfPresent(); cfg != nil && cfg.Telemetry.Enabled != nil {
		why = ""
		if cfg.TelemetryEnabled() {
			state = "on"
		}
	}
	// A build whose key was blanked (`make POSTHOG_KEY=`) reports nowhere
	// whatever the config says, and since the key is compiled in, asking the
	// binary is the only way to confirm that worked.
	if !telemetry.HasProjectKey() {
		if why != "" {
			why += ", "
		}
		why += "no project key in this build"
	}
	if why == "" {
		return state
	}
	return state + " (" + why + ")"
}
