// Package mmauth handles the mmauth:// SSO redirect that Mattermost's GitLab
// mobile-login flow uses to hand a session token back to a native client:
// building the login URL, extracting the token from the callback link, and — on
// Linux — auto-capturing the token via an OS scheme handler so the user needn't
// copy-paste. Shared by `matterbox login` and the welcome setup wizard.
package mmauth

import (
	"net/url"
	"strings"
)

// Redirect is the redirect_to handed to Mattermost's mobile-login endpoint.
// "mmauth://" is in the server's default AppCustomURLSchemes, so the endpoint
// accepts it with no server-side configuration. After SSO the server bounces
// the browser to mmauth://callback?MMAUTHTOKEN=…, which is captured either via
// the OS scheme handler (Linux) or by the user pasting the link.
const Redirect = "mmauth://callback"

// LoginURL builds the GitLab SSO mobile-login URL for the given server (a
// trailing slash is trimmed). The caller must pass a real, non-empty server.
func LoginURL(server string) string {
	return strings.TrimRight(server, "/") +
		"/oauth/gitlab/mobile_login?redirect_to=" + url.QueryEscape(Redirect)
}

// ExtractToken pulls the session token out of whatever was pasted or captured:
// an mmauth://callback?MMAUTHTOKEN=… link, or a bare token. A URL without the
// MMAUTHTOKEN param (or any other multi-segment string) is rejected rather than
// mistaken for a token.
func ExtractToken(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	if s == "" {
		return ""
	}
	if u, err := url.Parse(s); err == nil {
		if t := strings.TrimSpace(u.Query().Get("MMAUTHTOKEN")); t != "" {
			return t
		}
	}
	// Not an mmauth:// link carrying the token → only accept it as a raw token
	// if it looks like one (no URL/whitespace punctuation).
	if strings.ContainsAny(s, " \t/?&#") {
		return ""
	}
	return s
}
