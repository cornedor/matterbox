package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

func newSQLAuthorModel() Model {
	return Model{
		teams:     []*model.Team{{Id: "t1", DisplayName: "Engineering", Name: "eng"}},
		userNames: map[string]string{"u1": "me", "u2": "alice"},
		me:        &model.User{Id: "u1", Username: "me"},
		channels: map[string][]*model.Channel{
			"t1": {{Id: "c1", TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: "General"}},
			dmTeamID: {
				{Id: "d1", Type: model.ChannelTypeDirect, Name: "u1__u2"},
			},
		},
	}
}

// TestSQLAuthorName covers the breadcrumb-prefixed author the SQL tab puts in
// front of each result row: "Team › #channel › user" for a normal channel and
// "DMs › @partner › user" for a DM (where partner and author can coincide).
func TestSQLAuthorName(t *testing.T) {
	m := newSQLAuthorModel()
	cases := []struct {
		name string
		post *model.Post
		want string
	}{
		{
			name: "normal channel prefixes team and channel",
			post: &model.Post{ChannelId: "c1", UserId: "u2"},
			want: "Engineering › #General › alice",
		},
		{
			name: "DM prefixes the partner, then the author",
			post: &model.Post{ChannelId: "d1", UserId: "u2"},
			want: "DMs › @alice › alice", // partner alice, author alice — the 2x case
		},
		{
			name: "DM authored by me still names the partner first",
			post: &model.Post{ChannelId: "d1", UserId: "u1"},
			want: "DMs › @alice › me",
		},
		{
			name: "unknown channel falls back to a short id",
			post: &model.Post{ChannelId: "zzzzzzzzzzzz", UserId: "u2"},
			want: "#zzzzzzzz › alice",
		},
		{
			name: "no channel is just the author",
			post: &model.Post{UserId: "u2"},
			want: "alice",
		},
		{
			name: "channel with no author (aggregate row) is just the breadcrumb",
			post: &model.Post{ChannelId: "c1"},
			want: "Engineering › #General",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.sqlAuthorName(tc.post); got != tc.want {
				t.Errorf("sqlAuthorName = %q; want %q", got, tc.want)
			}
		})
	}
}

// sqlModelWithTwoRows returns a model sitting on the SQL tab whose query has
// already run and returned two message rows (focus has dropped into the
// results). Shared by the selection / mouse tests.
func sqlModelWithTwoRows() Model {
	m := newSQLAuthorModel()
	m.keys = newKeyMap("ctrl")
	m.mouseEnabled = true
	m.sql = newSQLState(true)
	m.sql.view.SetWidth(80)
	m.sql.view.SetHeight(20)
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if k, _, _ := m.tabAt(i); k == tabSQL {
			m.teamIdx = i
		}
	}
	m.focus = focusSQL
	cols := []string{"raw_json"}
	rows := [][]any{
		{[]byte(`{"id":"p1","channel_id":"c1","user_id":"u2","message":"first","create_at":1700000000000}`)},
		{[]byte(`{"id":"p2","channel_id":"c1","user_id":"u2","message":"second","create_at":1700000001000}`)},
	}
	out, _ := m.applySQLResults(sqlResultsMsg{seq: m.sql.seq, query: "SELECT raw_json FROM posts", cols: cols, rows: rows})
	return out.(Model)
}

// TestSQLResultsSelectionFlow: running a query drops focus into the results,
// arrows move the selection, the selection bar shows, and esc returns to the
// editor.
func TestSQLResultsSelectionFlow(t *testing.T) {
	m := sqlModelWithTwoRows()

	if m.focus != focusSQLResults {
		t.Fatalf("after run: focus = %v, want focusSQLResults", m.focus)
	}
	if len(m.sql.posts) != 2 || m.sql.idx != 0 {
		t.Fatalf("posts=%d idx=%d, want 2/0", len(m.sql.posts), m.sql.idx)
	}
	if !strings.Contains(m.sql.view.View(), "▎") {
		t.Errorf("selected row has no selection bar:\n%s", m.sql.view.View())
	}

	// ↓ moves the selection, ↑ moves it back.
	out, _ := m.handleSQLResultsKey(keyPress(tea.KeyDown))
	m = out.(Model)
	if m.sql.idx != 1 {
		t.Fatalf("after ↓: idx = %d, want 1", m.sql.idx)
	}
	out, _ = m.handleSQLResultsKey(keyPress(tea.KeyUp))
	m = out.(Model)
	if m.sql.idx != 0 {
		t.Fatalf("after ↑: idx = %d, want 0", m.sql.idx)
	}

	// esc returns to the editor.
	out, _ = m.handleSQLResultsKey(keyStr("esc"))
	m = out.(Model)
	if m.focus != focusSQL {
		t.Fatalf("after esc: focus = %v, want focusSQL", m.focus)
	}
}

// TestSQLMouseClickSelectsRow: clicking a result row maps to it and selects it
// (focusing the result list), and clicking the editor region above the results
// returns focus to the query editor.
func TestSQLMouseClickSelectsRow(t *testing.T) {
	m := sqlModelWithTwoRows()
	_, top, _, _, _ := m.sqlGeom()

	// A click above the result viewport (title / editor / rule) targets the editor.
	if h := m.hitSQLRow(2, top-1); h.zone != hitSQL || h.idx != -1 {
		t.Fatalf("editor region: hit = %v,%d want hitSQL,-1", h.zone, h.idx)
	}
	// First row sits at the top of the viewport.
	if h := m.hitSQLRow(2, top); h.zone != hitSQL || h.idx != 0 {
		t.Fatalf("row 0: hit = %v,%d want hitSQL,0", h.zone, h.idx)
	}
	// Second row sits at its recorded visual offset.
	if got := len(m.sql.rowStarts); got != 3 {
		t.Fatalf("rowStarts len = %d, want 3", got)
	}
	if h := m.hitSQLRow(2, top+m.sql.rowStarts[1]); h.zone != hitSQL || h.idx != 1 {
		t.Fatalf("row 1: hit = %v,%d want hitSQL,1", h.zone, h.idx)
	}

	// Clicking row 1 selects it and focuses the result list.
	out, _ := m.clickSQLRow(1)
	m2 := out.(Model)
	if m2.focus != focusSQLResults || m2.sql.idx != 1 {
		t.Fatalf("clickSQLRow(1): focus=%v idx=%d, want focusSQLResults/1", m2.focus, m2.sql.idx)
	}
	// Clicking the editor region returns focus to the editor.
	out, _ = m2.clickSQLRow(-1)
	if m3 := out.(Model); m3.focus != focusSQL {
		t.Fatalf("clickSQLRow(-1): focus=%v, want focusSQL", m3.focus)
	}
}

// TestSQLResultLinkResolves: a markdown link in a result row renders as an
// OSC 8 hyperlink, and linkAt resolves it from the SQL viewport content — the
// basis for click-to-open (handleMouseClick routes hitSQL → linkAt → activateLink).
func TestSQLResultLinkResolves(t *testing.T) {
	m := newSQLAuthorModel()
	m.keys = newKeyMap("ctrl")
	m.mouseEnabled = true
	m.sql = newSQLState(true)
	m.sql.view.SetWidth(80)
	m.sql.view.SetHeight(20)
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if k, _, _ := m.tabAt(i); k == tabSQL {
			m.teamIdx = i
		}
	}
	m.focus = focusSQL

	cols := []string{"raw_json"}
	rows := [][]any{
		{[]byte(`{"id":"p1","channel_id":"c1","user_id":"u2","message":"check [the docs](https://example.com) now","create_at":1700000000000}`)},
	}
	out, _ := m.applySQLResults(sqlResultsMsg{seq: m.sql.seq, query: "q", cols: cols, rows: rows})
	m = out.(Model)

	lines := strings.Split(m.sql.view.GetContent(), "\n")
	found := false
	for li := range lines {
		for col := 0; col < 80 && !found; col++ {
			if url, ok := m.linkAt(focusSQLResults, li, col); ok && strings.Contains(url, "example.com") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("markdown link not resolvable from SQL result content:\n%s", m.sql.view.GetContent())
	}
}

// TestSQLZeroRowsKeepsEditorFocus: a query with no rows leaves the cursor in
// the editor so the user can fix it immediately.
func TestSQLZeroRowsKeepsEditorFocus(t *testing.T) {
	m := newSQLAuthorModel()
	m.keys = newKeyMap("ctrl")
	m.sql = newSQLState(true)
	m.sql.view.SetWidth(80)
	m.sql.view.SetHeight(20)
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if k, _, _ := m.tabAt(i); k == tabSQL {
			m.teamIdx = i
		}
	}
	m.focus = focusSQL

	out, _ := m.applySQLResults(sqlResultsMsg{seq: m.sql.seq, query: "SELECT 1 WHERE 0", cols: []string{"x"}, rows: nil})
	m = out.(Model)
	if m.focus != focusSQL {
		t.Fatalf("0 rows: focus = %v, want focusSQL (stay in editor)", m.focus)
	}
}

func TestSQLCellCoercion(t *testing.T) {
	if got := sqlCellString(int64(42)); got != "42" {
		t.Errorf("int64 -> %q", got)
	}
	if got := sqlCellString([]byte("hi")); got != "hi" {
		t.Errorf("[]byte -> %q", got)
	}
	if got := sqlCellString(nil); got != "" {
		t.Errorf("nil -> %q", got)
	}
	if n, ok := sqlCellInt64([]byte("100")); !ok || n != 100 {
		t.Errorf("[]byte int -> %d, %v", n, ok)
	}
	if n, ok := sqlCellInt64(float64(7)); !ok || n != 7 {
		t.Errorf("float -> %d, %v", n, ok)
	}
}

// TestRenderSQLRowReconstructsFromRawJSON: a row that selected raw_json renders
// as a full message — author resolved, message body present — even though the
// individual columns (user_id / message) weren't selected.
func TestRenderSQLRowReconstructsFromRawJSON(t *testing.T) {
	m := newSQLAuthorModel()
	m.sql = newSQLState(false)
	m.sql.view.SetWidth(80)
	raw := []byte(`{"id":"p1","channel_id":"c1","user_id":"u2","message":"hello from raw","create_at":1700000000000}`)
	cols := []string{"raw_json"}
	row := []any{raw}
	p := reconstructSQLPost(cols, row)
	joined := strings.Join(m.renderSQLRow(p, cols, row, 80), "\n")
	if !strings.Contains(joined, "alice") {
		t.Errorf("expected author 'alice' in render, got:\n%s", joined)
	}
	if !strings.Contains(joined, "hello from raw") {
		t.Errorf("expected message body in render, got:\n%s", joined)
	}
	if !strings.Contains(joined, "Engineering") {
		t.Errorf("expected team breadcrumb in render, got:\n%s", joined)
	}
}
