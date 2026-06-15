package ui

import "testing"

func TestAISearchQuery(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"the new cms acme uses?", "the new cms acme uses?", true},
		{"  what is storyblok ?  ", "what is storyblok ?", true},
		{"plain search", "", false},
		{"?", "", false},       // no text before ?
		{"in:foo?", "", false}, // only a modifier
		{"team:bar what changed?", "team:bar what changed?", true},
	}
	for _, tc := range cases {
		got, ok := aiSearchQuery(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("aiSearchQuery(%q) = (%q, %v); want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}
