package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/jira"
)

// configuredJiraModel builds a Model whose Jira client is enabled (points at a
// dummy base URL with credentials) so the open path runs. No network call
// happens unless the returned fetch Cmd is invoked.
func configuredJiraModel(t *testing.T, projects ...string) Model {
	t.Helper()
	m := New(nil, nil)
	m.width, m.height = 120, 40
	m.jiraClient = jira.New(jira.Config{
		BaseURL:  "https://example.atlassian.net",
		Email:    "me@x.test",
		APIToken: "tok",
	})
	m.jiraProjects = projects
	return m
}

func TestOpenReferenceOpensPanel(t *testing.T) {
	m := configuredJiraModel(t, "ABC")
	m.posts = []*model.Post{{Id: "p1", Message: "fixing https://example.atlassian.net/browse/ABC-1 now"}}
	m.postIdx = 0
	m.focus = focusMessages

	updated, cmd := m.openRefForPost(m.posts[0])
	got := updated.(Model)

	if !got.refOpen {
		t.Fatal("expected reference panel to open")
	}
	if got.focus != focusRef {
		t.Errorf("focus = %v, want focusRef", got.focus)
	}
	if len(got.refs) != 1 || got.refs[0].kind != refJira || got.refs[0].jiraKey != "ABC-1" {
		t.Errorf("refs = %+v, want one Jira ABC-1", got.refs)
	}
	if cmd == nil {
		t.Error("expected a fetch Cmd")
	}
}

func TestOpenReferenceBareIDNeedsAllowlist(t *testing.T) {
	// Same bare id, once without and once with the project allowlisted.
	post := &model.Post{Message: "blocked by ABC-42"}

	noAllow := configuredJiraModel(t)
	updated, _ := noAllow.openRefForPost(post)
	if updated.(Model).refOpen {
		t.Error("bare id should not open the panel without an allowlisted project")
	}

	allow := configuredJiraModel(t, "ABC")
	updated, _ = allow.openRefForPost(post)
	if got := updated.(Model); !got.refOpen || got.refs[0].jiraKey != "ABC-42" {
		t.Errorf("allowlisted bare id should open: open=%v refs=%+v", got.refOpen, got.refs)
	}
}

func TestOpenReferenceNoIssue(t *testing.T) {
	m := configuredJiraModel(t, "ABC")
	post := &model.Post{Message: "just some plain text"}

	updated, cmd := m.openRefForPost(post)
	got := updated.(Model)
	if got.refOpen {
		t.Error("expected panel to stay closed when no reference is named")
	}
	if cmd != nil {
		t.Error("expected no Cmd when nothing to open")
	}
	if !strings.Contains(got.status, "no Jira issue or GitLab MR") {
		t.Errorf("status = %q", got.status)
	}
}

func TestOpenReferenceNotConfigured(t *testing.T) {
	m := New(nil, nil) // default jira + gitlab clients have no credentials → disabled
	m.width, m.height = 120, 40
	post := &model.Post{Message: "https://example.atlassian.net/browse/ABC-1"}

	updated, cmd := m.openRefForPost(post)
	got := updated.(Model)
	if got.refOpen {
		t.Error("expected panel to stay closed when no provider is configured")
	}
	if cmd != nil {
		t.Error("expected no Cmd when unconfigured")
	}
	if !strings.Contains(got.status, "no reference provider configured") {
		t.Errorf("status = %q", got.status)
	}
}

func TestOpenReferenceClosesThread(t *testing.T) {
	// The thread sidebar and the reference panel share the single right slot.
	m := configuredJiraModel(t)
	m.threadOpen = true
	m.threadRootID = "root"
	post := &model.Post{Message: "https://example.atlassian.net/browse/ZZ-9"}

	updated, _ := m.openRefForPost(post)
	got := updated.(Model)
	if got.threadOpen {
		t.Error("opening the reference panel should close the thread panel")
	}
	if !got.refOpen {
		t.Error("expected reference panel open")
	}
}

func TestJiraPanelRendersIssue(t *testing.T) {
	m := configuredJiraModel(t, "ABC")
	m.posts = []*model.Post{{Message: "https://example.atlassian.net/browse/ABC-1"}}
	m.postIdx = 0
	m.focus = focusMessages
	updated, _ := m.openRefForPost(m.posts[0])
	got := updated.(Model)

	// Inject a loaded issue the way the fetch Cmd's result would.
	final, _ := got.handleJiraLoaded(jiraLoadedMsg{
		gen: got.refGen,
		key: "ABC-1",
		issue: &jira.Issue{
			Key:         "ABC-1",
			Summary:     "Fix the widget",
			Type:        "Bug",
			Status:      "In Progress",
			Assignee:    "Ada Lovelace",
			Description: "Some **bold** detail.",
			URL:         "https://example.atlassian.net/browse/ABC-1",
		},
	})
	fm := final.(Model)

	// Render the pane directly — viewContent() depends on the active tab (a
	// fresh test model sits on the Feed tab), whereas the pane render is what we
	// want to exercise here.
	pane := fm.renderRefPane(24, 48)
	for _, want := range []string{"ABC-1", "Fix the widget", "In Progress", "Ada Lovelace"} {
		if !strings.Contains(pane, want) {
			t.Errorf("rendered pane missing %q", want)
		}
	}
}

func TestOpenReferenceMarkdownBareKey(t *testing.T) {
	// Bare keys wrapped in markdown formatting should open the panel.
	m := configuredJiraModel(t, "ABC")
	m.posts = []*model.Post{{Message: "blocked by **ABC-42**"}}
	m.postIdx = 0
	m.focus = focusMessages

	updated, cmd := m.openRefForPost(m.posts[0])
	got := updated.(Model)

	if !got.refOpen {
		t.Fatal("expected reference panel to open for markdown bare key")
	}
	if len(got.refs) != 1 || got.refs[0].jiraKey != "ABC-42" {
		t.Errorf("refs = %+v, want one Jira ABC-42", got.refs)
	}
	if cmd == nil {
		t.Error("expected a fetch Cmd")
	}
}

func TestOpenReferenceBareKeyPos(t *testing.T) {
	// Positions should be accurate for bare keys, not relying on strings.Index.
	m := configuredJiraModel(t, "ABC")
	m.posts = []*model.Post{{Message: "ABC-1 and https://example.atlassian.net/browse/ABC-2"}}
	m.postIdx = 0
	m.focus = focusMessages

	updated, _ := m.openRefForPost(m.posts[0])
	got := updated.(Model)

	if len(got.refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(got.refs))
	}
	// Bare key ABC-1 is at position 0.
	if got.refs[0].jiraKey != "ABC-1" || got.refs[0].pos != 0 {
		t.Errorf("first ref = %+v, want jiraKey=ABC-1 pos=0", got.refs[0])
	}
	// URL ABC-2 is at position 10.
	if got.refs[1].jiraKey != "ABC-2" || got.refs[1].pos != 10 {
		t.Errorf("second ref = %+v, want jiraKey=ABC-2 pos=10", got.refs[1])
	}
}

func TestCloseRefRestoresFocus(t *testing.T) {
	m := configuredJiraModel(t)
	post := &model.Post{Message: "https://example.atlassian.net/browse/ABC-1"}
	updated, _ := m.openRefForPost(post)
	got := updated.(Model)

	got.closeRef()
	if got.refOpen {
		t.Error("expected panel closed")
	}
	if got.focus != focusMessages {
		t.Errorf("focus = %v, want focusMessages", got.focus)
	}
	if got.refs != nil {
		t.Errorf("refs not cleared: %+v", got.refs)
	}
}
