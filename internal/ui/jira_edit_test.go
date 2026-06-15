package ui

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/jira"
)

// loadedJiraModel returns a model with the panel open and an issue already
// loaded, so the field-edit hotkeys are live.
func loadedJiraModel(t *testing.T) Model {
	t.Helper()
	m := configuredJiraModel(t, "ABC")
	m.posts = []*model.Post{{Message: "https://example.atlassian.net/browse/ABC-1"}}
	m.postIdx = 0
	m.focus = focusMessages
	updated, _ := m.openRefForPost(m.posts[0])
	got := updated.(Model)
	final, _ := got.handleJiraLoaded(jiraLoadedMsg{
		gen: got.refGen,
		key: "ABC-1",
		issue: &jira.Issue{
			Key: "ABC-1", Summary: "Fix the widget", Status: "To Do",
			Priority: "Medium", PriorityID: "3",
			Assignee: "Ada", AssigneeAccountID: "acc-1",
			StoryPoints: "5",
		},
	})
	return final.(Model)
}

func TestJiraHotkeyOpensStatusPicker(t *testing.T) {
	m := loadedJiraModel(t)
	updated, cmd := m.handleRefKey(keyStr("s"))
	got := updated.(Model)
	if !got.jiraPicker.active || got.jiraPicker.kind != jiraPickStatus {
		t.Fatalf("status picker not active: %+v", got.jiraPicker)
	}
	if !got.jiraPicker.loading {
		t.Error("expected loading state until the fetch returns")
	}
	if got.jiraPicker.issueKey != "ABC-1" {
		t.Errorf("issueKey = %q", got.jiraPicker.issueKey)
	}
	if cmd == nil {
		t.Error("expected a fetch Cmd")
	}
}

func TestJiraHotkeyNoIssueIsNoop(t *testing.T) {
	// While the issue is still loading (no jiraIssue), the edit keys must not
	// open a picker — they fall through to the viewport.
	m := configuredJiraModel(t, "ABC")
	post := &model.Post{Message: "https://example.atlassian.net/browse/ABC-1"}
	updated, _ := m.openRefForPost(post)
	got := updated.(Model) // jiraIssue is nil (fetch not run)
	updated, _ = got.handleRefKey(keyStr("p"))
	if updated.(Model).jiraPicker.active {
		t.Error("picker should not open before an issue is loaded")
	}
}

func TestJiraHotkeyOpensPointsInput(t *testing.T) {
	m := loadedJiraModel(t)
	updated, _ := m.handleRefKey(keyStr("P"))
	got := updated.(Model)
	if !got.jiraPointsActive {
		t.Fatal("points input not active")
	}
	if got.jiraPointsInput.Value() != "5" {
		t.Errorf("points input seeded %q, want 5", got.jiraPointsInput.Value())
	}
	if got.jiraPointsKey != "ABC-1" {
		t.Errorf("points key = %q", got.jiraPointsKey)
	}
}

func TestJiraPickerLoadedSelectsCurrent(t *testing.T) {
	m := loadedJiraModel(t)
	m.startJiraPicker(jiraPickPriority, "Set priority", false)
	updated, _ := m.handleJiraPickerLoaded(jiraPickerLoadedMsg{
		gen: m.jiraPicker.gen, seq: m.jiraPicker.fetchSeq, kind: jiraPickPriority,
		items: []jiraPickerItem{
			{id: "1", label: "Highest"},
			{id: "3", label: "Medium", current: true},
			{id: "5", label: "Low"},
		},
	})
	got := updated.(Model)
	if got.jiraPicker.loading {
		t.Error("still loading after result")
	}
	if got.jiraPicker.idx != 1 {
		t.Errorf("cursor idx = %d, want 1 (the current value)", got.jiraPicker.idx)
	}
}

func TestJiraPickerLoadedDropsStale(t *testing.T) {
	m := loadedJiraModel(t)
	m.startJiraPicker(jiraPickPriority, "Set priority", false)
	updated, _ := m.handleJiraPickerLoaded(jiraPickerLoadedMsg{
		gen: m.jiraPicker.gen + 99, kind: jiraPickPriority,
		items: []jiraPickerItem{{id: "1", label: "Highest"}},
	})
	if !updated.(Model).jiraPicker.loading {
		t.Error("a stale (wrong-gen) result should be ignored, leaving loading set")
	}
}

// pickerHasLabel reports whether the picker currently lists an item labelled
// (prefix-matched) with want.
func pickerHasLabel(m Model, want string) bool {
	for _, it := range m.jiraPicker.items {
		if strings.HasPrefix(it.label, want) {
			return true
		}
	}
	return false
}

func TestJiraAssigneeServerSearch(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/user/assignable/search":
			gotQuery = r.URL.Query().Get("query")
			if gotQuery == "alan" {
				_, _ = w.Write([]byte(`[{"accountId":"a2","displayName":"Alan Turing"}]`))
			} else {
				_, _ = w.Write([]byte(`[{"accountId":"a1","displayName":"Ada Lovelace"},{"accountId":"a2","displayName":"Alan Turing"}]`))
			}
		case "/rest/api/3/myself":
			_, _ = w.Write([]byte(`{"accountId":"me-1","displayName":"Me"}`))
		}
	}))
	defer srv.Close()

	m := loadedJiraModel(t)
	m.jiraClient = jira.New(jira.Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})

	// Open the picker; the initial empty-query search runs and shows the meta
	// rows plus the default page.
	cmd := m.openJiraAssigneePicker()
	if cmd == nil {
		t.Fatal("expected an initial search Cmd")
	}
	updated, _ := m.handleJiraPickerLoaded(cmd().(jiraPickerLoadedMsg))
	m = updated.(Model)
	if gotQuery != "" {
		t.Errorf("initial query = %q, want empty", gotQuery)
	}
	if !pickerHasLabel(m, "Unassigned") || !pickerHasLabel(m, "Ada Lovelace") {
		t.Errorf("default items = %+v", m.jiraPicker.items)
	}

	// Type "alan"; each change bumps fetchSeq so stale responses are dropped.
	for _, r := range "alan" {
		u, _ := m.handleJiraPickerKey(keyStr(string(r)))
		m = u.(Model)
	}
	if m.jiraPicker.filter.Value() != "alan" {
		t.Fatalf("filter value = %q, want alan", m.jiraPicker.filter.Value())
	}
	if m.jiraPicker.fetchSeq <= 1 {
		t.Errorf("fetchSeq = %d, expected typing to advance it", m.jiraPicker.fetchSeq)
	}

	// A debounce for a superseded query is ignored.
	if _, c := m.handleJiraAssigneeDebounce(jiraAssigneeDebounceMsg{seq: 1}); c != nil {
		t.Error("stale debounce should be ignored")
	}

	// The latest debounce runs the server search; the results replace the list.
	updated, fetchCmd := m.handleJiraAssigneeDebounce(jiraAssigneeDebounceMsg{seq: m.jiraPicker.fetchSeq})
	m = updated.(Model)
	if fetchCmd == nil {
		t.Fatal("debounce should trigger the server fetch")
	}
	updated, _ = m.handleJiraPickerLoaded(fetchCmd().(jiraPickerLoadedMsg))
	m = updated.(Model)

	if gotQuery != "alan" {
		t.Errorf("server query = %q, want alan", gotQuery)
	}
	if pickerHasLabel(m, "Unassigned") {
		t.Error("meta rows should be hidden while searching")
	}
	if !pickerHasLabel(m, "Alan Turing") || pickerHasLabel(m, "Ada Lovelace") {
		t.Errorf("search items = %+v", m.jiraPicker.items)
	}
}

func TestJiraPickerMoveClamps(t *testing.T) {
	m := loadedJiraModel(t)
	m.startJiraPicker(jiraPickPriority, "Set priority", false)
	m.jiraPicker.loading = false
	m.jiraPicker.items = []jiraPickerItem{{id: "1"}, {id: "2"}}
	m.jiraPicker.idx = 0
	m.jiraPickerMove(-1) // already at top
	if m.jiraPicker.idx != 0 {
		t.Errorf("idx = %d, want 0", m.jiraPicker.idx)
	}
	m.jiraPickerMove(5) // past the end
	if m.jiraPicker.idx != 1 {
		t.Errorf("idx = %d, want 1 (clamped)", m.jiraPicker.idx)
	}
}

func TestApplyJiraPickSetsPriority(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	m := loadedJiraModel(t)
	m.jiraClient = jira.New(jira.Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
	m.startJiraPicker(jiraPickPriority, "Set priority", false)
	m.jiraPicker.loading = false
	m.jiraPicker.items = []jiraPickerItem{{id: "1", label: "Highest"}, {id: "3", label: "Medium"}}
	m.jiraPicker.idx = 0

	updated, cmd := m.applyJiraPick()
	got := updated.(Model)
	if got.jiraPicker.active {
		t.Error("picker should close on apply")
	}
	if !strings.Contains(got.status, "updating ABC-1 priority") {
		t.Errorf("status = %q", got.status)
	}
	if cmd == nil {
		t.Fatal("expected a mutation Cmd")
	}
	msg, ok := cmd().(jiraMutatedMsg)
	if !ok {
		t.Fatalf("cmd returned %T", cmd())
	}
	if msg.err != nil {
		t.Fatalf("mutation failed: %v", msg.err)
	}
	if gotMethod != http.MethodPut || gotPath != "/rest/api/3/issue/ABC-1" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(gotBody, `"1"`) || !strings.Contains(gotBody, "priority") {
		t.Errorf("body = %q", gotBody)
	}
	if msg.field != "priority" || msg.key != "ABC-1" {
		t.Errorf("msg = %+v", msg)
	}
}

func TestApplyJiraPointsClears(t *testing.T) {
	const fieldMeta = `[{"id":"customfield_10016","name":"Story point estimate"}]`
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/field" {
			_, _ = w.Write([]byte(fieldMeta))
			return
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	m := loadedJiraModel(t)
	m.jiraClient = jira.New(jira.Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
	m.openJiraPointsInput()
	m.jiraPointsInput.SetValue("") // clear the seeded value

	updated, cmd := m.applyJiraPoints()
	if updated.(Model).jiraPointsActive {
		t.Error("points input should close on apply")
	}
	if cmd == nil {
		t.Fatal("expected a mutation Cmd")
	}
	if msg := cmd().(jiraMutatedMsg); msg.err != nil {
		t.Fatalf("mutation failed: %v", msg.err)
	}
	if !strings.Contains(gotBody, `"customfield_10016":null`) {
		t.Errorf("body = %q, want field cleared to null", gotBody)
	}
}

func TestHandleJiraMutatedReloadsOnSuccess(t *testing.T) {
	m := loadedJiraModel(t)
	prevGen := m.refGen
	updated, cmd := m.handleJiraMutated(jiraMutatedMsg{key: "ABC-1", field: "status"})
	got := updated.(Model)
	if cmd == nil || got.refGen == prevGen {
		t.Error("success should reload the issue (bump refGen, return a fetch Cmd)")
	}
	if !strings.Contains(got.status, "updated") {
		t.Errorf("status = %q", got.status)
	}
}

func TestHandleJiraMutatedError(t *testing.T) {
	m := loadedJiraModel(t)
	updated, cmd := m.handleJiraMutated(jiraMutatedMsg{key: "ABC-1", field: "status", err: fmt.Errorf("boom")})
	got := updated.(Model)
	if cmd != nil {
		t.Error("error path should not reload")
	}
	if !strings.Contains(got.status, "failed") {
		t.Errorf("status = %q", got.status)
	}
}

func TestJiraPickerWindowsLongList(t *testing.T) {
	const maxH = 20 // small body area so the window is well under the list size
	m := loadedJiraModel(t)
	m.startJiraPicker(jiraPickAssignee, "Set assignee", true)
	m.jiraPicker.loading = false
	items := make([]jiraPickerItem, 0, 50)
	for i := 0; i < 50; i++ {
		items = append(items, jiraPickerItem{id: fmt.Sprintf("a%d", i), label: fmt.Sprintf("User %02d", i)})
	}
	m.jiraPicker.items = items

	// Walk the cursor to the bottom; the render windows around it.
	for i := 0; i < len(items)-1; i++ {
		m.jiraPickerMove(1)
	}
	if m.jiraPicker.idx != len(items)-1 {
		t.Fatalf("cursor idx = %d, want %d", m.jiraPicker.idx, len(items)-1)
	}

	// The popup must fit within maxH, window the list (not render all 50), show
	// a "more above" indicator, and keep the selected row visible.
	out := m.renderJiraPicker(maxH)
	if lines := strings.Count(out, "\n") + 1; lines > maxH {
		t.Errorf("rendered picker is %d lines, exceeds maxH %d", lines, maxH)
	}
	if !strings.Contains(out, "more") {
		t.Errorf("expected a scroll indicator, got:\n%s", out)
	}
	if strings.Contains(out, "User 00") {
		t.Error("first item visible while scrolled to the bottom — list did not window")
	}
	if !strings.Contains(out, "User 49") {
		t.Error("selected (last) item not visible in the window")
	}
}

func TestJiraPickerKeyEscCloses(t *testing.T) {
	m := loadedJiraModel(t)
	m.startJiraPicker(jiraPickStatus, "Set status", false)
	updated, _ := m.handleJiraPickerKey(keyStr("esc"))
	if updated.(Model).jiraPicker.active {
		t.Error("esc should close the picker")
	}
}
