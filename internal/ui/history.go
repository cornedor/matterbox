package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// historyPopupMaxWidth caps the popup's outer width. Wider terminals
// don't get arbitrarily wide popups — readability beats fill.
const historyPopupMaxWidth = 96

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

// historyDims returns the popup's outer width and content (inner)
// height. Width is min(80% of terminal, historyPopupMaxWidth) and
// clamped to a sensible floor. Height leaves a few rows of margin
// around the popup.
func (m *Model) historyDims() (outerW, innerH int) {
	outerW = m.width * 4 / 5
	if outerW > historyPopupMaxWidth {
		outerW = historyPopupMaxWidth
	}
	if outerW < 30 {
		outerW = 30
	}
	if outerW > m.width-2 {
		outerW = m.width - 2
	}
	if outerW < 1 {
		outerW = 1
	}

	// Outer border + padding eat 4 vertical rows (top/bottom border +
	// top/bottom padding=0 in our style → really just 2). Add 2 for
	// title + separator rows below.
	bodyH := m.height - 4
	if bodyH < 6 {
		bodyH = 6
	}
	innerH = bodyH - 4 // border (2) + title (1) + separator (1) = 4
	if innerH < 3 {
		innerH = 3
	}
	return outerW, innerH
}

// sizeHistoryView keeps the popup's viewport in sync with the current
// terminal size. Call before rendering and on every relevant resize.
func (m *Model) sizeHistoryView() {
	w, h := m.historyDims()
	inner := w - 4 // border (2) + padding (1) left/right
	if inner < 1 {
		inner = 1
	}
	m.historyView.SetWidth(inner)
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
	m.historyView, cmd = m.historyView.Update(msg)
	return m, cmd
}

// renderHistoryPopup composes the bordered popup. The viewport's
// SoftWrap=true means long lines re-wrap to the popup width as the
// user scrolls.
func (m *Model) renderHistoryPopup() string {
	if !m.historyMode || m.historyPost == nil {
		return ""
	}
	outerW, _ := m.historyDims()
	inner := outerW - 4
	if inner < 1 {
		inner = 1
	}

	name := m.userNames[m.historyPost.UserId]
	if name == "" {
		name = m.historyPost.UserId
	}
	title := titleStyle.Render("Edit history") + "  " +
		lipgloss.NewStyle().Foreground(dimColor).Render(
			fmt.Sprintf("%s · %d revision(s)", name, len(m.historyRevisions)),
		)
	dim := lipgloss.NewStyle().Foreground(dimColor)
	rule := dim.Render(strings.Repeat("─", inner))

	body := m.historyView.View()
	rows := []string{title, rule, body}

	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(focusedColor).
		Padding(0, 1).
		Width(outerW).
		Render(strings.Join(rows, "\n"))
}
