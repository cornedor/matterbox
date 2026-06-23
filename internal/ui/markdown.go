package ui

import (
	"regexp"
	"strconv"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	emoji "github.com/kyokomi/emoji/v2"

	"matterbox/internal/gitlab"
)

// selfMentionReCache memoises the @self mention regex per username. The
// per-message render paths (search/feed bubbles via styleMentions, post bodies
// via renderMarkdown) all match the same standalone-@self pattern, and the set
// of usernames is tiny and effectively fixed (almost always just the logged-in
// user), so caching avoids a regexp.MustCompile on every rendered line.
var selfMentionReCache sync.Map // string -> *regexp.Regexp

func selfMentionRe(self string) *regexp.Regexp {
	if re, ok := selfMentionReCache.Load(self); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`(^|[^a-zA-Z0-9_.-])@` + regexp.QuoteMeta(self) + `(?:[^a-zA-Z0-9_.-]|$)`)
	selfMentionReCache.Store(self, re)
	return re
}

var (
	mdBoldStyle      = lipgloss.NewStyle().Bold(true)
	mdItalicStyle    = lipgloss.NewStyle().Italic(true)
	mdStrikeStyle    = lipgloss.NewStyle().Strikethrough(true)
	mdCodeStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	mdCodeBlockStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	// mdCodeOpen is the ANSI lead sequence mdCodeStyle emits before content.
	// Extracted once so inline code can be closed with a foreground-only reset
	// that does not wipe enclosing character styles (italic, bold, strike).
	mdCodeOpen      = ansiOpenSeq(mdCodeStyle)
	mdFenceStyle    = lipgloss.NewStyle().Foreground(dimColor)
	mdQuoteBarStyle = lipgloss.NewStyle().Foreground(dimColor)

	mdLinkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Underline(true)
	// mdLinkHoverStyle paints the link under the mouse: the same blue underline
	// plus a subtle background so the hovered link reads as "clickable now".
	// Applied (OSC 8-safely, see highlightLink) only to the hovered post.
	mdLinkHoverStyle = mdLinkStyle.Background(lipgloss.Color("238"))

	mdCodeSpanRe = regexp.MustCompile("`([^`\n]+)`")
	mdBoldRe     = regexp.MustCompile(`\*\*([^*]+?)\*\*`)
	mdItalicRe   = regexp.MustCompile(`\*([^*\s][^*]*?)\*`)
	mdStrikeRe   = regexp.MustCompile(`~~([^~]+?)~~`)
	// Underscore emphasis (_italic_, __bold__). Mattermost/CommonMark accept
	// both `*` and `_`, but underscores can't form *intraword* emphasis — that
	// is what keeps snake_case and __init__-style identifiers from being
	// italicised. The leading/trailing `\b` enforce that boundary: `_` is a word
	// character, so `\b_` only matches an underscore whose neighbour is a
	// non-word char (or string edge). Because `\b` is zero-width it consumes no
	// surrounding text, so adjacent spans like "_a_ _b_" both still match.
	mdBoldUnderscoreRe   = regexp.MustCompile(`\b__([^_]+?)__\b`)
	mdItalicUnderscoreRe = regexp.MustCompile(`\b_([^_\s][^_]*?)_\b`)
	mdImageRe            = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	mdLinkRe             = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	mdURLRe              = regexp.MustCompile(`https?://[^\s<>\x00]+`)
)

const (
	mdCodeSentinel = "\x00MDCODE"
	mdLinkSentinel = "\x00MDLINK"
)

// ansiOpenSeq returns the ANSI escape sequence a style emits immediately
// before its content. The sentinel must not appear in the style's output.
func ansiOpenSeq(s lipgloss.Style) string {
	const sentinel = "\x00"
	r := s.Render(sentinel)
	if i := strings.Index(r, sentinel); i >= 0 {
		return r[:i]
	}
	return ""
}

// renderCodeSpan styles inline code with the configured foreground colour but
// closes it with \x1b[39m (reset foreground only) instead of lipgloss's usual
// \x1b[0m (reset all). That preserves enclosing emphasis spans: a code span
// inside _..._ no longer cancels the outer italic.
func renderCodeSpan(content string) string {
	return mdCodeOpen + content + "\x1b[39m"
}

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

// leadingSpaces counts the run of leading space characters in s.
func leadingSpaces(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

// fenceMarker reports whether line opens a code fence and returns the fence
// character ('`' or '~') and its run length. Per CommonMark a fence may be
// indented up to three spaces and must be at least three characters long; a
// backtick fence's info string may not itself contain a backtick (so a stray
// ``` mid-line isn't an opening fence).
func fenceMarker(line string) (ch byte, runLen int, ok bool) {
	indent := leadingSpaces(line)
	if indent > 3 {
		return 0, 0, false
	}
	t := line[indent:]
	if len(t) == 0 || (t[0] != '`' && t[0] != '~') {
		return 0, 0, false
	}
	c := t[0]
	n := 0
	for n < len(t) && t[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	if c == '`' && strings.ContainsRune(t[n:], '`') {
		return 0, 0, false
	}
	return c, n, true
}

// fenceLang returns the info string (language tag) after an opening fence.
func fenceLang(line string, runLen int) string {
	return strings.TrimSpace(line[leadingSpaces(line)+runLen:])
}

// isClosingFence reports whether line closes a fence opened with ch repeated
// openLen times: the same character, at least as long, nothing but whitespace
// after it, and indented no more than three spaces. Matching the open character
// keeps a ``` line inside a ~~~ block as content rather than a closer.
func isClosingFence(line string, ch byte, openLen int) bool {
	indent := leadingSpaces(line)
	if indent > 3 {
		return false
	}
	t := line[indent:]
	n := 0
	for n < len(t) && t[n] == ch {
		n++
	}
	return n >= openLen && strings.TrimSpace(t[n:]) == ""
}

// isIndentedCode reports whether line begins with the four-space (or single-tab)
// markup indent of an indented code block. Callers gate this on block context:
// indented code may not interrupt a paragraph, so it must follow a blank line.
func isIndentedCode(line string) bool {
	return strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")
}

// stripIndent removes the four-space (or single-tab) markup indent from one line
// of an indented code block, leaving any further indentation as content.
func stripIndent(line string) string {
	if strings.HasPrefix(line, "\t") {
		return line[1:]
	}
	return line[4:]
}

// indentedCodeRun, given lines and a start index i where lines[i] opens an
// indented code block, returns the de-indented body lines and the index of the
// first line past the block. Interior blank lines are kept; trailing ones are
// dropped and left for normal processing so they still render as blanks.
func indentedCodeRun(lines []string, i int) (body []string, next int) {
	for i < len(lines) {
		ln := lines[i]
		if isIndentedCode(ln) {
			body = append(body, stripIndent(ln))
			i++
			continue
		}
		if strings.TrimSpace(ln) == "" {
			// A blank line stays inside the block only if more indented code
			// follows before any non-indented, non-blank line.
			j := i + 1
			for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
			if j < len(lines) && isIndentedCode(lines[j]) {
				body = append(body, "")
				i++
				continue
			}
		}
		break
	}
	for len(body) > 0 && body[len(body)-1] == "" {
		body = body[:len(body)-1]
	}
	return body, i
}

// expandTabs replaces tab characters with spaces, advancing to the next
// multiple of tabWidth (column counter resets on each newline). lipgloss
// measures a tab as zero cells, but the terminal advances it to the next tab
// stop — so a tab-laden paste (e.g. a cookie dump) measures far narrower than
// it paints. The viewport then under-counts wrapped rows and the message
// overflows its budgeted height, pushing the layout off-screen. Expanding tabs
// up front keeps measured width equal to painted width everywhere downstream.
// A tabWidth of 4 matches the indent CommonMark assigns a leading tab, so the
// indented-code detection below still fires.
func expandTabs(s string, tabWidth int) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	col := 0
	for _, r := range s {
		switch r {
		case '\t':
			n := tabWidth - col%tabWidth
			for i := 0; i < n; i++ {
				b.WriteByte(' ')
			}
			col += n
		case '\n':
			b.WriteByte('\n')
			col = 0
		default:
			b.WriteRune(r)
			col++
		}
	}
	return b.String()
}

// renderMarkdown renders a Mattermost message body. Each output line is
// already indented with the two-space message gutter. ei (may be nil) resolves
// custom server emoji to inline Kitty-graphics placeholders; mr (may be nil)
// rewrites GitLab MR URLs to inline badge pills.
func renderMarkdown(msg string, ei *emojiImages, mr mrInlineFn, self string) string {
	lines := strings.Split(strings.TrimRight(expandTabs(msg, 4), "\n"), "\n")
	out := make([]string, 0, len(lines))
	prevBlank := true // start of message counts as preceded by a blank line
	for i := 0; i < len(lines); i++ {
		raw := lines[i]

		// Fenced code block: ``` or ~~~ (CommonMark allows either fence
		// character; the closer must use the same character and be at least as
		// long, so a ``` inside a ~~~ block stays content).
		if ch, runLen, ok := fenceMarker(raw); ok {
			out = append(out, "  "+mdFenceStyle.Render(strings.TrimSpace(raw)))
			for i++; i < len(lines); i++ {
				if isClosingFence(lines[i], ch, runLen) {
					out = append(out, "  "+mdFenceStyle.Render(strings.TrimSpace(lines[i])))
					break
				}
				out = append(out, "  "+mdCodeBlockStyle.Render(lines[i]))
			}
			prevBlank = false
			continue
		}

		// Indented code block: a run of lines indented four spaces (or a tab)
		// that follows a blank line.
		if prevBlank && isIndentedCode(raw) {
			body, next := indentedCodeRun(lines, i)
			for _, b := range body {
				out = append(out, "  "+mdCodeBlockStyle.Render(b))
			}
			i = next - 1 // the outer loop's i++ lands on the terminating line
			prevBlank = false
			continue
		}

		if strings.HasPrefix(raw, ">") {
			content := strings.TrimPrefix(strings.TrimPrefix(raw, ">"), " ")
			out = append(out, "  "+mdQuoteBarStyle.Render("┃")+" "+renderInline(content, ei, mr, self))
			prevBlank = false
			continue
		}
		out = append(out, "  "+renderInline(raw, ei, mr, self))
		prevBlank = strings.TrimSpace(raw) == ""
	}
	return strings.Join(out, "\n")
}

// emojiShortcodeRe matches a `:shortcode:` left unresolved by kyokomi — i.e. a
// custom server emoji candidate. The class mirrors Mattermost's emoji-name
// charset; code spans are already stashed before this runs.
var emojiShortcodeRe = regexp.MustCompile(`:([a-zA-Z0-9_+\-]+):`)

func renderInline(s string, ei *emojiImages, mr mrInlineFn, self string) string {
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

	if self != "" {
		mentionRe := selfMentionRe(self)
		s = mentionRe.ReplaceAllStringFunc(s, func(m string) string {
			atUser := "@" + self
			idx := strings.Index(m, atUser)
			if idx < 0 {
				return m
			}
			prefix := m[:idx]
			suffix := m[idx+len(atUser):]
			return prefix + mentionStyle.Render(atUser) + suffix
		})
	}

	// Bold before italic for each delimiter family so the double-marker form
	// isn't eaten by the single-marker pass.
	s = mdBoldRe.ReplaceAllStringFunc(s, func(m string) string {
		return mdBoldStyle.Render(m[2 : len(m)-2])
	})
	s = mdBoldUnderscoreRe.ReplaceAllStringFunc(s, func(m string) string {
		return mdBoldStyle.Render(m[2 : len(m)-2])
	})
	s = mdItalicRe.ReplaceAllStringFunc(s, func(m string) string {
		return mdItalicStyle.Render(m[1 : len(m)-1])
	})
	s = mdItalicUnderscoreRe.ReplaceAllStringFunc(s, func(m string) string {
		return mdItalicStyle.Render(m[1 : len(m)-1])
	})
	s = mdStrikeRe.ReplaceAllStringFunc(s, func(m string) string {
		return mdStrikeStyle.Render(m[2 : len(m)-2])
	})

	for i, c := range codes {
		s = strings.Replace(s, mdCodeSentinel+strconv.Itoa(i)+"\x00", renderCodeSpan(c), 1)
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
