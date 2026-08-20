package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"matterbox/internal/libav"
)

// version is the release name, stamped at link time by the Makefile:
//
//	go build -ldflags "-X matterbox/internal/cli.version=v1.0.0" .
//
// It is deliberately empty in a plain `go build`, so versionString() can fall
// back to the VCS revision the toolchain records in every binary built from a
// git checkout. That way even an unstamped build can name itself in a bug
// report instead of claiming to be "dev".
var version string

// buildStamp is what the toolchain recorded about this binary: the commit it
// came from, whether that tree was dirty, and the build tags it was compiled
// with. Reading it costs a map walk over debug.BuildInfo, so it happens once
// per `version` invocation rather than at init.
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

// versionString is the one-liner behind `matterbox --version`: the stamped
// release when there is one, otherwise the commit, otherwise "dev".
func versionString() string {
	s := readBuildStamp()
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

// newVersionCmd prints everything a bug report wants to know: which build
// this is, which optional features were compiled into it (`make` picks those
// per machine, so two people's binaries differ), and the toolchain/platform.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the matterbox version, build tags, and platform",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := readBuildStamp()
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "matterbox %s\n", versionString())
			if s.time != "" {
				fmt.Fprintf(out, "built:  %s\n", s.time)
			}
			tags := s.tags
			if tags == "" {
				tags = "(none)"
			}
			fmt.Fprintf(out, "tags:   %s\n", tags)
			// A `video` build links the system's ffmpeg, whose license depends
			// on how that ffmpeg was configured — so two binaries from the same
			// commit can differ on whether they may be handed to anyone. Only
			// the binary itself knows, so it says. Omitted entirely when no
			// libav is linked, which is every release build.
			if s := libav.Linked().Summary(); s != "" {
				fmt.Fprintf(out, "ffmpeg: %s\n", s)
			}
			fmt.Fprintf(out, "go:     %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}
