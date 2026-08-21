package telemetry

import (
	"os"
	"runtime"
	"strings"
)

// Environment facts, mapped onto the catalogue's closed sets. Every one of
// these is about the machine and the build rather than the person: which
// terminal, which architecture, how big the window is. They matter because most
// "nobody uses this feature" findings turn out to be "nobody *can* use this
// feature" — a media feature looks dead when the terminal has no graphics
// protocol, and the three-pane layout looks ignored at 80 columns because it
// doesn't fit.
//
// Everything here maps to a fixed label. An unrecognised terminal reports
// "other", never the raw environment variable, because $TERM_PROGRAM is
// occasionally set to something bespoke by a corporate image and that would be
// identifying.

// OSName maps runtime.GOOS onto the catalogue's set.
func OSName() string {
	switch runtime.GOOS {
	case "linux", "darwin", "windows", "freebsd", "openbsd", "netbsd":
		return runtime.GOOS
	}
	return "other"
}

// ArchName maps runtime.GOARCH onto the catalogue's set.
func ArchName() string {
	switch runtime.GOARCH {
	case "amd64", "arm64", "arm", "386":
		return runtime.GOARCH
	}
	return "other"
}

// terminalEnv is the ordered list of environment variables that identify a
// terminal, and the labels their values map to. Order matters: a multiplexer
// wins over the terminal hosting it, because the multiplexer is what actually
// constrains what matterbox can do — under tmux the graphics probe is
// unreliable and passthrough is fragile, which is a different product.
var terminalEnv = []struct {
	env    string
	values map[string]string
}{
	// Multiplexers first, and by presence rather than value.
	{env: "ZELLIJ", values: nil},
	{env: "TMUX", values: nil},
	{env: "TERM_PROGRAM", values: map[string]string{
		"ghostty":          "ghostty",
		"iTerm.app":        "iterm2",
		"Apple_Terminal":   "apple_terminal",
		"WezTerm":          "wezterm",
		"vscode":           "vscode",
		"kitty":            "kitty",
		"alacritty":        "alacritty",
		"Hyper":            "other",
		"tmux":             "tmux",
		"WarpTerminal":     "other",
		"rio":              "other",
		"contour":          "other",
		"Windows Terminal": "windows_terminal",
	}},
	{env: "TERM", values: map[string]string{
		"xterm-kitty":           "kitty",
		"xterm-ghostty":         "ghostty",
		"wezterm":               "wezterm",
		"alacritty":             "alacritty",
		"foot":                  "foot",
		"foot-extra":            "foot",
		"screen":                "screen",
		"screen-256color":       "screen",
		"tmux-256color":         "tmux",
		"konsole-256color":      "konsole",
		"xterm-256color":        "xterm",
		"xterm":                 "xterm",
		"rxvt-unicode-256color": "rxvt",
		"rxvt-unicode":          "rxvt",
	}},
}

// multiplexerLabel names the multiplexer for the presence-only entries above.
var multiplexerLabel = map[string]string{"ZELLIJ": "zellij", "TMUX": "tmux"}

// DetectTerminal identifies the terminal from the environment, as one of the
// catalogue's labels. Returns "unknown" when nothing identifying is set (a
// stripped cron environment) and "other" when something is set but isn't on the
// list — so a bespoke $TERM_PROGRAM is counted without being repeated.
func DetectTerminal() string {
	for _, src := range terminalEnv {
		v := strings.TrimSpace(os.Getenv(src.env))
		if v == "" {
			continue
		}
		if src.values == nil {
			return multiplexerLabel[src.env]
		}
		if label, ok := src.values[v]; ok {
			return label
		}
		// Set, but not a value we know. Keep looking — $TERM may still name
		// something recognisable — and fall back to "other" if it doesn't.
		if src.env == "TERM_PROGRAM" {
			continue
		}
		return "other"
	}
	// GNOME Terminal and several others identify only through VTE.
	if os.Getenv("VTE_VERSION") != "" {
		return "gnome_terminal"
	}
	if os.Getenv("KONSOLE_VERSION") != "" {
		return "konsole"
	}
	if os.Getenv("TERM") != "" || os.Getenv("TERM_PROGRAM") != "" {
		return "other"
	}
	return "unknown"
}

// GoVersion returns the toolchain version, trimmed to the "go1.26.6" prefix.
// The full string can carry local toolchain flags (this machine reports
// "go1.26.6-X:nodwarf5"), which describe someone's build environment rather
// than the release.
func GoVersion() string {
	v := runtime.Version()
	if i := strings.IndexAny(v, " -"); i > 0 {
		v = v[:i]
	}
	return v
}
