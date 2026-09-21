package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"matterbox/internal/forge"
)

// The review half of the GitLab provider: the merge request's full diff, the
// inline discussions already on it, and posting a new one. See internal/forge's
// diff.go for the shared types and the unified-diff parser; this file is the
// wire format and nothing else.

// Client implements the optional review half of the provider set.
var _ forge.Reviewer = (*Client)(nil)

const (
	// diffPerPage is the page size for the diffs endpoint (GitLab's maximum).
	diffPerPage = 100
	// diffMaxPages caps how much of a huge merge request we pull. A thousand
	// changed files is already past the point where reviewing it in a terminal
	// is the plan; the view says the diff was truncated rather than pretending
	// it is complete.
	diffMaxPages = 10
	// discussionMaxPages caps the inline-conversation fetch the same way.
	discussionMaxPages = 5
)

// Diff returns the merge request's full diff, serving a cached copy when it has
// one. Invalidate (which Approve/Merge already call) drops it along with the
// change itself.
func (c *Client) Diff(ctx context.Context, project string, iid int) (*forge.Diff, error) {
	if !c.Enabled() {
		return nil, forge.ErrNotConfigured
	}
	if hit, ok := c.diffs.Get(project, iid); ok {
		return hit, nil
	}
	d, err := c.fetchDiff(ctx, project, iid)
	if err != nil {
		return nil, err
	}
	c.diffs.Put(project, iid, d)
	return d, nil
}

// fetchDiff reads the diff refs off the merge request, then pages the diffs
// endpoint. On an instance too old for that endpoint (it arrived in GitLab
// 15.7) it falls back to the deprecated /changes, which answers with the whole
// diff — capped by the server — in one call.
func (c *Client) fetchDiff(ctx context.Context, project string, iid int) (*forge.Diff, error) {
	refs, err := c.diffRefs(ctx, project, iid)
	if err != nil {
		return nil, err
	}
	files, truncated, err := c.diffPages(ctx, project, iid)
	if err != nil {
		var se *forge.StatusErr
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return c.changesDiff(ctx, project, iid)
		}
		return nil, err
	}
	return &forge.Diff{Refs: refs, Files: files, Truncated: truncated}, nil
}

// apiDiffRefs is GitLab's diff_refs block: the three commits an inline note is
// positioned against.
type apiDiffRefs struct {
	BaseSHA  string `json:"base_sha"`
	StartSHA string `json:"start_sha"`
	HeadSHA  string `json:"head_sha"`
}

func (r apiDiffRefs) toRefs() forge.DiffRefs {
	return forge.DiffRefs{BaseSHA: r.BaseSHA, StartSHA: r.StartSHA, HeadSHA: r.HeadSHA}
}

// diffRefs fetches just the commit refs from the merge request.
func (c *Client) diffRefs(ctx context.Context, project string, iid int) (forge.DiffRefs, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d", encodePath(project), iid)
	var resp struct {
		DiffRefs apiDiffRefs `json:"diff_refs"`
	}
	if err := c.rest.Do(ctx, http.MethodGet, path, forge.Label(c, project, iid), nil, &resp); err != nil {
		return forge.DiffRefs{}, err
	}
	return resp.DiffRefs.toRefs(), nil
}

// apiFileDiff is one entry of the diffs / changes payload.
type apiFileDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	Diff        string `json:"diff"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
	Generated   bool   `json:"generated_file"`
}

func (a apiFileDiff) toFileDiff() forge.FileDiff {
	return forge.FileDiff{
		OldPath:   a.OldPath,
		NewPath:   a.NewPath,
		Diff:      a.Diff,
		New:       a.NewFile,
		Deleted:   a.DeletedFile,
		Renamed:   a.RenamedFile,
		Generated: a.Generated,
		// GitLab has no binary flag: a binary file arrives as git's own notice
		// in place of a hunk, which is also exactly what we want to show.
		Binary: strings.HasPrefix(a.Diff, "Binary files") || strings.HasPrefix(a.Diff, "GIT binary patch"),
	}
}

// diffPages walks the paginated diffs endpoint. A short page ends the walk; a
// full page at the cap means there is more, which the caller reports as a
// truncated diff.
func (c *Client) diffPages(ctx context.Context, project string, iid int) ([]forge.FileDiff, bool, error) {
	var files []forge.FileDiff
	for page := 1; page <= diffMaxPages; page++ {
		path := fmt.Sprintf("/projects/%s/merge_requests/%d/diffs?per_page=%d&page=%d",
			encodePath(project), iid, diffPerPage, page)
		var batch []apiFileDiff
		if err := c.rest.Do(ctx, http.MethodGet, path, "diff", nil, &batch); err != nil {
			return nil, false, err
		}
		for _, a := range batch {
			files = append(files, a.toFileDiff())
		}
		if len(batch) < diffPerPage {
			return files, false, nil
		}
	}
	return files, true, nil
}

// changesDiff is the pre-15.7 path: /changes hands over the diff refs and every
// file in one response, flagging its own truncation as "overflow".
func (c *Client) changesDiff(ctx context.Context, project string, iid int) (*forge.Diff, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/changes", encodePath(project), iid)
	var resp struct {
		DiffRefs apiDiffRefs   `json:"diff_refs"`
		Overflow bool          `json:"overflow"`
		Changes  []apiFileDiff `json:"changes"`
	}
	if err := c.rest.Do(ctx, http.MethodGet, path, "diff", nil, &resp); err != nil {
		return nil, err
	}
	d := &forge.Diff{Refs: resp.DiffRefs.toRefs(), Truncated: resp.Overflow}
	for _, a := range resp.Changes {
		d.Files = append(d.Files, a.toFileDiff())
	}
	return d, nil
}

// apiPosition is the anchor GitLab stores on an inline note.
type apiPosition struct {
	PositionType string `json:"position_type"`
	OldPath      string `json:"old_path"`
	NewPath      string `json:"new_path"`
	OldLine      int    `json:"old_line"`
	NewLine      int    `json:"new_line"`
}

// apiNote is one note of a discussion.
type apiNote struct {
	ID        int64        `json:"id"`
	Body      string       `json:"body"`
	System    bool         `json:"system"`
	Resolved  bool         `json:"resolved"`
	CreatedAt string       `json:"created_at"`
	Author    *apiUser     `json:"author"`
	Position  *apiPosition `json:"position"`
}

// Threads returns the merge request's conversations — the inline ones and the
// ones on the merge request as a whole, in GitLab's own order. System notes
// ("changed the title", "added 3 commits") are dropped: they are activity, not
// conversation, and nobody reviews them.
func (c *Client) Threads(ctx context.Context, project string, iid int) ([]forge.Thread, error) {
	if !c.Enabled() {
		return nil, forge.ErrNotConfigured
	}
	var out []forge.Thread
	for page := 1; page <= discussionMaxPages; page++ {
		path := fmt.Sprintf("/projects/%s/merge_requests/%d/discussions?per_page=100&page=%d",
			encodePath(project), iid, page)
		var batch []struct {
			ID    string    `json:"id"`
			Notes []apiNote `json:"notes"`
		}
		if err := c.rest.Do(ctx, http.MethodGet, path, "discussions", nil, &batch); err != nil {
			return nil, err
		}
		for _, d := range batch {
			if t, ok := toThread(d.ID, d.Notes); ok {
				out = append(out, t)
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	return out, nil
}

// toThread flattens one discussion, reporting false for the ones that carry no
// conversation at all (a lone system note). A discussion with no text position
// is kept, unanchored: that is the merge request's own comment thread.
func toThread(id string, notes []apiNote) (forge.Thread, bool) {
	if len(notes) == 0 {
		return forge.Thread{}, false
	}
	t := forge.Thread{ID: id, Resolved: notes[0].Resolved}
	if pos := notes[0].Position; pos != nil && pos.PositionType == "text" {
		t.Path, t.OldPath = pos.NewPath, pos.OldPath
		t.OldLine, t.NewLine = pos.OldLine, pos.NewLine
		if t.Path == "" {
			t.Path = pos.OldPath
		}
	}
	for _, n := range notes {
		if n.System {
			continue
		}
		note := forge.Note{ID: strconv.FormatInt(n.ID, 10), Body: n.Body}
		if n.Author != nil {
			note.Author = n.Author.Name
		}
		if ts, err := time.Parse(time.RFC3339, n.CreatedAt); err == nil {
			note.Created = ts
		}
		t.Notes = append(t.Notes, note)
	}
	if len(t.Notes) == 0 {
		return forge.Thread{}, false
	}
	return t, true
}

// AddNote posts an inline note: a reply when ReplyTo names a discussion,
// otherwise a new conversation anchored to the line. GitLab validates the
// position against the diff, so a note on a line the diff doesn't contain comes
// back as a 400 with its own message rather than landing somewhere wrong.
func (c *Client) AddNote(ctx context.Context, project string, iid int, n forge.NewNote) error {
	if !c.Enabled() {
		return forge.ErrNotConfigured
	}
	base := fmt.Sprintf("/projects/%s/merge_requests/%d/discussions", encodePath(project), iid)
	if n.ReplyTo != "" {
		body := map[string]any{"body": n.Body}
		return c.rest.Do(ctx, http.MethodPost, base+"/"+n.ReplyTo+"/notes", "reply", body, nil)
	}
	pos := map[string]any{
		"base_sha":      n.Refs.BaseSHA,
		"start_sha":     n.Refs.StartSHA,
		"head_sha":      n.Refs.HeadSHA,
		"position_type": "text",
		"new_path":      n.NewPath,
		"old_path":      n.OldPath,
	}
	// Exactly the sides the line exists on: an added line has no old number and
	// sending 0 for it is rejected.
	if n.OldLine > 0 {
		pos["old_line"] = n.OldLine
	}
	if n.NewLine > 0 {
		pos["new_line"] = n.NewLine
	}
	body := map[string]any{"body": n.Body, "position": pos}
	return c.rest.Do(ctx, http.MethodPost, base, "note", body, nil)
}

// ResolveThread resolves or reopens an inline conversation. GitLab only accepts
// this for a resolvable discussion, which every diff note is; the overall
// discussion is not, and the diff view never offers it one.
func (c *Client) ResolveThread(ctx context.Context, project string, iid int, threadID string, resolved bool) error {
	if !c.Enabled() {
		return forge.ErrNotConfigured
	}
	if threadID == "" {
		return errors.New("gitlab: no discussion to resolve")
	}
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/discussions/%s",
		encodePath(project), iid, threadID)
	what := "resolve"
	if !resolved {
		what = "reopen"
	}
	return c.rest.Do(ctx, http.MethodPut, path, what, map[string]any{"resolved": resolved}, nil)
}
