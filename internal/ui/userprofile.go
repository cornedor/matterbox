package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// "View profile" from the > palette: who wrote the selected message. The model
// only caches usernames (m.userNames), so the full profile is fetched fresh,
// together with live presence, and shown in the shared text-popup sheet.

// userProfileMsg carries the fetched profile back to Update. status may be
// empty when the presence call failed — the popup then falls back to the
// cached presence rather than dropping the whole profile.
type userProfileMsg struct {
	user   *model.User
	status string
	err    error
}

// viewProfileCommand returns the palette entry, and whether it applies — it
// does while a conversation is open, the same rule as the pin and mark-unread
// entries. It stays listed regardless of focus and reports "no message
// selected" when run without one, for the same reason those do.
func (m *Model) viewProfileCommand() (switcherCommand, bool) {
	if m.findChannel(m.openChannelID) == nil {
		return switcherCommand{}, false
	}
	return switcherCommand{
		name: "View profile",
		tid:  "view_profile",
		desc: "who wrote the selected message: name, presence, position, local time",
		run:  runViewProfile,
	}, true
}

// runViewProfile fetches the selected post's author. The author is the post's
// UserId — for a webhook post that is the bot behind it, not the
// override_username costume, and the popup says so via the bot rows.
func runViewProfile(m *Model, _ string) tea.Cmd {
	p := m.selectedPost()
	if p == nil || p.UserId == "" {
		m.status = "no message selected"
		return nil
	}
	m.status = "loading profile…"
	client, ctx, id := m.client, m.ctx, p.UserId
	return func() tea.Msg {
		us, err := client.UsersByIDs(ctx, []string{id})
		if err != nil {
			return userProfileMsg{err: err}
		}
		if len(us) == 0 {
			return userProfileMsg{err: fmt.Errorf("user %s not found", id)}
		}
		status := ""
		if ss, err := client.UsersStatuses(ctx, []string{id}); err == nil {
			status = ss[id]
		}
		return userProfileMsg{user: us[0], status: status}
	}
}

// applyUserProfile opens the popup (or reports the failure on the status
// line). The fetch is also a fresher answer than our caches, so the name and
// presence maps are updated on the way — the sidebar dot and any rendered
// @mention benefit for free.
func (m *Model) applyUserProfile(msg userProfileMsg) {
	if msg.err != nil {
		m.status = "profile: " + oneLine(msg.err.Error())
		return
	}
	u := msg.user
	m.userNames[u.Id] = u.Username
	if msg.status != "" {
		m.statuses[u.Id] = msg.status
	}
	if cs := u.GetCustomStatus(); cs != nil {
		m.customStatuses[u.Id] = *cs
	}
	m.status = ""
	m.openTextPopup("User profile", m.renderUserProfile(u, m.statuses[u.Id]))
}

// renderUserProfile lays out the popup body: a name headline, the presence +
// custom-status line, then aligned label rows. Rows with nothing to say are
// dropped — most servers hide the email, and few people set a timezone.
func (m *Model) renderUserProfile(u *model.User, status string) string {
	nameStyle := lipgloss.NewStyle().Bold(true)
	dim := lipgloss.NewStyle().Foreground(dimColor)

	var b strings.Builder
	head := "@" + u.Username
	if full := u.GetFullName(); full != "" {
		head = full + " " + dim.Render("· @"+u.Username)
	}
	b.WriteString(nameStyle.Render(head) + "\n")

	glyph, st := statusGlyph(status, statusDot, statusHollowDot)
	presence := status
	if presence == "" {
		presence = "offline"
	}
	line := st.Render(glyph) + " " + presence
	if cs, ok := m.profileCustomStatus(u); ok {
		line += dim.Render(" — ") + strings.TrimSpace(m.renderEmojiGlyph(cs.Emoji)+" "+cs.Text)
	}
	b.WriteString(line + "\n")

	type row struct{ label, value string }
	var rows []row
	add := func(label, value string) {
		if value != "" {
			rows = append(rows, row{label, value})
		}
	}
	add("Nickname", u.Nickname)
	add("Position", u.Position)
	add("Email", u.Email)
	if tz := u.GetPreferredTimezone(); tz != "" {
		add("Local time", time.Now().In(u.GetTimezoneLocation()).Format("15:04")+" ("+tz+")")
	}
	if u.CreateAt > 0 {
		add("Member since", time.UnixMilli(u.CreateAt).Format("2 Jan 2006"))
	}
	if u.IsBot {
		add("Bot", "yes")
		add("Bot purpose", u.BotDescription)
	}
	if u.IsSystemAdmin() {
		add("Roles", "system admin")
	}
	if u.DeleteAt != 0 {
		add("Account", "deactivated")
	}

	labelW := 0
	for _, r := range rows {
		labelW = max(labelW, lipgloss.Width(r.label))
	}
	for _, r := range rows {
		pad := strings.Repeat(" ", labelW-lipgloss.Width(r.label))
		b.WriteString("\n" + dim.Render(r.label) + pad + "  " + r.value)
	}
	return b.String()
}

// profileCustomStatus is the fetched user's custom status, dropped once past
// its expiry — the same rule as the sidebar's dmCustomStatus, minus the config
// gate: an explicitly requested profile shows it regardless.
func (m *Model) profileCustomStatus(u *model.User) (model.CustomStatus, bool) {
	cs := u.GetCustomStatus()
	if cs == nil || (cs.Emoji == "" && cs.Text == "") {
		return model.CustomStatus{}, false
	}
	if !cs.ExpiresAt.IsZero() && !cs.ExpiresAt.After(time.Now()) {
		return model.CustomStatus{}, false
	}
	return *cs, true
}
