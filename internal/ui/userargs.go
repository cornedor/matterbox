package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Username completion for palette commands that take a "@a, @b" list ("Start
// group DM", "Add members"). The token after the last comma is matched against
// the local username cache instantly and the server directory after the usual
// switcher debounce; enter or tab completes the selected match, and enter on
// an empty token runs the command.

// userArgToken returns the partial username being typed — the text after the
// last comma, trimmed and without its "@" — and the value before it.
func userArgToken(v string) (head, token string) {
	i := strings.LastIndex(v, ",")
	head, token = v[:i+1], v[i+1:]
	return head, strings.TrimPrefix(strings.TrimSpace(token), "@")
}

// userArgNames returns the usernames already completed in v, lower-cased.
func userArgNames(v string) map[string]bool {
	head, _ := userArgToken(v)
	out := map[string]bool{}
	for _, p := range strings.Split(head, ",") {
		if n := strings.TrimPrefix(strings.TrimSpace(p), "@"); n != "" {
			out[strings.ToLower(n)] = true
		}
	}
	return out
}

// inUserArgMode reports whether the captive arg prompt takes usernames.
func (m Model) inUserArgMode() bool {
	return m.switcherCmdPending != nil && m.switcherCmdPending.userArgs
}

// userArgMatches merges directory hits (they carry full names) ahead of local
// cache hits, dropping yourself and anyone already listed.
func (m *Model) userArgMatches() []*model.User {
	v := m.switcher.Value()
	_, token := userArgToken(v)
	if token == "" {
		return nil
	}
	skip := userArgNames(v)
	if m.me != nil {
		skip[strings.ToLower(m.me.Username)] = true
	}
	out := make([]*model.User, 0, mentionLimit)
	add := func(u *model.User) {
		if u == nil || u.Username == "" || skip[strings.ToLower(u.Username)] || len(out) == mentionLimit {
			return
		}
		skip[strings.ToLower(u.Username)] = true
		out = append(out, u)
	}
	for _, u := range m.switcherUsers {
		add(u)
	}
	for _, u := range m.localMentionMatches(strings.ToLower(token)) {
		add(u)
	}
	return out
}

// acceptUserArg replaces the partial token with the selected username and a
// trailing separator, ready for the next name. Reports false when there's
// nothing to complete.
func (m *Model) acceptUserArg() bool {
	matches := m.userArgMatches()
	if len(matches) == 0 || m.switcherIdx >= len(matches) {
		return false
	}
	u := matches[m.switcherIdx]
	head, _ := userArgToken(m.switcher.Value())
	if head != "" {
		head += " "
	}
	m.switcher.SetValue(head + "@" + u.Username + ", ")
	m.switcher.CursorEnd()
	if m.userNames != nil && u.Id != "" {
		m.userNames[u.Id] = u.Username
	}
	m.switcherIdx = 0
	m.switcherUsers = nil
	m.switcherUserSeq++
	return true
}

// userArgLabel renders "@username  Full Name (nick)" for a match row.
func userArgLabel(u *model.User) string {
	label := "@" + u.Username
	extra := strings.TrimSpace(u.GetFullName())
	if u.Nickname != "" {
		if extra != "" {
			extra += " (" + u.Nickname + ")"
		} else {
			extra = u.Nickname
		}
	}
	if extra != "" {
		label += "  " + extra
	}
	return label
}

// renderUserArgRows renders the match list under the arg prompt.
func (m *Model) renderUserArgRows(inner int) []string {
	dim := lipgloss.NewStyle().Foreground(dimColor)
	matches := m.userArgMatches()
	rows := make([]string, 0, len(matches))
	for i, u := range matches {
		line := "  " + truncate(userArgLabel(u), inner-2)
		if i == m.switcherIdx {
			line = selectedRow.Width(inner).Render(line)
		}
		rows = append(rows, line)
	}
	if len(rows) == 0 {
		if _, token := userArgToken(m.switcher.Value()); token != "" {
			rows = append(rows, dim.Render("  no matching users"))
		}
	}
	return rows
}
