package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// confirmDialogMaxWidth caps the dialog's outer width so it stays
// readable on very wide terminals.
const confirmDialogMaxWidth = 60

// openDeleteConfirm enters the delete-confirmation modal for the given
// post. The summary line is precomputed here so it doesn't need to walk
// posts again on every render. Empty post or empty Id (optimistic stub)
// is a no-op.
func (m *Model) openDeleteConfirm(p *model.Post) {
	if p == nil || p.Id == "" {
		return
	}
	m.deleteConfirmPostID = p.Id
	m.deleteConfirmText = postSummary(p)
}

// closeDeleteConfirm clears the modal state without performing the
// delete. Safe to call when no modal is open.
func (m *Model) closeDeleteConfirm() {
	m.deleteConfirmPostID = ""
	m.deleteConfirmText = ""
}

// postSummary returns a one-line preview of the post body, used as the
// dialog's secondary line so the user can sanity-check which message
// they're about to lose. Attachment-only posts get an explicit label so
// the dialog doesn't render a blank.
func postSummary(p *model.Post) string {
	body := strings.TrimSpace(p.Message)
	body = strings.ReplaceAll(body, "\n", " ")
	if body == "" {
		if p.Metadata != nil && len(p.Metadata.Files) > 0 {
			return "(attachment-only message)"
		}
		return "(empty message)"
	}
	const maxLen = 60
	r := []rune(body)
	if len(r) > maxLen {
		return string(r[:maxLen-1]) + "…"
	}
	return body
}

// renderDeleteConfirm composes the modal. Layout adapted from the
// lipgloss `examples/layout` dialog snippet — rounded border, centred
// question, two buttons (the destructive option highlighted).
func (m Model) renderDeleteConfirm() string {
	if m.deleteConfirmPostID == "" {
		return ""
	}

	outerW := confirmDialogMaxWidth
	if outerW > m.width-4 {
		outerW = m.width - 4
	}
	if outerW < 30 {
		outerW = 30
	}
	inner := outerW - 8 // border (2) + padding (6: 3 each side)
	if inner < 1 {
		inner = 1
	}

	question := lipgloss.NewStyle().
		Width(inner).
		Align(lipgloss.Center).
		Bold(true).
		Render("Are you sure you want to delete this message?")
	preview := lipgloss.NewStyle().
		Width(inner).
		Align(lipgloss.Center).
		Foreground(dimColor).
		Italic(true).
		Render(m.deleteConfirmText)

	buttonBase := lipgloss.NewStyle().
		Padding(0, 3).
		MarginTop(1)
	deleteBtn := buttonBase.
		Foreground(lipgloss.Color("15")).
		Background(lipgloss.Color("9")). // red — destructive
		Bold(true).
		Underline(true).
		Render("Yes (y)")
	cancelBtn := buttonBase.
		Foreground(lipgloss.Color("15")).
		Background(lipgloss.Color("8")).
		MarginLeft(2).
		Render("Cancel (n)")

	buttons := lipgloss.NewStyle().
		Width(inner).
		Align(lipgloss.Center).
		Render(lipgloss.JoinHorizontal(lipgloss.Top, deleteBtn, cancelBtn))

	body := lipgloss.JoinVertical(lipgloss.Center, question, preview, buttons)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(focusedColor).
		Padding(1, 3).
		Render(body)
}
