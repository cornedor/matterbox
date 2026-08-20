package gitlab

import (
	"fmt"
	"reflect"
	"testing"

	"matterbox/internal/forge"
)

// short renders a detected ref the way GitLab writes it, for readable failures.
func short(r forge.Ref) string { return fmt.Sprintf("%s!%d", r.Repo, r.Number) }

func TestRefsURL(t *testing.T) {
	base := "https://git.example.com"
	text := "see https://git.example.com/group/sub/proj/-/merge_requests/42 for the fix"
	got := Refs(text, base)
	if len(got) != 1 {
		t.Fatalf("got %d refs, want 1: %v", len(got), got)
	}
	if got[0].Repo != "group/sub/proj" || got[0].Number != 42 {
		t.Errorf("ref = %+v, want group/sub/proj!42", got[0])
	}
}

func TestRefsURLWrongHost(t *testing.T) {
	text := "https://gitlab.com/foo/bar/-/merge_requests/7"
	if got := Refs(text, "https://git.example.com"); len(got) != 0 {
		t.Errorf("foreign-host URL should not match: %v", got)
	}
}

func TestRefsShortForm(t *testing.T) {
	got := Refs("blocked by magento-2/bergtoys!2577 now", "https://git.example.com")
	if len(got) != 1 || got[0].Repo != "magento-2/bergtoys" || got[0].Number != 2577 {
		t.Fatalf("short ref = %v, want magento-2/bergtoys!2577", got)
	}
}

func TestRefsShortFormNeedsNamespace(t *testing.T) {
	// A bare "word!3" (no project path) must not be taken for a ref.
	if got := Refs("that is huge!3 times bigger", "https://git.example.com"); len(got) != 0 {
		t.Errorf("bare word!N should not match: %v", got)
	}
}

func TestRefsDedupAndOrder(t *testing.T) {
	// Same MR as URL then short form, plus a second MR earlier in the text.
	text := "first a/b!1 then https://git.example.com/g/p/-/merge_requests/9 and again g/p!9"
	got := Refs(text, "https://git.example.com")
	want := []string{"a/b!1", "g/p!9"}
	var names []string
	for _, r := range got {
		names = append(names, short(r))
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("refs = %v, want %v (deduped, in appearance order)", names, want)
	}
}

func TestRefsEmptyBaseURLStillFindsShortRefs(t *testing.T) {
	got := Refs("g/p!5 and https://git.example.com/g/p/-/merge_requests/6", "")
	// URL needs a configured host; the short ref does not.
	if len(got) != 1 || short(got[0]) != "g/p!5" {
		t.Errorf("with empty baseURL want only short ref g/p!5, got %v", got)
	}
}
