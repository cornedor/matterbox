// Package github is the GitHub implementation of forge.Provider: it fetches
// issue and pull-request detail from github.com or a GitHub Enterprise host so
// the TUI can show it inline in the reference side panel. It is the sibling of
// internal/forge/gitlab and, like it, depends on nothing but internal/forge — so
// it unit-tests against an httptest server with no real instance.
//
// Authentication is a token in the Authorization header (a fine-grained or
// classic personal access token, or the one the gh CLI already stored). Bodies
// come back as GitHub-flavored markdown, which the UI renders with its shared
// markdown renderer.
//
// Numbers live in one namespace (/issues/{n}); Get classifies via that endpoint
// and only then loads pull-request extras (branches, checks, reviews). Two
// shapes differ from GitLab and are mapped here rather than in the UI:
//
//   - A pull request's state is open/closed, with "merged" a separate flag, so a
//     merged PR is reported as forge.StateMerged.
//   - There are no pipeline stages. Check runs are grouped by the app that
//     produced them (GitHub Actions, a coverage bot, …) and the older commit
//     statuses become one more group, which reads like stages in the panel.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"matterbox/internal/forge"
)

// DefaultBaseURL is the instance used when none is configured: github.com.
const DefaultBaseURL = "https://github.com"

// apiVersion pins the REST API's dated contract, as GitHub asks clients to.
const apiVersion = "2022-11-28"

// perPage caps the paged lists we read (check runs, reviews). One page is
// plenty for a panel that shows a handful of lines.
const perPage = 100

// Config is the subset of user config this package needs. BaseURL is the
// instance root — https://github.com, or an Enterprise host — and Token is a
// personal access token.
type Config struct {
	BaseURL string
	Token   string
}

// Client fetches and caches pull requests for one instance. The zero value is
// not usable; use New. Safe for concurrent use.
type Client struct {
	baseURL string // browse root, no trailing slash
	token   string
	rest    *forge.REST
	cache   forge.Cache
}

// Client is the GitHub half of the panel's provider set.
var _ forge.Provider = (*Client)(nil)

// New builds a Client from cfg, defaulting to github.com when no base URL is
// configured. The returned client is always non-nil; call Enabled to see
// whether it has enough configuration to actually fetch.
func New(cfg Config) *Client {
	base := forge.NormalizeBaseURL(cfg.BaseURL)
	if base == "" {
		base = DefaultBaseURL
	}
	token := strings.TrimSpace(cfg.Token)
	return &Client{
		baseURL: base,
		token:   token,
		rest: forge.NewREST(APIRoot(base), "github", func(r *http.Request) {
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			r.Header.Set("Accept", "application/vnd.github+json")
			r.Header.Set("X-GitHub-Api-Version", apiVersion)
		}),
	}
}

// APIRoot maps a browse root to its REST root. github.com serves its API from
// api.github.com; every Enterprise host serves it from /api/v3 on the same
// host. Exported so a caller can point tests (or a proxy) at one deliberately.
func APIRoot(baseURL string) string {
	base := strings.TrimRight(forge.NormalizeBaseURL(baseURL), "/")
	switch forge.HostOf(base) {
	case "github.com", "www.github.com":
		return "https://api.github.com"
	case "":
		return ""
	}
	return base + "/api/v3"
}

// Name is the forge's display name, used as the panel title.
func (c *Client) Name() string { return "GitHub" }

// Noun is what GitHub calls a mergeable change request in action errors
// ("cannot approve …: pull request is closed"). The "nothing found" line uses
// forgeNouns, which names issues too.
func (c *Client) Noun() string { return "pull request" }

// Sigil is the separator in GitHub's short reference form (owner/repo#12).
func (c *Client) Sigil() string { return "#" }

// ChecksHeading: GitHub calls them checks, whoever produced them.
func (c *Client) ChecksHeading() string { return "Checks" }

// Enabled: GitHub answers read-only requests for public repositories without
// any credentials, so the panel is offered with or without a token. What a
// token adds is private repositories, the approve/merge actions, and a rate
// limit worth having (see AutoFetch).
func (c *Client) Enabled() bool {
	return c != nil && c.baseURL != ""
}

// AutoFetch: only with a token. Anonymous GitHub allows 60 requests an hour per
// IP address and one pull request costs up to four of them, so a scroll through
// a feed full of links would spend the hour's budget on badges nobody asked for
// — and leave the panel rate-limited when someone did ask. Tokenless, fetching
// stays user-initiated.
func (c *Client) AutoFetch() bool {
	return c.Enabled() && c.token != ""
}

// BaseURL returns the configured instance root (no trailing slash). Used to
// recognise pull-request links pointing at this instance.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

// Refs extracts the issues and pull requests named in text, resolved against
// this instance. See detect.go for the forms recognised.
func (c *Client) Refs(text string) []forge.Ref {
	return Refs(text, c.BaseURL())
}

// WebURL returns a browse URL for the number. GitHub serves both issues and
// pull requests under /issues/N (PRs redirect to /pull/N), so one form covers
// both before Get has classified the number.
func (c *Client) WebURL(repo string, number int) string {
	if c == nil || c.baseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/issues/%d", c.baseURL, repo, number)
}

// MergeMethods are GitHub's three merge strategies. The repository may have
// disabled some of them; GitHub answers 405 with the reason, which the panel
// reports, rather than us guessing from settings we'd need another call to read.
func (c *Client) MergeMethods() []forge.MergeMethod {
	return []forge.MergeMethod{
		{ID: "merge", Label: "merge commit", Key: "m"},
		{ID: "squash", Label: "squash", Key: "s"},
		{ID: "rebase", Label: "rebase", Key: "r"},
	}
}

// apiPR mirrors the slice of the pull-request response we read.
type apiPR struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	State          string    `json:"state"` // open / closed
	Draft          bool      `json:"draft"`
	Merged         bool      `json:"merged"`
	Locked         bool      `json:"locked"`
	Body           string    `json:"body"`
	HTMLURL        string    `json:"html_url"`
	UpdatedAt      string    `json:"updated_at"`
	ChangedFiles   int       `json:"changed_files"`
	Mergeable      *bool     `json:"mergeable"`
	MergeableState string    `json:"mergeable_state"`
	User           *apiUser  `json:"user"`
	Assignees      []apiUser `json:"assignees"`
	RequestedTeams []struct {
		Name string `json:"name"`
	} `json:"requested_teams"`
	RequestedReviewers []apiUser `json:"requested_reviewers"`
	Labels             []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Head *apiRef `json:"head"`
	Base *apiRef `json:"base"`
}

type apiRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type apiUser struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

// display is the reviewer/author name to show: GitHub only fills in the real
// name on a dedicated user fetch, so in practice this is the login.
func (u *apiUser) display() string {
	if u == nil {
		return ""
	}
	if u.Name != "" {
		return u.Name
	}
	return u.Login
}

// apiIssue mirrors the /issues/{number} response. PullRequest is non-nil when
// the number is a pull request (GitHub stores that pointer on the issue object).
type apiIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"` // open / closed
	Locked    bool      `json:"locked"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	UpdatedAt string    `json:"updated_at"`
	User      *apiUser  `json:"user"`
	Assignees []apiUser `json:"assignees"`
	Labels    []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest *struct{} `json:"pull_request"`
}

// Get returns the issue or pull request, serving a cached copy when present.
// kind is a detect-time hint (forge.KindPull / KindIssue / empty):
//
//   - KindPull: one GET /pulls/{n} (+ best-effort checks/reviews).
//   - KindIssue: one GET /issues/{n}.
//   - empty (short owner/repo#N): tries pulls first; on 404 probes issues —
//     the pre-issue-support explain() path, so PR badges stay at one required
//     call and Issues:read is only needed for actual issues / ambiguous refs.
//
// Use Invalidate (then Get) to force a refetch.
func (c *Client) Get(ctx context.Context, repo string, number int, kind string) (*forge.Change, error) {
	if !c.Enabled() {
		return nil, forge.ErrNotConfigured
	}
	if hit, ok := c.cache.Get(repo, number); ok {
		return hit, nil
	}
	item, err := c.fetch(ctx, repo, number, kind)
	if err != nil {
		return nil, err
	}
	c.cache.Put(repo, number, item)
	return item, nil
}

// Invalidate drops any cached copy so the next Get refetches.
func (c *Client) Invalidate(repo string, number int) {
	if c == nil {
		return
	}
	c.cache.Invalidate(repo, number)
}

func (c *Client) fetch(ctx context.Context, repo string, number int, kind string) (*forge.Change, error) {
	switch kind {
	case forge.KindIssue:
		return c.fetchIssue(ctx, repo, number)
	case forge.KindPull:
		return c.fetchPull(ctx, repo, number)
	}
	// Ambiguous short form: pulls first (one call for a PR), issues on 404.
	ch, err := c.fetchPull(ctx, repo, number)
	if err == nil {
		return ch, nil
	}
	if !isNotFound(err) {
		return nil, err
	}
	issue, ierr := c.fetchIssue(ctx, repo, number)
	if ierr == nil {
		return issue, nil
	}
	// Prefer the pull-not-found error (with private-repo hint when tokenless).
	return nil, err
}

func (c *Client) fetchIssue(ctx context.Context, repo string, number int) (*forge.Change, error) {
	label := forge.Label(c, repo, number)
	var issue apiIssue
	path := fmt.Sprintf("/repos/%s/issues/%d", repo, number)
	if err := c.rest.Do(ctx, http.MethodGet, path, label, nil, &issue); err != nil {
		return nil, c.explain(err)
	}
	if issue.PullRequest != nil {
		// /issues/N URL that is actually a PR — load pull extras.
		return c.fetchPull(ctx, repo, number)
	}
	ch := toIssueChange(issue, repo)
	if issue.HTMLURL != "" {
		ch.WebURL = issue.HTMLURL
	} else {
		ch.WebURL = c.WebURL(repo, number)
	}
	return ch, nil
}

func (c *Client) fetchPull(ctx context.Context, repo string, number int) (*forge.Change, error) {
	label := forge.Label(c, repo, number)
	var a apiPR
	path := fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
	if err := c.rest.Do(ctx, http.MethodGet, path, label, nil, &a); err != nil {
		return nil, c.explain(err)
	}
	ch := toChange(a, repo)
	if a.HTMLURL != "" {
		ch.WebURL = a.HTMLURL
	} else {
		ch.WebURL = fmt.Sprintf("%s/%s/pull/%d", c.baseURL, repo, number)
	}
	// Checks (best-effort): the head commit's check runs plus the older commit
	// statuses, which plenty of CI still posts. One PR costs up to four calls
	// total when checks+statuses+reviews all succeed.
	if a.Head != nil && a.Head.SHA != "" {
		if checks := c.checks(ctx, repo, a.Head.SHA); checks != nil {
			ch.Checks = checks
		}
	}
	if ap, err := c.approvals(ctx, repo, number, a.RequestedReviewers); err == nil {
		ch.Approvals = ap
	}
	return ch, nil
}

// explain rewrites the errors whose plain HTTP meaning would send a user looking
// for the wrong problem:
//
//   - A rate-limit refusal arrives as 403, which otherwise reads as "check token
//     / scopes". Anonymous requests get 60 an hour, so this is the error a
//     tokenless setup meets first.
//   - A 404 without a token is as likely to be a private repository as a typo.
func (c *Client) explain(err error) error {
	var se *forge.StatusErr
	if !errors.As(err, &se) {
		return err
	}
	if rateLimited(se) {
		if c.token == "" {
			return fmt.Errorf("github: out of anonymous requests (60 an hour) — add a token to keep reading")
		}
		return fmt.Errorf("github: rate limit reached — try again in a few minutes")
	}
	if se.Code == http.StatusNotFound && c.token == "" {
		return fmt.Errorf("%w — a private repository needs a token", err)
	}
	return err
}

func isNotFound(err error) bool {
	var se *forge.StatusErr
	return errors.As(err, &se) && se.Code == http.StatusNotFound
}

// toIssueChange flattens a plain issue (not a pull request) into Change.
func toIssueChange(a apiIssue, repo string) *forge.Change {
	ch := &forge.Change{
		Repo:        repo,
		Number:      a.Number,
		Title:       a.Title,
		State:       openClosedState(a.State, false, a.Locked),
		Author:      a.User.display(),
		Description: a.Body,
		IsIssue:     true,
	}
	for _, u := range a.Assignees {
		ch.Assignees = append(ch.Assignees, u.display())
	}
	for _, l := range a.Labels {
		ch.Labels = append(ch.Labels, l.Name)
	}
	if t, err := time.Parse(time.RFC3339, a.UpdatedAt); err == nil {
		ch.UpdatedAt = t
	}
	return ch
}

// rateLimited reports whether a refusal is GitHub's rate limiter rather than a
// permission problem. The remaining-requests header is authoritative; the
// message is the fallback for the secondary limits, which don't set it.
func rateLimited(se *forge.StatusErr) bool {
	if se.Code != http.StatusForbidden && se.Code != http.StatusTooManyRequests {
		return false
	}
	if se.Header.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	return strings.Contains(strings.ToLower(se.Msg), "rate limit")
}

// toChange flattens the API response into the render-ready change (minus the
// URL and the best-effort checks / reviews the caller fills in).
func toChange(a apiPR, repo string) *forge.Change {
	ch := &forge.Change{
		Repo:         repo,
		Number:       a.Number,
		Title:        a.Title,
		State:        state(a),
		Draft:        a.Draft,
		Author:       a.User.display(),
		Description:  a.Body,
		HasConflicts: a.MergeableState == "dirty" || (a.Mergeable != nil && !*a.Mergeable),
	}
	if a.Head != nil {
		ch.SourceBranch = a.Head.Ref
	}
	if a.Base != nil {
		ch.TargetBranch = a.Base.Ref
	}
	for _, u := range a.Assignees {
		ch.Assignees = append(ch.Assignees, u.display())
	}
	for _, u := range a.RequestedReviewers {
		ch.Reviewers = append(ch.Reviewers, u.display())
	}
	for _, t := range a.RequestedTeams {
		ch.Reviewers = append(ch.Reviewers, "@"+t.Name)
	}
	for _, l := range a.Labels {
		ch.Labels = append(ch.Labels, l.Name)
	}
	if a.ChangedFiles > 0 {
		ch.ChangesCount = fmt.Sprintf("%d", a.ChangedFiles)
	}
	if t, err := time.Parse(time.RFC3339, a.UpdatedAt); err == nil {
		ch.UpdatedAt = t
	}
	ch.Mergeable, ch.MergeStatus = mergeability(a)
	return ch
}

// state maps GitHub's open/closed plus the merged flag onto the shared
// vocabulary, so a merged PR reads as merged rather than merely closed.
func state(a apiPR) string {
	return openClosedState(a.State, a.Merged, a.Locked)
}

func openClosedState(state string, merged, locked bool) string {
	switch {
	case merged:
		return forge.StateMerged
	case state == "closed":
		return forge.StateClosed
	case locked:
		return forge.StateLocked
	default:
		return forge.StateOpen
	}
}

// mergeability turns mergeable_state into the merge gate and the phrase shown
// next to it. GitHub computes the state asynchronously, so "unknown" means "ask
// again in a moment" rather than "no".
//
// clean/has_hooks accept a merge; unstable does too (a non-required check is
// failing, which GitHub allows you to merge past) and says so. Everything else
// blocks: dirty is a conflict, behind needs an update, blocked means a required
// review or check is missing, draft means mark it ready first.
func mergeability(a apiPR) (bool, string) {
	switch a.MergeableState {
	case "clean", "has_hooks":
		return true, "mergeable"
	case "unstable":
		return true, "mergeable · a check is failing"
	case "dirty":
		return false, "conflicts"
	case "behind":
		return false, "behind the target branch"
	case "blocked":
		return false, "blocked (required review or check)"
	case "draft":
		return false, "draft"
	case "unknown", "":
		// No state yet (or a closed PR, where GitHub stops computing it).
		if a.Merged {
			return false, "merged"
		}
		if a.State == "closed" {
			return false, "closed"
		}
		return false, "mergeability not computed yet"
	default:
		return false, strings.ReplaceAll(a.MergeableState, "_", " ")
	}
}

// apiCheckRun is one check run for the head commit.
type apiCheckRun struct {
	Name        string `json:"name"`
	Status      string `json:"status"`     // queued / in_progress / completed
	Conclusion  string `json:"conclusion"` // success / failure / neutral / …
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	HTMLURL     string `json:"html_url"`
	App         *struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"app"`
}

// checks reads the head commit's check runs and commit statuses and folds them
// into one forge.Checks. Returns nil when the commit has neither, so the panel
// says "none" rather than showing an empty section. Best-effort throughout: a
// failing call contributes nothing instead of failing the fetch.
func (c *Client) checks(ctx context.Context, repo, sha string) *forge.Checks {
	var groups []forge.Group
	var earliest, latest time.Time

	var runs struct {
		CheckRuns []apiCheckRun `json:"check_runs"`
	}
	path := fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=%d", repo, sha, perPage)
	if err := c.rest.Do(ctx, http.MethodGet, path, "check runs", nil, &runs); err == nil {
		byApp := map[string][]forge.Job{}
		var order []string
		for _, r := range runs.CheckRuns {
			app := "checks"
			if r.App != nil && r.App.Name != "" {
				app = r.App.Name
			} else if r.App != nil && r.App.Slug != "" {
				app = r.App.Slug
			}
			if _, ok := byApp[app]; !ok {
				order = append(order, app)
			}
			byApp[app] = append(byApp[app], forge.Job{Name: r.Name, Status: runStatus(r)})
			earliest, latest = span(earliest, latest, r.StartedAt, r.CompletedAt)
		}
		for _, app := range order {
			groups = append(groups, forge.Group{Name: app, Jobs: byApp[app]})
		}
	}

	// The pre-checks commit-status API: still what many external CI systems
	// post, and invisible in the check-runs list.
	var combined struct {
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"` // success / pending / failure / error
		} `json:"statuses"`
	}
	path = fmt.Sprintf("/repos/%s/commits/%s/status?per_page=%d", repo, sha, perPage)
	if err := c.rest.Do(ctx, http.MethodGet, path, "commit status", nil, &combined); err == nil && len(combined.Statuses) > 0 {
		jobs := make([]forge.Job, 0, len(combined.Statuses))
		for _, s := range combined.Statuses {
			jobs = append(jobs, forge.Job{Name: s.Context, Status: statusState(s.State)})
		}
		groups = append(groups, forge.Group{Name: "commit statuses", Jobs: jobs})
	}

	if len(groups) == 0 {
		return nil
	}
	var all []forge.Job
	for _, g := range groups {
		all = append(all, g.Jobs...)
	}
	overall := forge.WorstStatus(all)
	checks := &forge.Checks{
		Status: overall,
		Label:  checksLabel(overall),
		WebURL: c.baseURL + "/" + repo + "/commit/" + sha + "/checks",
		Groups: groups,
	}
	if !earliest.IsZero() && latest.After(earliest) {
		checks.Duration = int(latest.Sub(earliest).Seconds())
	}
	return checks
}

// span widens the [earliest, latest] window with one run's timestamps, so the
// panel can show how long the checks took — GitHub reports no total.
func span(earliest, latest time.Time, startedAt, completedAt string) (time.Time, time.Time) {
	if t, err := time.Parse(time.RFC3339, startedAt); err == nil {
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	if t, err := time.Parse(time.RFC3339, completedAt); err == nil {
		if t.After(latest) {
			latest = t
		}
	}
	return earliest, latest
}

// runStatus normalizes a check run: an incomplete run reports its status, a
// completed one its conclusion.
func runStatus(r apiCheckRun) string {
	if r.Status != "completed" {
		switch r.Status {
		case "in_progress":
			return forge.StatusRunning
		default: // queued / waiting / pending / requested
			return forge.StatusPending
		}
	}
	switch r.Conclusion {
	case "success":
		return forge.StatusSuccess
	case "failure", "timed_out", "startup_failure":
		return forge.StatusFailed
	case "cancelled", "stale":
		return forge.StatusCanceled
	case "skipped":
		return forge.StatusSkipped
	case "action_required", "neutral":
		return forge.StatusManual
	default:
		return forge.StatusPending
	}
}

// statusState normalizes a commit status's state.
func statusState(s string) string {
	switch s {
	case "success":
		return forge.StatusSuccess
	case "failure", "error":
		return forge.StatusFailed
	default: // pending
		return forge.StatusPending
	}
}

// checksLabel is the human word for the overall checks status, matching the
// tone of GitLab's own pipeline labels ("passed", "failed", "running").
func checksLabel(status string) string {
	switch status {
	case forge.StatusSuccess:
		return "passed"
	case forge.StatusFailed:
		return "failed"
	case forge.StatusRunning:
		return "running"
	case forge.StatusCanceled:
		return "canceled"
	case forge.StatusSkipped:
		return "skipped"
	case forge.StatusManual:
		return "action required"
	default:
		return "pending"
	}
}

// approvals reads the review history and reduces it to who currently approves
// and who currently blocks. GitHub keeps every review, so only a reviewer's
// last decisive review (APPROVED / CHANGES_REQUESTED / DISMISSED) counts;
// comment-only reviews never change a verdict.
//
// The number of approvals a branch requires lives in branch protection, which
// needs admin rights to read, so Required/Left stay 0 and the panel shows names
// instead of an n/m tally. requested carries the reviewers who haven't answered
// yet, which is what makes "waiting on 2" visible at all.
func (c *Client) approvals(ctx context.Context, repo string, number int, requested []apiUser) (*forge.Approvals, error) {
	var reviews []struct {
		State string   `json:"state"`
		User  *apiUser `json:"user"`
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=%d", repo, number, perPage)
	if err := c.rest.Do(ctx, http.MethodGet, path, "reviews", nil, &reviews); err != nil {
		return nil, err
	}
	verdict := map[string]string{} // login → last decisive state
	var order []string
	for _, r := range reviews {
		login := r.User.display()
		if login == "" {
			continue
		}
		switch strings.ToUpper(r.State) {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			if _, seen := verdict[login]; !seen {
				order = append(order, login)
			}
			verdict[login] = strings.ToUpper(r.State)
		}
	}
	ap := &forge.Approvals{}
	for _, login := range order {
		switch verdict[login] {
		case "APPROVED":
			ap.By = append(ap.By, login)
		case "CHANGES_REQUESTED":
			ap.ChangesRequested = append(ap.ChangesRequested, login)
		}
	}
	ap.Approved = len(ap.By) > 0 && len(ap.ChangesRequested) == 0
	// Reviewers still to answer: what "left" means without branch protection.
	if pending := pendingReviewers(requested, verdict); pending > 0 {
		ap.Left = pending
	}
	return ap, nil
}

// pendingReviewers counts requested reviewers who haven't left a decisive
// review yet.
func pendingReviewers(requested []apiUser, verdict map[string]string) int {
	n := 0
	for _, u := range requested {
		if _, answered := verdict[u.display()]; !answered {
			n++
		}
	}
	return n
}

// Approve submits an approving review, then invalidates the cache so the next
// Get reflects it. Issues cannot be approved this way.
func (c *Client) Approve(ctx context.Context, repo string, number int) error {
	if err := c.canWrite(); err != nil {
		return err
	}
	if issue, err := c.isPlainIssue(ctx, repo, number); err == nil && issue {
		return fmt.Errorf("github: %s is an issue — only pull requests can be approved", forge.Label(c, repo, number))
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, number)
	body := map[string]any{"event": "APPROVE"}
	if err := c.rest.Do(ctx, http.MethodPost, path, "approve", body, nil); err != nil {
		return c.explain(err)
	}
	c.Invalidate(repo, number)
	return nil
}

// canWrite refuses the actions that need credentials before spending a call on
// them: reading is anonymous-friendly, approving and merging are not.
func (c *Client) canWrite() error {
	if !c.Enabled() {
		return forge.ErrNotConfigured
	}
	if c.token == "" {
		return errors.New("github: this needs a token — set github.token, GITHUB_TOKEN, or run `gh auth login`")
	}
	return nil
}

// Merge merges the pull request with the given method (one of MergeMethods'
// ids), then invalidates the cache. The source branch is left alone: GitHub
// deletes it itself when the repository is set to, and deleting it from here
// would be a second call undoing a repository-level choice. Issues cannot be
// merged.
func (c *Client) Merge(ctx context.Context, repo string, number int, method string) error {
	if err := c.canWrite(); err != nil {
		return err
	}
	if issue, err := c.isPlainIssue(ctx, repo, number); err == nil && issue {
		return fmt.Errorf("github: %s is an issue — only pull requests can be merged", forge.Label(c, repo, number))
	}
	if method == "" {
		method = "merge"
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d/merge", repo, number)
	body := map[string]any{"merge_method": method}
	if err := c.rest.Do(ctx, http.MethodPut, path, "merge", body, nil); err != nil {
		return c.explain(err)
	}
	c.Invalidate(repo, number)
	return nil
}

// isPlainIssue reports whether number is an issue (not a pull request). Prefer
// the cache; otherwise one issues GET. Errors leave the caller free to try the
// write and surface whatever GitHub returns.
func (c *Client) isPlainIssue(ctx context.Context, repo string, number int) (bool, error) {
	if hit, ok := c.cache.Get(repo, number); ok {
		return hit.IsIssue, nil
	}
	var issue apiIssue
	path := fmt.Sprintf("/repos/%s/issues/%d", repo, number)
	if err := c.rest.Do(ctx, http.MethodGet, path, forge.Label(c, repo, number), nil, &issue); err != nil {
		return false, err
	}
	return issue.PullRequest == nil, nil
}
