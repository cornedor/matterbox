// Package github detects GitHub issue and pull-request references in message
// text so the UI can open a side panel. Like jira/gitlab detectors it is
// dependency-free and unit-testable.
package github

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// URL form: https://host/owner/repo/issues/123 or /pull/123.
// host is matched separately against the configured base_url (instance).
var issuePullURLRe = regexp.MustCompile(`https?://([^/\s]+)/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/(issues|pull)/(\d+)\b`)

// Ref is a detected GitHub issue or pull-request reference with its byte
// offset in the source text, so the UI can order refs across providers by
// first appearance.
type Ref struct {
	Repo   string // owner/repo
	Number int
	IsPull bool
	Pos    int
}

// Refs extracts GitHub issue / PR references named in text, in order of first
// appearance, deduplicated.
func Refs(text, baseURL string) []Ref {
	baseHost := hostOf(baseURL)
	if baseHost == "" {
		return nil
	}

	seen := map[string]int{} // canonical key -> index into out
	var out []Ref

	add := func(repo string, number int, isPull bool, pos int) {
		key := strings.ToLower(repo) + "#" + strconv.Itoa(number)
		if i, ok := seen[key]; ok {
			if pos < out[i].Pos {
				out[i].Pos = pos
			}
			return
		}
		seen[key] = len(out)
		out = append(out, Ref{
			Repo:   repo,
			Number: number,
			IsPull: isPull,
			Pos:    pos,
		})
	}

	for _, m := range issuePullURLRe.FindAllStringSubmatchIndex(text, -1) {
		// capture groups:
		// 1: host, 2: repo, 3: issues/pull, 4: number
		if len(m) < 10 {
			continue
		}
		host := text[m[2]:m[3]]
		repo := text[m[4]:m[5]]
		kind := text[m[6]:m[7]]
		nstr := text[m[8]:m[9]]

		if !hostMatches(host, baseHost) {
			continue
		}

		n, err := strconv.Atoi(nstr)
		if err != nil {
			continue
		}
		add(repo, n, kind == "pull", m[0])
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Pos < out[j].Pos })
	return out
}

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

func hostMatches(host, baseHost string) bool {
	return baseHost != "" && strings.ToLower(host) == baseHost
}
