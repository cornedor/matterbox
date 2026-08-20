// Package github fetches issue / pull-request detail from a GitHub instance
// so the TUI can show it inline in the reference side panel.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errNotConfigured = errors.New("github: not configured (need base_url and token)")

const requestTimeout = 20 * time.Second

// Config is the subset of user config this package needs.
type Config struct {
	BaseURL string
	Token   string
}

// Client fetches and caches issue / pull-request detail for one GitHub
// instance.
//
// The zero value is not usable; use New.
// Safe for concurrent use.
type Client struct {
	webBase string // e.g. https://github.com
	apiBase string // e.g. https://api.github.com or https://ghe/api/v3
	token   string

	http *http.Client

	mu    sync.Mutex
	cache map[string]*Item // key: lower(repo) + "#" + number
}

func New(cfg Config) *Client {
	webBase := normalizeWebBase(cfg.BaseURL)
	apiBase, _ := apiBaseFromWebBase(webBase)
	return &Client{
		webBase: webBase,
		apiBase: apiBase,
		token:   strings.TrimSpace(cfg.Token),
		http:    &http.Client{Timeout: requestTimeout},
		cache:   map[string]*Item{},
	}
}

func normalizeWebBase(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return raw
}

func apiBaseFromWebBase(webBase string) (string, error) {
	webBase = normalizeWebBase(webBase)
	if webBase == "" {
		return "", errors.New("github: empty base_url")
	}
	u, err := parseWebBase(webBase)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", errors.New("github: base_url missing host")
	}
	if strings.EqualFold(u.Host, "github.com") {
		return "https://api.github.com", nil
	}
	// Enterprise REST root is always /api/v3 on the host (ignore any path on
	// the web origin so scheme-less and path-suffixed bases stay consistent
	// with githubauth.APIBaseFromWebBase).
	return u.Scheme + "://" + u.Host + "/api/v3", nil
}

type parsedURL struct {
	Scheme string
	Host   string
}

func parseWebBase(webBase string) (parsedURL, error) {
	uu, err := url.Parse(webBase)
	if err != nil {
		return parsedURL{}, err
	}
	return parsedURL{Scheme: uu.Scheme, Host: uu.Host}, nil
}

// Enabled reports whether the client has enough configuration to fetch.
func (c *Client) Enabled() bool {
	return c != nil && c.webBase != "" && c.apiBase != "" && c.token != ""
}

func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.webBase
}

// WebURL returns the human URL for an item.
func (c *Client) WebURL(repo string, number int, isPull bool) string {
	if c == nil || c.webBase == "" {
		return ""
	}
	if repo == "" || number <= 0 {
		return ""
	}
	if isPull {
		return fmt.Sprintf("%s/%s/pull/%d", c.webBase, repo, number)
	}
	return fmt.Sprintf("%s/%s/issues/%d", c.webBase, repo, number)
}

// Item is the render-ready representation of an issue / PR.
type Item struct {
	Repo   string
	Number int
	IsPull bool

	Title     string
	State     string
	Draft     bool
	UpdatedAt time.Time
	URL       string

	Author string
	Labels []string
	Body   string

	// PR-specific fields.
	SourceBranch   string
	TargetBranch   string
	ChangedFiles   int
	ChecksState    string // combined commit status: success/pending/failure/error/""
	MergeableState string
}

// Mergeable reports whether GitHub considers the PR ready to merge.
func (it *Item) Mergeable() bool {
	if it == nil || !it.IsPull {
		return false
	}
	if it.State != "open" || it.Draft {
		return false
	}
	return it.MergeableState == "clean"
}

// Invalidate drops the cached item for repo#number.
func (c *Client) Invalidate(repo string, number int) {
	if c == nil {
		return
	}
	key := strings.ToLower(repo) + "#" + strconv.Itoa(number)
	c.mu.Lock()
	delete(c.cache, key)
	c.mu.Unlock()
}

// Get fetches item detail for repo#number.
func (c *Client) Get(ctx context.Context, repo string, number int) (*Item, error) {
	if !c.Enabled() {
		return nil, errNotConfigured
	}
	repo = strings.TrimSpace(repo)
	if repo == "" || number <= 0 {
		return nil, errors.New("github: invalid repo/number")
	}
	key := strings.ToLower(repo) + "#" + strconv.Itoa(number)
	c.mu.Lock()
	if hit, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return hit, nil
	}
	c.mu.Unlock()

	// 1) Fetch /issues/{number} which includes pull_request pointer when it
	// is a PR.
	issue, isPull, err := c.getIssueBase(ctx, repo, number)
	if err != nil {
		return nil, err
	}

	item := &Item{
		Repo:      repo,
		Number:    number,
		IsPull:    isPull,
		Title:     issue.Title,
		State:     issue.State,
		Draft:     false,
		UpdatedAt: issue.UpdatedAt,
		URL:       issue.HTMLURL,
		Author:    issue.User.Login,
		Labels:    issue.labels(),
		Body:      issue.Body,
	}

	// 2) If it's a PR, fetch /pulls/{number} for branch/mergeability and
	// best-effort combined CI status on the head commit. A failed PR fetch
	// must not be cached — otherwise the panel sticks on a title-only stub
	// until Invalidate (refresh / mutate).
	if isPull {
		pr, err := c.getPR(ctx, repo, number)
		if err != nil {
			return nil, err
		}
		item.Draft = pr.Draft
		item.SourceBranch = pr.Head.Ref
		item.TargetBranch = pr.Base.Ref
		item.ChangedFiles = pr.ChangedFiles
		item.MergeableState = pr.MergeableState
		if pr.Merged {
			item.State = "merged"
		}
		if pr.Head.SHA != "" {
			if st, serr := c.getCommitStatus(ctx, repo, pr.Head.SHA); serr == nil {
				item.ChecksState = st
			}
		}
	}

	// Stable ordering for deterministic rendering/tests.
	sort.Strings(item.Labels)

	c.mu.Lock()
	c.cache[key] = item
	c.mu.Unlock()
	return item, nil
}

type issueAPI struct {
	Title     string    `json:"title"`
	State     string    `json:"state"`
	HTMLURL   string    `json:"html_url"`
	UpdatedAt time.Time `json:"updated_at"`
	Body      string    `json:"body"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest *struct {
		// Non-empty when this issue is actually a pull request.
	} `json:"pull_request"`
}

func (i issueAPI) labels() []string {
	out := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		if l.Name != "" {
			out = append(out, l.Name)
		}
	}
	return out
}

func (c *Client) getIssueBase(ctx context.Context, repo string, number int) (issueAPI, bool, error) {
	owner, name := splitRepo(repo)
	if owner == "" || name == "" {
		return issueAPI{}, false, errors.New("github: invalid repo path (expected owner/repo)")
	}

	endpoint := fmt.Sprintf("%s/repos/%s/issues/%d", c.apiBase, owner+"/"+name, number)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return issueAPI{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return issueAPI{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return issueAPI{}, false, fmt.Errorf("github: issue fetch HTTP %d", resp.StatusCode)
	}
	var body issueAPI
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return issueAPI{}, false, err
	}

	if body.PullRequest != nil {
		return body, true, nil
	}
	return body, false, nil
}

type prAPI struct {
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	MergeableState string `json:"mergeable_state"`
	ChangedFiles   int    `json:"changed_files"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (c *Client) getPR(ctx context.Context, repo string, number int) (prAPI, error) {
	owner, name := splitRepo(repo)
	if owner == "" || name == "" {
		return prAPI{}, errors.New("github: invalid repo path (expected owner/repo)")
	}

	endpoint := fmt.Sprintf("%s/repos/%s/pulls/%d", c.apiBase, owner+"/"+name, number)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return prAPI{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return prAPI{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return prAPI{}, fmt.Errorf("github: pr fetch HTTP %d", resp.StatusCode)
	}
	var body prAPI
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return prAPI{}, err
	}
	return body, nil
}

func (c *Client) getCommitStatus(ctx context.Context, repo, sha string) (string, error) {
	owner, name := splitRepo(repo)
	if owner == "" || name == "" {
		return "", errors.New("github: invalid repo path (expected owner/repo)")
	}
	endpoint := fmt.Sprintf("%s/repos/%s/commits/%s/status", c.apiBase, owner+"/"+name, sha)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("github: status fetch HTTP %d", resp.StatusCode)
	}
	var body struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.State, nil
}

// Approve submits an APPROVE review on the pull request, then invalidates cache.
func (c *Client) Approve(ctx context.Context, repo string, number int) error {
	if !c.Enabled() {
		return errNotConfigured
	}
	owner, name := splitRepo(repo)
	if owner == "" || name == "" || number <= 0 {
		return errors.New("github: invalid repo/number")
	}
	path := fmt.Sprintf("%s/repos/%s/pulls/%d/reviews", c.apiBase, owner+"/"+name, number)
	if err := c.doJSON(ctx, http.MethodPost, path, map[string]string{"event": "APPROVE"}); err != nil {
		return err
	}
	c.Invalidate(repo, number)
	return nil
}

// Merge merges the pull request, then invalidates cache.
func (c *Client) Merge(ctx context.Context, repo string, number int) error {
	if !c.Enabled() {
		return errNotConfigured
	}
	owner, name := splitRepo(repo)
	if owner == "" || name == "" || number <= 0 {
		return errors.New("github: invalid repo/number")
	}
	path := fmt.Sprintf("%s/repos/%s/pulls/%d/merge", c.apiBase, owner+"/"+name, number)
	if err := c.doJSON(ctx, http.MethodPut, path, map[string]string{}); err != nil {
		return err
	}
	c.Invalidate(repo, number)
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, url string, body any) error {
	var rdr *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github: %s HTTP %d%s", method, resp.StatusCode, readGitHubAPIError(resp))
	}
	return nil
}

// readGitHubAPIError pulls a short message from an error response body without
// echoing tokens (GitHub's JSON "message" field is safe prose).
func readGitHubAPIError(resp *http.Response) string {
	const max = 512
	buf := make([]byte, max+1)
	n, _ := io.ReadFull(resp.Body, buf)
	if n <= 0 {
		return ""
	}
	raw := buf[:min(n, max)]
	var parsed struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil && strings.TrimSpace(parsed.Message) != "" {
		msg := strings.TrimSpace(parsed.Message)
		if len(msg) > 160 {
			msg = msg[:157] + "…"
		}
		return ": " + msg
	}
	return ""
}

func splitRepo(repo string) (owner, name string) {
	parts := strings.Split(strings.TrimSpace(repo), "/")
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}
