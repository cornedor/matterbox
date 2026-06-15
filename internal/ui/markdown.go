package ui

import (
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	emoji "github.com/kyokomi/emoji/v2"

	"matterbox/internal/gitlab"
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

// mrInlineFn resolves a bare GitLab MR URL to an inline badge string.
// When it returns ok=true the badge replaces the raw URL entirely (including
// any OSC 8 link); when ok=false the URL is rendered as a normal hyperlink.
// A nil mrInlineFn means MR badge substitution is disabled (e.g. in ref-panel
// descriptions and tests).
type mrInlineFn func(rawURL string) (badge string, ok bool)

// renderMarkdown renders a Mattermost message body. Each output line is
// already indented with the two-space message gutter. ei (may be nil) resolves
// custom server emoji to inline Kitty-graphics placeholders; mr (may be nil)
// rewrites GitLab MR URLs to inline badge pills.
func renderMarkdown(msg string, ei *emojiImages, mr mrInlineFn) string {
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
			out = append(out, "  "+mdQuoteBarStyle.Render("┃")+" "+renderInline(content, ei, mr))
			continue
		}
		out = append(out, "  "+renderInline(raw, ei, mr))
	}
	return strings.Join(out, "\n")
}

// emojiShortcodeRe matches a `:shortcode:` left unresolved by kyokomi — i.e. a
// custom server emoji candidate. The class mirrors Mattermost's emoji-name
// charset; code spans are already stashed before this runs.
var emojiShortcodeRe = regexp.MustCompile(`:([a-zA-Z0-9_+\-]+):`)

func renderInline(s string, ei *emojiImages, mr mrInlineFn) string {
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
	//
	// Bare GitLab MR URLs are handled specially: when mr is set and the URL
	// matches a merge-request link, it is replaced with an inline badge pill
	// rather than a plain hyperlink. The badge is inserted directly (not
	// stashed) so its ANSI escapes survive the later styling passes unchanged.
	type linkEntry struct{ url, text string }
	var links []linkEntry
	var mrBadges []struct {
		sentinel string
		badge    string
	}
	s = mdLinkRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdLinkRe.FindStringSubmatch(m)
		links = append(links, linkEntry{sub[2], sub[1]})
		return mdLinkSentinel + strconv.Itoa(len(links)-1) + "\x00"
	})
	const mrBadgeSentinel = "\x00MRBADGE"
	s = mdURLRe.ReplaceAllStringFunc(s, func(m string) string {
		clean, trailing := trimTrailingURLPunct(m)
		if mr != nil {
			if badge, ok := mr(clean); ok {
				// Stash badge separately — its ANSI bytes must not be styled.
				idx := len(mrBadges)
				mrBadges = append(mrBadges, struct {
					sentinel string
					badge    string
				}{mrBadgeSentinel + strconv.Itoa(idx) + "\x00", badge})
				return mrBadgeSentinel + strconv.Itoa(idx) + "\x00" + trailing
			}
		}
		links = append(links, linkEntry{clean, clean})
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
	for _, b := range mrBadges {
		s = strings.Replace(s, b.sentinel, b.badge, 1)
	}
	return s
}

// parseMRURL parses a raw URL and returns its GitLab project path and MR iid
// when the URL matches the configured instance. Used by mrInlineFn closures.
func parseMRURL(rawURL, baseURL string) (project string, iid int, ok bool) {
	refs := gitlab.Refs(rawURL, baseURL)
	if len(refs) == 0 {
		return "", 0, false
	}
	return refs[0].Project, refs[0].IID, true
}
