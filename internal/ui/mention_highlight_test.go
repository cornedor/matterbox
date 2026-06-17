package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestRenderInlineMention(t *testing.T) {
	got := renderInline("hello @alice there", nil, nil, "alice")
	if !strings.Contains(got, mentionStyle.Render("@alice")) {
		t.Errorf("expected mention to be styled, got %q", got)
	}
	if plain := ansi.Strip(got); plain != "hello @alice there" {
		t.Errorf("visible text changed: %q", plain)
	}
}

func TestRenderInlineNoFalseMention(t *testing.T) {
	got := renderInline("email@alice.com", nil, nil, "alice")
	if strings.Contains(got, mentionStyle.Render("@alice")) {
		t.Errorf("expected no mention styling in email, got %q", got)
	}
}

func TestRenderMarkdownMention(t *testing.T) {
	got := renderMarkdown("hey @alice", nil, nil, "alice")
	if !strings.Contains(got, mentionStyle.Render("@alice")) {
		t.Errorf("expected mention in markdown, got %q", got)
	}
}

func TestStyleMentions(t *testing.T) {
	got := styleMentions("hello @alice there", "alice", lipgloss.NewStyle())
	if !strings.Contains(got, mentionStyle.Render("@alice")) {
		t.Errorf("expected mention styled, got %q", got)
	}
}
