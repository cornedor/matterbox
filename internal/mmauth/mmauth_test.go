package mmauth

import (
	"net/url"
	"strings"
	"testing"
)

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
			if got := ExtractToken(c.in); got != c.want {
				t.Errorf("ExtractToken(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestLoginURL(t *testing.T) {
	for _, server := range []string{"https://mm.example.com", "https://mm.example.com/"} {
		got := LoginURL(server)
		want := "https://mm.example.com/oauth/gitlab/mobile_login?redirect_to=" + url.QueryEscape(Redirect)
		if got != want {
			t.Errorf("LoginURL(%q) = %q, want %q", server, got, want)
		}
		if !strings.Contains(got, "mmauth%3A%2F%2Fcallback") {
			t.Errorf("LoginURL(%q) didn't url-encode the mmauth:// redirect: %q", server, got)
		}
	}
}
