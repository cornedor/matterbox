package forge

import (
	"context"
	"reflect"
	"testing"
)

// stubProvider is an inert Provider whose only real answer is its sigil — a
// compile-time check that the interface is implementable outside its own
// subpackages, and enough to test Label.
type stubProvider struct{ sigil string }

var _ Provider = stubProvider{}

func (s stubProvider) Name() string              { return "Stub" }
func (s stubProvider) Noun() string              { return "change request" }
func (s stubProvider) ItemNouns() string         { return s.Noun() }
func (s stubProvider) Sigil() string             { return s.sigil }
func (s stubProvider) Icon() string              { return "" }
func (s stubProvider) ChecksHeading() string     { return "Checks" }
func (s stubProvider) Enabled() bool             { return true }
func (s stubProvider) AutoFetch() bool           { return true }
func (s stubProvider) BaseURL() string           { return "https://stub.test" }
func (s stubProvider) Refs(string) []Ref         { return nil }
func (s stubProvider) WebURL(string, int) string { return "" }
func (s stubProvider) Get(context.Context, string, int, string) (*Change, error) {
	return nil, nil
}
func (s stubProvider) Invalidate(string, int)                           {}
func (s stubProvider) Approve(context.Context, string, int) error       { return nil }
func (s stubProvider) MergeMethods() []MergeMethod                      { return nil }
func (s stubProvider) Merge(context.Context, string, int, string) error { return nil }

func TestLabelUsesTheForgeSigil(t *testing.T) {
	if got := Label(stubProvider{"!"}, "group/proj", 12); got != "group/proj!12" {
		t.Errorf("GitLab-style label = %q", got)
	}
	if got := Label(stubProvider{"#"}, "owner/repo", 12); got != "owner/repo#12" {
		t.Errorf("GitHub-style label = %q", got)
	}
	// A missing provider still produces something readable rather than "12".
	if got := Label(nil, "owner/repo", 12); got != "owner/repo#12" {
		t.Errorf("nil provider label = %q", got)
	}
}

func TestWorstStatusSeverityOrder(t *testing.T) {
	cases := []struct {
		name string
		jobs []Job
		want string
	}{
		{"failure dominates", []Job{{Status: StatusSuccess}, {Status: StatusFailed}, {Status: StatusRunning}}, StatusFailed},
		{"running over success", []Job{{Status: StatusSuccess}, {Status: StatusRunning}}, StatusRunning},
		{"pending over success", []Job{{Status: StatusSuccess}, {Status: StatusPending}}, StatusPending},
		{"success over skipped", []Job{{Status: StatusSuccess}, {Status: StatusSkipped}}, StatusSuccess},
		{"success over manual", []Job{{Status: StatusManual}, {Status: StatusSuccess}}, StatusSuccess},
		{"empty is success", nil, StatusSuccess},
		// An unclassified status must not be reported as a pass.
		{"unknown ranks as pending", []Job{{Status: StatusSuccess}, {Status: "who-knows"}}, "who-knows"},
	}
	for _, c := range cases {
		if got := WorstStatus(c.jobs); got != c.want {
			t.Errorf("%s: WorstStatus = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDedupeRefsKeepsEarliestPositionAndOrders(t *testing.T) {
	got := DedupeRefs([]Ref{
		{Repo: "o/r", Number: 9, Pos: 40}, // the URL form, later in the text
		{Repo: "a/b", Number: 1, Pos: 5},
		{Repo: "O/R", Number: 9, Pos: 12}, // the same PR, written earlier and cased differently
	})
	want := []Ref{
		{Repo: "a/b", Number: 1, Pos: 5},
		{Repo: "o/r", Number: 9, Pos: 12},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DedupeRefs = %+v, want %+v", got, want)
	}
}

func TestNormalizeBaseURLAndHostMatching(t *testing.T) {
	if got := NormalizeBaseURL(" git.example.com/ "); got != "https://git.example.com" {
		t.Errorf("NormalizeBaseURL = %q", got)
	}
	if got := NormalizeBaseURL(""); got != "" {
		t.Errorf("empty base URL should stay empty, got %q", got)
	}
	if HostOf("https://GIT.example.com/x") != "git.example.com" {
		t.Errorf("HostOf lowercases the host")
	}
	if !HostMatches("GIT.example.com", "git.example.com") {
		t.Error("host matching must be case-insensitive")
	}
	if HostMatches("git.example.com", "") {
		t.Error("an unconfigured host must match nothing")
	}
}

func TestCacheRoundTrip(t *testing.T) {
	var c Cache
	if _, ok := c.Get("o/r", 1); ok {
		t.Error("empty cache should miss")
	}
	ch := &Change{Repo: "o/r", Number: 1}
	c.Put("o/r", 1, ch)
	// Keys are case-insensitive, like the forges' own repo paths.
	if got, ok := c.Get("O/R", 1); !ok || got != ch {
		t.Error("cache should hit regardless of case")
	}
	c.Invalidate("o/r", 1)
	if _, ok := c.Get("o/r", 1); ok {
		t.Error("invalidated entry should be gone")
	}
}
