package cli

import "testing"

func TestExtractToken(t *testing.T) {
	const tok = "abc123def456ghi789jkl012mn" // 26-char-ish session token shape

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"mmauth link", "mmauth://callback?MMAUTHTOKEN=" + tok + "&MMCSRF=xyz&srv=https://mm.example.com", tok},
		{"mmauth link, token last", "mmauth://callback?srv=https://mm.example.com&MMAUTHTOKEN=" + tok, tok},
		{"link with whitespace", "  mmauth://callback?MMAUTHTOKEN=" + tok + "  ", tok},
		{"quoted link", `"mmauth://callback?MMAUTHTOKEN=` + tok + `"`, tok},
		{"raw token", tok, tok},
		{"raw token padded", "  " + tok + "\n", tok},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"link without token param", "mmauth://callback?MMCSRF=xyz", ""},
		{"url-shaped garbage", "https://mm.example.com/login", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractToken(c.in); got != c.want {
				t.Errorf("extractToken(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	if got := fingerprint("abcdef1234567890wxyz"); got != "abcdef…wxyz" {
		t.Errorf("fingerprint long = %q", got)
	}
	// Short tokens are fully masked rather than revealing head/tail.
	if got := fingerprint("short"); got != "•••••" {
		t.Errorf("fingerprint short = %q", got)
	}
}
