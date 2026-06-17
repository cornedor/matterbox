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
	"bytes"
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

// errNotConfigured is returned by every call when the client lacks a base URL
// or credentials (Enabled would report false).
var errNotConfigured = fmt.Errorf("jira: not configured (need base_url, email, api_token)")

// requestTimeout bounds a single issue fetch. Generous for a slow instance; a
// stalled server fails the call rather than hanging the UI's fetch goroutine.
const requestTimeout = 20 * time.Second

// issueFields is the field list requested from the API. Keeping it explicit
// (rather than the default "*all") keeps the response small and the JSON we
// have to decode predictable.
const issueFields = "summary,status,assignee,reporter,issuetype,priority,labels,updated,description,comment"

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
	// priorities is the instance-wide priority list, fetched once and cached
	// (it's global, not per-issue). myself is the authenticated account, also
	// cached (used for "Assign to me"). Both are behind mu and nil until first
	// resolved. See Priorities / Myself.
	priorities []Option
	myself     *User
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

	// Comments is the issue's comment thread (oldest first, the order the API
	// returns), flattened for display. CommentTotal is the server's total — it
	// can exceed len(Comments) when the inline field paged, so the panel can
	// say "…and N more".
	Comments     []Comment
	CommentTotal int

	// IDs of the current selection, so the field pickers can mark the active
	// row. Display-only fields (Status, Reporter) don't need one here: status
	// changes go through Transitions, not by id.
	PriorityID        string
	AssigneeAccountID string
}

// Comment is one issue comment, flattened for display. Body is markdown
// (converted from ADF); AuthorID (accountId) is what a reply's @mention writes.
type Comment struct {
	Author   string
	AuthorID string
	Body     string
	Created  time.Time
}

// Mention names a user to ping in a comment. AddComment turns it into a real
// ADF mention node — the only form Jira notifies on; plain "@name" text does
// not.
type Mention struct {
	AccountID   string
	DisplayName string
}

// Option is a pickable choice with a stable id and a human label — a workflow
// transition (id = transition id, Name = the resulting status) or a priority.
type Option struct {
	ID   string
	Name string
}

// User is an assignable account: AccountID is what SetAssignee writes,
// DisplayName is shown in the picker.
type User struct {
	AccountID   string
	DisplayName string
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
		Comment     *struct {
			Comments []apiComment `json:"comments"`
			Total    int          `json:"total"`
		} `json:"comment"`
	} `json:"fields"`
}

type named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type user struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
}

// apiComment mirrors one entry of the issue's inline comment field. Body is the
// ADF document, flattened to markdown via adfToMarkdown.
type apiComment struct {
	Author  *user           `json:"author"`
	Body    json.RawMessage `json:"body"`
	Created string          `json:"created"`
}

// Get returns the issue for key, serving a cached copy when present. Use
// Invalidate (then Get) to force a refetch.
func (c *Client) Get(ctx context.Context, key string) (*Issue, error) {
	if !c.Enabled() {
		return nil, errNotConfigured
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

	path := "/rest/api/3/issue/" + url.PathEscape(key) + "?fields=" + url.QueryEscape(fields)
	body, err := c.doRaw(ctx, http.MethodGet, path, key, nil)
	if err != nil {
		return nil, err
	}

	var decoded apiIssue
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode issue: %w", err)
	}
	iss := c.toIssue(decoded)
	iss.StoryPoints = extractStoryPoints(body, spFields)
	return iss, nil
}

// doRaw performs an authenticated request to path (relative to baseURL, which
// must begin with "/" and may carry a query string), sending body as JSON when
// non-nil, and returns the raw response body. A non-2xx status becomes a
// statusError; what labels the request in that error (an issue key, or e.g.
// "priorities"). Each call carries its own requestTimeout.
func (c *Client) doRaw(ctx context.Context, method, path, what string, body any) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call jira: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp.StatusCode, what, respBody)
	}
	return respBody, nil
}

// do is doRaw plus JSON decode into out (skip with out=nil, e.g. a 204 mutation
// response).
func (c *Client) do(ctx context.Context, method, path, what string, body, out any) error {
	respBody, err := c.doRaw(ctx, method, path, what, body)
	if err != nil {
		return err
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// statusError turns a non-2xx into a message the panel can show, special-casing
// the auth/visibility cases that a user can actually act on. what labels the
// request (an issue key, or e.g. "priorities").
func statusError(code int, what string, body []byte) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("jira: not authorized for %s — check email / api_token", what)
	case http.StatusNotFound:
		return fmt.Errorf("jira: %s not found (or no access)", what)
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
		iss.PriorityID = a.Fields.Priority.ID
	}
	if a.Fields.IssueType != nil {
		iss.Type = a.Fields.IssueType.Name
	}
	if a.Fields.Assignee != nil && a.Fields.Assignee.DisplayName != "" {
		iss.Assignee = a.Fields.Assignee.DisplayName
		iss.AssigneeAccountID = a.Fields.Assignee.AccountID
	}
	if a.Fields.Reporter != nil {
		iss.Reporter = a.Fields.Reporter.DisplayName
	}
	// Jira stamps updated as e.g. 2026-06-15T09:41:00.000+0200.
	if t, err := time.Parse("2006-01-02T15:04:05.999-0700", a.Fields.Updated); err == nil {
		iss.Updated = t
	}
	if a.Fields.Comment != nil {
		iss.CommentTotal = a.Fields.Comment.Total
		for _, ac := range a.Fields.Comment.Comments {
			cm := Comment{Body: adfToMarkdown(ac.Body)}
			if ac.Author != nil {
				cm.Author = ac.Author.DisplayName
				cm.AuthorID = ac.Author.AccountID
			}
			if t, err := time.Parse("2006-01-02T15:04:05.999-0700", ac.Created); err == nil {
				cm.Created = t
			}
			iss.Comments = append(iss.Comments, cm)
		}
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
	var fields []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/field", "field metadata", nil, &fields); err != nil {
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

// Transitions returns the workflow transitions available from the issue's
// current status — the only status changes Jira will accept. Each Option's ID
// is the transition id (pass to DoTransition); Name is the resulting status.
func (c *Client) Transitions(ctx context.Context, key string) ([]Option, error) {
	if !c.Enabled() {
		return nil, errNotConfigured
	}
	var resp struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   named  `json:"to"`
		} `json:"transitions"`
	}
	path := "/rest/api/3/issue/" + url.PathEscape(key) + "/transitions"
	if err := c.do(ctx, http.MethodGet, path, key, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]Option, 0, len(resp.Transitions))
	for _, t := range resp.Transitions {
		name := t.To.Name // the status the transition lands on
		if name == "" {
			name = t.Name
		}
		out = append(out, Option{ID: t.ID, Name: name})
	}
	return out, nil
}

// DoTransition moves the issue along the given transition, then invalidates the
// cache so the next Get reflects the new status (and any cascading fields like
// resolution).
func (c *Client) DoTransition(ctx context.Context, key, transitionID string) error {
	if !c.Enabled() {
		return errNotConfigured
	}
	body := map[string]any{"transition": map[string]string{"id": transitionID}}
	path := "/rest/api/3/issue/" + url.PathEscape(key) + "/transitions"
	if err := c.do(ctx, http.MethodPost, path, key, body, nil); err != nil {
		return err
	}
	c.Invalidate(key)
	return nil
}

// Priorities returns the instance's global priority list (Highest…Lowest),
// cached after the first call.
func (c *Client) Priorities(ctx context.Context) ([]Option, error) {
	if !c.Enabled() {
		return nil, errNotConfigured
	}
	c.mu.Lock()
	if c.priorities != nil {
		p := c.priorities
		c.mu.Unlock()
		return p, nil
	}
	c.mu.Unlock()

	var resp []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/priority", "priorities", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]Option, 0, len(resp))
	for _, p := range resp {
		out = append(out, Option{ID: p.ID, Name: p.Name})
	}
	c.mu.Lock()
	c.priorities = out
	c.mu.Unlock()
	return out, nil
}

// SetPriority sets the issue's priority and invalidates the cache.
func (c *Client) SetPriority(ctx context.Context, key, priorityID string) error {
	if !c.Enabled() {
		return errNotConfigured
	}
	body := map[string]any{"fields": map[string]any{"priority": map[string]string{"id": priorityID}}}
	path := "/rest/api/3/issue/" + url.PathEscape(key)
	if err := c.do(ctx, http.MethodPut, path, key, body, nil); err != nil {
		return err
	}
	c.Invalidate(key)
	return nil
}

// AssignableUsers returns users who can be assigned to the issue, optionally
// narrowed by query (matched server-side against name/email). An empty query
// returns the default page. Jira caps the response (50 by default), so a large
// project must search rather than rely on the first page.
func (c *Client) AssignableUsers(ctx context.Context, key, query string) ([]User, error) {
	if !c.Enabled() {
		return nil, errNotConfigured
	}
	var resp []struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
	}
	path := "/rest/api/3/user/assignable/search?issueKey=" + url.QueryEscape(key)
	if q := strings.TrimSpace(query); q != "" {
		path += "&query=" + url.QueryEscape(q)
	}
	if err := c.do(ctx, http.MethodGet, path, key, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(resp))
	for _, u := range resp {
		out = append(out, User{AccountID: u.AccountID, DisplayName: u.DisplayName})
	}
	return out, nil
}

// Myself returns the authenticated account (for "Assign to me"), cached.
func (c *Client) Myself(ctx context.Context) (User, error) {
	if !c.Enabled() {
		return User{}, errNotConfigured
	}
	c.mu.Lock()
	if c.myself != nil {
		u := *c.myself
		c.mu.Unlock()
		return u, nil
	}
	c.mu.Unlock()

	var resp struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/myself", "current user", nil, &resp); err != nil {
		return User{}, err
	}
	u := User{AccountID: resp.AccountID, DisplayName: resp.DisplayName}
	c.mu.Lock()
	c.myself = &u
	c.mu.Unlock()
	return u, nil
}

// SetAssignee assigns the issue to accountID, or unassigns it when accountID is
// "" (sends accountId:null). Invalidates the cache.
func (c *Client) SetAssignee(ctx context.Context, key, accountID string) error {
	if !c.Enabled() {
		return errNotConfigured
	}
	body := map[string]any{"accountId": nil}
	if accountID != "" {
		body["accountId"] = accountID
	}
	path := "/rest/api/3/issue/" + url.PathEscape(key) + "/assignee"
	if err := c.do(ctx, http.MethodPut, path, key, body, nil); err != nil {
		return err
	}
	c.Invalidate(key)
	return nil
}

// SetStoryPoints writes raw (a number, or "" to clear) to the issue's
// story-points custom field, then invalidates the cache. Errors clearly when no
// such field is detected or raw isn't numeric.
func (c *Client) SetStoryPoints(ctx context.Context, key, raw string) error {
	if !c.Enabled() {
		return errNotConfigured
	}
	fields := c.resolveStoryPointFields(ctx)
	if len(fields) == 0 {
		return fmt.Errorf("jira: no story-points field detected — set jira.story_points_field")
	}
	var value any // nil clears the field
	if raw = strings.TrimSpace(raw); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("jira: story points must be a number, got %q", raw)
		}
		value = v
	}
	body := map[string]any{"fields": map[string]any{fields[0]: value}}
	path := "/rest/api/3/issue/" + url.PathEscape(key)
	if err := c.do(ctx, http.MethodPut, path, key, body, nil); err != nil {
		return err
	}
	c.Invalidate(key)
	return nil
}

// AddComment posts text as a new comment on the issue, then invalidates the
// cache so the next Get includes it. text is plain (blank lines separate
// paragraphs, "> " lines become a blockquote — see textToADF); when mention is
// non-nil the comment opens with a real @mention of that user, which is what a
// reply uses to actually notify them.
func (c *Client) AddComment(ctx context.Context, key, text string, mention *Mention) error {
	if !c.Enabled() {
		return errNotConfigured
	}
	if strings.TrimSpace(text) == "" && mention == nil {
		return fmt.Errorf("jira: empty comment")
	}
	body := map[string]any{"body": textToADF(text, mention)}
	path := "/rest/api/3/issue/" + url.PathEscape(key) + "/comment"
	if err := c.do(ctx, http.MethodPost, path, key, body, nil); err != nil {
		return err
	}
	c.Invalidate(key)
	return nil
}
