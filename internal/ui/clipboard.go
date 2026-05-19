package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

type clipboardKind int

const (
	clipFile clipboardKind = iota
	clipImage
)

// clipboardPayload is one thing pulled off the clipboard that should
// become an attachment. For clipFile, path is the user's existing file
// (no copy). For clipImage, path is a freshly-written tempfile under
// pasteTempDir() and isTemp is true so it gets cleaned up on dismiss.
type clipboardPayload struct {
	kind     clipboardKind
	filename string
	mime     string
	path     string
	size     int64
	isTemp   bool
}

// clipboardReadMsg is the result of readClipboard. payloads non-empty means
// at least one attachable item (file/image) was found. text non-empty means
// the clipboard held plain text — used as a fallback so ctrl+v inserts text
// into the focused input when there is nothing to attach.
type clipboardReadMsg struct {
	payloads []clipboardPayload
	text     string
	err      error
}

// MIME types we'll attach, in preference order. text/uri-list wins
// because file managers expose both uri-list and a synthesised image
// for image files — preferring uri-list avoids a wasted copy.
var attachableMIMEs = []struct {
	mime string
	ext  string
}{
	{"text/uri-list", ""},
	{"image/png", ".png"},
	{"image/jpeg", ".jpg"},
	{"image/webp", ".webp"},
	{"image/gif", ".gif"},
}

type clipTool struct {
	name string // "wl-paste" or "xclip"
	path string
}

var (
	clipToolOnce sync.Once
	clipToolVal  clipTool
)

func detectClipTool() clipTool {
	clipToolOnce.Do(func() {
		if p, err := exec.LookPath("wl-paste"); err == nil {
			clipToolVal = clipTool{name: "wl-paste", path: p}
			return
		}
		if p, err := exec.LookPath("xclip"); err == nil {
			clipToolVal = clipTool{name: "xclip", path: p}
			return
		}
	})
	return clipToolVal
}

// readClipboard inspects the clipboard, picks the best matching MIME,
// and returns one or more payloads. Returns payloads:nil, err:nil when
// the clipboard holds only text or unsupported types — the caller
// should treat that as "no file in clipboard" (a non-error).
func readClipboard() tea.Cmd {
	return func() tea.Msg {
		tool := detectClipTool()
		if tool.name == "" {
			return clipboardReadMsg{err: errors.New("install wl-clipboard or xclip to paste files")}
		}

		types, err := clipListTypes(tool)
		if err != nil {
			return clipboardReadMsg{err: err}
		}
		typeSet := map[string]bool{}
		for _, t := range types {
			typeSet[strings.ToLower(strings.TrimSpace(t))] = true
		}

		for _, m := range attachableMIMEs {
			if !typeSet[m.mime] {
				continue
			}
			data, err := clipReadType(tool, m.mime)
			if err != nil {
				return clipboardReadMsg{err: err}
			}
			if len(data) == 0 {
				continue
			}
			if m.mime == "text/uri-list" {
				ps := parseURIList(data)
				if len(ps) == 0 {
					// All URIs dead or non-file:// — treat as no match.
					continue
				}
				return clipboardReadMsg{payloads: ps}
			}
			p, err := writeImageTemp(data, m.mime, m.ext)
			if err != nil {
				return clipboardReadMsg{err: err}
			}
			return clipboardReadMsg{payloads: []clipboardPayload{p}}
		}
		// Fallback: no attachable type — try plain text so ctrl+v still
		// does something useful when the clipboard holds a copied string.
		for _, mime := range []string{"text/plain;charset=utf-8", "text/plain", "UTF8_STRING", "STRING"} {
			if !typeSet[strings.ToLower(mime)] {
				continue
			}
			data, err := clipReadType(tool, mime)
			if err != nil || len(data) == 0 {
				continue
			}
			return clipboardReadMsg{text: string(data)}
		}
		return clipboardReadMsg{}
	}
}

func clipListTypes(t clipTool) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch t.name {
	case "wl-paste":
		cmd = exec.CommandContext(ctx, t.path, "--list-types")
	case "xclip":
		cmd = exec.CommandContext(ctx, t.path, "-selection", "clipboard", "-o", "-t", "TARGETS")
	default:
		return nil, fmt.Errorf("no clipboard tool")
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		// Empty clipboard is not an error worth surfacing.
		if strings.Contains(errOut.String(), "No selection") || strings.Contains(errOut.String(), "Nothing is copied") {
			return nil, nil
		}
		return nil, fmt.Errorf("list clipboard types: %w", err)
	}
	return strings.Split(strings.TrimSpace(out.String()), "\n"), nil
}

func clipReadType(t clipTool, mime string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch t.name {
	case "wl-paste":
		cmd = exec.CommandContext(ctx, t.path, "--type", mime, "--no-newline")
	case "xclip":
		cmd = exec.CommandContext(ctx, t.path, "-selection", "clipboard", "-o", "-t", mime)
	default:
		return nil, fmt.Errorf("no clipboard tool")
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("read clipboard %s: %w: %s", mime, err, strings.TrimSpace(errOut.String()))
	}
	return out.Bytes(), nil
}

func parseURIList(data []byte) []clipboardPayload {
	var out []clipboardPayload
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "file://") {
			continue
		}
		u, err := url.Parse(line)
		if err != nil {
			continue
		}
		p, err := url.PathUnescape(u.Path)
		if err != nil {
			continue
		}
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		out = append(out, clipboardPayload{
			kind:     clipFile,
			filename: filepath.Base(p),
			path:     p,
			size:     info.Size(),
		})
	}
	return out
}

func pasteTempDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "matterbox-paste")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func writeImageTemp(data []byte, mime, ext string) (clipboardPayload, error) {
	dir, err := pasteTempDir()
	if err != nil {
		return clipboardPayload{}, err
	}
	name := fmt.Sprintf("screenshot-%s%s", time.Now().Format("20060102-150405.000"), ext)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return clipboardPayload{}, err
	}
	return clipboardPayload{
		kind:     clipImage,
		filename: name,
		mime:     mime,
		path:     path,
		size:     int64(len(data)),
		isTemp:   true,
	}, nil
}

// xdgOpenPath fires xdg-open on a local path. Mirrors the pattern in
// (Model).openOpenable but for paths we already have on disk.
func xdgOpenPath(name, path string) tea.Cmd {
	return func() tea.Msg {
		if err := exec.Command("xdg-open", path).Start(); err != nil {
			return attachmentOpenedMsg{name: name, err: err}
		}
		return attachmentOpenedMsg{name: name}
	}
}
