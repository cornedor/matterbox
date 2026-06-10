package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// urls extracts the url field of every openable that carries one, in
// order, so tests can assert on extraction + dedup without caring about
// the file-backed entries.
func urls(opens []openable) []string {
	var out []string
	for _, o := range opens {
		if o.file == nil {
			out = append(out, o.url)
		}
	}
	return out
}

func TestCollectOpenablesLinksAndURLs(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want []string
	}{
		{
			name: "image",
			msg:  "look ![a cat](https://ex.com/cat.png)",
			want: []string{"https://ex.com/cat.png"},
		},
		{
			name: "markdown link",
			msg:  "see [the docs](https://ex.com/docs)",
			want: []string{"https://ex.com/docs"},
		},
		{
			name: "bare url with trailing punctuation",
			msg:  "ship it at https://ex.com/ok.",
			want: []string{"https://ex.com/ok"},
		},
		{
			name: "image is not duplicated by the link pattern",
			msg:  "![alt](https://ex.com/x.png)",
			want: []string{"https://ex.com/x.png"},
		},
		{
			name: "link is not duplicated by the bare-url scan",
			msg:  "[docs](https://ex.com/docs)",
			want: []string{"https://ex.com/docs"},
		},
		{
			name: "mixed, in message order, deduped",
			msg:  "![img](https://ex.com/i.png) and [docs](https://ex.com/d) plus https://ex.com/bare",
			want: []string{"https://ex.com/i.png", "https://ex.com/d", "https://ex.com/bare"},
		},
		{
			name: "no targets",
			msg:  "just some plain text",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := urls(collectOpenables(&model.Post{Message: tc.msg}))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("at %d: got %q, want %q (full: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

func TestCollectOpenablesFilesFirstThenLinks(t *testing.T) {
	p := &model.Post{
		Message: "here: https://ex.com/link",
		Metadata: &model.PostMetadata{
			Files: []*model.FileInfo{{Id: "f1", Name: "report.pdf"}},
		},
	}
	opens := collectOpenables(p)
	if len(opens) != 2 {
		t.Fatalf("want 2 openables, got %d: %+v", len(opens), opens)
	}
	if opens[0].file == nil || opens[0].name != "report.pdf" {
		t.Fatalf("first openable should be the attachment, got %+v", opens[0])
	}
	if opens[1].file != nil || opens[1].url != "https://ex.com/link" {
		t.Fatalf("second openable should be the link, got %+v", opens[1])
	}
}
