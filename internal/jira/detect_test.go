package jira

import (
	"reflect"
	"testing"
)

func issueRefKeys(refs []IssueRef) []string {
	if len(refs) == 0 {
		return nil
	}
	keys := make([]string, len(refs))
	for i, r := range refs {
		keys[i] = r.Key
	}
	return keys
}

func TestRefs(t *testing.T) {
	const base = "https://example.atlassian.net"
	tests := []struct {
		name     string
		text     string
		projects []string
		wantKeys []string
	}{
		{
			name:     "browse url always matches",
			text:     "see https://example.atlassian.net/browse/ABC-123 for details",
			wantKeys: []string{"ABC-123"},
		},
		{
			name:     "browse url on any atlassian host",
			text:     "https://other.atlassian.net/browse/XY-9",
			wantKeys: []string{"XY-9"},
		},
		{
			name:     "bare key needs allowlist",
			text:     "fixed in ABC-123 today",
			wantKeys: nil,
		},
		{
			name:     "bare key with allowlist",
			text:     "fixed in ABC-123 today",
			projects: []string{"ABC"},
			wantKeys: []string{"ABC-123"},
		},
		{
			name:     "bare key case-insensitive project allowlist",
			text:     "ABC-7 and DEF-8",
			projects: []string{"abc"},
			wantKeys: []string{"ABC-7"},
		},
		{
			name:     "allowlist is the only gate for bare keys",
			text:     "UTF-8 COVID-19 ISO-8601",
			projects: []string{"UTF", "COVID", "ISO"},
			// These DO match once their prefixes are allowlisted — the gate is
			// the allowlist, not a denylist. So a user only lists real project
			// keys, and unlisted look-alikes (the common case) never trigger.
			wantKeys: []string{"UTF-8", "COVID-19", "ISO-8601"},
		},
		{
			name:     "single-letter prefix is not a valid key",
			text:     "H-2 is not a jira key",
			projects: []string{"H"},
			// Jira project keys are at least two characters, so H-2 can't match
			// even when allowlisted — one more guard against stray "X-1" tokens.
			wantKeys: nil,
		},
		{
			name:     "trailing punctuation trimmed",
			text:     "(ABC-123) and ABC-124.",
			projects: []string{"ABC"},
			wantKeys: []string{"ABC-123", "ABC-124"},
		},
		{
			name:     "dedup across url and bare",
			text:     "https://example.atlassian.net/browse/ABC-1 and again ABC-1",
			projects: []string{"ABC"},
			wantKeys: []string{"ABC-1"},
		},
		{
			name:     "non-atlassian browse url ignored",
			text:     "https://evil.example.com/browse/ABC-123",
			wantKeys: nil,
		},
		{
			name:     "order of appearance preserved",
			text:     "DEF-2 then ABC-1",
			projects: []string{"ABC", "DEF"},
			wantKeys: []string{"DEF-2", "ABC-1"},
		},
		{
			name:     "markdown bold bare key",
			text:     "fixing **ABC-123** now",
			projects: []string{"ABC"},
			wantKeys: []string{"ABC-123"},
		},
		{
			name:     "inline code bare key",
			text:     "use `ABC-123` for tracking",
			projects: []string{"ABC"},
			wantKeys: []string{"ABC-123"},
		},
		{
			name:     "strikethrough bare key",
			text:     "~ABC-123~ is resolved",
			projects: []string{"ABC"},
			wantKeys: []string{"ABC-123"},
		},
		{
			name:     "table cell bare key",
			text:     "|ABC-123|ABC-124|",
			projects: []string{"ABC"},
			wantKeys: []string{"ABC-123", "ABC-124"},
		},
		{
			name:     "markdown link text bare key",
			text:     "see [ABC-123](https://example.com)",
			projects: []string{"ABC"},
			wantKeys: []string{"ABC-123"},
		},
		{
			name:     "colon after bare key",
			text:     "ABC-123: do this",
			projects: []string{"ABC"},
			wantKeys: []string{"ABC-123"},
		},
		{
			name:     "slash after bare key",
			text:     "ABC-123/needs-work",
			projects: []string{"ABC"},
			wantKeys: []string{"ABC-123"},
		},
		{
			name:     "not inside a longer word",
			text:     "ABC-123foo is not a key",
			projects: []string{"ABC"},
			wantKeys: nil,
		},
		{
			name:     "not inside a dashed number",
			text:     "ABC-123-456 is not a key",
			projects: []string{"ABC"},
			wantKeys: []string{"ABC-123"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Refs(tt.text, base, tt.projects)
			gotKeys := issueRefKeys(got)
			if !reflect.DeepEqual(gotKeys, tt.wantKeys) {
				t.Errorf("Refs() keys = %v, want %v", gotKeys, tt.wantKeys)
			}
		})
	}
}

func TestRefsPositions(t *testing.T) {
	const base = "https://example.atlassian.net"
	text := "see https://example.atlassian.net/browse/ABC-1 and later ABC-2"
	refs := Refs(text, base, []string{"ABC"})
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}
	if refs[0].Key != "ABC-1" || refs[0].Pos != 4 {
		t.Errorf("first ref = %+v, want Key=ABC-1 Pos=4", refs[0])
	}
	if refs[1].Key != "ABC-2" || refs[1].Pos != 57 {
		t.Errorf("second ref = %+v, want Key=ABC-2 Pos=57", refs[1])
	}
}

func TestRefsNoBaseURL(t *testing.T) {
	// With no configured base URL, atlassian.net links still resolve.
	got := Refs("https://anything.atlassian.net/browse/ZZ-9", "", nil)
	if want := []string{"ZZ-9"}; !reflect.DeepEqual(issueRefKeys(got), want) {
		t.Errorf("Refs() = %v, want %v", issueRefKeys(got), want)
	}
}
