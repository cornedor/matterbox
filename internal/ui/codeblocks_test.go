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
