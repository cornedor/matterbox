package ui

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Creating a channel. The "Create channel" entry in the > palette (F1) opens
// this modal, which exposes every field POST /api/v4/channels honours from the
// client: team, type, display name, URL slug, purpose, header, group-sync,
// auto-translation and the channel banner. The server owns the rest of
// model.Channel (id, creator, timestamps, counters), and Props is left out on
// purpose — it's free-form plugin storage, not a channel setting.
//
// Group-sync and the banner only take effect on servers with the matching
// licence. They're shown regardless: the server is the authority, and its
// rejection lands in the modal's error row rather than being second-guessed
// here.

// Form rows, in display order. Rows are either text inputs, left/right
// selectors (team, type) or booleans toggled with space.
const (
	ccTeam = iota
	ccType
	ccDisplayName
	ccURL
	ccPurpose
	ccHeader
	ccGroupConstrained
	ccAutoTranslation
	ccBannerEnabled
	ccBannerText
	ccBannerColor
	ccRowCount
)

// createChannelDialogWidth is the modal's preferred outer width; it shrinks to
// fit narrow terminals, down to ccMinDialogWidth.
const createChannelDialogWidth = 76
const ccMinDialogWidth = 44

// ccLabelWidth is the width of the left-hand label column.
const ccLabelWidth = 18

// ccMinNoteWidth is the narrowest a checkbox's explanatory note may be before
// it's dropped entirely — below this it truncates to noise.
const ccMinNoteWidth = 12

// ccHexColor mirrors the server's channelHexColorRegex (#RGB or #RRGGBB), so a
// bad banner colour is caught before the round-trip.
var ccHexColor = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// createChannelState owns the modal. It's held behind a pointer on Model (nil
// when closed): the textinputs are fat, and this is cold state that no
// unconditional layout/render pass touches.
type createChannelState struct {
	teams   []*model.Team // snapshot at open, so the selector order is stable
	teamIdx int
	typ     model.ChannelType

	inputs [ccRowCount]textinput.Model // only the text rows are initialised
	row    int

	// urlEdited records that the user typed into the URL row, which stops the
	// slug tracking the display name (matching the web app).
	urlEdited bool

	groupConstrained bool
	autoTranslation  bool
	bannerEnabled    bool

	submitting bool
	errMsg     string
}

// ccTextRow reports whether the row holds a textinput.
func ccTextRow(row int) bool {
	switch row {
	case ccDisplayName, ccURL, ccPurpose, ccHeader, ccBannerText, ccBannerColor:
		return true
	}
	return false
}

// ccBoolRow reports whether the row is a checkbox.
func ccBoolRow(row int) bool {
	switch row {
	case ccGroupConstrained, ccAutoTranslation, ccBannerEnabled:
		return true
	}
	return false
}

// runCreateChannel is the > command entry point. The switcher has already
// closed itself, so this just raises the modal.
func runCreateChannel(m *Model, _ string) tea.Cmd {
	return m.openCreateChannel()
}

// openCreateChannel inflates the form, defaulting the team to the one whose tab
// is focused (falling back to any real team, since the DMs/Feed/Search tabs
// have no team of their own).
func (m *Model) openCreateChannel() tea.Cmd {
	if len(m.teams) == 0 {
		m.status = "create channel: you don't belong to any team"
		return nil
	}
	st := &createChannelState{
		teams: append([]*model.Team(nil), m.teams...),
		typ:   model.ChannelTypeOpen,
		row:   ccDisplayName, // the first field worth typing into
	}
	target := m.fallbackTeamID()
	for i, t := range st.teams {
		if t.Id == target {
			st.teamIdx = i
			break
		}
	}

	newInput := func(placeholder string, limit int) textinput.Model {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		ti.CharLimit = limit
		ti.SetWidth(ccValueWidth(m.width))
		return ti
	}
	st.inputs[ccDisplayName] = newInput("Marketing", model.ChannelDisplayNameMaxRunes)
	st.inputs[ccURL] = newInput("marketing", model.ChannelNameMaxLength)
	st.inputs[ccPurpose] = newInput("What is this channel for?", model.ChannelPurposeMaxRunes)
	st.inputs[ccHeader] = newInput("Shown next to the channel name", model.ChannelHeaderMaxRunes)
	st.inputs[ccBannerText] = newInput("Announcement shown atop the channel", model.ChannelBannerInfoMaxLength)
	st.inputs[ccBannerColor] = newInput("#4578ff", 7)

	m.createChan = st
	return st.inputs[st.row].Focus()
}

// closeCreateChannel tears the modal down. Safe to call when it isn't open.
func (m *Model) closeCreateChannel() {
	m.createChan = nil
}

// ccDims returns the modal's outer width, its inner content width (inside the
// border and padding), and the width left over for a row's value column. It's
// the single source of the layout arithmetic: the textinputs are sized from it
// at open, and the renderer clamps every row to it.
func ccDims(termWidth int) (outer, inner, value int) {
	outer = createChannelDialogWidth
	if cap := termWidth - 4; cap > 0 && outer > cap {
		outer = cap
	}
	if outer < ccMinDialogWidth {
		outer = ccMinDialogWidth
	}
	inner = outer - 8                // border (2) + padding (6)
	value = inner - ccLabelWidth - 1 // label column + its trailing space
	return outer, inner, value
}

// ccValueWidth is the width available to a row's value column.
func ccValueWidth(termWidth int) int {
	_, _, v := ccDims(termWidth)
	return v
}

// slugifyChannelName derives a URL slug from a display name: lowercase, with
// runs of separators collapsed to a single dash and anything else dropped. The
// result satisfies model.IsValidChannelIdentifier for any input that leaves at
// least one alphanumeric behind (an all-emoji name slugs to "", which the
// submit-time validation reports).
func slugifyChannelName(s string) string {
	var b strings.Builder
	dashPending := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if dashPending {
				b.WriteByte('-')
				dashPending = false
			}
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			// Only emit the dash once we know a character follows it, so the
			// slug can't start or end with one.
			dashPending = b.Len() > 0
		}
	}
	// Only [a-z0-9-] was written, so bytes and runes coincide here.
	out := b.String()
	if len(out) > model.ChannelNameMaxLength {
		out = strings.TrimRight(out[:model.ChannelNameMaxLength], "-")
	}
	return out
}

// ---- key handling --------------------------------------------------------

// handleCreateChannelKey owns every keystroke while the modal is open.
func (m Model) handleCreateChannelKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	st := m.createChan
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closeCreateChannel()
		return m, nil
	case "enter":
		if st.submitting {
			return m, nil
		}
		return m.submitCreateChannel()
	case "tab", "down":
		return m, m.moveCreateChannelRow(1)
	case "shift+tab", "up":
		return m, m.moveCreateChannelRow(-1)
	}

	// Selector and checkbox rows claim the keys a textinput would otherwise
	// swallow. Text rows fall through so ←/→ still move the cursor and space
	// types a space.
	switch {
	case st.row == ccTeam:
		switch msg.String() {
		case "left", "right":
			delta := 1
			if msg.String() == "left" {
				delta = -1
			}
			st.teamIdx = (st.teamIdx + delta + len(st.teams)) % len(st.teams)
			return m, nil
		}
	case st.row == ccType:
		switch msg.String() {
		case "left", "right", "space":
			if st.typ == model.ChannelTypeOpen {
				st.typ = model.ChannelTypePrivate
			} else {
				st.typ = model.ChannelTypeOpen
			}
			return m, nil
		}
	case ccBoolRow(st.row):
		switch msg.String() {
		case "space", "left", "right":
			m.toggleCreateChannelBool(st.row)
			return m, nil
		}
	}

	if ccTextRow(st.row) {
		before := st.inputs[st.row].Value()
		var cmd tea.Cmd
		st.inputs[st.row], cmd = st.inputs[st.row].Update(msg)
		after := st.inputs[st.row].Value()
		if after != before {
			st.errMsg = ""
			switch st.row {
			case ccURL:
				// Any edit here detaches the slug from the display name.
				st.urlEdited = true
			case ccDisplayName:
				if !st.urlEdited {
					st.inputs[ccURL].SetValue(slugifyChannelName(after))
					// SetValue leaves the cursor where it was, which would
					// drop the user's first keystroke mid-slug when they tab
					// over to edit it.
					st.inputs[ccURL].CursorEnd()
				}
			}
		}
		return m, cmd
	}
	return m, nil
}

// toggleCreateChannelBool flips a checkbox row.
func (m *Model) toggleCreateChannelBool(row int) {
	st := m.createChan
	st.errMsg = ""
	switch row {
	case ccGroupConstrained:
		st.groupConstrained = !st.groupConstrained
	case ccAutoTranslation:
		st.autoTranslation = !st.autoTranslation
	case ccBannerEnabled:
		st.bannerEnabled = !st.bannerEnabled
	}
}

// moveCreateChannelRow steps the focus by delta, wrapping, and keeps textinput
// focus in step with the focused row so exactly one cursor is visible.
func (m *Model) moveCreateChannelRow(delta int) tea.Cmd {
	st := m.createChan
	if ccTextRow(st.row) {
		st.inputs[st.row].Blur()
	}
	st.row = (st.row + delta + ccRowCount) % ccRowCount
	if ccTextRow(st.row) {
		return st.inputs[st.row].Focus()
	}
	return nil
}

// ---- submit --------------------------------------------------------------

// channelCreatedMsg carries the outcome of a create. ch is nil when err is set.
type channelCreatedMsg struct {
	ch  *model.Channel
	err error
}

// channel turns the filled-in form into the record to POST, or reports the row
// to jump to and why. Validation mirrors model.Channel.IsValid's
// client-checkable rules so the common mistakes (no name, bad slug, banner
// without text) don't cost a round-trip; everything else — permissions,
// licence, duplicate slug — is the server's call and comes back through the
// modal's error row.
func (st *createChannelState) channel() (ch *model.Channel, row int, errMsg string) {
	display := strings.TrimSpace(st.inputs[ccDisplayName].Value())
	// An untouched URL row still slugs the display name: the field shows the
	// derived value, but a user who cleared it shouldn't hit "URL required".
	name := strings.TrimSpace(st.inputs[ccURL].Value())
	if name == "" {
		name = slugifyChannelName(display)
	}
	bannerText := strings.TrimSpace(st.inputs[ccBannerText].Value())
	bannerColor := strings.TrimSpace(st.inputs[ccBannerColor].Value())

	switch {
	case display == "":
		return nil, ccDisplayName, "display name is required"
	case utf8.RuneCountInString(display) > model.ChannelDisplayNameMaxRunes:
		return nil, ccDisplayName, "display name is too long"
	case !model.IsValidChannelIdentifier(name):
		return nil, ccURL, "URL must be lowercase letters, numbers, - or _"
	case st.bannerEnabled && bannerText == "":
		return nil, ccBannerText, "banner text is required when the banner is on"
	case st.bannerEnabled && !ccHexColor.MatchString(bannerColor):
		return nil, ccBannerColor, "banner colour must be a hex value like #4578ff"
	}

	ch = &model.Channel{
		TeamId:          st.teams[st.teamIdx].Id,
		Type:            st.typ,
		DisplayName:     display,
		Name:            name,
		Purpose:         strings.TrimSpace(st.inputs[ccPurpose].Value()),
		Header:          strings.TrimSpace(st.inputs[ccHeader].Value()),
		AutoTranslation: st.autoTranslation,
	}
	// Both are omitted unless set: a nil GroupConstrained means "no group sync"
	// and a nil BannerInfo means "no banner", which is what servers that don't
	// know these fields expect to see.
	if st.groupConstrained {
		ch.GroupConstrained = model.NewPointer(true)
	}
	if st.bannerEnabled {
		ch.BannerInfo = &model.ChannelBannerInfo{
			Enabled:         model.NewPointer(true),
			Text:            model.NewPointer(bannerText),
			BackgroundColor: model.NewPointer(bannerColor),
		}
	}
	return ch, 0, ""
}

// submitCreateChannel validates the form and fires the create, or parks the
// cursor on the offending row.
func (m Model) submitCreateChannel() (tea.Model, tea.Cmd) {
	st := m.createChan
	ch, row, errMsg := st.channel()
	if errMsg != "" {
		st.errMsg, st.row = errMsg, row
		return m, m.focusCreateChannelRow()
	}
	st.errMsg = ""
	st.submitting = true
	client, ctx := m.client, m.ctx
	return m, func() tea.Msg {
		created, err := client.CreateChannel(ctx, ch)
		return channelCreatedMsg{ch: created, err: err}
	}
}

// focusCreateChannelRow points the textinput focus at the current row, used
// after validation jumps the cursor to the offending field.
func (m *Model) focusCreateChannelRow() tea.Cmd {
	st := m.createChan
	for i := range st.inputs {
		if ccTextRow(i) && i != st.row {
			st.inputs[i].Blur()
		}
	}
	if ccTextRow(st.row) {
		return st.inputs[st.row].Focus()
	}
	return nil
}

// applyChannelCreated closes the modal and jumps to the new channel (see
// adoptChannel, which also splices it into the sidebar — it isn't there yet).
// A failure keeps the modal open with the server's message so the user can fix
// the field and retry.
func (m Model) applyChannelCreated(msg channelCreatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if m.createChan == nil {
			m.status = "create channel: " + oneLine(msg.err.Error())
			return m, nil
		}
		m.createChan.submitting = false
		m.createChan.errMsg = oneLine(msg.err.Error())
		return m, nil
	}
	m.closeCreateChannel()

	ch := msg.ch
	// adoptChannel sets its own "loading messages…" status as a side effect, so
	// claim the status bar after it, not before.
	cmds := m.adoptChannel(ch)
	m.status = "created " + m.channelLabel(ch)
	return m, cmds
}

// ---- render --------------------------------------------------------------

// renderCreateChannel draws the form. Layout follows the other modals: rounded
// border, centred title, then one row per field with the focused label lit.
func (m *Model) renderCreateChannel() string {
	st := m.createChan
	if st == nil {
		return ""
	}
	_, inner, valueW := ccDims(m.width)
	// The modal can outlive a terminal resize, and this is the only place the
	// value column is recomputed — re-size the inputs to match before drawing.
	for i := range st.inputs {
		if ccTextRow(i) && st.inputs[i].Width() != valueW {
			st.inputs[i].SetWidth(valueW)
		}
	}

	dim := lipgloss.NewStyle().Foreground(dimColor)
	hint := lipgloss.NewStyle().Foreground(dimColor).Italic(true)

	label := func(text string, row int) string {
		st2 := lipgloss.NewStyle().Width(ccLabelWidth)
		switch {
		case row == st.row:
			st2 = st2.Foreground(focusedColor).Bold(true)
		case !m.ccRowEnabled(row):
			st2 = st2.Foreground(dimColor)
		}
		return st2.Render(text)
	}
	// checkbox renders a bool row. The note explains what the flag does; it's
	// truncated to the value column and dropped outright once that column is
	// too narrow to say anything useful.
	checkbox := func(on bool, row int, note string) string {
		box := "[ ]"
		if on {
			box = "[✓]"
		}
		style := lipgloss.NewStyle()
		if !m.ccRowEnabled(row) {
			style = style.Foreground(dimColor)
		} else if on {
			style = style.Foreground(focusedColor)
		}
		out := style.Render(box)
		if noteW := valueW - 4; noteW >= ccMinNoteWidth {
			out += " " + hint.Render(truncate(note, noteW))
		}
		return out
	}
	selector := func(text string) string {
		return dim.Render("‹ ") + lipgloss.NewStyle().Bold(true).Render(truncate(text, valueW-4)) + dim.Render(" ›")
	}
	// Every row is clamped to the content width so a long note or team name
	// can't push the border out past the terminal.
	row := func(name string, idx int, value string) string {
		return lipgloss.NewStyle().MaxWidth(inner).Render(
			lipgloss.JoinHorizontal(lipgloss.Top, label(name, idx), " ", value))
	}

	typeName := "Public"
	if st.typ == model.ChannelTypePrivate {
		typeName = "Private"
	}

	rows := []string{
		row("Team", ccTeam, selector(displayTeam(st.teams[st.teamIdx]))),
		row("Type", ccType, selector(typeName)),
		"",
		row("Display name", ccDisplayName, st.inputs[ccDisplayName].View()),
		row("URL", ccURL, st.inputs[ccURL].View()),
		row("Purpose", ccPurpose, st.inputs[ccPurpose].View()),
		row("Header", ccHeader, st.inputs[ccHeader].View()),
		"",
		row("Group sync", ccGroupConstrained, checkbox(st.groupConstrained, ccGroupConstrained, "membership managed by groups")),
		row("Auto-translate", ccAutoTranslation, checkbox(st.autoTranslation, ccAutoTranslation, "translate messages for members")),
		"",
		row("Banner", ccBannerEnabled, checkbox(st.bannerEnabled, ccBannerEnabled, "show a coloured banner atop the channel")),
		row("Banner text", ccBannerText, st.inputs[ccBannerText].View()),
		row("Banner colour", ccBannerColor, st.inputs[ccBannerColor].View()),
	}

	footer := "tab/↑↓ field · ←/→ change · space toggle · ↵ create · esc cancel"
	if st.submitting {
		footer = "creating…"
	}
	body := []string{
		lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Bold(true).Render("Create a channel"),
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

// ccRowEnabled reports whether the row's value currently has any effect.
// Disabled rows are still drawn — the option exists, it just doesn't apply to
// the channel as configured.
func (m *Model) ccRowEnabled(row int) bool {
	switch row {
	case ccBannerText, ccBannerColor:
		return m.createChan.bannerEnabled
	}
	return true
}
