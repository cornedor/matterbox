package ui

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Terminals have no drag-and-drop protocol. The emulator handles the OS-level
// drop (XDND on X11, wl_data_device on Wayland, NSDragging on macOS) in its GUI
// layer and writes the dropped file's path into the pty, shell-quoted, exactly
// as if it had been pasted. So a drop arrives as an ordinary tea.PasteMsg and
// the only way to tell it from a real paste is to look at the content.
//
// droppedFiles reports whether a paste is really a file drop. The rule is
// deliberately strict: every token must resolve to an existing regular file at
// an absolute path. One stray word, one missing file, one relative path and it
// returns false, leaving the paste to be inserted as text. That keeps prose
// ("have a look at /etc/hosts") and relative paths that happen to exist under
// the cwd ("internal/ui/view.go") from being silently swallowed.
func droppedFiles(paste string) ([]clipboardPayload, bool) {
	tokens, ok := splitDropTokens(paste)
	if !ok || len(tokens) == 0 {
		return nil, false
	}
	payloads := make([]clipboardPayload, 0, len(tokens))
	for _, tok := range tokens {
		path, ok := dropPath(tok)
		if !ok {
			return nil, false
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, false
		}
		payloads = append(payloads, clipboardPayload{
			kind:     clipFile,
			filename: filepath.Base(path),
			path:     path,
			size:     info.Size(),
		})
	}
	return payloads, true
}

// dropPath resolves one token to an absolute local path: a file:// URI (what
// VTE-based terminals paste) is decoded, a leading ~ is expanded. Anything not
// absolute afterwards is rejected — a drop always yields an absolute path, so
// insisting on one costs nothing and rules out most false positives.
func dropPath(tok string) (string, bool) {
	if strings.HasPrefix(tok, "file://") {
		u, err := url.Parse(tok)
		if err != nil {
			return "", false
		}
		p, err := url.PathUnescape(u.Path)
		if err != nil {
			return "", false
		}
		tok = p
	}
	tok = expandUserPath(tok)
	if !filepath.IsAbs(tok) {
		return "", false
	}
	return filepath.Clean(tok), true
}

// splitDropTokens splits a paste into tokens the way a shell would, because
// that is how terminals quote the paths they paste: single quotes (kitty, VTE,
// iTerm2), double quotes, or backslash-escaped spaces (Terminal.app). Multiple
// files arrive space- or newline-separated. An unterminated quote means this
// isn't a drop, so it reports false.
func splitDropTokens(s string) ([]string, bool) {
	var (
		tokens []string
		cur    strings.Builder
		quote  rune // 0 when outside quotes, else the opening quote rune
		open   bool // cur holds a token — possibly the empty one from ""
	)
	flush := func() {
		if open {
			tokens = append(tokens, cur.String())
			cur.Reset()
			open = false
		}
	}

	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case quote == '\'':
			// POSIX single quotes: everything is literal until the next quote.
			if c == '\'' {
				quote = 0
				continue
			}
			cur.WriteRune(c)
		case quote == '"':
			// Inside double quotes a backslash escapes only these four; anywhere
			// else it stands for itself.
			if c == '\\' && i+1 < len(rs) && strings.ContainsRune("\"\\$`", rs[i+1]) {
				i++
				cur.WriteRune(rs[i])
				continue
			}
			if c == '"' {
				quote = 0
				continue
			}
			cur.WriteRune(c)
		case c == '\'' || c == '"':
			quote = c
			open = true
		case c == '\\':
			if i+1 >= len(rs) {
				return nil, false // dangling escape: not a path we can trust
			}
			i++
			cur.WriteRune(rs[i])
			open = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			cur.WriteRune(c)
			open = true
		}
	}
	if quote != 0 {
		return nil, false // unterminated quote
	}
	flush()
	return tokens, true
}

// attachDroppedFiles attaches a paste that is really a file drop, reporting
// whether it consumed the paste. When it returns false the caller inserts the
// paste as text as usual.
func (m *Model) attachDroppedFiles(content string) (tea.Cmd, bool) {
	if !m.attachOnDrop || m.openChannelID == "" {
		return nil, false
	}
	// Inside a fenced code block a path is being quoted, not dropped.
	if m.focus == focusInput && m.input.InCodeBlock() {
		return nil, false
	}
	payloads, ok := droppedFiles(content)
	if !ok {
		return nil, false
	}
	if len(payloads) == 1 {
		m.status = "attached " + payloads[0].filename
	} else {
		m.status = fmt.Sprintf("attached %d files", len(payloads))
	}
	// addAttachments overwrites status when it hits the per-post cap, so it
	// runs after the optimistic message above.
	return m.addAttachments(payloads, "drop"), true
}
