package jira

import (
	"net/url"
	"regexp"
	"strings"
)

// issueKeyRe matches a Jira issue key: an uppercase project key (a letter then
// letters/digits) and a number, e.g. ABC-123 or BTPWA-2316. The surrounding
// boundaries are enforced by the callers (URL path / word split) so the bare
// pattern stays simple.
var issueKeyRe = regexp.MustCompile(`[A-Z][A-Z0-9]+-[0-9]+`)

// browseURLRe pulls the key out of a /browse/KEY link. Host matching is done
// separately against the configured base URL.
var browseURLRe = regexp.MustCompile(`https?://([^/\s]+)/browse/([A-Z][A-Z0-9]+-[0-9]+)`)

// bareKeyRe is the strict, anchored form used to validate a whitespace-split
// token as a bare issue ID (so "ABC-123." or "(ABC-123)" still match after the
// caller trims punctuation, but "ABC-123-foo" does not).
var bareKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9]+-[0-9]+$`)

// bareKeySearchRe is the unanchored form used to find bare issue IDs anywhere
// in text (including inside markdown formatting like **ABC-123** or `ABC-123`).
// Word boundaries prevent matching inside longer tokens like "ABC-123foo".
var bareKeySearchRe = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-[0-9]+\b`)

// IssueRef is a detected Jira issue key with its byte offset in the source
// text, so the UI can order references by first appearance.
type IssueRef struct {
	Key string
	Pos int
}

// Refs extracts the Jira issue keys named in text, in order of first
// appearance, deduplicated. Two forms are detected:
//
//   - A /browse/KEY URL whose host matches baseURL's host, or any
//     *.atlassian.net host — always safe, since the path is unambiguous.
//   - A bare KEY (ABC-123) in the text, but ONLY when its project key is in
//     projects. With an empty allowlist no bare keys match, which keeps
//     everyday strings like "UTF-8" or "COVID-19" from opening the panel.
//
// baseURL may be empty (then only *.atlassian.net URLs and allowlisted bare
// keys match).
func Refs(text, baseURL string, projects []string) []IssueRef {
	var refs []IssueRef
	seen := map[string]bool{}
	add := func(k string, pos int) {
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		refs = append(refs, IssueRef{Key: k, Pos: pos})
	}

	baseHost := hostOf(baseURL)

	// URL form first so a linked key is captured before the bare-key scan sees
	// the same token inside the URL.
	for _, m := range browseURLRe.FindAllStringSubmatchIndex(text, -1) {
		// browseURLRe has 2 capture groups → 3 pairs (6 indices).
		if len(m) < 6 {
			continue
		}
		host := text[m[2]:m[3]]
		key := text[m[4]:m[5]]
		if hostMatches(host, baseHost) {
			add(key, m[0])
		}
	}

	// Bare keys, gated by the project allowlist.
	allow := projectSet(projects)
	if len(allow) > 0 {
		for _, m := range bareKeySearchRe.FindAllStringIndex(text, -1) {
			tok := text[m[0]:m[1]]
			proj := tok[:strings.IndexByte(tok, '-')]
			if allow[strings.ToUpper(proj)] {
				add(tok, m[0])
			}
		}
	}
	return refs
}

// hostOf returns the lowercased host of a URL, or "" if it doesn't parse to one.
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Host)
}

// hostMatches reports whether a URL host should be treated as the configured
// Jira instance: an exact match to baseHost, or any atlassian.net subdomain
// (Cloud) so a link to the instance is recognised even before base_url is set.
func hostMatches(host, baseHost string) bool {
	host = strings.ToLower(host)
	if baseHost != "" && host == baseHost {
		return true
	}
	return strings.HasSuffix(host, ".atlassian.net")
}

// projectSet builds an upper-cased lookup of the allowlist.
func projectSet(projects []string) map[string]bool {
	if len(projects) == 0 {
		return nil
	}
	set := make(map[string]bool, len(projects))
	for _, p := range projects {
		if p = strings.TrimSpace(p); p != "" {
			set[strings.ToUpper(p)] = true
		}
	}
	return set
}
