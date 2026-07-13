package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

func TestSplitDropTokens(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  []string
		wantK bool
	}{
		{"bare path", "/tmp/a.png", []string{"/tmp/a.png"}, true},
		{"single quoted", "'/tmp/my file.png'", []string{"/tmp/my file.png"}, true},
		{"double quoted", `"/tmp/my file.png"`, []string{"/tmp/my file.png"}, true},
		{"backslash escaped", `/tmp/my\ file.png`, []string{"/tmp/my file.png"}, true},
		{"two files", "/tmp/a.png /tmp/b.png", []string{"/tmp/a.png", "/tmp/b.png"}, true},
		{"newline separated", "/tmp/a.png\n/tmp/b.png", []string{"/tmp/a.png", "/tmp/b.png"}, true},
		{"trailing space", "/tmp/a.png ", []string{"/tmp/a.png"}, true},
		{"quoted then bare", "'/tmp/a b.png' /tmp/c.png", []string{"/tmp/a b.png", "/tmp/c.png"}, true},
		{"escaped quote in name", `/tmp/it\'s.png`, []string{"/tmp/it's.png"}, true},
		{"prose stays tokenised", "look at /etc/hosts", []string{"look", "at", "/etc/hosts"}, true},
		{"unterminated quote", "'/tmp/a.png", nil, false},
		{"dangling escape", `/tmp/a.png\`, nil, false},
		{"empty", "", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := splitDropTokens(tc.in)
			if ok != tc.wantK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantK)
			}
			if !ok {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			}
		})
	}
}

// A drop must survive every quoting style a terminal might use, and only a
// paste that is entirely existing files may be swallowed.
func TestDroppedFiles(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(png, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	spaced := filepath.Join(dir, "my report.pdf")
	if err := os.WriteFile(spaced, []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("bare path attaches", func(t *testing.T) {
		got, ok := droppedFiles(png)
		if !ok || len(got) != 1 {
			t.Fatalf("droppedFiles(%q) = %v, %v", png, got, ok)
		}
		if got[0].filename != "shot.png" || got[0].path != png {
			t.Errorf("payload = %+v", got[0])
		}
		if got[0].size != 4 {
			t.Errorf("size = %d, want 4", got[0].size)
		}
		if got[0].isTemp {
			t.Error("a dropped file is the user's own; it must not be marked temp")
		}
	})

	t.Run("shell-quoted path with a space", func(t *testing.T) {
		got, ok := droppedFiles("'" + spaced + "'")
		if !ok || len(got) != 1 || got[0].path != spaced {
			t.Fatalf("got %v, %v", got, ok)
		}
	})

	t.Run("backslash-escaped path with a space", func(t *testing.T) {
		got, ok := droppedFiles(filepath.Join(dir, `my\ report.pdf`))
		if !ok || len(got) != 1 || got[0].path != spaced {
			t.Fatalf("got %v, %v", got, ok)
		}
	})

	t.Run("file:// URI is decoded", func(t *testing.T) {
		got, ok := droppedFiles("file://" + dir + "/my%20report.pdf")
		if !ok || len(got) != 1 || got[0].path != spaced {
			t.Fatalf("got %v, %v", got, ok)
		}
	})

	t.Run("multiple files", func(t *testing.T) {
		got, ok := droppedFiles(png + " '" + spaced + "'")
		if !ok || len(got) != 2 {
			t.Fatalf("got %v, %v", got, ok)
		}
		if got[0].path != png || got[1].path != spaced {
			t.Errorf("paths = %q, %q", got[0].path, got[1].path)
		}
	})

	t.Run("trailing newline and spaces are ignored", func(t *testing.T) {
		if _, ok := droppedFiles("  " + png + "\n"); !ok {
			t.Error("padded drop should still attach")
		}
	})

	// Everything below must stay text.
	t.Run("prose mentioning a real path", func(t *testing.T) {
		if _, ok := droppedFiles("have a look at " + png); ok {
			t.Error("prose around a path must not be swallowed")
		}
	})

	t.Run("path that does not exist", func(t *testing.T) {
		if _, ok := droppedFiles(filepath.Join(dir, "nope.png")); ok {
			t.Error("missing file must not attach")
		}
	})

	t.Run("directory", func(t *testing.T) {
		if _, ok := droppedFiles(dir); ok {
			t.Error("a directory is not an attachable file")
		}
	})

	t.Run("relative path is rejected even when it exists", func(t *testing.T) {
		// Relative paths get quoted in chat all the time ("see internal/ui/view.go")
		// and would resolve against the cwd — never treat one as a drop.
		if _, ok := droppedFiles("dropfiles.go"); ok {
			t.Error("relative path must not attach")
		}
	})

	t.Run("one bad path poisons the whole paste", func(t *testing.T) {
		if _, ok := droppedFiles(png + " " + filepath.Join(dir, "nope.png")); ok {
			t.Error("a paste is a drop only when every token is a real file")
		}
	})

	t.Run("empty paste", func(t *testing.T) {
		if _, ok := droppedFiles("   "); ok {
			t.Error("empty paste is not a drop")
		}
	})

	t.Run("url is not a file", func(t *testing.T) {
		if _, ok := droppedFiles("https://example.com/a.png"); ok {
			t.Error("http url must stay text")
		}
	})
}

// dropModel is a composer sitting on an open channel, ready to accept a drop.
func dropModel(t *testing.T) Model {
	t.Helper()
	m := composerModel([]*model.Post{p("a", 100)}, 0)
	m.ctx = context.Background()
	m.openChannelID = "c"
	m.attachOnDrop = true
	m.focus = focusInput
	m.input.Focus()
	// New() seeds these; a literal Model doesn't, and the paste-as-text path
	// walks the slash-command cache.
	m.serverCmds = map[string][]serverCommand{}
	m.serverCmdsReq = map[string]bool{}
	return m
}

// The whole point of the feature: a dropped path becomes an attachment, and
// the path itself never lands in the composer.
func TestHandlePasteAttachesDrop(t *testing.T) {
	png := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(png, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := dropModel(t)

	out, _ := m.handlePaste(tea.PasteMsg{Content: png})
	m = out.(Model)

	if len(m.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(m.attachments))
	}
	if m.attachments[0].filename != "shot.png" {
		t.Errorf("filename = %q, want shot.png", m.attachments[0].filename)
	}
	if got := m.input.Value(); got != "" {
		t.Errorf("composer = %q, want empty — the path must not be typed out too", got)
	}
}

// A drop while reading (focus on the message pane) still attaches; today an
// unfocused paste is dropped on the floor entirely.
func TestHandlePasteAttachesDropFromMessagePane(t *testing.T) {
	png := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(png, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := dropModel(t)
	m.focus = focusMessages

	out, _ := m.handlePaste(tea.PasteMsg{Content: png})
	if got := len(out.(Model).attachments); got != 1 {
		t.Fatalf("attachments = %d, want 1", got)
	}
}

// An ordinary paste must be unaffected — it still types into the composer.
func TestHandlePasteTextStillTypes(t *testing.T) {
	m := dropModel(t)

	out, _ := m.handlePaste(tea.PasteMsg{Content: "hello there"})
	m = out.(Model)

	if len(m.attachments) != 0 {
		t.Fatalf("attachments = %d, want 0", len(m.attachments))
	}
	if got := m.input.Value(); got != "hello there" {
		t.Errorf("composer = %q, want %q", got, "hello there")
	}
}

// attach_on_drop: false pastes the path as text instead.
func TestHandlePasteDropDisabled(t *testing.T) {
	png := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(png, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := dropModel(t)
	m.attachOnDrop = false

	out, _ := m.handlePaste(tea.PasteMsg{Content: png})
	m = out.(Model)

	if len(m.attachments) != 0 {
		t.Fatalf("attachments = %d, want 0", len(m.attachments))
	}
	if m.input.Value() != png {
		t.Errorf("composer = %q, want the raw path", m.input.Value())
	}
}

// Inside a fenced code block the user is quoting a path, not dropping a file.
func TestHandlePasteDropInCodeBlockStaysText(t *testing.T) {
	png := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(png, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := dropModel(t)
	m.input.SetValue("```\n")
	m.input.CursorEnd()

	out, _ := m.handlePaste(tea.PasteMsg{Content: png})
	m = out.(Model)

	if len(m.attachments) != 0 {
		t.Fatalf("attachments = %d, want 0", len(m.attachments))
	}
	if !strings.Contains(m.input.Value(), png) {
		t.Errorf("composer = %q, want the path pasted as text", m.input.Value())
	}
}
