package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Mattermost's web client caps a post at 5 file attachments; mirror it
// here so the user sees the same limit they'd see in the official UI.
const maxAttachmentsPerPost = 5

type attState int

const (
	attUploading attState = iota
	attUploaded
	attFailed
)

type pendingAttachment struct {
	id        string
	filename  string
	size      int64
	mime      string
	localPath string
	isTemp    bool
	state     attState
	spinner   spinner.Model
	fileID    string
	err       error
}

type attachmentUploadedMsg struct {
	id     string
	fileID string
	size   int64
	err    error
}

func newAttachmentID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func newAttachmentSpinner() spinner.Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(focusedColor)
	return s
}

// addAttachments appends payloads as pending uploads, kicks off each
// upload, and starts the spinner ticking for each. Enforces the 5-file
// cap; over-budget payloads are dropped with a status message.
func (m *Model) addAttachments(payloads []clipboardPayload) tea.Cmd {
	if len(payloads) == 0 {
		return nil
	}
	if m.uploadCancel == nil {
		m.uploadCancel = map[string]context.CancelFunc{}
	}

	vis := m.visibleChannels()
	if m.channelIdx >= len(vis) {
		m.status = "no channel selected"
		return nil
	}
	channelID := vis[m.channelIdx].Id

	var cmds []tea.Cmd
	dropped := 0
	for _, p := range payloads {
		if len(m.attachments) >= maxAttachmentsPerPost {
			dropped++
			if p.isTemp {
				_ = os.Remove(p.path)
			}
			continue
		}
		att := pendingAttachment{
			id:        newAttachmentID(),
			filename:  p.filename,
			size:      p.size,
			mime:      p.mime,
			localPath: p.path,
			isTemp:    p.isTemp,
			state:     attUploading,
			spinner:   newAttachmentSpinner(),
		}
		m.attachments = append(m.attachments, att)
		cmds = append(cmds, att.spinner.Tick, m.uploadAttachment(att.id, att.localPath, att.filename, channelID))
	}

	switch {
	case dropped > 0 && len(payloads) == dropped:
		m.status = fmt.Sprintf("max %d attachments per post", maxAttachmentsPerPost)
	case dropped > 0:
		m.status = fmt.Sprintf("attached %d (max %d per post)", len(payloads)-dropped, maxAttachmentsPerPost)
	}
	m.resizeMessagesViewport()
	return tea.Batch(cmds...)
}

// uploadAttachment reads the file from disk and pushes it to Mattermost
// in the background. The result arrives as attachmentUploadedMsg; the
// id field is matched against m.attachments to find the right chip.
// If the chip was removed before this returns, the msg is dropped.
func (m *Model) uploadAttachment(id, path, filename, channelID string) tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	if m.uploadCancel == nil {
		m.uploadCancel = map[string]context.CancelFunc{}
	}
	m.uploadCancel[id] = cancel
	client := m.client
	return func() tea.Msg {
		data, err := os.ReadFile(path)
		if err != nil {
			return attachmentUploadedMsg{id: id, err: err}
		}
		info, err := client.UploadFile(ctx, channelID, filename, data)
		if err != nil {
			return attachmentUploadedMsg{id: id, err: err}
		}
		return attachmentUploadedMsg{id: id, fileID: info.Id, size: info.Size}
	}
}

// applyUploadResult routes a completed upload to its chip. Stale msgs
// (chip was removed mid-upload) are dropped silently.
func (m *Model) applyUploadResult(msg attachmentUploadedMsg) {
	if cancel, ok := m.uploadCancel[msg.id]; ok {
		cancel()
		delete(m.uploadCancel, msg.id)
	}
	for i := range m.attachments {
		if m.attachments[i].id != msg.id {
			continue
		}
		if msg.err != nil {
			m.attachments[i].state = attFailed
			m.attachments[i].err = msg.err
			m.status = fmt.Sprintf("upload %s: %v", m.attachments[i].filename, msg.err)
			return
		}
		m.attachments[i].state = attUploaded
		m.attachments[i].fileID = msg.fileID
		if msg.size > 0 {
			m.attachments[i].size = msg.size
		}
		return
	}
}

// removeAttachment drops a chip (cancelling its upload if in flight)
// and adjusts attachmentIdx + focus so the UI stays sane.
func (m *Model) removeAttachment(id string) {
	if cancel, ok := m.uploadCancel[id]; ok {
		cancel()
		delete(m.uploadCancel, id)
	}
	for i := range m.attachments {
		if m.attachments[i].id != id {
			continue
		}
		att := m.attachments[i]
		if att.isTemp && strings.HasPrefix(filepath.Clean(att.localPath), filepath.Join(os.TempDir(), "matterbox-paste")) {
			_ = os.Remove(att.localPath)
		}
		m.attachments = append(m.attachments[:i], m.attachments[i+1:]...)
		break
	}
	if m.attachmentIdx >= len(m.attachments) {
		m.attachmentIdx = len(m.attachments) - 1
	}
	if m.attachmentIdx < 0 {
		m.attachmentIdx = 0
	}
	if len(m.attachments) == 0 && m.focus == focusAttachments {
		m.focus = focusInput
		m.input.Focus()
	}
	m.resizeMessagesViewport()
}

// clearAttachments drops everything (called after a successful send).
func (m *Model) clearAttachments() {
	for _, att := range m.attachments {
		if cancel, ok := m.uploadCancel[att.id]; ok {
			cancel()
			delete(m.uploadCancel, att.id)
		}
		if att.isTemp && strings.HasPrefix(filepath.Clean(att.localPath), filepath.Join(os.TempDir(), "matterbox-paste")) {
			_ = os.Remove(att.localPath)
		}
	}
	m.attachments = nil
	m.attachmentIdx = 0
}

// collectAttachmentFileIDs returns server file IDs ready to attach to
// the next post. Only fully-uploaded chips contribute.
func (m Model) collectAttachmentFileIDs() []string {
	var out []string
	for _, att := range m.attachments {
		if att.state == attUploaded && att.fileID != "" {
			out = append(out, att.fileID)
		}
	}
	return out
}

// hasUploadingAttachments reports whether Send should wait.
func (m Model) hasUploadingAttachments() bool {
	for _, att := range m.attachments {
		if att.state == attUploading {
			return true
		}
	}
	return false
}

// tickAttachmentSpinners forwards a spinner.TickMsg to every uploading
// chip's spinner. Spinners self-discriminate via TickMsg.ID, so this is
// safe to broadcast.
func (m *Model) tickAttachmentSpinners(msg spinner.TickMsg) tea.Cmd {
	var cmds []tea.Cmd
	for i := range m.attachments {
		if m.attachments[i].state != attUploading {
			continue
		}
		sp, cmd := m.attachments[i].spinner.Update(msg)
		m.attachments[i].spinner = sp
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// renderAttachmentBar builds the chip strip shown above the textarea.
// Returns "" when there are no attachments. Width caps how wide chip
// rows can be before wrapping to a second row.
func (m Model) renderAttachmentBar(width int) string {
	if len(m.attachments) == 0 {
		return ""
	}
	focused := m.focus == focusAttachments
	if width < 10 {
		width = 10
	}

	chips := make([]string, len(m.attachments))
	widths := make([]int, len(m.attachments))
	for i, att := range m.attachments {
		c := chipText(att, focused && i == m.attachmentIdx, focused)
		chips[i] = c
		widths[i] = lipgloss.Width(c)
	}

	// Greedy wrap into rows; chips themselves don't break.
	var rows []string
	var cur []string
	curW := 0
	for i, c := range chips {
		w := widths[i]
		add := w
		if len(cur) > 0 {
			add++ // single-space separator between chips
		}
		if curW+add > width && len(cur) > 0 {
			rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cur...))
			cur = nil
			curW = 0
			add = w
		}
		if len(cur) > 0 {
			cur = append(cur, " ")
		}
		cur = append(cur, c)
		curW += add
	}
	if len(cur) > 0 {
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cur...))
	}

	if focused {
		hint := lipgloss.NewStyle().Foreground(dimColor).Render(
			"←/→ select · o/↵ open · d/x remove · tab leave",
		)
		rows = append([]string{hint}, rows...)
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func chipText(att pendingAttachment, selected, focused bool) string {
	var glyph string
	switch att.state {
	case attUploading:
		glyph = att.spinner.View()
	case attUploaded:
		glyph = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("✓")
	case attFailed:
		glyph = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("!")
	}

	name := truncate(att.filename, 28)
	var nameStyle lipgloss.Style
	if selected {
		nameStyle = lipgloss.NewStyle().Bold(true)
	}
	if att.state == attFailed {
		nameStyle = nameStyle.Foreground(lipgloss.Color("9"))
	}
	body := nameStyle.Render(name)

	sizeText := ""
	if att.size > 0 {
		sizeText = " " + lipgloss.NewStyle().Foreground(dimColor).Render(humanSize(att.size))
	}

	border := dimColor
	if selected && focused {
		border = focusedColor
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s%s", glyph, body, sizeText))
}
