package ui

import (
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	emoji "github.com/kyokomi/emoji/v2"
)

var (
	mdBoldStyle      = lipgloss.NewStyle().Bold(true)
	mdItalicStyle    = lipgloss.NewStyle().Italic(true)
	mdStrikeStyle    = lipgloss.NewStyle().Strikethrough(true)
	mdCodeStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	mdCodeBlockStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	mdFenceStyle     = lipgloss.NewStyle().Foreground(dimColor)
	mdQuoteBarStyle  = lipgloss.NewStyle().Foreground(dimColor)

	mdLinkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Underline(true)

	mdCodeSpanRe = regexp.MustCompile("`([^`\n]+)`")
	mdBoldRe     = regexp.MustCompile(`\*\*([^*]+?)\*\*`)
	mdItalicRe   = regexp.MustCompile(`\*([^*\s][^*]*?)\*`)
	mdStrikeRe   = regexp.MustCompile(`~~([^~]+?)~~`)
	mdImageRe    = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	mdLinkRe     = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	mdURLRe      = regexp.MustCompile(`https?://[^\s<>\x00]+`)
)

const (
	mdCodeSentinel = "\x00MDCODE"
	mdLinkSentinel = "\x00MDLINK"
)

// osc8Link wraps text in an OSC 8 hyperlink escape pointing at url. The
// terminal (Ghostty) makes the whole run clickable and keeps it
// clickable even when soft-wrapping splits it across visual rows, since
// the hyperlink state persists between the open and close sequences.
func osc8Link(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// trimTrailingURLPunct splits trailing sentence punctuation off a bare
// URL match so e.g. "see https://x.com/a." links only "https://x.com/a"
// and leaves the period as plain text. A closing paren is only trimmed
// when it's unbalanced (URLs legitimately contain balanced parens).
func trimTrailingURLPunct(u string) (clean, trailing string) {
	i := len(u)
	for i > 0 {
		c := u[i-1]
		if strings.IndexByte(".,;:!?\"'", c) >= 0 {
			i--
			continue
		}
		if c == ')' && strings.Count(u[:i], ")") > strings.Count(u[:i], "(") {
			i--
			continue
		}
		break
	}
	return u[:i], u[i:]
}

// renderMarkdown renders a Mattermost message body. Each output line is
// already indented with the two-space message gutter. ei (may be nil) resolves
// custom server emoji to inline Kitty-graphics placeholders; a nil manager
// leaves them as literal :name: text.
func renderMarkdown(msg string, ei *emojiImages) string {
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
			out = append(out, "  "+mdQuoteBarStyle.Render("┃")+" "+renderInline(content, ei))
			continue
		}
		out = append(out, "  "+renderInline(raw, ei))
	}
	return strings.Join(out, "\n")
}

// emojiShortcodeRe matches a `:shortcode:` left unresolved by kyokomi — i.e. a
// custom server emoji candidate. The class mirrors Mattermost's emoji-name
// charset; code spans are already stashed before this runs.
var emojiShortcodeRe = regexp.MustCompile(`:([a-zA-Z0-9_+\-]+):`)

func renderInline(s string, ei *emojiImages) string {
	if s == "" {
		return ""
	}
	// Stash code spans so their contents aren't reinterpreted as markdown.
	var codes []string
	s = mdCodeSpanRe.ReplaceAllStringFunc(s, func(m string) string {
		codes = append(codes, m[1:len(m)-1])
		return mdCodeSentinel + strconv.Itoa(len(codes)-1) + "\x00"
	})

	// Unicode emoji first (kyokomi font glyphs, exactly as before). Any
	// surviving :name: is either a Mattermost skin-tone variant kyokomi spells
	// differently (resolved to a unicode glyph) or a custom-emoji candidate
	// resolved to an inline-image placeholder when ready (and recorded as a
	// sighting otherwise). The placeholder carries no markdown metacharacters,
	// so the styling passes below can't corrupt it.
	s = emoji.Sprint(s)
	s = emojiShortcodeRe.ReplaceAllStringFunc(s, func(m string) string {
		name := m[1 : len(m)-1]
		if g := unicodeEmojiGlyph(name); g != "" {
			return g
		}
		if ei != nil {
			if ph, ok := ei.inline(name); ok {
				return ph
			}
		}
		return m
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

	// Stash links (markdown [text](url) first, then bare URLs) so their
	// URL characters aren't reinterpreted by the styling passes below and
	// so the styling passes don't run on URLs already claimed by a
	// markdown link. They're restored as OSC 8 hyperlinks at the end.
	var links []struct{ url, text string }
	s = mdLinkRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdLinkRe.FindStringSubmatch(m)
		links = append(links, struct{ url, text string }{sub[2], sub[1]})
		return mdLinkSentinel + strconv.Itoa(len(links)-1) + "\x00"
	})
	s = mdURLRe.ReplaceAllStringFunc(s, func(m string) string {
		clean, trailing := trimTrailingURLPunct(m)
		links = append(links, struct{ url, text string }{clean, clean})
		return mdLinkSentinel + strconv.Itoa(len(links)-1) + "\x00" + trailing
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
	for i, l := range links {
		s = strings.Replace(s, mdLinkSentinel+strconv.Itoa(i)+"\x00", osc8Link(l.url, mdLinkStyle.Render(l.text)), 1)
	}
	return s
}
