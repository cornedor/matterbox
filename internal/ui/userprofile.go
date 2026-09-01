package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// "View profile" from the > palette: who wrote the selected message, shown in
// the same right-side pane the channel-info key raises — profile mode is an
// infoMode sibling of the channel facts, so selection, hover, click and scroll
// all come from the panel's target machinery. The model only caches usernames
// (m.userNames), so the full profile is fetched fresh, together with live
// presence.

// userProfileMsg carries the fetched profile back to Update. status may be
// empty when the presence call failed — the panel then falls back to the
// cached presence rather than dropping the whole profile.
type userProfileMsg struct {
	userID string
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

// runViewProfile raises the profile panel for the selected post's author. The
// author is the post's UserId — for a webhook post that is the bot behind it,
// not the override_username costume, and the panel says so via the bot rows.
func runViewProfile(m *Model, _ string) tea.Cmd {
	p := m.selectedPost()
	if p == nil || p.UserId == "" {
		// Running the command again while the panel holds focus lands here
		// (the selection bar follows focus), so it reads as the close gesture.
		if m.infoOpen && m.infoMode == infoModeProfile {
			m.closeInfo()
			return nil
		}
		m.status = "no message selected"
		return nil
	}
	return m.raiseUserProfile(p.UserId)
}

// raiseUserProfile opens the info panel in profile mode for the given user and
// starts the fetch. It mirrors raiseChannelInfo: the panel shares the single
// right slot with the thread sidebar / reference panel, and running the command
// again for the same user closes it (a toggle).
func (m *Model) raiseUserProfile(userID string) tea.Cmd {
	c := m.findChannel(m.openChannelID)
	if c == nil {
		m.status = "no channel open"
		return nil
	}
	if m.infoOpen && m.infoMode == infoModeProfile && m.infoProfileUserID == userID {
		m.closeInfo()
		return nil
	}
	var threadCmd tea.Cmd
	if m.threadOpen {
		threadCmd = m.closeThread()
	}
	if m.refOpen {
		m.closeRef()
	}
	m.infoOpen = true
	m.infoChannelID = c.Id
	m.infoMode = infoModeProfile
	m.infoMainIdx = -1
	m.resetInfoProfile()
	m.infoProfileUserID = userID
	m.infoTargets = nil
	m.infoIdx = -1
	m.infoHoverIdx = -1
	m.infoScrollFree = false
	m.focus = focusInfo
	m.input.Blur()
	m.infoView.GotoTop()
	m.status = "profile · ↵ send a DM · esc closes"
	m.resizeMessagesViewport()
	m.renderMessages()
	m.renderInfo()
	return tea.Batch(threadCmd, m.fetchUserProfile(userID))
}

// resetInfoProfile clears profile mode's state; part of every panel teardown
// and re-raise.
func (m *Model) resetInfoProfile() {
	m.infoProfileUserID = ""
	m.infoProfile = nil
	m.infoProfileLoaded = false
	m.infoProfileErr = nil
}

// fetchUserProfile loads the profile + live presence. A failure is carried
// back on the message (shown in the panel) rather than the global status line,
// like the panel's other fetches.
func (m *Model) fetchUserProfile(userID string) tea.Cmd {
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		us, err := client.UsersByIDs(ctx, []string{userID})
		if err != nil {
			return userProfileMsg{userID: userID, err: err}
		}
		if len(us) == 0 {
			return userProfileMsg{userID: userID, err: fmt.Errorf("user %s not found", userID)}
		}
		status := ""
		if ss, err := client.UsersStatuses(ctx, []string{userID}); err == nil {
			status = ss[userID]
		}
		return userProfileMsg{userID: userID, user: us[0], status: status}
	}
}

// applyUserProfile lands the fetch in the panel (unless it was closed or
// re-aimed meanwhile). The fetch is also a fresher answer than our caches, so
// the name and presence maps are updated on the way — the sidebar dot and any
// rendered @mention benefit for free.
func (m *Model) applyUserProfile(msg userProfileMsg) {
	if u := msg.user; u != nil {
		m.userNames[u.Id] = u.Username
		if msg.status != "" {
			m.statuses[u.Id] = msg.status
		}
		if cs := u.GetCustomStatus(); cs != nil {
			m.customStatuses[u.Id] = *cs
		}
	}
	if !m.infoOpen || m.infoMode != infoModeProfile || msg.userID != m.infoProfileUserID {
		return // stale (closed or switched)
	}
	m.infoProfileLoaded = true
	m.infoProfileErr = msg.err
	m.infoProfile = msg.user
	m.renderInfo()
}

// infoProfileContent builds profile mode's (lines, targets): a name headline,
// the presence + custom-status line, the aligned fact rows, and a DM action
// row that reuses the member target the channel view's rows activate with.
// Rows with nothing to say are dropped — most servers hide the email, and few
// people set a timezone.
func (m *Model) infoProfileContent() ([]string, []infoTarget) {
	var lines []string
	var targets []infoTarget

	switch {
	case m.infoProfileErr != nil:
		lines = append(lines, infoLabelStyle.Render("Profile"))
		lines = append(lines, "  "+infoErrStyle.Render(m.infoProfileErr.Error()))
		return lines, targets
	case !m.infoProfileLoaded:
		lines = append(lines, infoLabelStyle.Render("Profile"))
		lines = append(lines, "  "+infoDimStyle.Render("loading…"))
		return lines, targets
	}
	u := m.infoProfile

	head := "@" + u.Username
	if full := u.GetFullName(); full != "" {
		head = full + " " + infoDimStyle.Render("· @"+u.Username)
	}
	lines = append(lines, lipgloss.NewStyle().Bold(true).Render(head))

	status := m.statuses[u.Id]
	glyph, st := statusGlyph(status, statusDot, statusHollowDot)
	if status == "" {
		status = "offline"
	}
	presence := st.Render(glyph) + " " + status
	if cs, ok := m.profileCustomStatus(u); ok {
		presence += infoDimStyle.Render(" — ") + strings.TrimSpace(m.renderEmojiGlyph(cs.Emoji)+" "+cs.Text)
	}
	lines = append(lines, presence)

	lines = append(lines, "", infoLabelStyle.Render("Profile"))
	add := func(key, value string) {
		if value != "" {
			lines = append(lines, infoMetaLine(key, value))
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

	if m.me == nil || u.Id != m.me.Id {
		lines = append(lines, "")
		start := len(lines)
		lines = append(lines, "  "+infoActionStyle.Render("✉ Send a direct message…"))
		targets = append(targets, infoTarget{kind: infoTargetMember, userID: u.Id, startRow: start, endRow: start})
	}
	return lines, targets
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
