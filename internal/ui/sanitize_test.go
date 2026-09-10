package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// termInjections are escape sequences a sender can put in any remote-authored
// field: clear screen, set the window title, and the OSC 52 clipboard write
// (MMSA-2026-00599) — the last two also in their single-byte C1 spelling.
var termInjections = []string{
	"\x1b[2J\x1b[H",
	"\x1b]0;pwned\x07",
	"\x1b]52;c;cHduZWQ=\x07",
	"2J",
	"52;c;cHduZWQ=",
}

// hasTermControl reports whether s carries a character the terminal would act on.
// The renderers below produce no styling escapes for plain text, so for these
// inputs the whole output must be free of them.
func hasTermControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		if r == '\n' || r == '\t' {
			return false
		}
		return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
	})
}

func TestRenderMarkdownStripsTerminalEscapes(t *testing.T) {
	for _, p := range termInjections {
		got := renderMarkdown("before "+p+" after", nil, nil, "")
		if hasTermControl(got) {
			t.Errorf("renderMarkdown kept a control char from %q: %q", p, got)
		}
		if !strings.Contains(got, "before ") || !strings.Contains(got, " after") {
			t.Errorf("renderMarkdown dropped visible text for %q: %q", p, got)
		}
	}
}

func TestRenderMarkdownKeepsLayoutWhitespace(t *testing.T) {
	got := renderMarkdown("one\ntwo", nil, nil, "")
	if got != "  one\n  two" {
		t.Fatalf("newline handling changed: %q", got)
	}
}

func TestPostAuthorNameStripsTerminalEscapes(t *testing.T) {
	for _, p := range termInjections {
		m := &Model{userNames: map[string]string{"u1": "bob" + p}}
		post := &model.Post{UserId: "u1"}
		if got := m.postAuthorName(post); hasTermControl(got) {
			t.Errorf("username kept a control char from %q: %q", p, got)
		}
		post.AddProp("override_username", "hook"+p)
		if got := m.postAuthorName(post); hasTermControl(got) {
			t.Errorf("override_username kept a control char from %q: %q", p, got)
		}
	}
}

func TestChannelAndTeamNamesStripTerminalEscapes(t *testing.T) {
	for _, p := range termInjections {
		if got := displayChannel(&model.Channel{DisplayName: "town" + p}); hasTermControl(got) {
			t.Errorf("channel display name kept %q: %q", p, got)
		}
		if got := displayChannel(&model.Channel{Name: "town" + p}); hasTermControl(got) {
			t.Errorf("channel name kept %q: %q", p, got)
		}
		if got := displayTeam(&model.Team{DisplayName: "team" + p}); hasTermControl(got) {
			t.Errorf("team display name kept %q: %q", p, got)
		}
	}
}

func TestNormalizeFilenameStripsTerminalEscapes(t *testing.T) {
	for _, p := range termInjections {
		if got := normalizeFilename("report" + p + ".pdf"); hasTermControl(got) {
			t.Errorf("filename kept a control char from %q: %q", p, got)
		}
	}
	// The zero-width behaviour it already had is unchanged.
	if got := normalizeFilename("Scherm­afbeelding"); got != "Schermafbeelding" {
		t.Fatalf("zero-width strip regressed: %q", got)
	}
}

func TestRenderEmojiGlyphStripsTerminalEscapes(t *testing.T) {
	var m Model
	for _, p := range termInjections {
		if got := m.renderEmojiGlyph("shrug" + p); hasTermControl(got) {
			t.Errorf("emoji name kept a control char from %q: %q", p, got)
		}
	}
}

func TestPostSummaryStripsTerminalEscapes(t *testing.T) {
	for _, p := range termInjections {
		if got := postSummary(&model.Post{Message: "hi " + p}); hasTermControl(got) {
			t.Errorf("postSummary kept a control char from %q: %q", p, got)
		}
	}
}
