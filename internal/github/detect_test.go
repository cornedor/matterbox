package github

import "testing"

func TestRefsIssueURL(t *testing.T) {
	text := "see https://github.com/org/repo/issues/42 for details"
	refs := Refs(text, "https://github.com")
	if len(refs) != 1 {
		t.Fatalf("refs = %d, want 1", len(refs))
	}
	if refs[0].Repo != "org/repo" || refs[0].Number != 42 || refs[0].IsPull {
		t.Errorf("refs[0] = %+v, want org/repo#42 issue", refs[0])
	}
}

func TestRefsPullURL(t *testing.T) {
	text := "https://github.com/org/repo/pull/7"
	refs := Refs(text, "https://github.com")
	if len(refs) != 1 {
		t.Fatalf("refs = %d, want 1", len(refs))
	}
	if refs[0].Repo != "org/repo" || refs[0].Number != 7 || !refs[0].IsPull {
		t.Errorf("refs[0] = %+v, want org/repo#7 PR", refs[0])
	}
}

func TestRefsWrongHost(t *testing.T) {
	text := "https://gitlab.com/org/repo/-/issues/1"
	refs := Refs(text, "https://github.com")
	if len(refs) != 0 {
		t.Errorf("refs = %+v, want none for wrong host", refs)
	}
}

func TestRefsDedupesSameRef(t *testing.T) {
	text := "https://github.com/org/repo/issues/3 and again https://github.com/org/repo/issues/3"
	refs := Refs(text, "https://github.com")
	if len(refs) != 1 {
		t.Fatalf("refs = %d, want 1 (deduped)", len(refs))
	}
}

func TestRefsEmptyBaseURL(t *testing.T) {
	if refs := Refs("https://github.com/a/b/issues/1", ""); len(refs) != 0 {
		t.Errorf("empty base_url should match no refs, got %+v", refs)
	}
}
