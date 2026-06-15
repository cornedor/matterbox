package jira

import (
	"reflect"
	"testing"
)

func TestRefs(t *testing.T) {
	const base = "https://example.atlassian.net"
	tests := []struct {
		name     string
		text     string
		projects []string
		want     []string
	}{
		{
			name: "browse url always matches",
			text: "see https://example.atlassian.net/browse/ABC-123 for details",
			want: []string{"ABC-123"},
		},
		{
			name: "browse url on any atlassian host",
			text: "https://other.atlassian.net/browse/XY-9",
			want: []string{"XY-9"},
		},
		{
			name: "bare key needs allowlist",
			text: "fixed in ABC-123 today",
			want: nil,
		},
		{
			name:     "bare key with allowlist",
			text:     "fixed in ABC-123 today",
			projects: []string{"ABC"},
			want:     []string{"ABC-123"},
		},
		{
			name:     "bare key case-insensitive project allowlist",
			text:     "ABC-7 and DEF-8",
			projects: []string{"abc"},
			want:     []string{"ABC-7"},
		},
		{
			name:     "allowlist is the only gate for bare keys",
			text:     "UTF-8 COVID-19 ISO-8601",
			projects: []string{"UTF", "COVID", "ISO"},
			// These DO match once their prefixes are allowlisted — the gate is
			// the allowlist, not a denylist. So a user only lists real project
			// keys, and unlisted look-alikes (the common case) never trigger.
			want: []string{"UTF-8", "COVID-19", "ISO-8601"},
		},
		{
			name:     "single-letter prefix is not a valid key",
			text:     "H-2 is not a jira key",
			projects: []string{"H"},
			// Jira project keys are at least two characters, so H-2 can't match
			// even when allowlisted — one more guard against stray "X-1" tokens.
			want: nil,
		},
		{
			name:     "trailing punctuation trimmed",
			text:     "(ABC-123) and ABC-124.",
			projects: []string{"ABC"},
			want:     []string{"ABC-123", "ABC-124"},
		},
		{
			name:     "dedup across url and bare",
			text:     "https://example.atlassian.net/browse/ABC-1 and again ABC-1",
			projects: []string{"ABC"},
			want:     []string{"ABC-1"},
		},
		{
			name: "non-atlassian browse url ignored",
			text: "https://evil.example.com/browse/ABC-123",
			want: nil,
		},
		{
			name:     "order of appearance preserved",
			text:     "DEF-2 then ABC-1",
			projects: []string{"ABC", "DEF"},
			want:     []string{"DEF-2", "ABC-1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Refs(tt.text, base, tt.projects)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Refs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRefsNoBaseURL(t *testing.T) {
	// With no configured base URL, atlassian.net links still resolve.
	got := Refs("https://anything.atlassian.net/browse/ZZ-9", "", nil)
	if want := []string{"ZZ-9"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Refs() = %v, want %v", got, want)
	}
}
