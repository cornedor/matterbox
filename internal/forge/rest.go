package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// RequestTimeout bounds a single HTTP call. Generous for a slow instance; a
// stalled server fails the call rather than hanging the UI's fetch goroutine.
const RequestTimeout = 20 * time.Second

// maxResponse caps how much of a response body is read, so a misconfigured
// base URL pointing at something enormous can't be pulled into memory.
const maxResponse = 8 << 20

// REST is the JSON-over-HTTP plumbing every provider shares: build a request
// against an API root, authenticate it, read the body, and turn a non-2xx into
// a message the panel can show. Providers differ in how they authenticate and
// in what paths they call, not in any of this.
type REST struct {
	root  string                // API root, no trailing slash
	label string                // forge name for error text ("gitlab")
	auth  func(r *http.Request) // sets the auth header(s) on every request
	http  *http.Client
}

// NewREST builds a REST client for an API root. label names the forge in error
// messages; auth sets whatever header the forge authenticates with.
func NewREST(root, label string, auth func(*http.Request)) *REST {
	return &REST{
		root:  strings.TrimRight(root, "/"),
		label: label,
		auth:  auth,
		http:  &http.Client{Timeout: RequestTimeout},
	}
}

// Root is the API root requests are made against.
func (r *REST) Root() string { return r.root }

// DoRaw performs an authenticated request to path (relative to the API root,
// beginning with "/" and optionally carrying a query string), sending body as
// JSON when non-nil, and returns the raw response body. A non-2xx status becomes
// an error labelled by what — a change-request reference, or an action name like
// "approve".
func (r *REST) DoRaw(ctx context.Context, method, path, what string, body any) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, r.root+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.auth != nil {
		r.auth(req)
	}

	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", r.label, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, StatusError(r.label, resp.StatusCode, what, respBody)
	}
	return respBody, nil
}

// Do is DoRaw plus a JSON decode into out (pass nil to skip it, e.g. for an
// empty mutation response).
func (r *REST) Do(ctx context.Context, method, path, what string, body, out any) error {
	respBody, err := r.DoRaw(ctx, method, path, what, body)
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

// StatusErr is a non-2xx response from a forge. Providers return it so a caller
// can act on the status — a 404 in particular, which is ambiguous enough to be
// worth a second look (see the github provider's issue-vs-pull-request check).
type StatusErr struct {
	Label string // forge name, e.g. "github"
	Code  int    // HTTP status
	What  string // what was being fetched: a reference, or an action name
	Msg   string // the forge's own message, when it sent one
}

// StatusError turns a non-2xx into an error carrying a message the panel can
// show, special-casing the cases a user can act on. Both GitLab and GitHub answer
// with a {"message": …} body; we surface it when present, since it carries the
// reason a merge or approve was refused.
func StatusError(label string, code int, what string, body []byte) error {
	e := &StatusErr{Label: label, Code: code, What: what, Msg: APIMessage(body)}
	if e.Msg == "" {
		if txt := strings.TrimSpace(string(body)); txt != "" {
			if len(txt) > 200 {
				txt = txt[:200] + "…"
			}
			e.Msg = txt
		}
	}
	return e
}

func (e *StatusErr) Error() string {
	switch e.Code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("%s: not authorized for %s — check token / scopes", e.Label, e.What)
	case http.StatusNotFound:
		return fmt.Sprintf("%s: %s not found (or no access)", e.Label, e.What)
	}
	if e.Msg != "" {
		return fmt.Sprintf("%s %s: %s", e.Label, e.What, e.Msg)
	}
	return fmt.Sprintf("%s server %d: %s", e.Label, e.Code, http.StatusText(e.Code))
}

// APIMessage extracts a human message from a forge error body. GitLab sends
// {"message": "..."} or {"message": {"field": ["..."]}}; GitHub sends
// {"message": "...", "errors": [...]}. "error" covers GitLab's OAuth-style
// replies.
func APIMessage(body []byte) string {
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

// WorstStatus collapses a set of jobs into one status, worst-wins, so a group
// header reflects a failing or running job even when it's below the collapsed
// cut. Severity: failed > running > pending > success > manual > canceled >
// skipped; no jobs at all reports success.
func WorstStatus(jobs []Job) string {
	rank := map[string]int{
		StatusFailed:   7,
		StatusRunning:  6,
		StatusPending:  5,
		StatusSuccess:  4,
		StatusManual:   3,
		StatusCanceled: 2,
		StatusSkipped:  1,
	}
	worst, worstRank := StatusSuccess, 0
	for _, j := range jobs {
		r, ok := rank[j.Status]
		if !ok {
			r = rank[StatusPending] // unclassified ~ in-progress, not success
		}
		if r > worstRank {
			worst, worstRank = j.Status, r
		}
	}
	return worst
}
