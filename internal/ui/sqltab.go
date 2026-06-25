package ui

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/viewport"
)

// sqlMaxRows caps how many result rows we pull into the viewport. The SQL tab
// renders every row as a (potentially multi-line) chat message, so an
// unbounded `SELECT *` would otherwise build a giant string each render.
const sqlMaxRows = 1000

// sqlQueryTimeout bounds a single query so a runaway scan can't wedge the tab.
// The read-only handle honours context cancellation via SQLite's interrupt.
const sqlQueryTimeout = 20 * time.Second

// sqlConsumed are the post columns the chat rendering already understands
// (used for the prefixed author, timestamp, and body). Any *other* selected
// column is shown as a "col=value" suffix so aggregate / custom queries stay
// legible. Keys are lower-cased column names.
var sqlConsumed = map[string]bool{
	"id": true, "user_id": true, "channel_id": true, "root_id": true,
	"message": true, "create_at": true, "update_at": true,
	"edit_at": true, "delete_at": true, "raw_json": true,
}

// sqlResultsMsg carries the outcome of a RawQuery. Stale responses are dropped
// when seq no longer matches m.sql.seq. err is the stringified error (kept as a
// string so the msg stays cheaply copyable across the tick boundary).
type sqlResultsMsg struct {
	seq       int
	query     string
	cols      []string
	rows      [][]any
	truncated bool
	err       string
}

// sqlState owns the SQL tab: a multi-line query editor over the local message
// cache, with results rendered as chat messages. See onSQLTab / handleSQLKey.
type sqlState struct {
	// built is true once newSQLState has constructed the textarea/viewport.
	// layoutPanes sizes this tab on every frame, including on the partially-
	// built Model values some tests use; an unbuilt textarea's nested viewport
	// panics on SetWidth, so sizing short-circuits until it's built.
	built bool

	input textarea.Model
	view  viewport.Model

	query     string // last query we actually ran
	seq       int    // bumps on each run; drops stale results
	cols      []string
	rows      [][]any
	posts     []*model.Post // one reconstructed post per row (parallel to rows)
	idx       int           // selected result row, when focus == focusSQLResults
	truncated bool          // result had more than sqlMaxRows rows
	loading   bool
	err       string
	notReady  bool // store unavailable

	// rowStarts[i] is the visual-row offset of result row i in the viewport
	// (rowStarts[len] is the total), so selection can scroll into view without
	// re-measuring every line. Rebuilt by renderSQLResults.
	rowStarts []int
}

// newSQLState constructs the textarea / viewport used by the SQL tab. Called
// once at startup from New().
func newSQLState(storeAvailable bool) sqlState {
	ta := textarea.New()
	ta.Placeholder = "SELECT * FROM posts ORDER BY create_at DESC LIMIT 50    (enter runs · alt+↵ newline)"
	ta.CharLimit = 8000
	ta.ShowLineNumbers = false
	ta.DynamicHeight = true
	ta.MinHeight = 3
	ta.MaxHeight = 10
	ta.MaxContentHeight = 10000
	ta.SetHeight(3)
	ta.SetPromptFunc(2, inputPromptFunc("❯ "))
	taStyles := ta.Styles()
	taStyles.Focused.CursorLine = lipgloss.NewStyle()
	taStyles.Blurred.CursorLine = lipgloss.NewStyle()
	taStyles.Cursor.Blink = false
	ta.SetStyles(taStyles)
	// Enter runs the query (handled in handleSQLKey); alt+enter / shift+enter
	// insert a newline so multi-line SQL is still possible. Mirrors the composer.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("alt+enter", "shift+enter"),
		key.WithHelp("alt+↵/shift+↵", "newline"),
	)

	vp := viewport.New()
	vp.SoftWrap = true

	return sqlState{
		built:    true,
		input:    ta,
		view:     vp,
		notReady: !storeAvailable,
	}
}

// onSQLTab reports whether the synthetic SQL tab is currently selected.
func (m *Model) onSQLTab() bool {
	kind, _, _ := m.tabAt(m.teamIdx)
	return kind == tabSQL
}

// openSQLTab switches to the synthetic SQL tab and focuses the editor.
// Idempotent — calling it while already on SQL just re-focuses the input.
func (m *Model) openSQLTab() tea.Cmd {
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if kind, _, _ := m.tabAt(i); kind == tabSQL {
			m.teamIdx = i
			break
		}
	}
	m.filterMode = false
	m.filter.SetValue("")
	m.filter.Blur()
	m.input.Blur()
	m.search.input.Blur()
	m.focus = focusSQL
	return m.sql.input.Focus()
}

// handleSQLKey owns every keystroke while focus == focusSQL. Enter runs the
// query; esc clears results (or leaves to the tab strip when already empty);
// pgup/pgdn scroll the results; everything else edits the multi-line query.
func (m Model) handleSQLKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "ctrl+c": // hardwired quit
		return m, tea.Quit
	case msg.String() == "esc": // hardwired clear / leave
		if m.sql.input.Value() != "" || len(m.sql.rows) > 0 || m.sql.err != "" {
			m.sql.input.SetValue("")
			m.sql.rows = nil
			m.sql.cols = nil
			m.sql.posts = nil
			m.sql.idx = 0
			m.sql.query = ""
			m.sql.err = ""
			m.sql.truncated = false
			m.layoutPanes()
			m.renderSQLResults()
			return m, nil
		}
		m.sql.input.Blur()
		m.focus = focusTeams
		return m, nil
	case msg.String() == "enter": // run
		return m.runSQL()
	case msg.String() == "pgup":
		m.sql.view.ScrollUp(m.sql.view.Height() / 2)
		return m, nil
	case msg.String() == "pgdown":
		m.sql.view.ScrollDown(m.sql.view.Height() / 2)
		return m, nil
	case key.Matches(msg, m.keys.Tab):
		return m.cycleFocus(1)
	case key.Matches(msg, m.keys.ShiftTab):
		return m.cycleFocus(-1)
	case key.Matches(msg, m.keys.Paste):
		return m, readClipboard()
	}

	var cmd tea.Cmd
	old := m.sql.input.Value()
	m.sql.input, cmd = m.sql.input.Update(msg)
	// A newline (or its removal) changes the editor height; reflow the result
	// viewport so it keeps filling the space the editor leaves behind.
	if m.sql.input.Value() != old {
		m.layoutPanes()
	}
	return m, cmd
}

// sqlSelectedPost returns the post under the result cursor, or nil.
func (m *Model) sqlSelectedPost() *model.Post {
	if m.sql.idx < 0 || m.sql.idx >= len(m.sql.posts) {
		return nil
	}
	return m.sql.posts[m.sql.idx]
}

// handleSQLResultsKey owns keystrokes while focus == focusSQLResults: the
// result list has the cursor. Up/Down (and Home/End/PgUp/PgDn) move the
// selection; the read-only message actions (view/open attachment, image
// preview, download, copy markdown/code) act on the selected row by reusing
// the same handlers the messages pane uses; esc / i return to the editor.
func (m Model) handleSQLResultsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	n := len(m.sql.posts)
	switch {
	case msg.String() == "ctrl+c": // hardwired quit
		return m, tea.Quit
	case msg.String() == "esc":
		// Back to the editor to refine the query. (i also returns there, via
		// focusComposer — the global handler claims it before this one.)
		m.focus = focusSQL
		m.renderSQLResults() // drop the selection bar
		return m, m.sql.input.Focus()
	case key.Matches(msg, m.keys.Up):
		if m.sql.idx > 0 {
			m.sql.idx--
			m.renderSQLResults()
		}
		return m, nil
	case key.Matches(msg, m.keys.Down):
		if m.sql.idx < n-1 {
			m.sql.idx++
			m.renderSQLResults()
		}
		return m, nil
	case key.Matches(msg, m.keys.Home):
		m.sql.idx = 0
		m.renderSQLResults()
		return m, nil
	case key.Matches(msg, m.keys.End):
		m.sql.idx = n - 1
		m.renderSQLResults()
		return m, nil
	case key.Matches(msg, m.keys.PageUp):
		m.sql.idx -= m.messagesPageStep()
		if m.sql.idx < 0 {
			m.sql.idx = 0
		}
		m.renderSQLResults()
		return m, nil
	case key.Matches(msg, m.keys.PageDown):
		m.sql.idx += m.messagesPageStep()
		if m.sql.idx > n-1 {
			m.sql.idx = n - 1
		}
		m.renderSQLResults()
		return m, nil
	case key.Matches(msg, m.keys.Tab):
		return m.cycleFocus(1)
	case key.Matches(msg, m.keys.ShiftTab):
		return m.cycleFocus(-1)
	}

	// Read-only message actions on the selected row — the same handlers the
	// messages pane uses, so attachments / previews behave identically.
	p := m.sqlSelectedPost()
	if p == nil {
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.OpenAttach):
		return m.openFromPost(p)
	case key.Matches(msg, m.keys.Preview):
		return m.openImagePreview(p)
	case key.Matches(msg, m.keys.Download):
		return m.downloadFromPost(p)
	case key.Matches(msg, m.keys.CopyMD):
		return m, m.copyPostMarkdown(p)
	case key.Matches(msg, m.keys.CopyCode):
		return m.copyCodeFromPost(p)
	}
	return m, nil
}

// clickSQLRow handles a left click on the SQL tab. idx -1 is the editor region
// above the results — clicking there focuses the query editor; idx >= 0 selects
// that result row and focuses the result list, so the message action keys act
// on it. A click only selects: there's no single canonical "open" action.
func (m Model) clickSQLRow(idx int) (tea.Model, tea.Cmd) {
	if idx < 0 {
		m.focus = focusSQL
		m.renderSQLResults() // drop the selection bar
		return m, m.sql.input.Focus()
	}
	if idx >= len(m.sql.posts) {
		return m, nil
	}
	m.focus = focusSQLResults
	m.sql.idx = idx
	m.sql.input.Blur()
	m.renderSQLResults()
	return m, nil
}

// runSQL kicks off the query currently in the editor on a worker goroutine.
// Empty queries are a no-op; a missing store shows the not-ready hint.
func (m Model) runSQL() (tea.Model, tea.Cmd) {
	q := strings.TrimSpace(m.sql.input.Value())
	if q == "" {
		return m, nil
	}
	if m.store == nil {
		m.sql.notReady = true
		m.renderSQLResults()
		return m, nil
	}
	m.sql.seq++
	m.sql.loading = true
	m.sql.err = ""
	m.renderSQLResults()
	return m, m.runSQLQuery(m.sql.seq, q)
}

// runSQLQuery runs the query on the store's read-only handle off the UI
// goroutine and reports back via sqlResultsMsg.
func (m Model) runSQLQuery(seq int, query string) tea.Cmd {
	st := m.store
	parent := m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, sqlQueryTimeout)
		defer cancel()
		res, err := st.RawQuery(ctx, query, sqlMaxRows)
		out := sqlResultsMsg{seq: seq, query: query, err: errString(err)}
		if res != nil {
			out.cols = res.Columns
			out.rows = res.Rows
			out.truncated = res.Truncated
		}
		return out
	}
}

// applySQLResults installs the rows from a finished query if it's still fresh.
// When the query returned rows, focus drops from the editor into the result
// list so the message action keys (view image, preview, download, copy) act on
// the selected row straight away.
func (m Model) applySQLResults(msg sqlResultsMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.sql.seq {
		return m, nil
	}
	m.sql.loading = false
	m.sql.query = msg.query
	m.sql.err = msg.err
	m.sql.cols = msg.cols
	m.sql.rows = msg.rows
	m.sql.truncated = msg.truncated
	m.sql.idx = 0
	// Reconstruct each row into a post once, here, so renderSQLResults (which
	// runs on every selection move) doesn't re-parse raw_json per keystroke.
	m.sql.posts = make([]*model.Post, len(msg.rows))
	for i, row := range msg.rows {
		m.sql.posts[i] = reconstructSQLPost(msg.cols, row)
	}
	m.sql.view.GotoTop()
	// Drop into the results so they're immediately navigable — but only if the
	// user is still sitting in the editor that launched the query (don't yank
	// focus if they tabbed to the strip or another tab meanwhile).
	if msg.err == "" && len(msg.rows) > 0 && m.onSQLTab() && m.focus == focusSQL {
		m.focus = focusSQLResults
		m.sql.input.Blur()
	}
	m.renderSQLResults()
	return m, nil
}

// sizeSQLView keeps the SQL viewport in sync with the body area and the
// (dynamic) editor height. width is the pane's outer width (border included);
// height is the pane's inner content height (border already subtracted).
func (m *Model) sizeSQLView(width, height int) {
	if !m.sql.built {
		return // partially-built test Model; nothing to size yet
	}
	innerW := width - 2 // strip the pane's left/right border
	if innerW < 10 {
		innerW = 10
	}
	m.sql.input.SetWidth(innerW - 4)
	// Header rows inside the pane:
	//   1            — titleRow
	//   1 + inputH   — inputBox (top border + the editor's current height)
	//   1            — rule
	headerRows := 3 + m.sql.input.Height()
	bodyH := height - headerRows
	if bodyH < 1 {
		bodyH = 1
	}
	m.sql.view.SetWidth(innerW)
	m.sql.view.SetHeight(bodyH)
}

// renderSQLResults populates the result viewport: each row rendered as a chat
// message whose author is prefixed with its team/channel (or DM) breadcrumb.
// When the results have focus the selected row is marked with a selection bar
// and scrolled into view, mirroring the messages pane.
func (m *Model) renderSQLResults() {
	m.sqlContentVer++ // invalidate the wrap-index cache (link hit-testing)
	m.sql.rowStarts = nil
	dim := lipgloss.NewStyle().Foreground(dimColor)
	switch {
	case m.sql.notReady:
		m.sql.view.SetContent(dim.Render("  local message cache is unavailable — the SQL tab needs the SQLite store"))
		return
	case m.sql.err != "":
		m.sql.view.SetContent(lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("  error: " + m.sql.err))
		return
	case m.sql.loading:
		m.sql.view.SetContent(dim.Render("  running…"))
		return
	case m.sql.query == "":
		m.sql.view.SetContent(dim.Render(strings.Join([]string{
			"  read-only SQL over your message cache — enter runs, alt+↵ for a newline",
			"",
			"  examples:",
			"    SELECT * FROM posts ORDER BY create_at DESC LIMIT 50",
			"    SELECT * FROM posts WHERE message LIKE '%deploy%' ORDER BY create_at DESC",
			"    SELECT channel_id, COUNT(*) AS n FROM posts GROUP BY channel_id ORDER BY n DESC",
		}, "\n")))
		return
	case len(m.sql.rows) == 0:
		m.sql.view.SetContent(dim.Render("  0 rows"))
		return
	}

	// Clamp the selection in case the result set shrank under it.
	if m.sql.idx >= len(m.sql.posts) {
		m.sql.idx = len(m.sql.posts) - 1
	}
	if m.sql.idx < 0 {
		m.sql.idx = 0
	}

	width := m.sql.view.Width()
	decorate := m.focus == focusSQLResults
	bar := selectedBarStyle.Render("▎")

	var lines []string
	rowStarts := make([]int, len(m.sql.posts)+1)
	selVisStart, selVisRows := -1, 0
	visAcc := 0
	for i, p := range m.sql.posts {
		if i > 0 {
			lines = append(lines, "") // blank separator between rows
			visAcc++
		}
		rowStarts[i] = visAcc
		chunk := m.renderSQLRow(p, m.sql.cols, m.sql.rows[i], width)
		if i == m.sql.idx {
			selVisStart = visAcc
			if decorate {
				chunk = decorateSelected(chunk, bar)
			}
		}
		rows := postVisualRows(chunk, width)
		if i == m.sql.idx {
			selVisRows = rows
		}
		lines = append(lines, chunk...)
		visAcc += rows
	}
	rowStarts[len(m.sql.posts)] = visAcc
	m.sql.rowStarts = rowStarts
	if m.sql.truncated {
		lines = append(lines, "", dim.Render("  … more rows — showing the first "+strconv.Itoa(sqlMaxRows)))
	}
	m.sql.view.SetContentLines(lines)

	// Scroll the selected row into view (only meaningful while the list has focus).
	if h := m.sql.view.Height(); h > 0 && decorate && selVisStart >= 0 {
		visStart, visEnd := selVisStart, selVisStart+selVisRows
		off := m.sql.view.YOffset()
		switch {
		case visStart < off:
			off = visStart
		case visEnd > off+h:
			off = visEnd - h
		}
		if off < 0 {
			off = 0
		}
		m.sql.view.SetYOffset(off)
	}
}

// decorateSelected prefixes each line of a rendered row with the selection bar,
// replacing the two-space gutter so the content stays at the same x-position
// (mirrors renderMessages).
func decorateSelected(chunk []string, bar string) []string {
	out := make([]string, len(chunk))
	for j, l := range chunk {
		if strings.HasPrefix(l, "  ") {
			out[j] = bar + " " + l[2:]
		} else {
			out[j] = bar + " " + l
		}
	}
	return out
}

// reconstructSQLPost turns one result row into a *model.Post. raw_json is the
// richest source (attachments, reactions, props); explicit columns override /
// fill it in. Done once per result (not per render) — see applySQLResults.
func reconstructSQLPost(cols []string, row []any) *model.Post {
	idx := make(map[string]int, len(cols))
	for i, c := range cols {
		idx[strings.ToLower(c)] = i
	}
	cell := func(name string) (any, bool) {
		if i, ok := idx[name]; ok && i < len(row) {
			return row[i], true
		}
		return nil, false
	}
	var p model.Post
	if v, ok := cell("raw_json"); ok {
		if b := sqlCellBytes(v); len(b) > 0 {
			_ = json.Unmarshal(b, &p)
		}
	}
	if v, ok := cell("id"); ok {
		p.Id = sqlCellString(v)
	}
	if v, ok := cell("user_id"); ok {
		p.UserId = sqlCellString(v)
	}
	if v, ok := cell("channel_id"); ok {
		p.ChannelId = sqlCellString(v)
	}
	if v, ok := cell("root_id"); ok {
		p.RootId = sqlCellString(v)
	}
	if v, ok := cell("message"); ok {
		p.Message = sqlCellString(v)
	}
	if v, ok := cell("create_at"); ok {
		if n, ok := sqlCellInt64(v); ok {
			p.CreateAt = n
		}
	}
	return &p
}

// renderSQLRow renders one result row (its post already reconstructed) as a
// chat message. When the row carries post content it looks like a normal
// message — markdown body, attachments, reactions — with the author prefixed
// by its "Team › #channel" (or "DMs › @user") breadcrumb. Columns the chat
// view doesn't consume are appended as a dim "col=value" line, so aggregate /
// custom queries stay readable.
func (m *Model) renderSQLRow(p *model.Post, cols []string, row []any, width int) []string {
	// Header: prefixed author + (when known) timestamp.
	header := userStyle.Render(m.sqlAuthorName(p))
	if p.CreateAt > 0 {
		header += "  " + timeStyle.Render(formatPostTime(p.CreateAt))
	}
	lines := []string{header}

	if p.Message != "" {
		lines = appendBodyLines(lines, m.markdownBody(p), width)
	}
	if att := renderAttachments(p, width); att != "" {
		for _, l := range strings.Split(att, "\n") {
			lines = append(lines, wrapBodyLine(l, width)...)
		}
	}
	if rx := m.renderReactions(p); rx != "" {
		lines = append(lines, wrapBodyLine(rx, width)...)
	}

	// Extra columns (anything the chat view didn't already use) as a dim line.
	idx := make(map[string]int, len(cols))
	for i, c := range cols {
		idx[strings.ToLower(c)] = i
	}
	var extras []string
	for _, c := range cols {
		if sqlConsumed[strings.ToLower(c)] {
			continue
		}
		extras = append(extras, c+"="+sqlCellString(row[idx[strings.ToLower(c)]]))
	}
	if len(extras) > 0 {
		extraStyle := lipgloss.NewStyle().Foreground(dimColor)
		for _, l := range wrapBodyLine("  "+strings.Join(extras, "  "), width) {
			lines = append(lines, extraStyle.Render(l))
		}
	}
	return lines
}

// sqlAuthorName builds the breadcrumb-prefixed author for a result row:
// "Team › #channel › username" for a normal channel, "DMs › @partner › username"
// for a DM (the partner and the author can be the same person). Falls back to a
// short channel id when the channel isn't in the local list, and to just the
// author or "(row)" when there's no channel.
func (m *Model) sqlAuthorName(p *model.Post) string {
	author := ""
	if p.UserId != "" || p.Message != "" {
		author = m.postAuthorName(p)
	}
	prefix := ""
	if p.ChannelId != "" {
		if ch := m.findChannel(p.ChannelId); ch != nil {
			prefix = m.channelBreadcrumb(ch)
		} else {
			short := p.ChannelId
			if len(short) > 8 {
				short = short[:8]
			}
			prefix = "#" + short
		}
	}
	switch {
	case prefix != "" && author != "":
		return prefix + " › " + author
	case prefix != "":
		return prefix
	case author != "":
		return author
	default:
		return "(row)"
	}
}

// renderSQLPane composes the entire body of the SQL tab: title, the multi-line
// query editor, a separator rule, then the result viewport. Mirrors
// renderSearchPane with a textarea instead of a single-line input.
func (m Model) renderSQLPane(height, width int) string {
	innerH := height - 1 // bottom border (top connects to the tab strip)
	if innerH < 1 {
		innerH = 1
	}
	if width < 10 {
		width = 10
	}

	dim := lipgloss.NewStyle().Foreground(dimColor)
	title := titleStyle.Render("SQL")
	var meta string
	switch {
	case m.sql.notReady:
		meta = dim.Render("  unavailable")
	case m.sql.loading:
		meta = dim.Render("  running…")
	case m.sql.err != "":
		meta = dim.Render("  error")
	case m.sql.query != "":
		label := plural(len(m.sql.rows), "row", "rows")
		if m.sql.truncated {
			label = itoa(len(m.sql.rows)) + "+ rows"
		}
		meta = dim.Render("  " + label)
	}
	titleRow := title + meta

	inputBorder := dimColor
	if m.focus == focusSQL {
		inputBorder = focusedColor
	}
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(inputBorder).
		Width(width - 2).
		Render(m.sql.input.View())

	rule := dim.Render(strings.Repeat("─", width-2))
	body := m.sql.view.View()
	rows := []string{titleRow, inputBox, rule, body}

	style := lipgloss.NewStyle().
		Border(border).
		UnsetBorderTop().
		Width(width).
		Height(innerH)
	if m.focus == focusSQL {
		style = style.BorderForeground(focusedColor)
	} else {
		style = style.BorderForeground(dimColor)
	}
	return style.Render(strings.Join(rows, "\n"))
}

// sqlCellString renders a raw driver value for display.
func sqlCellString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case time.Time:
		return t.Local().Format("2006-01-02 15:04:05")
	default:
		return ""
	}
}

// sqlCellBytes returns the raw bytes of a TEXT/BLOB cell (raw_json), or nil.
func sqlCellBytes(v any) []byte {
	switch t := v.(type) {
	case []byte:
		return t
	case string:
		return []byte(t)
	default:
		return nil
	}
}

// sqlCellInt64 coerces a numeric cell to int64 (for create_at and friends).
func sqlCellInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case float64:
		return int64(t), true
	case []byte:
		if n, err := strconv.ParseInt(string(t), 10, 64); err == nil {
			return n, true
		}
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}
