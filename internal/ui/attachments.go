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

	"matterbox/internal/telemetry"
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
func (m *Model) addAttachments(payloads []clipboardPayload, via string) tea.Cmd {
	if len(payloads) == 0 {
		return nil
	}
	if m.uploadCancel == nil {
		m.uploadCancel = map[string]context.CancelFunc{}
	}

	channelID := m.openChannelID
	if channelID == "" {
		m.status = "no channel open"
		return nil
	}

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
		// Three separate paths exist (paste, drag-and-drop, the CLI) and nobody
		// knows whether any of them is discoverable. The filename never leaves
		// the machine — only its coarse kind and the size, which is what decides
		// whether the upload needs progress feedback.
		m.recordAttachment(via, att, "ok")
		cmds = append(cmds, att.spinner.Tick, m.uploadAttachment(att.id, att.localPath, att.filename, channelID))
	}

	switch {
	case dropped > 0 && len(payloads) == dropped:
		// Every file was refused by the per-post cap — the attach worked, the
		// budget didn't.
		m.recordAttachment(via, pendingAttachment{}, "denied")
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
			telemetry.OperationFailed(telemetry.Failure{
				Where:       "media.upload",
				Class:       telemetry.ClassifyError(msg.err),
				UserVisible: true,
				Err:         msg.err,
			})
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
func (m *Model) collectAttachmentFileIDs() []string {
	var out []string
	for _, att := range m.attachments {
		if att.state == attUploaded && att.fileID != "" {
			out = append(out, att.fileID)
		}
	}
	return out
}

// hasUploadingAttachments reports whether Send should wait.
func (m *Model) hasUploadingAttachments() bool {
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

// attachChipZone is one chip's horizontal extent on a row of the chip strip,
// in coordinates relative to the strip's own top-left cell. closeX0/closeX1
// bound the chip's × button; the rest of [x0,x1) is the chip body. Recorded by
// renderAttachmentBar and read back by the mouse layer (see hitAttachChip).
type attachChipZone struct {
	// y0/y1 bound the chip's screen lines (a chip is a 3-line box), x0/x1 its
	// columns; both relative to the strip's top-left cell.
	y0, y1           int
	x0, x1           int
	closeX0, closeX1 int
	idx              int
}

// renderAttachmentBar builds the chip strip shown above the textarea.
// Returns "" when there are no attachments. Width caps how wide chip
// rows can be before wrapping to a second row. Records the click zones for
// this frame in the view cache (mouse), which is why the whole strip is laid
// out in one pass rather than measured twice.
func (m *Model) renderAttachmentBar(width int) string {
	rows, zones := m.attachmentBarLayout(width)
	if len(rows) == 0 {
		return ""
	}
	if m.vcache != nil {
		m.vcache.attachZones = zones
		m.vcache.attachBarH = barLines(rows)
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// attachmentBarLayout lays the chips out into wrapped rows and returns both the
// rendered rows and each chip's click zone (relative to the strip's top-left).
func (m *Model) attachmentBarLayout(width int) ([]string, []attachChipZone) {
	if len(m.attachments) == 0 {
		return nil, nil
	}
	focused := m.focus == focusAttachments
	if width < 10 {
		width = 10
	}

	chips := make([]string, len(m.attachments))
	widths := make([]int, len(m.attachments))
	for i, att := range m.attachments {
		hovered := m.hover.zone == hitAttachment && m.hover.idx == i
		c := chipText(att, focused && i == m.attachmentIdx, focused, hovered)
		chips[i] = c
		widths[i] = lipgloss.Width(c)
	}

	// Greedy wrap into rows; chips themselves don't break.
	var rows []string
	var zones []attachChipZone
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
		x0 := curW + add - w
		// The × is the last content cell; its hit box also takes the padding
		// cell beside it, so a click one column off still closes the chip.
		zones = append(zones, attachChipZone{
			y0: len(rows), x0: x0, x1: x0 + w, idx: i,
			closeX0: x0 + w - 3, closeX1: x0 + w - 1,
		})
		curW += add
	}
	if len(cur) > 0 {
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cur...))
	}

	if focused {
		hint := lipgloss.NewStyle().Foreground(dimColor).Render(
			"←/→ select · ↵ preview · o open · d/x remove · ↑ messages",
		)
		rows = append([]string{hint}, rows...)
		for i := range zones {
			zones[i].y0++
		}
	}
	// Chips are boxes, so a wrapped row is several screen lines: turn each
	// zone's row index into the line range that row actually occupies.
	offs := make([]int, len(rows)+1)
	for i, r := range rows {
		offs[i+1] = offs[i] + lipgloss.Height(r)
	}
	for i := range zones {
		r := zones[i].y0
		zones[i].y0, zones[i].y1 = offs[r], offs[r+1]
	}
	return rows, zones
}

// barLines is the strip's height in screen lines (a chip row is a 3-line box).
func barLines(rows []string) int {
	n := 0
	for _, r := range rows {
		n += lipgloss.Height(r)
	}
	return n
}

func chipText(att pendingAttachment, selected, focused, hoverClose bool) string {
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
	closeColor := dimColor
	if selected && focused {
		border = focusedColor
		closeColor = focusedColor
	}
	// Trailing ×: the mouse target for dropping this attachment (d/x do the
	// same from the keyboard). Kept one cell wide so the zone maths in
	// attachmentBarLayout can address it from the chip's right edge. It turns
	// red under the pointer, since a click there destroys something and the
	// button carries no label of its own to say so.
	closeStyle := lipgloss.NewStyle().Foreground(closeColor)
	if hoverClose {
		closeStyle = closeStyle.Foreground(lipgloss.Color("9")).Bold(true)
	}
	closeBtn := closeStyle.Render("×")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s%s %s", glyph, body, sizeText, closeBtn))
}

// attachmentPreviewable reports whether a pending composer attachment is
// something the preview modal can render: a still image this build decodes, or
// an SVG. Answered from the filename and the MIME the source reported — the
// file has no FileInfo yet, and may still be uploading.
func attachmentPreviewable(att pendingAttachment) bool {
	ext := ""
	if i := strings.LastIndex(att.filename, "."); i >= 0 {
		ext = att.filename[i+1:]
	}
	mime, _, _ := strings.Cut(att.mime, ";")
	mime = strings.TrimSpace(mime)
	if strings.EqualFold(ext, "svg") || previewableMIME(mime) {
		return true
	}
	return decodableStillExt(ext)
}

// previewAttachment raises the preview modal on a pending attachment, reading
// it straight off disk (it may not have finished uploading).
func (m Model) previewAttachment(att pendingAttachment) (tea.Model, tea.Cmd) {
	if !attachmentPreviewable(att) {
		m.status = "no preview for " + att.filename
		return m, nil
	}
	return m.openPreviewItems([]previewItem{{path: att.localPath, name: att.filename}}, 0)
}
