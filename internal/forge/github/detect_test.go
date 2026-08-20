package github

import (
	"fmt"
	"reflect"
	"testing"

	"matterbox/internal/forge"
)

// short renders a detected ref the way GitHub writes it, for readable failures.
func short(r forge.Ref) string { return fmt.Sprintf("%s#%d", r.Repo, r.Number) }

func TestRefsURL(t *testing.T) {
	got := Refs("see https://github.com/anthropics/claude-code/pull/42 for the fix", "https://github.com")
	if len(got) != 1 {
		t.Fatalf("got %d refs, want 1: %v", len(got), got)
	}
	if got[0].Repo != "anthropics/claude-code" || got[0].Number != 42 {
		t.Errorf("ref = %+v, want anthropics/claude-code#42", got[0])
	}
}

func TestRefsURLWithTail(t *testing.T) {
	// A link copied from the Files-changed tab still names the PR.
	got := Refs("https://github.com/o/r/pull/7/files#diff-abc", "https://github.com")
	if len(got) != 1 || short(got[0]) != "o/r#7" {
		t.Errorf("refs = %v, want o/r#7", got)
	}
}

func TestRefsURLWrongHost(t *testing.T) {
	// An Enterprise-configured panel must not answer for github.com links.
	if got := Refs("https://github.com/o/r/pull/7", "https://git.example.com"); len(got) != 0 {
		t.Errorf("foreign-host URL should not match: %v", got)
	}
}

func TestRefsIssueURL(t *testing.T) {
	got := Refs("see https://github.com/o/r/issues/7 please", "https://github.com")
	if len(got) != 1 || short(got[0]) != "o/r#7" {
		t.Errorf("issue link = %v, want o/r#7", got)
	}
}

func TestRefsShortForm(t *testing.T) {
	got := Refs("blocked by anthropics/claude-code#2577 now", "https://github.com")
	if len(got) != 1 || short(got[0]) != "anthropics/claude-code#2577" {
		t.Fatalf("short ref = %v, want anthropics/claude-code#2577", got)
	}
}

func TestRefsShortFormNeedsOwner(t *testing.T) {
	// A bare "#3" is GitHub's in-repo form, but a chat message has no repo
	// context — and "#3" in prose would fire constantly.
	if got := Refs("see #3 for details", "https://github.com"); len(got) != 0 {
		t.Errorf("bare #N should not match: %v", got)
	}
	// A markdown heading must not read as a ref either.
	if got := Refs("# 3 things", "https://github.com"); len(got) != 0 {
		t.Errorf("heading should not match: %v", got)
	}
}

func TestRefsDedupAndOrder(t *testing.T) {
	text := "first a/b#1 then https://github.com/o/r/pull/9 and again o/r#9"
	got := Refs(text, "https://github.com")
	want := []string{"a/b#1", "o/r#9"}
	var names []string
	for _, r := range got {
		names = append(names, short(r))
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("refs = %v, want %v (deduped, in appearance order)", names, want)
	}
}

func TestRefsEmptyBaseURLStillFindsShortRefs(t *testing.T) {
	got := Refs("o/r#5 and https://github.com/o/r/pull/6", "")
	if len(got) != 1 || short(got[0]) != "o/r#5" {
		t.Errorf("with empty baseURL want only the short ref o/r#5, got %v", got)
	}
}
