// Package jira fetches issue detail from a Jira Cloud instance so the TUI
// can show it inline in a side panel (press the open-reference key on a
// message that names an issue — see internal/ui). It deliberately has no
// dependency on the UI or store packages so it can be unit-tested against an
// httptest server with no real instance.
//
// Cloud only: authentication is HTTP Basic with an account email and an API
// token (id.atlassian.com → API tokens), and descriptions come back as ADF
// (Atlassian Document Format) JSON, which adfToMarkdown flattens to markdown.
package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// requestTimeout bounds a single issue fetch. Generous for a slow instance; a
// stalled server fails the call rather than hanging the UI's fetch goroutine.
const requestTimeout = 20 * time.Second

// issueFields is the field list requested from the API. Keeping it explicit
// (rather than the default "*all") keeps the response small and the JSON we
// have to decode predictable.
const issueFields = "summary,status,assignee,reporter,issuetype,priority,labels,updated,description"

// Config is the subset of the user config this package needs. BaseURL is the
// instance root (https://your-instance.atlassian.net); Email + APIToken are the
// Cloud Basic-auth pair; Projects is the allowlist that gates bare-ID detection
// (see detect.go). StoryPointsField optionally pins the custom-field id for
// story points (e.g. "customfield_10016"); empty auto-detects it.
type Config struct {
	BaseURL          string
	Email            string
	APIToken         string
	Projects         []string
	StoryPointsField string
}

// Client fetches and caches issues for one instance. The zero value is not
// usable; use New. Safe for concurrent use.
type Client struct {
	baseURL    string // trimmed of any trailing slash
	auth       string // pre-encoded "Basic …" header value, empty when unconfigured
	spOverride string // configured story-points custom-field id, "" to auto-detect
	http       *http.Client

	mu    sync.Mutex
	cache map[string]*Issue
	// spFields are the resolved story-points custom-field ids (a configured
	// override, or every field named "story point…" from the field metadata).
	// spResolved guards the one-time resolution; both are behind mu.
	spFields   []string
	spResolved bool
}

// New builds a Client from cfg. The returned client is always non-nil; call
// Enabled to see whether it has enough configuration to actually fetch.
func New(cfg Config) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		spOverride: strings.TrimSpace(cfg.StoryPointsField),
		http:       &http.Client{Timeout: requestTimeout},
		cache:      map[string]*Issue{},
	}
	if cfg.Email != "" && cfg.APIToken != "" {
		raw := cfg.Email + ":" + cfg.APIToken
		c.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))
	}
	return c
}

// Enabled reports whether the client has a base URL and credentials — i.e.
// whether a fetch can succeed. The UI uses it to decide whether to offer the
// panel at all.
func (c *Client) Enabled() bool {
	return c != nil && c.baseURL != "" && c.auth != ""
}

// BaseURL returns the configured instance root (no trailing slash). Used by
// Refs to recognise /browse/KEY links pointing at this instance.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

// BrowseURL returns the human browse URL for an issue key (what `o` opens in a
// browser), e.g. https://your-instance.atlassian.net/browse/ABC-123.
func (c *Client) BrowseURL(key string) string {
	if c == nil || c.baseURL == "" {
		return ""
	}
	return c.baseURL + "/browse/" + key
}

// Issue is the flattened, render-ready form of a Jira issue. Description is
// markdown (converted from ADF); the rest are plain strings ready to label.
type Issue struct {
	Key         string
	Summary     string
	Type        string
	Status      string
	Priority    string
	Assignee    string
	Reporter    string
	Labels      []string
	Updated     time.Time
	URL         string
	Description string
	StoryPoints string // formatted estimate (e.g. "5", "2.5"), "" when unset
}

// apiIssue mirrors the slice of the REST response we read. Optional objects are
// pointers so an absent assignee/priority decodes to nil rather than an error.
type apiIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"`
		Labels      []string        `json:"labels"`
		Updated     string          `json:"updated"`
		Status      *named          `json:"status"`
		Priority    *named          `json:"priority"`
		IssueType   *named          `json:"issuetype"`
		Assignee    *user           `json:"assignee"`
		Reporter    *user           `json:"reporter"`
	} `json:"fields"`
}

type named struct {
	Name string `json:"name"`
}

type user struct {
	DisplayName string `json:"displayName"`
}

// Get returns the issue for key, serving a cached copy when present. Use
// Invalidate (then Get) to force a refetch.
func (c *Client) Get(ctx context.Context, key string) (*Issue, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("jira: not configured (need base_url, email, api_token)")
	}
	c.mu.Lock()
	if hit, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return hit, nil
	}
	c.mu.Unlock()

	issue, err := c.fetch(ctx, key)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cache[key] = issue
	c.mu.Unlock()
	return issue, nil
}

// Invalidate drops any cached copy of key so the next Get refetches.
func (c *Client) Invalidate(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.cache, key)
	c.mu.Unlock()
}

func (c *Client) fetch(ctx context.Context, key string) (*Issue, error) {
	// Resolve the story-points custom field(s) first so we can request them
	// alongside the standard fields. A failure here is non-fatal — the issue
	// still loads, just without story points.
	spFields := c.resolveStoryPointFields(ctx)
	fields := issueFields
	for _, f := range spFields {
		fields += "," + f
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	endpoint := c.baseURL + "/rest/api/3/issue/" + url.PathEscape(key) + "?fields=" + url.QueryEscape(fields)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call jira: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode, key, body)
	}

	var decoded apiIssue
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode issue: %w", err)
	}
	iss := c.toIssue(decoded)
	iss.StoryPoints = extractStoryPoints(body, spFields)
	return iss, nil
}

// statusError turns a non-200 into a message the panel can show, special-casing
// the auth/visibility cases that a user can actually act on.
func statusError(code int, key string, body []byte) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("jira: not authorized for %s — check email / api_token", key)
	case http.StatusNotFound:
		return fmt.Errorf("jira: %s not found (or no access)", key)
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	if msg == "" {
		msg = http.StatusText(code)
	}
	return fmt.Errorf("jira server %d: %s", code, msg)
}

func (c *Client) toIssue(a apiIssue) *Issue {
	iss := &Issue{
		Key:         a.Key,
		Summary:     a.Fields.Summary,
		Labels:      a.Fields.Labels,
		URL:         c.BrowseURL(a.Key),
		Description: adfToMarkdown(a.Fields.Description),
		Assignee:    "Unassigned",
	}
	if a.Fields.Status != nil {
		iss.Status = a.Fields.Status.Name
	}
	if a.Fields.Priority != nil {
		iss.Priority = a.Fields.Priority.Name
	}
	if a.Fields.IssueType != nil {
		iss.Type = a.Fields.IssueType.Name
	}
	if a.Fields.Assignee != nil && a.Fields.Assignee.DisplayName != "" {
		iss.Assignee = a.Fields.Assignee.DisplayName
	}
	if a.Fields.Reporter != nil {
		iss.Reporter = a.Fields.Reporter.DisplayName
	}
	// Jira stamps updated as e.g. 2026-06-15T09:41:00.000+0200.
	if t, err := time.Parse("2006-01-02T15:04:05.999-0700", a.Fields.Updated); err == nil {
		iss.Updated = t
	}
	return iss
}

// resolveStoryPointFields returns the custom-field id(s) that hold story
// points: the configured override if set, otherwise every field whose name
// contains "story point" (case-insensitive) from the instance's field
// metadata. The result is resolved once and cached. A failed lookup is cached
// as "none" only when an override is set; for auto-detect it stays unresolved
// so a later fetch retries (the metadata endpoint may have been transiently
// down). Returns nil when there's nothing to request.
func (c *Client) resolveStoryPointFields(ctx context.Context) []string {
	c.mu.Lock()
	if c.spResolved {
		f := c.spFields
		c.mu.Unlock()
		return f
	}
	c.mu.Unlock()

	if c.spOverride != "" {
		c.mu.Lock()
		c.spFields = []string{c.spOverride}
		c.spResolved = true
		f := c.spFields
		c.mu.Unlock()
		return f
	}

	ids, err := c.fetchStoryPointFieldIDs(ctx)
	if err != nil {
		return nil // leave unresolved so the next fetch retries
	}
	c.mu.Lock()
	c.spFields = ids
	c.spResolved = true
	c.mu.Unlock()
	return ids
}

// fetchStoryPointFieldIDs reads the instance field metadata and returns the ids
// of every field named like story points. Order follows the API response, so
// extractStoryPoints prefers whichever candidate the issue actually populates.
func (c *Client) fetchStoryPointFieldIDs(ctx context.Context) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL+"/rest/api/3/field", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jira field metadata: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var fields []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	var ids []string
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f.Name), "story point") {
			ids = append(ids, f.ID)
		}
	}
	return ids, nil
}

// extractStoryPoints pulls the first non-null numeric value among the candidate
// field ids out of the raw issue JSON, formatted without trailing zeros ("5",
// "2.5"). Returns "" when none is set.
func extractStoryPoints(body []byte, ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	var wrap struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return ""
	}
	for _, id := range ids {
		raw, ok := wrap.Fields[id]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var v float64
		if err := json.Unmarshal(raw, &v); err != nil {
			continue
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}
