package github

import (
	"regexp"
	"strconv"

	"matterbox/internal/forge"
)

// itemURLRe pulls owner/repo and the number out of an issue or pull-request
// link: https://host/owner/repo/issues/123 or …/pull/123 (with an optional
// /files, /commits, #comment tail). The host is matched separately against the
// configured instance. GitHub repositories are always exactly owner/repo — no
// nested groups, unlike GitLab — so both path segments are matched tightly.
var itemURLRe = regexp.MustCompile(`https?://([^/\s]+)/([A-Za-z0-9][\w.-]*)/([\w.-]+)/(issues|pull)/(\d+)`)

// itemRefRe matches a short reference owner/repo#123 — GitHub's own cross-repo
// form for both issues and pull requests. The leading boundary keeps it from
// firing mid-token. A bare "#123" is deliberately not matched: it's GitHub's
// in-repo form, but a chat message has no repo context, and "#1" appears in
// ordinary prose far too often. Exactly one "/" is required, so a GitLab-style
// group/sub/project#4 doesn't resolve here.
var itemRefRe = regexp.MustCompile(`(?:^|[\s(\[<])([A-Za-z0-9][\w.-]*/[\w.-]+)#(\d+)\b`)

// Refs extracts the issue and pull-request references named in text, in order
// of first appearance, deduplicated. Two forms are detected:
//
//   - An /issues/N or /pull/N URL whose host matches baseURL's host.
//   - A short owner/repo#N reference, taken to mean the configured instance
//     (short refs carry no host). Get decides whether the number is an issue
//     or a pull request.
//
// baseURL gives the instance host that URL links must match; an empty baseURL
// matches no URLs (but short refs still resolve).
func Refs(text, baseURL string) []forge.Ref {
	baseHost := forge.HostOf(baseURL)
	var out []forge.Ref

	for _, m := range itemURLRe.FindAllStringSubmatchIndex(text, -1) {
		host := text[m[2]:m[3]]
		repo := text[m[4]:m[5]] + "/" + text[m[6]:m[7]]
		number, err := strconv.Atoi(text[m[10]:m[11]])
		if err != nil {
			continue
		}
		if forge.HostMatches(host, baseHost) {
			out = append(out, forge.Ref{Repo: repo, Number: number, Pos: m[0]})
		}
	}

	for _, m := range itemRefRe.FindAllStringSubmatchIndex(text, -1) {
		repo := text[m[2]:m[3]]
		number, err := strconv.Atoi(text[m[4]:m[5]])
		if err != nil {
			continue
		}
		out = append(out, forge.Ref{Repo: repo, Number: number, Pos: m[2]})
	}

	return forge.DedupeRefs(out)
}
