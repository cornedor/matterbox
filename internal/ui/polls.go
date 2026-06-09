package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Matterpoll plugin emits action buttons with these well-known IDs.
const (
	pollVoteActionPrefix    = "vote"
	pollAddOptionActionID   = "addOption"
	pollEndActionID         = "endPoll"
	pollDeleteActionID      = "deletePoll"
	pollPostTypeMatterpoll  = "custom_matterpoll"
	pollAdminButtonTypeName = "custom_matterpoll_admin_button"
)

var (
	pollTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13")) // magenta
	pollOptionStyle = lipgloss.NewStyle()
	pollAccelStyle  = lipgloss.NewStyle().Foreground(focusedColor).Bold(true)
	pollFooterStyle = lipgloss.NewStyle().Foreground(dimColor).Italic(true)
	pollAdminStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")) // red
	pollBarStyle    = lipgloss.NewStyle().Foreground(dimColor)
	pollAuthorStyle = lipgloss.NewStyle().Foreground(dimColor)
)

// pollAction is a button extracted from a poll post's attachment, paired
// with the index assigned for the user-facing accelerator (1-9). Vote
// buttons are numbered; admin/utility buttons keep accel == 0.
type pollAction struct {
	id    string
	name  string
	vote  bool   // vote0/vote1/... — gets a numeric accelerator
	admin bool   // custom_matterpoll_admin_button — restricted to creator
	accel rune   // user-facing accelerator key (only set for vote, addOption, endPoll, deletePoll)
}

// pollData is what we render inline. It's reconstructed every render
// from the post's props so we don't have to worry about staleness when
// a post_edited WS event lands.
type pollData struct {
	title      string
	authorName string
	text       string // poll subtitle (totals, etc) — already markdown
	actions    []pollAction
	fields     []*model.MessageAttachmentField // final results when ended
	ended      bool
}

// isPoll returns true when the post is renderable as a poll: it carries
// an attachment with actions (live poll) or with result fields (ended).
// Both matterpoll and any other plugin that emits interactive
// attachments will trip this — the inline rendering doesn't assume
// matterpoll specifically except in the action IDs.
func isPoll(p *model.Post) bool {
	if p == nil {
		return false
	}
	atts := p.Attachments()
	if len(atts) == 0 {
		return false
	}
	for _, a := range atts {
		if a == nil {
			continue
		}
		if len(a.Actions) > 0 || len(a.Fields) > 0 {
			return true
		}
	}
	return false
}

// extractPoll pulls the first poll-shaped attachment off the post. Only
// the first is rendered — matterpoll polls only ever attach one and
// trying to be cleverer would muddle the inline layout.
func extractPoll(p *model.Post) *pollData {
	if p == nil {
		return nil
	}
	atts := p.Attachments()
	if len(atts) == 0 {
		return nil
	}
	a := atts[0]
	if a == nil {
		return nil
	}

	out := &pollData{
		title:      a.Title,
		authorName: a.AuthorName,
		text:       a.Text,
		fields:     a.Fields,
	}

	voteIdx := 0
	for _, act := range a.Actions {
		if act == nil {
			continue
		}
		pa := pollAction{id: act.Id, name: act.Name, admin: act.Type == pollAdminButtonTypeName}
		switch {
		case strings.HasPrefix(act.Id, pollVoteActionPrefix):
			pa.vote = true
			voteIdx++
			if voteIdx <= 9 {
				pa.accel = rune('0' + voteIdx)
			}
		case act.Id == pollAddOptionActionID:
			pa.accel = 'a'
		case act.Id == pollEndActionID:
			pa.accel = 'E'
		case act.Id == pollDeleteActionID:
			pa.accel = 'X'
		}
		out.actions = append(out.actions, pa)
	}

	// A poll with fields and no vote-actions is the "ended" state
	// matterpoll renders ("This poll has ended. The results are:").
	if len(a.Fields) > 0 {
		out.ended = true
		// In the ended state matterpoll still includes adminbutton
		// actions (Delete) — leave them in so the creator can clean up.
	}
	return out
}

// renderPoll returns the inline poll block as a slice of pre-indented
// lines so the existing two-space gutter convention is preserved.
// Width is the viewport's inner width (so lines wrap predictably).
// isSelected controls whether the interaction hint shows; we only
// surface it when the user could act on the poll right now.
func (m *Model) renderPoll(p *model.Post, width int, isSelected bool) []string {
	pd := extractPoll(p)
	if pd == nil {
		return nil
	}

	const gutter = "  "
	innerWidth := width - len(gutter)
	if innerWidth < 10 {
		innerWidth = 10
	}

	var lines []string

	if pd.title != "" {
		lines = append(lines, gutter+pollBarStyle.Render("┃ ")+pollTitleStyle.Render(pd.title))
	}
	if pd.authorName != "" {
		lines = append(lines, gutter+pollBarStyle.Render("┃ ")+pollAuthorStyle.Render("by "+pd.authorName))
	}

	if pd.ended {
		// Render final results as a list. Each Field's Value is a string
		// listing the voters; Title contains the option label + vote count.
		for _, f := range pd.fields {
			if f == nil {
				continue
			}
			title := strings.TrimSpace(f.Title)
			valStr := fmt.Sprintf("%v", f.Value)
			row := gutter + pollBarStyle.Render("┃ ") + pollTitleStyle.Render("✓ "+title)
			lines = append(lines, row)
			if valStr != "" && valStr != "<nil>" {
				lines = append(lines, gutter+pollBarStyle.Render("┃   ")+pollFooterStyle.Render(valStr))
			}
		}
		if t := strings.TrimSpace(stripMarkdownEm(pd.text)); t != "" {
			lines = append(lines, gutter+pollBarStyle.Render("┃ ")+pollFooterStyle.Render(t))
		}
		// Even ended polls may still have admin buttons (Delete).
		if hint := m.pollHint(pd, isSelected); hint != "" {
			lines = append(lines, gutter+pollBarStyle.Render("┃ ")+hint)
		}
		return lines
	}

	// Active poll — render vote options and footer/admin actions.
	for _, act := range pd.actions {
		if !act.vote {
			continue
		}
		var accel string
		if act.accel != 0 {
			accel = pollAccelStyle.Render(fmt.Sprintf("[%c]", act.accel))
		} else {
			accel = "[ ]"
		}
		row := fmt.Sprintf("%s%s %s %s", gutter, pollBarStyle.Render("┃"), accel, pollOptionStyle.Render(act.name))
		lines = append(lines, row)
	}
	if t := strings.TrimSpace(stripMarkdownEm(pd.text)); t != "" {
		lines = append(lines, gutter+pollBarStyle.Render("┃ ")+pollFooterStyle.Render(t))
	}

	// Trailing actions row: Add Option / End Poll / Delete Poll.
	var actRow []string
	for _, act := range pd.actions {
		if act.vote {
			continue
		}
		accel := ""
		if act.accel != 0 {
			accel = pollAccelStyle.Render(fmt.Sprintf("[%c] ", act.accel))
		}
		style := pollOptionStyle
		if act.admin {
			style = pollAdminStyle
		}
		actRow = append(actRow, accel+style.Render(act.name))
	}
	if len(actRow) > 0 {
		lines = append(lines, gutter+pollBarStyle.Render("┃ ")+strings.Join(actRow, "  "))
	}
	if hint := m.pollHint(pd, isSelected); hint != "" {
		lines = append(lines, gutter+pollBarStyle.Render("┃ ")+hint)
	}
	return lines
}

// stripMarkdownEm removes "---" horizontal rule lines and trims the
// pseudo-divider matterpoll uses in attachment text ("---\n**Total
// votes**: 3"). We render that on our own footer line instead.
func stripMarkdownEm(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) == "---" {
			continue
		}
		// Strip the **bold** markers so the text reads cleanly in our
		// dim/italic footer style — re-rendering markdown inside the
		// inline poll block would clash with the bar gutter alignment.
		l = strings.ReplaceAll(l, "**", "")
		out = append(out, l)
	}
	return strings.Join(out, " ")
}

// pollHint renders the short keyboard hint shown under the poll's
// actions. Empty when the post isn't currently selected — keys won't
// work on unselected posts, so the hint would be misleading.
func (m *Model) pollHint(pd *pollData, isSelected bool) string {
	if !isSelected || pd == nil {
		return ""
	}
	if pd.ended {
		// In the ended state only admin cleanup remains.
		hasDelete := false
		for _, a := range pd.actions {
			if a.id == pollDeleteActionID {
				hasDelete = true
				break
			}
		}
		if hasDelete {
			return pollFooterStyle.Render("X delete poll")
		}
		return ""
	}
	var parts []string
	if hasAction(pd, pollAddOptionActionID) {
		parts = append(parts, "a add option")
	}
	if hasAction(pd, pollEndActionID) {
		parts = append(parts, "E end")
	}
	if hasAction(pd, pollDeleteActionID) {
		parts = append(parts, "X delete")
	}
	parts = append([]string{"1-9 vote"}, parts...)
	return pollFooterStyle.Render(strings.Join(parts, " · "))
}

func hasAction(pd *pollData, id string) bool {
	for _, a := range pd.actions {
		if a.id == id {
			return true
		}
	}
	return false
}

// pollActionByKey resolves a single-rune accelerator keystroke to a
// pollAction. Returns ok=false when nothing matches — the caller falls
// back to the default messages-pane handler.
func pollActionByKey(p *model.Post, ch rune) (pollAction, bool) {
	pd := extractPoll(p)
	if pd == nil {
		return pollAction{}, false
	}
	for _, a := range pd.actions {
		if a.accel != 0 && a.accel == ch {
			return a, true
		}
	}
	return pollAction{}, false
}

// pollActionResultMsg lands on the bubbletea loop after a fire-and-forget
// DoPostAction completes. The server broadcasts a `post_edited` WS event
// shortly after which will replace the post in-place — this message is
// only here to surface errors and tweak status.
type pollActionResultMsg struct {
	actionID string
	err      error
}

// doPollAction returns the tea.Cmd that fires `actionID` against postID.
// All result handling routes through pollActionResultMsg.
func (m Model) doPollAction(postID, actionID string) tea.Cmd {
	client := m.client
	ctx := m.ctx
	return func() tea.Msg {
		err := client.DoPostAction(ctx, postID, actionID)
		return pollActionResultMsg{actionID: actionID, err: err}
	}
}

// ── Add-Option dialog (interactive dialog modal) ────────────────────────

// pollDialogState owns the modal-input flow used to satisfy interactive
// dialogs opened by plugin button presses (the main caller is matterpoll's
// "Add Option" action). Multiple text elements are supported; idx is the
// currently-focused input.
type pollDialogState struct {
	open       bool
	url        string
	callbackID string
	state      string
	channelID  string
	teamID     string
	title      string
	intro      string
	submitLbl  string
	elements   []model.DialogElement
	inputs     []textinput.Model
	idx        int
	submitting bool
	errMsg     string
}

// openPollDialog inflates the modal from the data carried on the
// open_dialog WS event. Returns true when at least one input element was
// present and the modal is now open. The caller is responsible for the
// (rare) error path when the WS data isn't shaped like an OpenDialogRequest.
func (m *Model) openPollDialog(req model.OpenDialogRequest) bool {
	if len(req.Dialog.Elements) == 0 {
		// A dialog without text inputs is just a confirm — matterpoll
		// doesn't currently emit one, but if a plugin did we'd auto-submit
		// with an empty submission to keep the flow moving. We don't auto-
		// submit blindly though; better to surface a status hint.
		m.status = "dialog \"" + req.Dialog.Title + "\" has no inputs to fill"
		return false
	}
	st := pollDialogState{
		open:       true,
		url:        req.URL,
		callbackID: req.Dialog.CallbackId,
		state:      req.Dialog.State,
		title:      req.Dialog.Title,
		intro:      req.Dialog.IntroductionText,
		submitLbl:  req.Dialog.SubmitLabel,
		elements:   req.Dialog.Elements,
	}
	for _, el := range req.Dialog.Elements {
		ti := textinput.New()
		ti.Prompt = "❯ "
		ti.CharLimit = 256
		if el.MaxLength > 0 {
			ti.CharLimit = el.MaxLength
		}
		if el.Placeholder != "" {
			ti.Placeholder = el.Placeholder
		}
		if el.Default != "" {
			ti.SetValue(el.Default)
		}
		ti.SetWidth(46)
		st.inputs = append(st.inputs, ti)
	}
	st.inputs[0].Focus()
	// Channel + team are needed in the submission request. When a thread
	// is open and a poll lives inside it, the thread's channel is what
	// the user just acted on. Otherwise the poll targets the open channel,
	// not the sidebar cursor — those diverge once the selection moves.
	if m.threadOpen && m.threadChannelID != "" {
		st.channelID = m.threadChannelID
		st.teamID = m.threadTeamID()
	} else if ch := m.findChannel(m.openChannelID); ch != nil {
		st.channelID = ch.Id
		st.teamID = ch.TeamId
	}
	m.pollDialog = st
	return true
}

func (m *Model) closePollDialog() {
	m.pollDialog = pollDialogState{}
}

// renderPollDialog draws the modal popup. Layout mirrors the other
// modals — rounded border, centred title, then one input per element
// with the focused row highlighted. Footer shows submit + cancel keys.
func (m *Model) renderPollDialog() string {
	if !m.pollDialog.open {
		return ""
	}
	outerW := confirmDialogMaxWidth
	if outerW > m.width-4 {
		outerW = m.width - 4
	}
	if outerW < 40 {
		outerW = 40
	}
	inner := outerW - 8
	if inner < 1 {
		inner = 1
	}

	title := m.pollDialog.title
	if title == "" {
		title = "Dialog"
	}
	header := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Bold(true).Render(title)
	intro := ""
	if m.pollDialog.intro != "" {
		intro = lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Foreground(dimColor).Render(m.pollDialog.intro)
	}

	rows := make([]string, 0, len(m.pollDialog.elements)*3)
	for i, el := range m.pollDialog.elements {
		label := el.DisplayName
		if label == "" {
			label = el.Name
		}
		labelStyle := lipgloss.NewStyle().Bold(true)
		if i == m.pollDialog.idx {
			labelStyle = labelStyle.Foreground(focusedColor)
		}
		rows = append(rows, labelStyle.Render(label))
		if el.HelpText != "" {
			rows = append(rows, lipgloss.NewStyle().Foreground(dimColor).Italic(true).Render(el.HelpText))
		}
		rows = append(rows, m.pollDialog.inputs[i].View())
		rows = append(rows, "")
	}

	footer := lipgloss.NewStyle().
		Width(inner).
		Align(lipgloss.Center).
		Foreground(dimColor).
		Italic(true).
		Render("tab next · ↵ submit · esc cancel")

	if m.pollDialog.errMsg != "" {
		rows = append(rows, lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(m.pollDialog.errMsg))
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		header,
		intro,
		"",
		strings.Join(rows, "\n"),
		footer,
	)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(focusedColor).
		Padding(1, 3).
		Render(body)
}

// handlePollDialogKey owns every keystroke while the dialog is open.
// Tab/shift-tab cycle the focused element, enter submits, esc cancels.
// Anything else routes into the currently-focused textinput.
func (m Model) handlePollDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closePollDialog()
		return m, nil
	case "enter":
		return m.submitPollDialog()
	case "tab":
		if len(m.pollDialog.inputs) > 1 {
			m.pollDialog.inputs[m.pollDialog.idx].Blur()
			m.pollDialog.idx = (m.pollDialog.idx + 1) % len(m.pollDialog.inputs)
			m.pollDialog.inputs[m.pollDialog.idx].Focus()
		}
		return m, nil
	case "shift+tab":
		if len(m.pollDialog.inputs) > 1 {
			m.pollDialog.inputs[m.pollDialog.idx].Blur()
			m.pollDialog.idx = (m.pollDialog.idx - 1 + len(m.pollDialog.inputs)) % len(m.pollDialog.inputs)
			m.pollDialog.inputs[m.pollDialog.idx].Focus()
		}
		return m, nil
	}
	// Forward to the focused input.
	if m.pollDialog.idx < len(m.pollDialog.inputs) {
		var cmd tea.Cmd
		m.pollDialog.inputs[m.pollDialog.idx], cmd = m.pollDialog.inputs[m.pollDialog.idx].Update(msg)
		return m, cmd
	}
	return m, nil
}

// pollDialogSubmittedMsg lands on the loop after SubmitInteractiveDialog
// returns. We use it both to surface error rows from the server (e.g.
// "duplicate option") and to close the modal on success.
type pollDialogSubmittedMsg struct {
	err  error
	resp *model.SubmitDialogResponse
}

func (m Model) submitPollDialog() (tea.Model, tea.Cmd) {
	if !m.pollDialog.open {
		return m, nil
	}
	// Collect submission map keyed by element Name.
	submission := map[string]any{}
	for i, el := range m.pollDialog.elements {
		val := strings.TrimSpace(m.pollDialog.inputs[i].Value())
		if val == "" && !el.Optional {
			m.pollDialog.errMsg = el.DisplayName + " is required"
			return m, nil
		}
		submission[el.Name] = val
	}
	if m.me == nil {
		m.pollDialog.errMsg = "no user session"
		return m, nil
	}
	req := &model.SubmitDialogRequest{
		Type:       "dialog_submission",
		URL:        m.pollDialog.url,
		CallbackId: m.pollDialog.callbackID,
		State:      m.pollDialog.state,
		UserId:     m.me.Id,
		ChannelId:  m.pollDialog.channelID,
		TeamId:     m.pollDialog.teamID,
		Submission: submission,
		Cancelled:  false,
	}
	m.pollDialog.submitting = true
	m.pollDialog.errMsg = ""
	client := m.client
	ctx := m.ctx
	return m, func() tea.Msg {
		resp, err := client.SubmitDialog(ctx, req)
		return pollDialogSubmittedMsg{err: err, resp: resp}
	}
}

// applyPollDialogResult reads the response from SubmitInteractiveDialog.
// A plain error (HTTP) tears the modal down with a status message. A
// per-element error map keeps the modal open with the offending fields
// flagged so the user can fix and retry.
func (m *Model) applyPollDialogResult(msg pollDialogSubmittedMsg) {
	m.pollDialog.submitting = false
	if msg.err != nil {
		m.pollDialog.errMsg = msg.err.Error()
		return
	}
	if msg.resp != nil {
		if msg.resp.Error != "" {
			m.pollDialog.errMsg = msg.resp.Error
			return
		}
		if len(msg.resp.Errors) > 0 {
			// Pick the first field error to surface in our small modal.
			for _, v := range msg.resp.Errors {
				m.pollDialog.errMsg = v
				break
			}
			return
		}
	}
	m.closePollDialog()
	m.status = "option added"
}

// applyOpenDialog parses an open_dialog WS event and opens the modal.
// The event data["dialog"] field is JSON-encoded by the server (per
// other event payloads); per Mattermost's web client it can also arrive
// as a typed map, so we re-marshal whatever we got and unmarshal into
// OpenDialogRequest for resilience.
func (m *Model) applyOpenDialog(ev *model.WebSocketEvent) {
	raw := ev.GetData()["dialog"]
	if raw == nil {
		return
	}
	var req model.OpenDialogRequest
	switch v := raw.(type) {
	case string:
		if err := json.Unmarshal([]byte(v), &req); err != nil {
			return
		}
	default:
		b, err := json.Marshal(raw)
		if err != nil {
			return
		}
		if err := json.Unmarshal(b, &req); err != nil {
			return
		}
	}
	// Some servers/plugins put the URL+trigger at the top level instead
	// of inside the Dialog struct — preserve them if so.
	if req.URL == "" {
		if s, ok := ev.GetData()["url"].(string); ok {
			req.URL = s
		}
	}
	if req.TriggerId == "" {
		if s, ok := ev.GetData()["trigger_id"].(string); ok {
			req.TriggerId = s
		}
	}
	m.openPollDialog(req)
}

// handlePollKey is the entry point from handleMessagesKey /
// handleThreadKey when a poll-bearing post is selected and the user
// pressed an accelerator. Returns (handled, cmd). When handled is false
// the caller falls through to its normal handling so existing keys
// (Tab, ↑/↓, etc.) still work on non-vote keystrokes.
func (m Model) handlePollKey(p *model.Post, msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if p == nil || p.Id == "" || !isPoll(p) {
		return m, nil, false
	}
	s := msg.String()
	if len(s) != 1 {
		return m, nil, false
	}
	r := rune(s[0])
	action, ok := pollActionByKey(p, r)
	if !ok {
		return m, nil, false
	}
	switch {
	case action.vote:
		m.status = "voting…"
	case action.id == pollAddOptionActionID:
		m.status = "opening add-option dialog…"
	case action.id == pollEndActionID:
		m.status = "ending poll…"
	case action.id == pollDeleteActionID:
		m.status = "deleting poll…"
	}
	return m, m.doPollAction(p.Id, action.id), true
}


