package ui

import (
	"sort"
	"strings"
	"sync"
	"unicode"

	"charm.land/lipgloss/v2"
	emoji "github.com/kyokomi/emoji/v2"
)

// emojiLimit caps the picker so the popup stays a few rows tall regardless
// of how many shortcodes match the query.
const emojiLimit = 8

// skinTones maps Mattermost's descriptive skin-tone shortcode suffixes to the
// Unicode Fitzpatrick modifier runes. Mattermost spells toned emoji as
// "<base>_<tone>_skin_tone" (e.g. ":+1_medium_light_skin_tone:") where kyokomi
// uses "<base>_toneN", so a direct codemap lookup misses. Ordered longest
// suffix first so "medium_light"/"medium_dark" win over the "light"/"dark"
// suffixes they contain.
// vs16 is the emoji variation selector (U+FE0F). It forces emoji presentation
// on the base glyph but is redundant — and non-canonical — once a skin-tone
// modifier follows, so it's stripped before the modifier is appended.
const vs16 = "️"

var skinTones = []struct {
	suffix string
	mod    rune
}{
	{"medium_light_skin_tone", '\U0001F3FC'},
	{"medium_dark_skin_tone", '\U0001F3FE'},
	{"medium_skin_tone", '\U0001F3FD'},
	{"light_skin_tone", '\U0001F3FB'},
	{"dark_skin_tone", '\U0001F3FF'},
}

// unicodeEmojiGlyph resolves a bare emoji shortcode (no colons) to a unicode
// glyph, or "" if kyokomi doesn't know it. Beyond a direct codemap lookup it
// understands Mattermost's "<base>_<tone>_skin_tone" naming: the base glyph is
// resolved and the matching Fitzpatrick modifier appended, composing the same
// grapheme kyokomi's "_toneN" variants produce. A trailing VS16 on the base is
// dropped first — it's redundant before a modifier and yields a non-canonical
// sequence some terminals split into two glyphs.
func unicodeEmojiGlyph(name string) string {
	cm := emoji.CodeMap()
	if g := cm[":"+name+":"]; g != "" {
		return g
	}
	for _, st := range skinTones {
		base := strings.TrimSuffix(name, "_"+st.suffix)
		if base == name {
			continue
		}
		if g := cm[":"+base+":"]; g != "" {
			return strings.TrimSuffix(g, vs16) + string(st.mod)
		}
	}
	return ""
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

// emojiNames is the sorted list of shortcodes (sans colons) built once from
// the kyokomi codemap. Sorting up front keeps prefix/substring matches
// deterministic without re-sorting per keystroke.
var (
	emojiOnce  sync.Once
	emojiNames []string
)

func emojiIndex() []string {
	emojiOnce.Do(func() {
		cm := emoji.CodeMap()
		emojiNames = make([]string, 0, len(cm))
		for code := range cm {
			emojiNames = append(emojiNames, strings.Trim(code, ":"))
		}
		sort.Strings(emojiNames)
	})
	return emojiNames
}

// updateEmoji recomputes picker state after the textarea has processed a
// key. Unlike mentions it never needs a fetch, so it returns nothing — the
// caller just discards the result.
func (m *Model) updateEmoji() {
	row := m.input.Line()
	li := m.input.LineInfo()
	col := li.StartColumn + li.ColumnOffset

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

	// Require at least one character after the ':' — the picker only opens
	// once you've typed ":" *and a letter*, never on a bare colon.
	query := strings.ToLower(string(runes[at+1 : col]))
	if query == "" {
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

// closeEmoji clears the picker.
func (m *Model) closeEmoji() {
	if !m.emoji.active {
		return
	}
	m.emoji = emojiState{}
}

// emojiMatches returns up to emojiLimit candidates, prefix matches first
// (":smi" → smile before kissing_smiling_eyes), then substring matches, so
// the most obvious completion sits at the top while fuzzier hits stay
// reachable. Custom (server) emoji are merged ahead of the kyokomi index
// within each tier so they stay discoverable against the much larger unicode
// set; glyphs are resolved at render time, not here.
func (m Model) emojiMatches(query string) []emojiItem {
	var prefix, infix []emojiItem
	seen := map[string]struct{}{}
	add := func(dst *[]emojiItem, name string) {
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		*dst = append(*dst, emojiItem{code: ":" + name + ":", name: name})
	}
	for _, name := range m.customEmojiNames {
		switch {
		case strings.HasPrefix(name, query):
			add(&prefix, name)
		case strings.Contains(name, query):
			add(&infix, name)
		}
	}
	for _, name := range emojiIndex() {
		if len(prefix) >= emojiLimit {
			break
		}
		switch {
		case strings.HasPrefix(name, query):
			add(&prefix, name)
		case strings.Contains(name, query):
			add(&infix, name)
		}
	}
	out := prefix
	for _, it := range infix {
		if len(out) >= emojiLimit {
			break
		}
		out = append(out, it)
	}
	if len(out) > emojiLimit {
		out = out[:emojiLimit]
	}
	return out
}

// acceptEmoji replaces ":<query>" with the selected shortcode + space at the
// captured position. Returns false when there's nothing usable so the caller
// falls through to the default key handler.
func (m *Model) acceptEmoji() bool {
	if !m.emoji.active || m.emoji.idx < 0 || m.emoji.idx >= len(m.emoji.items) {
		return false
	}
	it := m.emoji.items[m.emoji.idx]
	if it.code == "" {
		return false
	}
	lines := strings.Split(m.input.Value(), "\n")
	if m.emoji.line < 0 || m.emoji.line >= len(lines) {
		return false
	}
	runes := []rune(lines[m.emoji.line])
	li := m.input.LineInfo()
	col := li.StartColumn + li.ColumnOffset
	if col > len(runes) {
		col = len(runes)
	}
	if m.emoji.start > col {
		return false
	}
	replaced := string(runes[:m.emoji.start]) + it.code + " " + string(runes[col:])
	lines[m.emoji.line] = replaced
	m.history.checkpoint(m.composerContextKey(), m.input.Value())
	m.input.SetValue(strings.Join(lines, "\n"))
	m.syncInputHeight()
	m.closeEmoji()
	return true
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
