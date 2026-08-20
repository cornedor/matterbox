package gitlab

import (
	"regexp"
	"strconv"

	"matterbox/internal/forge"
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
func Refs(text, baseURL string) []forge.Ref {
	baseHost := forge.HostOf(baseURL)
	var out []forge.Ref

	for _, m := range mrURLRe.FindAllStringSubmatchIndex(text, -1) {
		host := text[m[2]:m[3]]
		project := trimSlashes(text[m[4]:m[5]])
		iid, err := strconv.Atoi(text[m[6]:m[7]])
		if err != nil || project == "" {
			continue
		}
		if forge.HostMatches(host, baseHost) {
			out = append(out, forge.Ref{Repo: project, Number: iid, Pos: m[0]})
		}
	}

	for _, m := range mrRefRe.FindAllStringSubmatchIndex(text, -1) {
		project := text[m[2]:m[3]]
		iid, err := strconv.Atoi(text[m[4]:m[5]])
		if err != nil {
			continue
		}
		out = append(out, forge.Ref{Repo: project, Number: iid, Pos: m[2]})
	}

	return forge.DedupeRefs(out)
}

// trimSlashes drops leading/trailing "/" from a captured project path.
func trimSlashes(s string) string {
	for len(s) > 0 && s[0] == '/' {
		s = s[1:]
	}
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
