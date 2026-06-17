package ui

import (
	"reflect"
	"testing"
)

func TestExtractCodeBlocks(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want []codeBlock
	}{
		{
			name: "none",
			msg:  "just a plain message\nwith two lines",
			want: nil,
		},
		{
			name: "one with language",
			msg:  "see this:\n```go\nfunc main() {}\n```\nthanks",
			want: []codeBlock{{lang: "go", content: "func main() {}"}},
		},
		{
			name: "one without language",
			msg:  "```\nplain code\n```",
			want: []codeBlock{{lang: "", content: "plain code"}},
		},
		{
			name: "multiple",
			msg:  "```go\na()\n```\nand\n```sh\nls -l\n```",
			want: []codeBlock{
				{lang: "go", content: "a()"},
				{lang: "sh", content: "ls -l"},
			},
		},
		{
			name: "multiline content preserved",
			msg:  "```py\nx = 1\ny = 2\n```",
			want: []codeBlock{{lang: "py", content: "x = 1\ny = 2"}},
		},
		{
			name: "unclosed fence still yields a block",
			msg:  "```go\nfunc half() {",
			want: []codeBlock{{lang: "go", content: "func half() {"}},
		},
		{
			name: "empty block",
			msg:  "```\n```",
			want: []codeBlock{{lang: "", content: ""}},
		},
		{
			name: "indented fence",
			msg:  "  ```go\n  indented\n  ```",
			want: []codeBlock{{lang: "go", content: "  indented"}},
		},
		{
			name: "tilde fence",
			msg:  "~~~\nplain code\n~~~",
			want: []codeBlock{{lang: "", content: "plain code"}},
		},
		{
			name: "tilde fence with language",
			msg:  "~~~js\nconsole.log(1)\n~~~",
			want: []codeBlock{{lang: "js", content: "console.log(1)"}},
		},
		{
			name: "backticks inside tilde fence stay content",
			msg:  "~~~\n```\nstill code\n```\n~~~",
			want: []codeBlock{{lang: "", content: "```\nstill code\n```"}},
		},
		{
			name: "indented code block after blank line",
			msg:  "intro\n\n    console.log(1)\n    alert(2)",
			want: []codeBlock{{lang: "", content: "console.log(1)\nalert(2)"}},
		},
		{
			name: "indented code keeps extra indent as content",
			msg:  "intro\n\n     deeper",
			want: []codeBlock{{lang: "", content: " deeper"}},
		},
		{
			name: "tab indented code block",
			msg:  "intro\n\n\tcode line",
			want: []codeBlock{{lang: "", content: "code line"}},
		},
		{
			name: "interior blank kept, trailing blank dropped",
			msg:  "intro\n\n    a\n\n    b\n\nafter",
			want: []codeBlock{{lang: "", content: "a\n\nb"}},
		},
		{
			name: "indentation that interrupts a paragraph is not code",
			msg:  "a paragraph line\n    not code",
			want: nil,
		},
		{
			name: "fence and indented block together",
			msg:  "```go\nfenced\n```\n\n    indented",
			want: []codeBlock{
				{lang: "go", content: "fenced"},
				{lang: "", content: "indented"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCodeBlocks(tt.msg)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("extractCodeBlocks(%q) = %#v, want %#v", tt.msg, got, tt.want)
			}
		})
	}
}
