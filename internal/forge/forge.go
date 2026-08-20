// Package forge is the provider-neutral half of the change-request side panel:
// the render-ready types the UI draws and the Provider interface each forge
// implements. A "change request" is whatever the forge calls the thing you open
// to get a branch merged — a GitLab merge request, a GitHub pull request, a
// Forgejo/Codeberg pull request — and the panel treats them alike.
//
// The UI holds a slice of Providers and never names a forge directly: it asks
// each one which references a message carries (Refs), fetches the ones the user
// opens (Get), and draws whatever comes back with one renderer. Adding a forge
// means adding a subpackage that satisfies Provider — no UI change.
//
// Subpackages own the wire format: internal/forge/gitlab, internal/forge/github.
// Both flatten their API into the types here, including normalizing check/job
// status onto the small vocabulary below, so the renderer has one set of glyphs
// to know about rather than one per forge.
package forge

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The change request's lifecycle state. The vocabulary is GitLab's (every forge
// maps onto it): a GitHub pull request that is closed-and-merged reports
// StateMerged, not StateClosed.
const (
	StateOpen   = "opened"
	StateMerged = "merged"
	StateClosed = "closed"
	StateLocked = "locked"
)

// The job/check status vocabulary. Providers map their own tokens onto these, so
// the UI has one glyph table and one severity ranking. Anything a provider can't
// classify becomes StatusPending — surfaced as in-progress rather than silently
// reported as success.
const (
	StatusSuccess  = "success"
	StatusFailed   = "failed"
	StatusRunning  = "running"
	StatusPending  = "pending"
	StatusCanceled = "canceled"
	StatusSkipped  = "skipped"
	StatusManual   = "manual"
)

// Provider is one forge the panel can read change requests from: a GitLab
// install, github.com or an Enterprise host, a Forgejo instance. Implementations
// are safe for concurrent use — the UI fetches from a background goroutine.
type Provider interface {
	// Name is the forge's display name ("GitLab", "GitHub"), used as the panel
	// title and in error text.
	Name() string
	// Noun is what this forge calls a change request ("merge request", "pull
	// request"), used in action errors so the panel speaks the user's dialect.
	Noun() string
	// ItemNouns is the phrase used in "nothing found" status text for this
	// forge ("merge request", "issue / pull request"). Distinct from Noun when
	// the forge opens more than one kind of numbered item.
	ItemNouns() string
	// Sigil is the separator in the forge's own short reference form: "!" for
	// GitLab (group/project!12), "#" for GitHub (owner/repo#12). Label builds
	// the full short form from it.
	Sigil() string
	// ChecksHeading labels the CI section in the panel with the forge's own word
	// for it — "Pipeline" on GitLab, "Checks" on GitHub.
	ChecksHeading() string
	// Enabled reports whether the provider has enough configuration to fetch.
	// A disabled provider is skipped entirely — never asked for Refs.
	Enabled() bool
	// AutoFetch reports whether the UI may fetch from this forge on its own, for
	// the inline badges it draws beside every change-request link on screen.
	// False when the forge is readable but on a budget too small to spend
	// unasked (anonymous GitHub: 60 requests an hour), leaving those fetches to
	// a keypress. A provider that can fetch at all usually returns Enabled().
	AutoFetch() bool
	// BaseURL is the configured instance root (no trailing slash), for
	// recognising links that point at this forge.
	BaseURL() string
	// Refs extracts the change requests named in text, in order of first
	// appearance, deduplicated.
	Refs(text string) []Ref
	// WebURL is the human page for a change request — what the browser key opens.
	WebURL(repo string, number int) string
	// Get returns the change request, serving a cached copy when it has one.
	// Kind is a detect-time hint from Ref.Kind (KindPull, KindIssue, or empty
	// when the form was ambiguous). Providers that only have one kind ignore it.
	Get(ctx context.Context, repo string, number int, kind string) (*Change, error)
	// Invalidate drops any cached copy so the next Get refetches.
	Invalidate(repo string, number int)
	// Approve records an approval (a GitLab approve, a GitHub approving review).
	Approve(ctx context.Context, repo string, number int) error
	// MergeMethods are the merge strategies the forge offers, in menu order. A
	// forge with a single way to merge returns exactly one entry, and the UI
	// asks a plain yes/no rather than offering a choice.
	MergeMethods() []MergeMethod
	// Merge merges the change request with one of MergeMethods' ids.
	Merge(ctx context.Context, repo string, number int, method string) error
}

// MergeMethod is one way a forge will merge a change request. ID is the wire
// value handed back to Merge (empty when the forge takes no method parameter);
// Key is the letter that picks it in the confirm modal.
type MergeMethod struct {
	ID    string
	Label string
	Key   string
}

// Ref is a detected change-request reference: the repository it lives in (a
// GitLab project path, a GitHub owner/repo), its number, and the byte offset of
// its first appearance — so refs from several forges (and Jira issues) can be
// ordered by where they appear in the message.
//
// Kind is optional detect-time knowledge: KindPull / KindIssue when the link
// path said so, or empty for ambiguous short forms (owner/repo#N). Providers
// use it to skip an extra classify round-trip.
type Ref struct {
	Repo   string
	Number int
	Pos    int
	Kind   string // KindPull, KindIssue, or empty
}

// Kind values for Ref.Kind / Provider.Get. Empty means the detector could not
// tell (short form); the provider then probes.
const (
	KindPull  = "pull"
	KindIssue = "issue"
)

// Change is the flattened, render-ready change request (or GitHub issue).
// Description is the forge's own markdown flavour, which the UI renders with
// its shared markdown renderer (no conversion needed, unlike Jira's ADF).
type Change struct {
	Repo         string
	Number       int
	Title        string
	State        string // one of the State* constants
	Draft        bool
	Author       string
	SourceBranch string
	TargetBranch string
	Assignees    []string
	Reviewers    []string
	Labels       []string
	ChangesCount string // "44", or "44+" when the forge caps it
	// IsIssue is true for a GitHub (or similar) issue that is not a pull
	// request. GitLab merge requests always leave it false. The panel skips
	// approve/merge/CI when set.
	IsIssue bool
	// Mergeable is whether the forge would accept a merge right now — the gate
	// for offering the merge action. MergeStatus is the human phrase shown
	// either way ("mergeable", "ci still running", "conflicts").
	Mergeable    bool
	MergeStatus  string
	HasConflicts bool
	Description  string
	WebURL       string
	UpdatedAt    time.Time

	Checks    *Checks    // nil when the change request has no CI
	Approvals *Approvals // nil when approvals couldn't be read (best-effort)
}

// Checks is the CI verdict for the change request's head commit: an overall
// status plus the per-job breakdown, grouped the way the forge groups it
// (GitLab pipeline stages, GitHub check suites / status contexts).
type Checks struct {
	Status   string // normalized: one of the Status* constants
	Label    string // human label, e.g. "passed"
	WebURL   string
	Duration int // seconds, 0 when unknown
	Groups   []Group
}

// Group is a named run of jobs — one pipeline stage, one check suite.
type Group struct {
	Name string
	Jobs []Job
}

// Job is one CI job's name and normalized status.
type Job struct {
	Name   string
	Status string
}

// Approvals summarizes who has signed off. Required/Left are GitLab's approval
// rules; forges without a readable requirement leave them 0 and the UI falls
// back to listing names.
type Approvals struct {
	Approved         bool
	Required         int
	Left             int
	By               []string
	ChangesRequested []string // reviewers currently blocking (GitHub)
}

// ErrNotConfigured is what every call returns when a provider lacks the base
// URL or token it needs (Enabled reports false).
var ErrNotConfigured = errors.New("forge: not configured (need a base URL and a token)")

// Label renders a reference the way the forge's own users write it:
// group/project!12 on GitLab, owner/repo#12 on GitHub.
func Label(p Provider, repo string, number int) string {
	sigil := "#"
	if p != nil && p.Sigil() != "" {
		sigil = p.Sigil()
	}
	return repo + sigil + strconv.Itoa(number)
}

// NormalizeBaseURL trims a base URL and supplies a default https:// scheme when
// it's missing, so a bare host like "git.example.com" still parses to a host
// (url.Parse treats a scheme-less string as a path) for link recognition and
// produces working API/browse URLs.
func NormalizeBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return raw
}

// HostOf returns the lowercased host of a URL, or "" if it doesn't parse to one.
func HostOf(raw string) string {
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

// HostMatches reports whether a URL host is the configured instance (exact,
// case-insensitive). No wildcard: a self-hosted forge has no shared suffix to
// recognise, and github.com is matched by its own host like any other.
func HostMatches(host, baseHost string) bool {
	return baseHost != "" && strings.ToLower(host) == baseHost
}

// Cache memoises fetched change requests per provider, keyed case-insensitively
// by repo and number. The zero value is ready to use.
type Cache struct {
	mu sync.Mutex
	m  map[string]*Change
}

func cacheKey(repo string, number int) string {
	return strings.ToLower(repo) + "#" + strconv.Itoa(number)
}

// Get returns the cached change request, if any.
func (c *Cache) Get(repo string, number int) (*Change, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.m[cacheKey(repo, number)]
	return ch, ok
}

// Put stores a fetched change request.
func (c *Cache) Put(repo string, number int, ch *Change) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*Change{}
	}
	c.m[cacheKey(repo, number)] = ch
}

// Invalidate drops one entry so the next fetch goes to the forge.
func (c *Cache) Invalidate(repo string, number int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, cacheKey(repo, number))
}

// DedupeRefs collapses duplicate references, keeping the earliest position of
// each, and returns them in appearance order. A message can name the same change
// request twice — once as a URL, once as a short ref — and both detection passes
// feed this.
func DedupeRefs(refs []Ref) []Ref {
	seen := map[string]int{} // canonical key → index into out
	var out []Ref
	for _, r := range refs {
		key := cacheKey(r.Repo, r.Number)
		if i, ok := seen[key]; ok {
			if r.Pos < out[i].Pos {
				out[i].Pos = r.Pos
			}
			// A URL that names the kind wins over an ambiguous short form.
			if out[i].Kind == "" && r.Kind != "" {
				out[i].Kind = r.Kind
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, r)
	}
	sortByPos(out)
	return out
}

// sortByPos is a stable insertion sort on Pos — the refs on one message number
// in the handful, so this beats reaching for the sort package.
func sortByPos(refs []Ref) {
	for i := 1; i < len(refs); i++ {
		for j := i; j > 0 && refs[j].Pos < refs[j-1].Pos; j-- {
			refs[j], refs[j-1] = refs[j-1], refs[j]
		}
	}
}
