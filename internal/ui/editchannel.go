package ui

import (
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Editing the open channel's identity: display name, URL slug, purpose and
// header. Three > palette entries (Rename channel, Edit purpose, Edit header)
// raise this one modal, each landing the cursor on the row it's named for —
// all four fields go to the same PATCH /channels/{id}, so separate dialogs
// would only be separate ways to type the same request.
//
// Privacy is deliberately NOT a row here: public↔private is its own endpoint
// with its own permissions and its own consequences, so it's a confirm instead
// (see channelactions.go). Only the fields the user actually changed are sent,
// so editing a purpose can't trip the rename permission.

// Form rows, in display order. All four are text inputs.
const (
	ceDisplayName = iota
	ceURL
	cePurpose
	ceHeader
	ceRowCount
)

// channelEditState owns the modal. Boxed on Model (nil when closed) for the
// same reason createChannelState is: fat textinputs that no unconditional
// layout/render pass touches.
type channelEditState struct {
	channelID string
	label     string // channel label at open, for the title

	// before holds the values the modal opened with, so submit can send only
	// what actually changed.
	before [ceRowCount]string

	inputs [ceRowCount]textinput.Model
	row    int

	submitting bool
	errMsg     string
}

// canEditChannel reports whether the channel's properties can be edited. DMs
// and group DMs have no display name, URL or purpose of their own, so they're
// excluded; the server would reject the patch anyway.
func canEditChannel(c *model.Channel) bool {
	return c != nil && (c.Type == model.ChannelTypeOpen || c.Type == model.ChannelTypePrivate)
}

// editChannelCommands returns the palette entries that raise the edit modal for
// the open channel, and whether any apply (nothing to edit on the Feed/Search/
// SQL tabs, or in a DM). Each entry opens the same form on a different row.
func (m Model) editChannelCommands() ([]switcherCommand, bool) {
	c := m.findChannel(m.openChannelID)
	if !canEditChannel(c) {
		return nil, false
	}
	label := m.channelLabel(c)
	return []switcherCommand{
		{
			name: "Rename " + label,
			desc: "change the channel's display name and URL",
			run:  runEditChannel(c.Id, ceDisplayName),
		},
		{
			name: "Edit purpose of " + label,
			desc: "the one-line description shown when browsing channels",
			run:  runEditChannel(c.Id, cePurpose),
		},
		{
			name: "Edit header of " + label,
			desc: "the text shown next to the channel name",
			run:  runEditChannel(c.Id, ceHeader),
		},
	}, true
}

// runEditChannel returns a runner that raises the edit modal for the channel
// that was open when the palette was raised, focused on the given row.
func runEditChannel(channelID string, row int) func(*Model, string) tea.Cmd {
	return func(m *Model, _ string) tea.Cmd {
		return m.openEditChannel(channelID, row)
	}
}

// openEditChannel inflates the form from the channel's current values.
func (m *Model) openEditChannel(channelID string, row int) tea.Cmd {
	c := m.findChannel(channelID)
	if !canEditChannel(c) {
		m.status = "can't edit this channel"
		return nil
	}
	st := &channelEditState{
		channelID: c.Id,
		label:     m.channelLabel(c),
		row:       row,
	}
	st.before = [ceRowCount]string{
		ceDisplayName: c.DisplayName,
		ceURL:         c.Name,
		cePurpose:     c.Purpose,
		ceHeader:      c.Header,
	}

	newInput := func(value, placeholder string, limit int) textinput.Model {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		ti.CharLimit = limit
		ti.SetWidth(ccValueWidth(m.width))
		ti.SetValue(value)
		ti.CursorEnd()
		return ti
	}
	// Like the header below, the display name may carry a text-effects payload;
	// the form shows the markup that produced it.
	st.inputs[ceDisplayName] = newInput(decompileEffects(c.DisplayName), "Marketing", model.ChannelDisplayNameMaxRunes)
	st.inputs[ceURL] = newInput(c.Name, "marketing", model.ChannelNameMaxLength)
	st.inputs[cePurpose] = newInput(c.Purpose, "What is this channel for?", model.ChannelPurposeMaxRunes)
	// The header may carry a text-effects payload; show the markup that produced
	// it (\rainbow{…}), the same way editing a post does (see decompileEffects).
	st.inputs[ceHeader] = newInput(decompileEffects(c.Header), "Shown next to the channel name", model.ChannelHeaderMaxRunes)

	m.chanEdit = st
	return st.inputs[st.row].Focus()
}

// closeEditChannel tears the modal down. Safe to call when it isn't open.
func (m *Model) closeEditChannel() {
	m.chanEdit = nil
}

// ---- key handling --------------------------------------------------------

// handleEditChannelKey owns every keystroke while the modal is open.
func (m Model) handleEditChannelKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	st := m.chanEdit
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closeEditChannel()
		return m, nil
	case "enter":
		if st.submitting {
			return m, nil
		}
		return m.submitEditChannel()
	case "tab", "down":
		return m, m.moveEditChannelRow(1)
	case "shift+tab", "up":
		return m, m.moveEditChannelRow(-1)
	}

	before := st.inputs[st.row].Value()
	var cmd tea.Cmd
	st.inputs[st.row], cmd = st.inputs[st.row].Update(msg)
	if st.inputs[st.row].Value() != before {
		st.errMsg = ""
	}
	return m, cmd
}

// moveEditChannelRow steps the focus by delta, wrapping, keeping exactly one
// textinput focused so only one cursor shows.
func (m *Model) moveEditChannelRow(delta int) tea.Cmd {
	st := m.chanEdit
	st.inputs[st.row].Blur()
	st.row = (st.row + delta + ceRowCount) % ceRowCount
	return st.inputs[st.row].Focus()
}

// ---- submit --------------------------------------------------------------

// channelPatchedMsg carries the outcome of an edit. ch is nil when err is set.
type channelPatchedMsg struct {
	channelID string
	ch        *model.Channel
	err       error
}

// patch turns the filled-in form into the record to PATCH, carrying only the
// fields the user actually changed — a purpose edit shouldn't send a rename the
// user may not be allowed to make. A nil patch with an empty errMsg means
// nothing changed. Validation mirrors the client-checkable half of
// model.Channel.IsValid, as the create form does; permissions and duplicate
// slugs are the server's call and come back through the error row.
func (st *channelEditState) patch() (patch *model.ChannelPatch, row int, errMsg string) {
	// Effect markup in the display name and header compiles to the wire form
	// (visible text + invisible payload), so other clients see the clean text —
	// the same treatment a message gets on send. The length checks run on the
	// compiled values: those are the strings the server counts.
	display := compileEffects(strings.TrimSpace(st.inputs[ceDisplayName].Value()))
	name := strings.TrimSpace(st.inputs[ceURL].Value())
	purpose := strings.TrimSpace(st.inputs[cePurpose].Value())
	header := compileEffects(strings.TrimSpace(st.inputs[ceHeader].Value()))

	switch {
	case display == "":
		return nil, ceDisplayName, "display name is required"
	case utf8.RuneCountInString(display) > model.ChannelDisplayNameMaxRunes:
		return nil, ceDisplayName, "display name is too long"
	case !model.IsValidChannelIdentifier(name):
		return nil, ceURL, "URL must be lowercase letters, numbers, - or _"
	case utf8.RuneCountInString(purpose) > model.ChannelPurposeMaxRunes:
		return nil, cePurpose, "purpose is too long"
	case utf8.RuneCountInString(header) > model.ChannelHeaderMaxRunes:
		return nil, ceHeader, "header is too long"
	}

	patch = &model.ChannelPatch{}
	changed := false
	for i, val := range [ceRowCount]string{
		ceDisplayName: display,
		ceURL:         name,
		cePurpose:     purpose,
		ceHeader:      header,
	} {
		if val == strings.TrimSpace(st.before[i]) {
			continue
		}
		changed = true
		switch i {
		case ceDisplayName:
			patch.DisplayName = model.NewPointer(val)
		case ceURL:
			patch.Name = model.NewPointer(val)
		case cePurpose:
			patch.Purpose = model.NewPointer(val)
		case ceHeader:
			patch.Header = model.NewPointer(val)
		}
	}
	if !changed {
		return nil, 0, ""
	}
	return patch, 0, ""
}

// submitEditChannel validates the form and fires the patch, parks the cursor on
// the offending row, or closes on a no-op edit.
func (m Model) submitEditChannel() (tea.Model, tea.Cmd) {
	st := m.chanEdit
	patch, row, errMsg := st.patch()
	if errMsg != "" {
		st.errMsg, st.row = errMsg, row
		return m, m.focusEditChannelRow()
	}
	if patch == nil {
		m.closeEditChannel()
		m.status = "no changes"
		return m, nil
	}
	st.errMsg = ""
	st.submitting = true
	channelID := st.channelID
	client, ctx := m.client, m.ctx
	return m, func() tea.Msg {
		ch, err := client.PatchChannel(ctx, channelID, patch)
		return channelPatchedMsg{channelID: channelID, ch: ch, err: err}
	}
}

// focusEditChannelRow points the textinput focus at the current row, used after
// validation jumps the cursor to the offending field.
func (m *Model) focusEditChannelRow() tea.Cmd {
	st := m.chanEdit
	for i := range st.inputs {
		if i != st.row {
			st.inputs[i].Blur()
		}
	}
	return st.inputs[st.row].Focus()
}

// applyChannelPatched closes the modal and folds the server's record into the
// sidebar's channel (the same pointer the panes render from, so an in-place
// update is all the info panel and title need). A rename re-sorts the bucket,
// which moves the row — so the cursor is re-pointed at it afterwards. A failure
// keeps the modal open with the server's message so the field can be fixed.
func (m Model) applyChannelPatched(msg channelPatchedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if m.chanEdit == nil {
			m.status = "edit channel: " + oneLine(msg.err.Error())
			return m, nil
		}
		m.chanEdit.submitting = false
		m.chanEdit.errMsg = oneLine(msg.err.Error())
		return m, nil
	}
	m.closeEditChannel()

	c := m.findChannel(msg.channelID)
	if c == nil { // archived or left while the patch was in flight
		return m, nil
	}
	renamed := c.DisplayName != msg.ch.DisplayName
	c.DisplayName = msg.ch.DisplayName
	c.Name = msg.ch.Name
	c.Purpose = msg.ch.Purpose
	c.Header = msg.ch.Header
	if renamed {
		m.sortTeamBucket(c.TeamId)
		m.switchToChannelHomeTeam(c) // the row moved; follow it
	}
	m.status = "updated " + m.channelLabel(c)
	return m, nil
}

// ---- render --------------------------------------------------------------

// renderEditChannel draws the form, reusing the create-channel modal's geometry
// so the two dialogs line up.
func (m *Model) renderEditChannel() string {
	st := m.chanEdit
	if st == nil {
		return ""
	}
	_, inner, valueW := ccDims(m.width)
	// The modal can outlive a terminal resize, and this is the only place the
	// value column is recomputed — re-size the inputs to match before drawing.
	for i := range st.inputs {
		if st.inputs[i].Width() != valueW {
			st.inputs[i].SetWidth(valueW)
		}
	}

	hint := lipgloss.NewStyle().Foreground(dimColor).Italic(true)
	label := func(text string, row int) string {
		s := lipgloss.NewStyle().Width(ccLabelWidth)
		if row == st.row {
			s = s.Foreground(focusedColor).Bold(true)
		}
		return s.Render(text)
	}
	row := func(name string, idx int) string {
		return lipgloss.NewStyle().MaxWidth(inner).Render(
			lipgloss.JoinHorizontal(lipgloss.Top, label(name, idx), " ", st.inputs[idx].View()))
	}

	rows := []string{
		row("Display name", ceDisplayName),
		row("URL", ceURL),
		"",
		row("Purpose", cePurpose),
		row("Header", ceHeader),
	}

	footer := "tab/↑↓ field · ↵ save · esc cancel"
	if st.submitting {
		footer = "saving…"
	}
	body := []string{
		lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Bold(true).
			Render(truncate("Edit "+st.label, inner)),
		"",
		strings.Join(rows, "\n"),
		"",
	}
	if st.errMsg != "" {
		body = append(body, lipgloss.NewStyle().Foreground(lipgloss.Color("9")).
			Width(inner).Render(truncate(st.errMsg, inner)))
	}
	body = append(body, hint.Width(inner).Align(lipgloss.Center).Render(footer))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(focusedColor).
		Padding(1, 3).
		Render(lipgloss.JoinVertical(lipgloss.Left, body...))
}
