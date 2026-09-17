package ui

import (
	"sort"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"matterbox/internal/emoji"
)

// emojiLimit caps the picker so the popup stays a few rows tall regardless
// of how many shortcodes match the query.
const emojiLimit = 8

// unicodeEmojiGlyph resolves a bare emoji shortcode (no colons) to a unicode
// glyph, or "" if Mattermost doesn't know the name — in which case it's a
// custom (server) emoji candidate. internal/emoji carries Mattermost's own
// table, so what resolves here resolves identically in the web client, down to
// the skin-tone variants and the gender-neutral defaults.
func unicodeEmojiGlyph(name string) string {
	return emoji.Glyph(name)
}

// emojiItem is one picker candidate: `code` is the colon-wrapped shortcode
// (e.g. ":smile:") inserted on accept; `name` is the bare shortcode, resolved
// to a glyph (unicode, custom image, or literal) at render time so a custom
// emoji's image appears as soon as it's ready without recomputing matches.
type emojiItem struct {
	code string
	name string
}

// emojiState mirrors mentionState but for `:`-triggered shortcode
// completion. `start` is the rune offset of the ':' in the logical line
// `line`; `query` is everything between ':' and the cursor (lower-cased).
// There's no fetch sequence — the shortcode set is static and matched
// entirely from the local index.
type emojiState struct {
	active bool
	line   int
	start  int
	query  string
	items  []emojiItem
	idx    int
}

// emojiIndex is the sorted list of shortcodes (sans colons) the picker offers:
// exactly the set Mattermost resolves, so nothing accepted here reaches anyone
// else as literal text.
func emojiIndex() []string { return emoji.Names() }

// updateEmoji recomputes picker state after the textarea has processed a
// key. Unlike mentions it never needs a fetch, so it returns nothing — the
// caller just discards the result.
func (m *Model) updateEmoji() {
	row, col := m.input.CursorRowCol()

	lines := strings.Split(m.input.Value(), "\n")
	if row < 0 || row >= len(lines) {
		m.closeEmoji()
		return
	}
	runes := []rune(lines[row])
	if col > len(runes) {
		col = len(runes)
	}

	// Scan backward from the cursor for a ':' at a word boundary (start of
	// line or preceded by whitespace). Stop at whitespace — a shortcode has
	// none. Stop at another ':' too: that's a completed ":name:" behind us,
	// not an open trigger.
	at := -1
	for i := col - 1; i >= 0; i-- {
		r := runes[i]
		if r == ':' {
			if i == 0 || unicode.IsSpace(runes[i-1]) {
				at = i
			}
			break
		}
		if unicode.IsSpace(r) {
			break
		}
	}
	if at < 0 {
		m.closeEmoji()
		return
	}

	// Require at least two characters after the ':' before opening — matching
	// Mattermost and Discord. This keeps the picker from firing on text
	// emoticons like ":)", ":D" or ":o", and costs nothing real: no shortcode
	// is shorter than two characters ("+1", "-1", "ok", "up", ...). A bare ":"
	// stays quiet too.
	if col-(at+1) < 2 {
		m.closeEmoji()
		return
	}
	query := strings.ToLower(string(runes[at+1 : col]))
	// Even with two characters, a query is only a shortcode in progress if it
	// holds shortcode characters ([a-z0-9_+-]). Anything else is a longer
	// emoticon like ":-)" or ":'(" — bail so the picker stays closed and Enter
	// still sends. Without this, a stray ")" matches names that merely contain
	// one (e.g. ":flag_Myanmar_(Burma):") and Enter would accept that.
	if !isEmojiQuery(query) {
		m.closeEmoji()
		return
	}
	if m.emoji.active && m.emoji.line == row && m.emoji.start == at && m.emoji.query == query {
		return
	}
	m.emoji.active = true
	m.emoji.line = row
	m.emoji.start = at
	m.emoji.query = query
	m.emoji.items = m.emojiMatches(query)
	m.emoji.idx = 0
	if len(m.emoji.items) == 0 {
		m.closeEmoji()
	}
}

// isEmojiQuery reports whether s looks like an emoji shortcode in progress:
// only the characters that can appear in a shortcode name. Text emoticons
// (":)", ":(", ":/", ":|", ...) carry punctuation that never starts a
// shortcode, so they're rejected and the picker never opens for them.
func isEmojiQuery(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '+' || r == '-':
		default:
			return false
		}
	}
	return true
}

// closeEmoji clears the picker.
func (m *Model) closeEmoji() {
	if !m.emoji.active {
		return
	}
	m.emoji = emojiState{}
}

// emojiMatches returns up to emojiLimit candidates ranked by, in order:
// match quality (exact > prefix > interior substring > fuzzy subsequence),
// then popularity (shortcodes you've accepted before float up within a
// tier), then custom-vs-unicode, then match position. Fuzzy matching means
// ":smle" still finds ":smile:"; the band-first ordering keeps the obvious
// prefix completion on top while fuzzier hits stay reachable. Custom
// (server) emoji are merged ahead of the unicode index within each tier so
// they stay discoverable against the much larger unicode set; glyphs are
// resolved at render time, not here.
func (m Model) emojiMatches(query string) []emojiItem {
	type cand struct {
		name   string
		band   int
		score  int
		custom bool
	}
	var cands []cand
	seen := map[string]struct{}{}
	consider := func(name string, custom bool) {
		if _, dup := seen[name]; dup {
			return
		}
		band, score, ok := fuzzyScore(name, query)
		if !ok {
			return
		}
		seen[name] = struct{}{}
		cands = append(cands, cand{name: name, band: band, score: score, custom: custom})
	}
	for _, name := range m.customEmojiNames {
		consider(name, true)
	}
	for _, name := range emojiIndex() {
		consider(name, false)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		// 1. Match quality: exact > prefix > substring > subsequence.
		if a.band != b.band {
			return a.band < b.band
		}
		// 2. Popularity: more-often-accepted shortcodes first.
		if ua, ub := m.emojiUsage[a.name], m.emojiUsage[b.name]; ua != ub {
			return ua > ub
		}
		// 3. Custom (server) emoji ahead of unicode for discoverability.
		if a.custom != b.custom {
			return a.custom
		}
		// 4. Finer match position, then name as a stable last resort.
		if a.score != b.score {
			return a.score < b.score
		}
		return a.name < b.name
	})
	if len(cands) > emojiLimit {
		cands = cands[:emojiLimit]
	}
	out := make([]emojiItem, len(cands))
	for i, c := range cands {
		out[i] = emojiItem{code: ":" + c.name + ":", name: c.name}
	}
	return out
}

// acceptEmoji replaces ":<query>" with the selected shortcode + space at the
// captured position. Returns (cmd, true) on success — cmd persists the
// updated popularity counter — or (nil, false) when there's nothing usable,
// so the caller falls through to the default key handler.
func (m *Model) acceptEmoji() (tea.Cmd, bool) {
	if !m.emoji.active || m.emoji.idx < 0 || m.emoji.idx >= len(m.emoji.items) {
		return nil, false
	}
	it := m.emoji.items[m.emoji.idx]
	if it.code == "" {
		return nil, false
	}
	lines := strings.Split(m.input.Value(), "\n")
	if m.emoji.line < 0 || m.emoji.line >= len(lines) {
		return nil, false
	}
	runes := []rune(lines[m.emoji.line])
	_, col := m.input.CursorRowCol()
	if col > len(runes) {
		col = len(runes)
	}
	if m.emoji.start > col {
		return nil, false
	}
	replaced := string(runes[:m.emoji.start]) + it.code + " " + string(runes[col:])
	lines[m.emoji.line] = replaced
	m.history.checkpoint(m.composerContextKey(), m.input.Value())
	m.input.SetValue(strings.Join(lines, "\n"))
	m.syncInputHeight()
	bump := m.bumpEmojiStat(it.name)
	m.closeEmoji()
	return bump, true
}

// emojiPopupStyle reuses the mention dropdown frame vocabulary.
var emojiPopupStyle = lipgloss.NewStyle().
	Border(border).
	BorderForeground(focusedColor).
	Padding(0, 1)

// renderEmojiPopup returns the dropdown or "" if it shouldn't show.
func (m *Model) renderEmojiPopup() string {
	if !m.emoji.active || len(m.emoji.items) == 0 {
		return ""
	}
	dim := lipgloss.NewStyle().Foreground(dimColor)
	rows := make([]string, 0, len(m.emoji.items))
	for i, it := range m.emoji.items {
		glyph := m.renderEmojiGlyph(it.name)
		if i == m.emoji.idx {
			// Don't dim the code on the highlighted row — the dim foreground
			// against the selection background is barely legible. Leave the
			// glyph and code at default (white) and let selectedRow paint the
			// background.
			rows = append(rows, selectedRow.Render(glyph+"  "+it.code))
			continue
		}
		rows = append(rows, glyph+"  "+dim.Render(it.code))
	}
	return emojiPopupStyle.Render(strings.Join(rows, "\n"))
}
