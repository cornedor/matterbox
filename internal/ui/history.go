package ui

import (
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// historyTimeFormat is used for the dim per-revision timestamps. Local
// timezone, fits a typical row without hogging it.
const historyTimeFormat = "2006-01-02 15:04"

// openHistory opens the edit-history popup for the given post. Loads
// any archived prior versions from the store; if there are none we
// still open the popup so the user gets explicit feedback (rather than
// silently swallowing the key).
func (m *Model) openHistory(p *model.Post) {
	if p == nil || p.Id == "" {
		return
	}
	var revisions []*model.Post
	if m.store != nil {
		if r, err := m.store.Revisions(p.Id); err == nil {
			revisions = r
		}
	}
	m.historyMode = true
	m.historyPost = p
	m.historyRevisions = revisions
	m.sizeHistoryView()
	m.renderHistory()
	m.historyView.GotoTop()
}

func (m *Model) closeHistory() {
	m.historyMode = false
	m.historyPost = nil
	m.historyRevisions = nil
}

// sizeHistoryView keeps the popup's viewport in sync with the current
// terminal size. Call before rendering and on every relevant resize.
func (m *Model) sizeHistoryView() {
	_, h := m.modalDims()
	m.historyView.SetWidth(m.modalInnerWidth())
	m.historyView.SetHeight(h)
}

// renderHistory populates the popup viewport. Each archived version is
// shown with a dim "edited at <ts>" header followed by the message
// body. The current (live) version is shown last, labelled "current",
// so the chronological progression reads top-to-bottom.
func (m *Model) renderHistory() {
	if m.historyPost == nil {
		return
	}
	dim := lipgloss.NewStyle().Foreground(dimColor)
	headerStyle := lipgloss.NewStyle().Foreground(dimColor).Italic(true)

	var parts []string
	if len(m.historyRevisions) == 0 {
		parts = append(parts, dim.Render(
			"No prior versions captured. Matterbox only records edits "+
				"it observed while running — earlier edits aren't "+
				"available from the Mattermost API."))
		parts = append(parts, "")
	}
	for i, r := range m.historyRevisions {
		label := fmt.Sprintf("v%d  (was, until %s)",
			i+1,
			time.UnixMilli(latestStamp(r)).Local().Format(historyTimeFormat),
		)
		parts = append(parts, headerStyle.Render(label))
		body := r.Message
		if body == "" {
			body = dim.Render("(empty)")
		}
		parts = append(parts, body, "")
	}
	// Current version, labelled.
	curLabel := fmt.Sprintf("v%d  current", len(m.historyRevisions)+1)
	if m.historyPost.EditAt > 0 {
		curLabel += "  (edited " +
			time.UnixMilli(m.historyPost.EditAt).Local().Format(historyTimeFormat) + ")"
	}
	parts = append(parts, headerStyle.Render(curLabel))
	body := m.historyPost.Message
	if body == "" {
		body = dim.Render("(empty)")
	}
	parts = append(parts, body)

	m.historyView.SetContentLines(parts)
}

// latestStamp returns the best timestamp we have for "when did this
// version stop being current": EditAt if set (the moment a NEWER edit
// replaced this one — confusingly, mattermost stores the new EditAt on
// the new row, so for an archived row this is the EditAt that WAS
// current at archive time), falling back to UpdateAt and CreateAt.
func latestStamp(p *model.Post) int64 {
	switch {
	case p.EditAt > 0:
		return p.EditAt
	case p.UpdateAt > 0:
		return p.UpdateAt
	default:
		return p.CreateAt
	}
}

// handleHistoryKey owns every keystroke while the popup is open. Esc /
// the dedicated keybinding close it; arrows and pgup/pgdn scroll
// through long content.
func (m Model) handleHistoryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.closeHistory()
		return m, nil
	}
	// alt+e (the bound open key) toggles the popup closed too.
	if key.Matches(msg, m.keys.ShowHistory) {
		m.closeHistory()
		return m, nil
	}
	var cmd tea.Cmd
	*m.historyView, cmd = m.historyView.Update(msg)
	return m, cmd
}

// renderHistoryPopup composes the bordered popup. The viewport's
// SoftWrap=true means long lines re-wrap to the popup width as the
// user scrolls.
func (m *Model) renderHistoryPopup() string {
	if !m.historyMode || m.historyPost == nil {
		return ""
	}
	name := m.userNames[m.historyPost.UserId]
	if name == "" {
		name = m.historyPost.UserId
	}
	sub := fmt.Sprintf("%s · %d revision(s)", name, len(m.historyRevisions))
	return m.renderModal("Edit history", sub, m.historyView.View())
}
