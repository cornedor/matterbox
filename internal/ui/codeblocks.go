package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// codeBlock is one fenced ``` block extracted from a post's raw markdown:
// the language tag after the opening fence (may be empty) and the verbatim
// content between the fences.
type codeBlock struct {
	lang    string
	content string
}

// extractCodeBlocks pulls every code block out of a post's raw markdown,
// mirroring renderMarkdown's parsing (markdown.go). It recognises all three
// CommonMark forms: ``` and ~~~ fences (the opening fence's trailing token is
// the language; content is preserved verbatim) and four-space/tab indented
// blocks (the markup indent is stripped, and the block must follow a blank
// line). A fence left unclosed at end-of-message still yields its block so a
// half-typed snippet can be copied.
func extractCodeBlocks(msg string) []codeBlock {
	lines := strings.Split(strings.TrimRight(msg, "\n"), "\n")
	var blocks []codeBlock
	prevBlank := true
	for i := 0; i < len(lines); i++ {
		raw := lines[i]

		if ch, runLen, ok := fenceMarker(raw); ok {
			lang := fenceLang(raw, runLen)
			var content []string
			for i++; i < len(lines) && !isClosingFence(lines[i], ch, runLen); i++ {
				content = append(content, lines[i])
			}
			blocks = append(blocks, codeBlock{lang: lang, content: strings.Join(content, "\n")})
			prevBlank = false
			continue
		}

		if prevBlank && isIndentedCode(raw) {
			body, next := indentedCodeRun(lines, i)
			blocks = append(blocks, codeBlock{content: strings.Join(body, "\n")})
			i = next - 1
			prevBlank = false
			continue
		}

		prevBlank = strings.TrimSpace(raw) == ""
	}
	return blocks
}

// copyCodeFromPost handles the copy-code action for a selected post: extract
// every fenced block, copy it straight away when there's exactly one, or raise
// the picker so the user can choose when there are several.
func (m Model) copyCodeFromPost(p *model.Post) (tea.Model, tea.Cmd) {
	m.recordActed(m.actedRecord("copy_code", p, "key"))
	blocks := extractCodeBlocks(p.Message)
	switch len(blocks) {
	case 0:
		m.status = "no code blocks in this message"
		return m, nil
	case 1:
		return m, m.copyText(blocks[0].content, "code block")
	default:
		m.openCodePicker(blocks)
		return m, nil
	}
}

// codePickerActive reports whether the code-block picker modal is up.
func (m *Model) codePickerActive() bool {
	return len(m.codePickerBlocks) > 0
}

// openCodePicker raises the modal listing every code block in a post.
// Callers only reach here when there's more than one block.
func (m *Model) openCodePicker(blocks []codeBlock) {
	m.codePickerBlocks = blocks
	m.codePickerIdx = 0
}

// closeCodePicker tears down picker state without copying anything.
func (m *Model) closeCodePicker() {
	m.codePickerBlocks = nil
	m.codePickerIdx = 0
}

// applyCodePick copies the highlighted block and closes the modal.
func (m *Model) applyCodePick() tea.Cmd {
	if m.codePickerIdx < 0 || m.codePickerIdx >= len(m.codePickerBlocks) {
		return nil
	}
	b := m.codePickerBlocks[m.codePickerIdx]
	m.closeCodePicker()
	return m.copyText(b.content, "code block")
}

// handleCodePickerKey owns every keystroke while the code-block picker is up.
// Digit accelerators 1-9 copy immediately; arrow keys navigate; enter copies
// the highlighted entry; esc cancels.
func (m Model) handleCodePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.closeCodePicker()
		return m, nil
	case "enter":
		cmd := m.applyCodePick()
		return m, cmd
	}
	if key.Matches(msg, m.keys.Up) {
		if m.codePickerIdx > 0 {
			m.codePickerIdx--
		}
		return m, nil
	}
	if key.Matches(msg, m.keys.Down) {
		if m.codePickerIdx < len(m.codePickerBlocks)-1 {
			m.codePickerIdx++
		}
		return m, nil
	}
	// Digit accelerators 1..9 → copy the matching block directly.
	if s := msg.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		idx := int(s[0] - '1')
		if idx < len(m.codePickerBlocks) {
			m.codePickerIdx = idx
			cmd := m.applyCodePick()
			return m, cmd
		}
	}
	return m, nil
}

// codeBlockPreview renders a one-line summary of a block for the picker: the
// first non-blank line, with leading whitespace trimmed and tabs squashed so
// indentation doesn't waste the row.
func codeBlockPreview(content string) string {
	for _, ln := range strings.Split(content, "\n") {
		t := strings.TrimSpace(ln)
		if t != "" {
			return strings.ReplaceAll(t, "\t", " ")
		}
	}
	return "(empty)"
}

// renderCodePicker draws the modal listing the post's code blocks. Layout
// mirrors the open-target picker: rounded border, centred header, then one row
// per block with a digit accelerator, the language tag, and a truncated
// first-line preview so the user can tell the blocks apart.
func (m *Model) renderCodePicker() string {
	if !m.codePickerActive() {
		return ""
	}
	outerW := confirmDialogMaxWidth
	if outerW > m.width-4 {
		outerW = m.width - 4
	}
	if outerW < 32 {
		outerW = 32
	}
	inner := outerW - 8
	if inner < 1 {
		inner = 1
	}

	header := lipgloss.NewStyle().
		Width(inner).
		Align(lipgloss.Center).
		Bold(true).
		Render("Copy code block")
	hint := lipgloss.NewStyle().
		Width(inner).
		Align(lipgloss.Center).
		Foreground(dimColor).
		Italic(true).
		Render("digit/↵ copies · ↑/↓ navigates · esc cancels")

	rowStyle := lipgloss.NewStyle()
	cursorStyle := lipgloss.NewStyle().Foreground(focusedColor).Bold(true)
	langStyle := lipgloss.NewStyle().Foreground(dimColor)
	rows := make([]string, 0, len(m.codePickerBlocks))
	for i, b := range m.codePickerBlocks {
		accel := " "
		if i < 9 {
			accel = fmt.Sprintf("%d", i+1)
		}
		lang := b.lang
		if lang == "" {
			lang = "text"
		}
		prefix := fmt.Sprintf("[%s] %s  ", accel, langStyle.Render(lang))
		preview := truncate(codeBlockPreview(b.content), inner-lipgloss.Width(prefix))
		text := prefix + preview
		if i == m.codePickerIdx {
			rows = append(rows, cursorStyle.Render("▸ "+text))
		} else {
			rows = append(rows, rowStyle.Render("  "+text))
		}
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		header,
		hint,
		"",
		strings.Join(rows, "\n"),
	)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(focusedColor).
		Padding(1, 3).
		Render(body)
}
