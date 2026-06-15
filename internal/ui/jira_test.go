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

	updated, cmd := m.openJiraForPost(m.posts[0])
	got := updated.(Model)

	if !got.jiraOpen {
		t.Fatal("expected jira panel to open")
	}
	if got.focus != focusJira {
		t.Errorf("focus = %v, want focusJira", got.focus)
	}
	if len(got.jiraRefs) != 1 || got.jiraRefs[0] != "ABC-1" {
		t.Errorf("jiraRefs = %v, want [ABC-1]", got.jiraRefs)
	}
	if cmd == nil {
		t.Error("expected a fetch Cmd")
	}
}

func TestOpenReferenceBareIDNeedsAllowlist(t *testing.T) {
	// Same bare id, once without and once with the project allowlisted.
	post := &model.Post{Message: "blocked by ABC-42"}

	noAllow := configuredJiraModel(t)
	updated, _ := noAllow.openJiraForPost(post)
	if updated.(Model).jiraOpen {
		t.Error("bare id should not open the panel without an allowlisted project")
	}

	allow := configuredJiraModel(t, "ABC")
	updated, _ = allow.openJiraForPost(post)
	if got := updated.(Model); !got.jiraOpen || got.jiraRefs[0] != "ABC-42" {
		t.Errorf("allowlisted bare id should open: open=%v refs=%v", got.jiraOpen, got.jiraRefs)
	}
}

func TestOpenReferenceNoIssue(t *testing.T) {
	m := configuredJiraModel(t, "ABC")
	post := &model.Post{Message: "just some plain text"}

	updated, cmd := m.openJiraForPost(post)
	got := updated.(Model)
	if got.jiraOpen {
		t.Error("expected panel to stay closed when no issue is named")
	}
	if cmd != nil {
		t.Error("expected no Cmd when nothing to open")
	}
	if !strings.Contains(got.status, "no Jira issue") {
		t.Errorf("status = %q", got.status)
	}
}

func TestOpenReferenceNotConfigured(t *testing.T) {
	m := New(nil, nil) // default jira client has no credentials → disabled
	m.width, m.height = 120, 40
	post := &model.Post{Message: "https://example.atlassian.net/browse/ABC-1"}

	updated, cmd := m.openJiraForPost(post)
	got := updated.(Model)
	if got.jiraOpen {
		t.Error("expected panel to stay closed when Jira is unconfigured")
	}
	if cmd != nil {
		t.Error("expected no Cmd when unconfigured")
	}
	if !strings.Contains(got.status, "not configured") {
		t.Errorf("status = %q", got.status)
	}
}

func TestOpenReferenceClosesThread(t *testing.T) {
	// The thread sidebar and the Jira panel share the single right slot.
	m := configuredJiraModel(t)
	m.threadOpen = true
	m.threadRootID = "root"
	post := &model.Post{Message: "https://example.atlassian.net/browse/ZZ-9"}

	updated, _ := m.openJiraForPost(post)
	got := updated.(Model)
	if got.threadOpen {
		t.Error("opening the Jira panel should close the thread panel")
	}
	if !got.jiraOpen {
		t.Error("expected jira panel open")
	}
}

func TestJiraPanelRendersIssue(t *testing.T) {
	m := configuredJiraModel(t, "ABC")
	m.posts = []*model.Post{{Message: "https://example.atlassian.net/browse/ABC-1"}}
	m.postIdx = 0
	m.focus = focusMessages
	updated, _ := m.openJiraForPost(m.posts[0])
	got := updated.(Model)

	// Inject a loaded issue the way the fetch Cmd's result would.
	final, _ := got.handleJiraLoaded(jiraLoadedMsg{
		gen: got.jiraGen,
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
	pane := fm.renderJiraPane(24, 48)
	for _, want := range []string{"ABC-1", "Fix the widget", "In Progress", "Ada Lovelace"} {
		if !strings.Contains(pane, want) {
			t.Errorf("rendered pane missing %q", want)
		}
	}
}

func TestCloseJiraRestoresFocus(t *testing.T) {
	m := configuredJiraModel(t)
	post := &model.Post{Message: "https://example.atlassian.net/browse/ABC-1"}
	updated, _ := m.openJiraForPost(post)
	got := updated.(Model)

	got.closeJira()
	if got.jiraOpen {
		t.Error("expected panel closed")
	}
	if got.focus != focusMessages {
		t.Errorf("focus = %v, want focusMessages", got.focus)
	}
	if got.jiraRefs != nil {
		t.Errorf("refs not cleared: %v", got.jiraRefs)
	}
}
