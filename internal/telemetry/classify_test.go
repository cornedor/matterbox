package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

// TestClassify pins the mapping from an error onto the catalogue's outcome and
// class, one case per rule, plus the wrapped-sentinel path that has to win over
// any text match.
func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		name           string
		err            error
		outcome, class string
	}{
		{"nil", nil, "ok", ""},
		{"deadline", fmt.Errorf("fetch: %w", context.DeadlineExceeded), "timeout", "network"},
		{"cancelled", fmt.Errorf("fetch: %w", context.Canceled), "cancelled", ""},
		{"permission sentinel", fmt.Errorf("open: %w", fs.ErrPermission), "error", "permission"},
		{"missing sentinel", fmt.Errorf("open: %w", fs.ErrNotExist), "error", "not_found"},
		{"cobra usage", errors.New(`unknown flag: --nope`), "error", "config"},
		{"unauthorized", errors.New("401 unauthorized"), "error", "auth"},
		{"forbidden", errors.New("403 forbidden"), "error", "permission"},
		{"missing channel", errors.New("no such channel"), "error", "not_found"},
		{"rate limited", errors.New("429 rate limit exceeded"), "error", "rate_limited"},
		{"dial", errors.New("dial tcp 10.0.0.1:443: connection refused"), "error", "network"},
		{"server", errors.New("502 bad gateway: internal server error"), "error", "server"},
		{"yaml", errors.New("yaml: line 3: did not find expected key"), "error", "parse"},
		{"unknown", errors.New("something nobody anticipated"), "error", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome, class := Classify(tc.err)
			if outcome != tc.outcome || class != tc.class {
				t.Errorf("Classify(%v) = (%q, %q), want (%q, %q)",
					tc.err, outcome, class, tc.outcome, tc.class)
			}
		})
	}
}

// TestTokenInMessageIsNotAlwaysAuth guards the ordering in Classify. The auth
// rule matches the bare word "token", which several errors carry without being
// login problems — and because auth is an environment class, misfiling one
// there does not merely mislabel it, it suppresses the issue entirely (see
// worthAnIssue). These are the real error strings from internal/auth and
// internal/githubauth that the ordering exists for.
func TestTokenInMessageIsNotAlwaysAuth(t *testing.T) {
	for _, tc := range []struct {
		err   string
		class string
	}{
		{"parse token file: unexpected end of JSON input", "parse"},
		{"write token file: no space left on device", "disk"},
		{"read token file: read-only file system", "disk"},
		// Still auth: these really are "you are not signed in".
		{"no token at /home/u/.config/matterbox/token — run `matterbox login` first", "auth"},
		{"token file /home/u/.config/matterbox/token has empty token", "auth"},
		{"token didn't authenticate against https://chat.example.com", "auth"},
	} {
		t.Run(tc.class+"/"+tc.err[:12], func(t *testing.T) {
			if got := ClassifyError(errors.New(tc.err)); got != tc.class {
				t.Errorf("ClassifyError(%q) = %q, want %q", tc.err, got, tc.class)
			}
			// The point of the distinction: a defect must reach the issue list.
			wantIssue := tc.class != "auth"
			if got := worthAnIssue(tc.class); got != wantIssue {
				t.Errorf("worthAnIssue(%q) = %v, want %v", tc.class, got, wantIssue)
			}
		})
	}
}
