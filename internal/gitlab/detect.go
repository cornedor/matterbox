package gitlab

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// mrURLRe pulls the project path and MR iid out of a merge-request link:
// https://host/group/.../project/-/merge_requests/123. The host is matched
// separately against the configured instance; the project path (group 2) is
// the non-greedy run up to the "/-/merge_requests/" separator, so nested
// groups (a/b/project) are captured whole.
var mrURLRe = regexp.MustCompile(`https?://([^/\s]+)/(\S+?)/-/merge_requests/(\d+)`)

// mrRefRe matches a short reference group/project!123 (GitLab's own !iid form,
// qualified with the project path). The leading boundary keeps it from firing
// mid-token; the path needs at least two "/"-separated segments so a bare
// "foo!3" (excitement, not a ref) doesn't match. Project paths are lowercase
// slugs but uppercase is tolerated. Used with FindAllStringSubmatchIndex so the
// project submatch start gives the appearance offset.
var mrRefRe = regexp.MustCompile(`(?:^|[\s(\[<])([a-zA-Z0-9][a-zA-Z0-9._-]*(?:/[a-zA-Z0-9._-]+)+)!(\d+)\b`)

// Ref is a detected merge-request reference: the project path, the iid, and the
// byte offset of its first appearance (so the UI can order refs across
// providers by where they appear in the message).
type Ref struct {
	Project string
	IID     int
	Pos     int
}

// String renders the canonical short form, group/project!iid.
func (r Ref) String() string { return fmt.Sprintf("%s!%d", r.Project, r.IID) }

// Refs extracts the merge-request references named in text, in order of first
// appearance, deduplicated. Two forms are detected:
//
//   - A /-/merge_requests/N URL whose host matches baseURL's host — always
//     safe, since the path is unambiguous.
//   - A short group/project!N reference. These are inherently instance-relative
//     (no host in the text), so they're always taken to mean the configured
//     instance; the path's required "/" guards against everyday "word!3".
//
// baseURL gives the instance host that URL links must match; an empty baseURL
// matches no URLs (but short refs still resolve).
func Refs(text, baseURL string) []Ref {
	baseHost := hostOf(baseURL)
	seen := map[string]int{} // canonical key → index into out
	var out []Ref
	add := func(project string, iid, pos int) {
		key := strings.ToLower(project) + "!" + strconv.Itoa(iid)
		if i, ok := seen[key]; ok {
			if pos < out[i].Pos {
				out[i].Pos = pos
			}
			return
		}
		seen[key] = len(out)
		out = append(out, Ref{Project: project, IID: iid, Pos: pos})
	}

	for _, m := range mrURLRe.FindAllStringSubmatchIndex(text, -1) {
		host := text[m[2]:m[3]]
		project := strings.Trim(text[m[4]:m[5]], "/")
		iid, err := strconv.Atoi(text[m[6]:m[7]])
		if err != nil || project == "" {
			continue
		}
		if hostMatches(host, baseHost) {
			add(project, iid, m[0])
		}
	}

	for _, m := range mrRefRe.FindAllStringSubmatchIndex(text, -1) {
		project := text[m[2]:m[3]]
		iid, err := strconv.Atoi(text[m[4]:m[5]])
		if err != nil {
			continue
		}
		add(project, iid, m[2])
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Pos < out[j].Pos })
	return out
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

// hostMatches reports whether a URL host is the configured instance (exact,
// case-insensitive). Unlike Jira there's no cloud-wildcard fallback — a
// self-hosted instance has no shared suffix to recognise.
func hostMatches(host, baseHost string) bool {
	return baseHost != "" && strings.ToLower(host) == baseHost
}
