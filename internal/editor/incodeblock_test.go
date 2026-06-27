package editor

import "testing"

func TestInCodeBlock(t *testing.T) {
	cases := []struct {
		name string
		text string // caret goes to the end of this text
		want bool
	}{
		{"plain", "hello world", false},
		{"after open fence", "```\n", true},
		{"after open fence with lang", "```go\n", true},
		{"inside fenced content", "```\nfoo\n", true},
		{"after close fence", "```\nfoo\n```\n", false},
		{"tilde fence", "~~~\nfoo\n", true},
		{"backtick fence not closed by tilde", "```\n~~~\n", true},
		{"indented code after blank", "intro\n\n    code\n    ", true},
		{"inline code is not a block", "`x` ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New()
			m.SetValue(tc.text)
			m.CursorEnd()
			if got := m.InCodeBlock(); got != tc.want {
				t.Fatalf("InCodeBlock(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
