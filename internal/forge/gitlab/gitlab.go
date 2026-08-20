// Package gitlab is the GitLab implementation of forge.Provider: it fetches
// merge-request detail from a GitLab instance so the TUI can show it inline in
// the reference side panel (press the open-reference key on a message that links
// a merge request — see internal/ui). Like internal/jira it has no dependency on
// the UI or store packages, so it can be unit-tested against an httptest server
// with no real instance.
//
// Authentication is a personal/project access token sent in the PRIVATE-TOKEN
// header. Descriptions come back as GitLab-flavored markdown, which the UI
// renders with its shared markdown renderer (no conversion needed, unlike
// Jira's ADF).
package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"matterbox/internal/forge"
)

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
	rest    *forge.REST
	cache   forge.Cache
}

// Client is the GitLab half of the panel's provider set.
var _ forge.Provider = (*Client)(nil)

// New builds a Client from cfg. The returned client is always non-nil; call
// Enabled to see whether it has enough configuration to actually fetch.
func New(cfg Config) *Client {
	base := forge.NormalizeBaseURL(cfg.BaseURL)
	token := strings.TrimSpace(cfg.Token)
	return &Client{
		baseURL: base,
		token:   token,
		rest: forge.NewREST(base+"/api/v4", "gitlab", func(r *http.Request) {
			r.Header.Set("PRIVATE-TOKEN", token)
		}),
	}
}

// Name is the forge's display name, used as the panel title.
func (c *Client) Name() string { return "GitLab" }

// Noun is what GitLab calls a change request.
func (c *Client) Noun() string { return "merge request" }

// Sigil is the separator in GitLab's short reference form (group/project!12).
func (c *Client) Sigil() string { return "!" }

// ChecksHeading: GitLab calls its CI a pipeline, and the panel says so.
func (c *Client) ChecksHeading() string { return "Pipeline" }

// Enabled reports whether the client has a base URL and token — i.e. whether a
// fetch can succeed. The UI uses it to decide whether to offer the panel.
func (c *Client) Enabled() bool {
	return c != nil && c.baseURL != "" && c.token != ""
}

// BaseURL returns the configured instance root (no trailing slash). Used to
// recognise merge-request links pointing at this instance.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

// Refs extracts the merge requests named in text, resolved against this
// instance. See detect.go for the forms recognised.
func (c *Client) Refs(text string) []forge.Ref {
	return Refs(text, c.BaseURL())
}

// WebURL returns the human merge-request URL (what the browser key opens).
func (c *Client) WebURL(project string, iid int) string {
	if c == nil || c.baseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/-/merge_requests/%d", c.baseURL, project, iid)
}

// MergeMethods: GitLab merges one way through the API (its squash option is a
// per-MR setting rather than a merge strategy), so the UI asks a plain yes/no
// rather than offering a choice.
func (c *Client) MergeMethods() []forge.MergeMethod {
	return []forge.MergeMethod{{ID: "", Label: "merge", Key: "m"}}
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
func (c *Client) Get(ctx context.Context, project string, iid int) (*forge.Change, error) {
	if !c.Enabled() {
		return nil, forge.ErrNotConfigured
	}
	if hit, ok := c.cache.Get(project, iid); ok {
		return hit, nil
	}
	mr, err := c.fetch(ctx, project, iid)
	if err != nil {
		return nil, err
	}
	c.cache.Put(project, iid, mr)
	return mr, nil
}

// Invalidate drops any cached copy of the MR so the next Get refetches.
func (c *Client) Invalidate(project string, iid int) {
	if c == nil {
		return
	}
	c.cache.Invalidate(project, iid)
}

func (c *Client) fetch(ctx context.Context, project string, iid int) (*forge.Change, error) {
	label := forge.Label(c, project, iid)
	base := fmt.Sprintf("/projects/%s/merge_requests/%d", encodePath(project), iid)
	body, err := c.rest.DoRaw(ctx, http.MethodGet, base+"?fields="+mrFields, label, nil)
	if err != nil {
		return nil, err
	}
	var a apiMR
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, fmt.Errorf("decode merge request: %w", err)
	}
	mr := toChange(a, project)
	mr.WebURL = c.WebURL(project, iid)

	// Pipeline jobs (best-effort): a per-job breakdown for the panel.
	if a.HeadPipeline != nil && a.HeadPipeline.ID != 0 && a.ProjectID != 0 {
		if groups, jerr := c.pipelineGroups(ctx, a.ProjectID, a.HeadPipeline.ID); jerr == nil {
			mr.Checks.Groups = groups
		}
	}
	// Approvals (best-effort): may be disabled on the instance/project.
	if ap, aerr := c.approvals(ctx, project, iid); aerr == nil {
		mr.Approvals = ap
	}
	return mr, nil
}

// toChange flattens the API response into the render-ready change (minus the
// URL and the best-effort pipeline jobs / approvals the caller fills in).
func toChange(a apiMR, project string) *forge.Change {
	ch := &forge.Change{
		Repo:         project,
		Number:       a.IID,
		Title:        a.Title,
		State:        a.State,
		Draft:        a.Draft,
		SourceBranch: a.SourceBranch,
		TargetBranch: a.TargetBranch,
		Labels:       a.Labels,
		ChangesCount: a.ChangesCount,
		HasConflicts: a.HasConflicts,
		Description:  a.Description,
		Mergeable:    a.DetailedMergeStatus == "mergeable",
	}
	ch.MergeStatus = mergeStatusText(a)
	if a.Author != nil {
		ch.Author = a.Author.Name
	}
	for _, u := range a.Assignees {
		ch.Assignees = append(ch.Assignees, u.Name)
	}
	for _, u := range a.Reviewers {
		ch.Reviewers = append(ch.Reviewers, u.Name)
	}
	if t, err := time.Parse(time.RFC3339, a.UpdatedAt); err == nil {
		ch.UpdatedAt = t
	}
	if hp := a.HeadPipeline; hp != nil {
		checks := &forge.Checks{
			Status:   normStatus(hp.Status),
			WebURL:   hp.WebURL,
			Duration: hp.Duration,
		}
		if hp.DetailedStatus != nil {
			checks.Label = hp.DetailedStatus.Label
		}
		if checks.Label == "" {
			checks.Label = hp.Status
		}
		ch.Checks = checks
	}
	return ch
}

// mergeStatusText is the merge-readiness phrase the panel shows: "mergeable", or
// the humanized blocking reason (ci_still_running → "ci still running") plus an
// explicit conflicts note when GitLab flags one that the reason doesn't mention.
func mergeStatusText(a apiMR) string {
	if a.DetailedMergeStatus == "mergeable" {
		return "mergeable"
	}
	txt := strings.ReplaceAll(a.DetailedMergeStatus, "_", " ")
	if txt == "" {
		txt = "not mergeable"
	}
	if a.HasConflicts && !strings.Contains(txt, "conflict") {
		txt += " · conflicts"
	}
	return txt
}

// pipelineGroups fetches the pipeline's jobs and groups them by stage, in the
// order the stages first appear in the response.
func (c *Client) pipelineGroups(ctx context.Context, projectID, pipelineID int) ([]forge.Group, error) {
	path := fmt.Sprintf("/projects/%d/pipelines/%d/jobs?per_page=100", projectID, pipelineID)
	var jobs []struct {
		Name   string `json:"name"`
		Stage  string `json:"stage"`
		Status string `json:"status"`
	}
	if err := c.rest.Do(ctx, http.MethodGet, path, "pipeline jobs", nil, &jobs); err != nil {
		return nil, err
	}
	var order []string
	byStage := map[string][]forge.Job{}
	for _, j := range jobs {
		if _, ok := byStage[j.Stage]; !ok {
			order = append(order, j.Stage)
		}
		byStage[j.Stage] = append(byStage[j.Stage], forge.Job{Name: j.Name, Status: normStatus(j.Status)})
	}
	groups := make([]forge.Group, 0, len(order))
	for _, name := range order {
		groups = append(groups, forge.Group{Name: name, Jobs: byStage[name]})
	}
	return groups, nil
}

// normStatus maps a GitLab job/pipeline status onto the shared vocabulary, so
// the UI needs one glyph table rather than one per forge.
func normStatus(s string) string {
	switch s {
	case "success", "passed":
		return forge.StatusSuccess
	case "failed":
		return forge.StatusFailed
	case "running":
		return forge.StatusRunning
	case "canceled", "canceling", "cancelled":
		return forge.StatusCanceled
	case "skipped":
		return forge.StatusSkipped
	case "manual":
		return forge.StatusManual
	default: // created / pending / scheduled / preparing / waiting_for_resource
		return forge.StatusPending
	}
}

// approvals fetches the MR's approval summary.
func (c *Client) approvals(ctx context.Context, project string, iid int) (*forge.Approvals, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/approvals", encodePath(project), iid)
	var resp struct {
		Approved          bool `json:"approved"`
		ApprovalsRequired int  `json:"approvals_required"`
		ApprovalsLeft     int  `json:"approvals_left"`
		ApprovedBy        []struct {
			User apiUser `json:"user"`
		} `json:"approved_by"`
	}
	if err := c.rest.Do(ctx, http.MethodGet, path, "approvals", nil, &resp); err != nil {
		return nil, err
	}
	ap := &forge.Approvals{Approved: resp.Approved, Required: resp.ApprovalsRequired, Left: resp.ApprovalsLeft}
	for _, a := range resp.ApprovedBy {
		ap.By = append(ap.By, a.User.Name)
	}
	return ap, nil
}

// Approve approves the merge request, then invalidates the cache so the next
// Get reflects the new approval state.
func (c *Client) Approve(ctx context.Context, project string, iid int) error {
	if !c.Enabled() {
		return forge.ErrNotConfigured
	}
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/approve", encodePath(project), iid)
	if err := c.rest.Do(ctx, http.MethodPost, path, "approve", nil, nil); err != nil {
		return err
	}
	c.Invalidate(project, iid)
	return nil
}

// Merge merges the merge request and asks GitLab to delete the source branch,
// then invalidates the cache. method is ignored: MergeMethods offers one way to
// merge, so there is nothing to choose.
func (c *Client) Merge(ctx context.Context, project string, iid int, method string) error {
	if !c.Enabled() {
		return forge.ErrNotConfigured
	}
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/merge", encodePath(project), iid)
	body := map[string]any{"should_remove_source_branch": true}
	if err := c.rest.Do(ctx, http.MethodPut, path, "merge", body, nil); err != nil {
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
