package ui

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	mdBoldStyle      = lipgloss.NewStyle().Bold(true)
	mdItalicStyle    = lipgloss.NewStyle().Italic(true)
	mdStrikeStyle    = lipgloss.NewStyle().Strikethrough(true)
	mdCodeStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	mdCodeBlockStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	mdFenceStyle     = lipgloss.NewStyle().Foreground(dimColor)
	mdQuoteBarStyle  = lipgloss.NewStyle().Foreground(dimColor)

	mdCodeSpanRe = regexp.MustCompile("`([^`\n]+)`")
	mdBoldRe     = regexp.MustCompile(`\*\*([^*]+?)\*\*`)
	mdItalicRe   = regexp.MustCompile(`\*([^*\s][^*]*?)\*`)
	mdStrikeRe   = regexp.MustCompile(`~~([^~]+?)~~`)
	mdImageRe    = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
)

const mdCodeSentinel = "\x00MDCODE"

// renderMarkdown renders a Mattermost message body. Each output line is
// already indented with the two-space message gutter.
func renderMarkdown(msg string) string {
	lines := strings.Split(strings.TrimRight(msg, "\n"), "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for _, raw := range lines {
		if strings.HasPrefix(strings.TrimLeft(raw, " "), "```") {
			marker := strings.TrimSpace(raw)
			out = append(out, "  "+mdFenceStyle.Render(marker))
			inFence = !inFence
			continue
		}
		if inFence {
			out = append(out, "  "+mdCodeBlockStyle.Render(raw))
			continue
		}
		if strings.HasPrefix(raw, ">") {
			content := strings.TrimPrefix(strings.TrimPrefix(raw, ">"), " ")
			out = append(out, "  "+mdQuoteBarStyle.Render("┃")+" "+renderInline(content))
			continue
		}
		out = append(out, "  "+renderInline(raw))
	}
	return strings.Join(out, "\n")
}

func renderInline(s string) string {
	if s == "" {
		return ""
	}
	// Stash code spans so their contents aren't reinterpreted as markdown.
	var codes []string
	s = mdCodeSpanRe.ReplaceAllStringFunc(s, func(m string) string {
		codes = append(codes, m[1:len(m)-1])
		return mdCodeSentinel + strconv.Itoa(len(codes)-1) + "\x00"
	})

	// Inline images first, so the bracketed alt text isn't mistaken for
	// other inline syntax.
	s = mdImageRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdImageRe.FindStringSubmatch(m)
		alt := sub[1]
		if alt == "" {
			alt = "image"
		}
		return attachmentStyle.Render("🖼️ " + alt)
	})

	s = mdBoldRe.ReplaceAllStringFunc(s, func(m string) string {
		return mdBoldStyle.Render(m[2 : len(m)-2])
	})
	s = mdItalicRe.ReplaceAllStringFunc(s, func(m string) string {
		return mdItalicStyle.Render(m[1 : len(m)-1])
	})
	s = mdStrikeRe.ReplaceAllStringFunc(s, func(m string) string {
		return mdStrikeStyle.Render(m[2 : len(m)-2])
	})

	for i, c := range codes {
		s = strings.Replace(s, mdCodeSentinel+strconv.Itoa(i)+"\x00", mdCodeStyle.Render(c), 1)
	}
	return s
}
