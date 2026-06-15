// Package gitlab fetches merge-request detail from a GitLab instance so the TUI
// can show it inline in a side panel (press the open-reference key on a message
// that links a merge request — see internal/ui). Like internal/jira it has no
// dependency on the UI or store packages so it can be unit-tested against an
// httptest server with no real instance.
//
// Authentication is a personal/project access token sent in the PRIVATE-TOKEN
// header. Descriptions come back as GitLab-flavored markdown, which the UI
// renders with its shared markdown renderer (no conversion needed, unlike
// Jira's ADF).
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// errNotConfigured is returned by every call when the client lacks a base URL
// or token (Enabled would report false).
var errNotConfigured = fmt.Errorf("gitlab: not configured (need base_url and token)")

// requestTimeout bounds a single HTTP call. Generous for a slow instance; a
// stalled server fails the call rather than hanging the UI's fetch goroutine.
const requestTimeout = 20 * time.Second

// mrFields is the field list requested from the API. The default response is
// already compact, but naming fields keeps the decode predictable.
const mrFields = "title,state,draft,author,source_branch,target_branch,assignees," +
	"reviewers,labels,changes_count,detailed_merge_status,has_conflicts,description," +
	"web_url,updated_at,head_pipeline,project_id"

// Config is the subset of user config this package needs. BaseURL is the
// instance root (https://git.example.com); Token is a personal/project access
// token with at least read_api (api for approve/merge).
type Config struct {
	BaseURL string
	Token   string
}

// Client fetches and caches merge requests for one instance. The zero value is
// not usable; use New. Safe for concurrent use.
type Client struct {
	baseURL string // trimmed of any trailing slash
	token   string
	http    *http.Client

	mu    sync.Mutex
	cache map[string]*MR // key: "project!iid"
}

// New builds a Client from cfg. The returned client is always non-nil; call
// Enabled to see whether it has enough configuration to actually fetch.
func New(cfg Config) *Client {
	return &Client{
		baseURL: normalizeBaseURL(cfg.BaseURL),
		token:   strings.TrimSpace(cfg.Token),
		http:    &http.Client{Timeout: requestTimeout},
		cache:   map[string]*MR{},
	}
}

// normalizeBaseURL trims a base URL and supplies a default https:// scheme when
// it's missing, so a bare host like "git.example.com" still parses to a host
// (url.Parse treats a scheme-less string as a path) for link recognition and
// produces working API/browse URLs.
func normalizeBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return raw
}

// Enabled reports whether the client has a base URL and token — i.e. whether a
// fetch can succeed. The UI uses it to decide whether to offer the panel.
func (c *Client) Enabled() bool {
	return c != nil && c.baseURL != "" && c.token != ""
}

// BaseURL returns the configured instance root (no trailing slash). Used by
// Refs to recognise merge-request links pointing at this instance.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

// WebURL returns the human merge-request URL (what `o` opens in a browser).
func (c *Client) WebURL(project string, iid int) string {
	if c == nil || c.baseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/-/merge_requests/%d", c.baseURL, project, iid)
}

// MR is the flattened, render-ready form of a merge request. Description is
// GitLab markdown; the rest are plain strings/slices ready to label.
type MR struct {
	Project             string
	IID                 int
	Title               string
	State               string // opened / merged / closed / locked
	Draft               bool
	Author              string
	SourceBranch        string
	TargetBranch        string
	Assignees           []string
	Reviewers           []string
	Labels              []string
	ChangesCount        string // "44", or "44+" when the API caps it
	DetailedMergeStatus string // mergeable, conflict, ci_still_running, …
	HasConflicts        bool
	Description         string
	WebURL              string
	UpdatedAt           time.Time

	Pipeline  *Pipeline  // nil when the MR has no pipeline
	Approvals *Approvals // nil when approvals couldn't be read (best-effort)
}

// Mergeable reports whether GitLab would accept a merge right now — the gate
// for offering the merge action.
func (m *MR) Mergeable() bool {
	return m != nil && m.DetailedMergeStatus == "mergeable"
}

// Pipeline is the head pipeline's overall status plus its per-stage jobs.
type Pipeline struct {
	ID       int
	Status   string // raw status: success / failed / running / canceled / …
	Label    string // human label from detailed_status, e.g. "passed"
	WebURL   string
	Duration int     // seconds, 0 when unknown
	Stages   []Stage // empty when the jobs list couldn't be read
}

// Stage groups the jobs that share a stage name, in pipeline order.
type Stage struct {
	Name string
	Jobs []Job
}

// Job is one CI job's name and status.
type Job struct {
	Name   string
	Status string // success / failed / running / skipped / manual / …
}

// Approvals summarizes the MR's approval state.
type Approvals struct {
	Approved bool
	Required int
	Left     int
	By       []string
}

// apiMR mirrors the slice of the REST response we read.
type apiMR struct {
	IID                 int       `json:"iid"`
	ProjectID           int       `json:"project_id"`
	Title               string    `json:"title"`
	State               string    `json:"state"`
	Draft               bool      `json:"draft"`
	SourceBranch        string    `json:"source_branch"`
	TargetBranch        string    `json:"target_branch"`
	Labels              []string  `json:"labels"`
	ChangesCount        string    `json:"changes_count"`
	DetailedMergeStatus string    `json:"detailed_merge_status"`
	HasConflicts        bool      `json:"has_conflicts"`
	Description         string    `json:"description"`
	WebURL              string    `json:"web_url"`
	UpdatedAt           string    `json:"updated_at"`
	Author              *apiUser  `json:"author"`
	Assignees           []apiUser `json:"assignees"`
	Reviewers           []apiUser `json:"reviewers"`
	HeadPipeline        *struct {
		ID             int    `json:"id"`
		Status         string `json:"status"`
		WebURL         string `json:"web_url"`
		Duration       int    `json:"duration"`
		DetailedStatus *struct {
			Label string `json:"label"`
		} `json:"detailed_status"`
	} `json:"head_pipeline"`
}

type apiUser struct {
	Name     string `json:"name"`
	Username string `json:"username"`
}

// Get returns the merge request, serving a cached copy when present. It makes
// up to three calls: the MR itself (required), its pipeline jobs and its
// approvals (both best-effort — a failure leaves those sections empty rather
// than failing the whole fetch). Use Invalidate (then Get) to force a refetch.
func (c *Client) Get(ctx context.Context, project string, iid int) (*MR, error) {
	if !c.Enabled() {
		return nil, errNotConfigured
	}
	cacheKey := strings.ToLower(project) + "!" + strconv.Itoa(iid)
	c.mu.Lock()
	if hit, ok := c.cache[cacheKey]; ok {
		c.mu.Unlock()
		return hit, nil
	}
	c.mu.Unlock()

	mr, err := c.fetch(ctx, project, iid)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cache[cacheKey] = mr
	c.mu.Unlock()
	return mr, nil
}

// Invalidate drops any cached copy of the MR so the next Get refetches.
func (c *Client) Invalidate(project string, iid int) {
	if c == nil {
		return
	}
	key := strings.ToLower(project) + "!" + strconv.Itoa(iid)
	c.mu.Lock()
	delete(c.cache, key)
	c.mu.Unlock()
}

func (c *Client) fetch(ctx context.Context, project string, iid int) (*MR, error) {
	base := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d", encodePath(project), iid)
	body, err := c.doRaw(ctx, http.MethodGet, base+"?fields="+mrFields, project, nil)
	if err != nil {
		return nil, err
	}
	var a apiMR
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, fmt.Errorf("decode merge request: %w", err)
	}
	mr := toMR(a, project)
	mr.WebURL = c.WebURL(project, iid)

	// Pipeline jobs (best-effort): a per-job breakdown for the panel.
	if a.HeadPipeline != nil && a.HeadPipeline.ID != 0 && a.ProjectID != 0 {
		if stages, jerr := c.pipelineStages(ctx, a.ProjectID, a.HeadPipeline.ID); jerr == nil {
			mr.Pipeline.Stages = stages
		}
	}
	// Approvals (best-effort): may be disabled on the instance/project.
	if ap, aerr := c.approvals(ctx, project, iid); aerr == nil {
		mr.Approvals = ap
	}
	return mr, nil
}

// toMR flattens the API response into the render-ready MR (minus the URL and
// the best-effort pipeline jobs / approvals the caller fills in).
func toMR(a apiMR, project string) *MR {
	mr := &MR{
		Project:             project,
		IID:                 a.IID,
		Title:               a.Title,
		State:               a.State,
		Draft:               a.Draft,
		SourceBranch:        a.SourceBranch,
		TargetBranch:        a.TargetBranch,
		Labels:              a.Labels,
		ChangesCount:        a.ChangesCount,
		DetailedMergeStatus: a.DetailedMergeStatus,
		HasConflicts:        a.HasConflicts,
		Description:         a.Description,
	}
	if a.Author != nil {
		mr.Author = a.Author.Name
	}
	for _, u := range a.Assignees {
		mr.Assignees = append(mr.Assignees, u.Name)
	}
	for _, u := range a.Reviewers {
		mr.Reviewers = append(mr.Reviewers, u.Name)
	}
	if t, err := time.Parse(time.RFC3339, a.UpdatedAt); err == nil {
		mr.UpdatedAt = t
	}
	if hp := a.HeadPipeline; hp != nil {
		p := &Pipeline{ID: hp.ID, Status: hp.Status, WebURL: hp.WebURL, Duration: hp.Duration}
		if hp.DetailedStatus != nil {
			p.Label = hp.DetailedStatus.Label
		}
		mr.Pipeline = p
	}
	return mr
}

// pipelineStages fetches the pipeline's jobs and groups them by stage, in the
// order the stages first appear in the response.
func (c *Client) pipelineStages(ctx context.Context, projectID, pipelineID int) ([]Stage, error) {
	path := fmt.Sprintf("/api/v4/projects/%d/pipelines/%d/jobs?per_page=100", projectID, pipelineID)
	var jobs []struct {
		Name   string `json:"name"`
		Stage  string `json:"stage"`
		Status string `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, path, "pipeline jobs", nil, &jobs); err != nil {
		return nil, err
	}
	var order []string
	byStage := map[string][]Job{}
	for _, j := range jobs {
		if _, ok := byStage[j.Stage]; !ok {
			order = append(order, j.Stage)
		}
		byStage[j.Stage] = append(byStage[j.Stage], Job{Name: j.Name, Status: j.Status})
	}
	stages := make([]Stage, 0, len(order))
	for _, name := range order {
		stages = append(stages, Stage{Name: name, Jobs: byStage[name]})
	}
	return stages, nil
}

// approvals fetches the MR's approval summary.
func (c *Client) approvals(ctx context.Context, project string, iid int) (*Approvals, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/approvals", encodePath(project), iid)
	var resp struct {
		Approved          bool `json:"approved"`
		ApprovalsRequired int  `json:"approvals_required"`
		ApprovalsLeft     int  `json:"approvals_left"`
		ApprovedBy        []struct {
			User apiUser `json:"user"`
		} `json:"approved_by"`
	}
	if err := c.do(ctx, http.MethodGet, path, "approvals", nil, &resp); err != nil {
		return nil, err
	}
	ap := &Approvals{Approved: resp.Approved, Required: resp.ApprovalsRequired, Left: resp.ApprovalsLeft}
	for _, a := range resp.ApprovedBy {
		ap.By = append(ap.By, a.User.Name)
	}
	return ap, nil
}

// Approve approves the merge request, then invalidates the cache so the next
// Get reflects the new approval state.
func (c *Client) Approve(ctx context.Context, project string, iid int) error {
	if !c.Enabled() {
		return errNotConfigured
	}
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/approve", encodePath(project), iid)
	if err := c.do(ctx, http.MethodPost, path, "approve", nil, nil); err != nil {
		return err
	}
	c.Invalidate(project, iid)
	return nil
}

// Merge merges the merge request, then invalidates the cache. removeBranch asks
// GitLab to delete the source branch after merge.
func (c *Client) Merge(ctx context.Context, project string, iid int, removeBranch bool) error {
	if !c.Enabled() {
		return errNotConfigured
	}
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/merge", encodePath(project), iid)
	body := map[string]any{"should_remove_source_branch": removeBranch}
	if err := c.do(ctx, http.MethodPut, path, "merge", body, nil); err != nil {
		return err
	}
	c.Invalidate(project, iid)
	return nil
}

// encodePath URL-encodes a project path for the API, turning the namespace
// slashes into %2F (e.g. group/project → group%2Fproject) as GitLab requires.
func encodePath(project string) string {
	return strings.ReplaceAll(project, "/", "%2F")
}

// doRaw performs an authenticated request to path (relative to baseURL, which
// must begin with "/" and may carry a query string), sending body as JSON when
// non-nil, and returns the raw response body. A non-2xx status becomes a
// statusError labelled by what (an MR ref, or e.g. "approve").
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
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call gitlab: %w", err)
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

// do is doRaw plus JSON decode into out (skip with out=nil, e.g. an empty
// mutation response).
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
// the cases a user can act on. GitLab returns a {"message": …} body; we surface
// it when present (it carries the reason a merge/approve was refused).
func statusError(code int, what string, body []byte) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("gitlab: not authorized for %s — check token / scopes", what)
	case http.StatusNotFound:
		return fmt.Errorf("gitlab: %s not found (or no access)", what)
	}
	if msg := apiMessage(body); msg != "" {
		return fmt.Errorf("gitlab %s: %s", what, msg)
	}
	if txt := strings.TrimSpace(string(body)); txt != "" {
		if len(txt) > 200 {
			txt = txt[:200] + "…"
		}
		return fmt.Errorf("gitlab server %d: %s", code, txt)
	}
	return fmt.Errorf("gitlab server %d: %s", code, http.StatusText(code))
}

// apiMessage extracts a human message from a GitLab error body, which is either
// {"message": "..."} or {"message": {"field": ["..."]}} (or "error").
func apiMessage(body []byte) string {
	var probe struct {
		Message json.RawMessage `json:"message"`
		Error   string          `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	if probe.Error != "" {
		return probe.Error
	}
	if len(probe.Message) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(probe.Message, &s) == nil {
		return s
	}
	var fields map[string][]string
	if json.Unmarshal(probe.Message, &fields) == nil {
		var parts []string
		for k, v := range fields {
			parts = append(parts, k+": "+strings.Join(v, ", "))
		}
		sort.Strings(parts)
		return strings.Join(parts, "; ")
	}
	return strings.Trim(string(probe.Message), `"`)
}
