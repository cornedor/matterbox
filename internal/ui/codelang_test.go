package ui

import (
	"testing"

	"matterbox/internal/editor"

	"github.com/alecthomas/chroma/v2/lexers"
)

func TestLangIndexResolvable(t *testing.T) {
	idx := langIndex()
	if len(idx) == 0 {
		t.Fatal("langIndex returned no languages")
	}
	// Every offered tag must resolve to a lexer, so the message pane actually
	// highlights what the picker inserts.
	for _, name := range idx {
		if lexers.Get(name) == nil {
			t.Errorf("offered tag %q does not resolve to a lexer", name)
		}
	}
	// The common chat languages are present.
	want := []string{"go", "python", "javascript", "json", "bash", "sql"}
	have := map[string]bool{}
	for _, n := range idx {
		have[n] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("expected %q in langIndex", w)
		}
	}
}

func TestLangMatches(t *testing.T) {
	// A prefix query surfaces its language and ranks it first.
	got := langMatches("pyth")
	if len(got) == 0 || got[0] != "python" {
		t.Errorf("pyth: want python first, got %v", got)
	}

	// Popularity breaks ties within a band: "go" beats alphabetically-earlier
	// prefix matches like "gas" / "genshin".
	got = langMatches("g")
	if len(got) == 0 || got[0] != "go" {
		t.Errorf("g: want go first (popularity), got %v", got)
	}

	// Capped at langLimit.
	if got := langMatches("a"); len(got) > langLimit {
		t.Errorf("a: returned %d results, want <= %d", len(got), langLimit)
	}

	// An empty query lists popular tags so the picker is useful right after ```.
	if got := langMatches(""); len(got) == 0 {
		t.Error("empty query returned no results, want popular tags")
	}

	// Garbage yields nothing.
	if got := langMatches("zznotalanguage"); len(got) != 0 {
		t.Errorf("garbage query returned %d results, want 0", len(got))
	}
}

func TestUpdateLangTrigger(t *testing.T) {
	// SetValue parks the cursor at the end of the value, so each case tests the
	// caret sitting just past the typed text.
	cases := []struct {
		text string
		want bool
		desc string
	}{
		{"```go", true, "opening fence with a language"},
		{"```", false, "bare opening fence stays quiet until a letter is typed"},
		{"  ```g", true, "fence indented up to 3 spaces still opens"},
		{"``go", false, "only two backticks is not a fence"},
		{"`code`", false, "inline code is not a fence"},
		{"    ```go", false, "four-space indent is code, not a fence"},
		{"text ```go", false, "fence must start the line"},
		{"``` go", false, "space before the tag is not a bare tag"},
		{"```go\ncode\n```", false, "closing fence carries no language"},
	}
	for _, tc := range cases {
		var m Model
		m.input = editor.New()
		m.input.SetWidth(40)
		m.input.SetValue(tc.text)
		m.updateLang()
		if m.lang.active != tc.want {
			t.Errorf("updateLang(%q) [%s]: active = %v, want %v", tc.text, tc.desc, m.lang.active, tc.want)
		}
	}
}

func TestAcceptLang(t *testing.T) {
	var m Model
	m.input = editor.New()
	m.input.SetWidth(40)
	m.input.SetValue("```pyth")
	m.updateLang()
	if !m.lang.active {
		t.Fatal("picker did not open for ```pyth")
	}
	if m.lang.items[m.lang.idx] != "python" {
		t.Fatalf("top candidate = %q, want python", m.lang.items[m.lang.idx])
	}
	if _, ok := m.acceptLang(); !ok {
		t.Fatal("acceptLang returned ok=false")
	}
	if got := m.input.Value(); got != "```python" {
		t.Errorf("after accept value = %q, want ```python", got)
	}
	// Caret parks just past the inserted tag, not at the buffer end (same here),
	// ready for the code on the next line.
	if off := m.input.CursorOffset(); off != len([]rune("```python")) {
		t.Errorf("cursor offset = %d, want %d", off, len([]rune("```python")))
	}
	if m.lang.active {
		t.Error("picker stayed active after accept")
	}
}
