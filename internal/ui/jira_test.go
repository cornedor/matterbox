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
	m := newTestModel()
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
	if !strings.Contains(got.status, "no Jira issue or") {
		t.Errorf("status = %q", got.status)
	}
}

// A Jira link with no Jira credentials opens nothing. A default model still has
// GitHub (public repositories read without a token), so the panel reports that
// the message named nothing it can open — and says which providers are off, the
// hint a user who has configured none of them needs.
func TestOpenReferenceNotConfigured(t *testing.T) {
	m := newTestModel() // no jira credentials, no gitlab; anonymous GitHub only
	m.width, m.height = 120, 40
	post := &model.Post{Message: "https://example.atlassian.net/browse/ABC-1"}

	updated, cmd := m.openRefForPost(post)
	got := updated.(Model)
	if got.refOpen {
		t.Error("expected panel to stay closed when no provider can open the link")
	}
	if cmd != nil {
		t.Error("expected no Cmd when unconfigured")
	}
	if !strings.Contains(got.status, "Jira") || !strings.Contains(got.status, "not configured") {
		t.Errorf("status = %q, want it to say Jira isn't configured", got.status)
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

// loadedJiraModelWith opens the reference panel on ABC-1 and injects iss as the
// loaded issue, the way the fetch Cmd's result would. (loadedJiraModel, in
// jira_edit_test.go, uses a fixed issue; this variant takes one with comments.)
func loadedJiraModelWith(t *testing.T, iss *jira.Issue) Model {
	t.Helper()
	m := configuredJiraModel(t, "ABC")
	m.posts = []*model.Post{{Message: "https://example.atlassian.net/browse/ABC-1"}}
	m.postIdx = 0
	m.focus = focusMessages
	updated, _ := m.openRefForPost(m.posts[0])
	got := updated.(Model)
	final, _ := got.handleJiraLoaded(jiraLoadedMsg{gen: got.refGen, key: "ABC-1", issue: iss})
	return final.(Model)
}

func TestJiraPanelRendersComments(t *testing.T) {
	m := loadedJiraModelWith(t, &jira.Issue{
		Key:          "ABC-1",
		Summary:      "Fix the widget",
		CommentTotal: 2,
		Comments: []jira.Comment{
			{Author: "Ada Lovelace", AuthorID: "a1", Body: "first thought"},
			{Author: "Alan Turing", AuthorID: "a2", Body: "a follow-up"},
		},
	})

	pane := m.renderRefPane(24, 48)
	for _, want := range []string{"Comments (2)", "Ada Lovelace", "first thought", "Alan Turing", "a follow-up"} {
		if !strings.Contains(pane, want) {
			t.Errorf("rendered pane missing %q", want)
		}
	}
}

func TestJiraReplyPrefillsComposer(t *testing.T) {
	m := loadedJiraModelWith(t, &jira.Issue{
		Key: "ABC-1",
		Comments: []jira.Comment{
			{Author: "Ada Lovelace", AuthorID: "a1", Body: "older"},
			{Author: "Alan Turing", AuthorID: "a2", Body: "the newest comment"},
		},
	})

	// R opens the reply-target picker, populated newest-first.
	m.openJiraReplyPicker()
	if !m.jiraPicker.active || m.jiraPicker.kind != jiraPickReplyTarget {
		t.Fatalf("reply picker not active: %+v", m.jiraPicker)
	}
	if len(m.jiraPicker.items) != 2 || !strings.Contains(m.jiraPicker.items[0].label, "Alan Turing") {
		t.Fatalf("picker items = %+v (want newest first)", m.jiraPicker.items)
	}

	// Selecting the newest comment opens the composer prefilled to reply to it.
	updated, _ := m.applyJiraPick()
	got := updated.(Model)
	if !got.jiraCommentActive {
		t.Fatal("expected comment composer active after picking a reply target")
	}
	if got.jiraPicker.active {
		t.Error("expected reply picker closed after selection")
	}
	if got.jiraCommentMention == nil || got.jiraCommentMention.AccountID != "a2" {
		t.Errorf("mention = %+v, want author a2", got.jiraCommentMention)
	}
	if v := got.jiraCommentInput.Value(); !strings.Contains(v, "Alan Turing wrote:") || !strings.Contains(v, "> the newest comment") {
		t.Errorf("composer not prefilled with the quote: %q", v)
	}
}

func TestJiraReplyWithNoCommentsIsNoOp(t *testing.T) {
	m := loadedJiraModelWith(t, &jira.Issue{Key: "ABC-1"})
	// R with no comments: the key handler should refuse rather than open an
	// empty picker.
	updated, _ := m.handleRefKey(keyStr("R"))
	got := updated.(Model)
	if got.jiraPicker.active {
		t.Error("reply picker should not open with no comments")
	}
	if !strings.Contains(got.status, "no comments to reply to") {
		t.Errorf("status = %q", got.status)
	}
}
