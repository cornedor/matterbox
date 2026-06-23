package ui

import (
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// mentionLimit caps both the local-cache prefilter and the API fetch so
// the popup stays a few rows tall regardless of how many users match.
const mentionLimit = 8

// mentionDebounce delays the autocomplete API call after a query change
// to coalesce rapid keystrokes (typing "anders" → one fetch, not six).
const mentionDebounce = 150 * time.Millisecond

// mentionState tracks an in-progress @-mention. `start` is the rune
// offset of the '@' in the logical line `line`; `query` is everything
// between '@' and the cursor (lower-cased for matching). `fetchSeq` is
// incremented on every query change so a late-arriving response from a
// previous query can be discarded.
type mentionState struct {
	active   bool
	line     int
	start    int
	query    string
	items    []*model.User
	idx      int
	fetchSeq int
}

// updateMention recomputes mention state after the textarea has
// processed a key. Returns a Tick cmd that fires the debounced fetch
// when the query changed, or nil otherwise.
func (m *Model) updateMention() tea.Cmd {
	row := m.input.Line()
	li := m.input.LineInfo()
	col := li.StartColumn + li.ColumnOffset

	lines := strings.Split(m.input.Value(), "\n")
	if row < 0 || row >= len(lines) {
		m.closeMention()
		return nil
	}
	runes := []rune(lines[row])
	if col > len(runes) {
		col = len(runes)
	}

	at := -1
	for i := col - 1; i >= 0; i-- {
		r := runes[i]
		if r == '@' {
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
		m.closeMention()
		return nil
	}

	query := strings.ToLower(string(runes[at+1 : col]))
	if m.mention.active && m.mention.line == row && m.mention.start == at && m.mention.query == query {
		return nil
	}
	m.mention.active = true
	m.mention.line = row
	m.mention.start = at
	m.mention.query = query
	m.mention.items = m.localMentionMatches(query)
	m.mention.idx = 0
	m.mention.fetchSeq++
	seq := m.mention.fetchSeq
	return tea.Tick(mentionDebounce, func(_ time.Time) tea.Msg {
		return mentionDebounceMsg{seq: seq}
	})
}

// closeMention clears the popup but keeps the fetchSeq counter so any
// in-flight fetch from a previous query is still ignored on arrival.
func (m *Model) closeMention() {
	if !m.mention.active {
		return
	}
	m.mention = mentionState{fetchSeq: m.mention.fetchSeq}
}

// localMentionMatches prefilters from already-resolved usernames so the
// popup shows something instantly while the debounced API fetch flies.
// Matching is fuzzy (so "@andrs" still finds "anders") and candidates are
// ranked by match quality first, then popularity — usernames you mention
// most float to the top within a tier — mirroring the switcher's ordering.
func (m *Model) localMentionMatches(query string) []*model.User {
	if len(m.userNames) == 0 {
		return nil
	}
	type cand struct {
		id, name string
		band     int
		score    int
		usage    int // mentionUsage[name], read once so the sort below doesn't re-hash
	}
	// less reports whether a should rank before b: match quality, then
	// popularity, then finer match position, then username as a stable last
	// resort. usage is precomputed so a comparison is pure field reads.
	less := func(a, b cand) bool {
		if a.band != b.band {
			return a.band < b.band // exact > prefix > substring > subsequence
		}
		if a.usage != b.usage {
			return a.usage > b.usage // more-often-mentioned first
		}
		if a.score != b.score {
			return a.score < b.score
		}
		return a.name < b.name
	}
	// The popup never shows more than mentionLimit rows, so keep only that many
	// best candidates in a small sorted buffer instead of materialising and
	// sorting the whole directory. This is O(n·mentionLimit) with no per-user
	// *model.User allocation — the directory can hold thousands of names.
	top := make([]cand, 0, mentionLimit)
	for id, name := range m.userNames {
		if name == "" {
			continue
		}
		band, score, ok := fuzzyScore(strings.ToLower(name), query)
		if !ok {
			continue
		}
		c := cand{id: id, name: name, band: band, score: score, usage: m.mentionUsage[name]}
		if len(top) == mentionLimit && !less(c, top[len(top)-1]) {
			continue // no better than the worst keeper
		}
		pos := sort.Search(len(top), func(i int) bool { return less(c, top[i]) })
		if len(top) < mentionLimit {
			top = append(top, cand{})
			copy(top[pos+1:], top[pos:])
		} else {
			copy(top[pos+1:], top[pos:len(top)-1]) // drop the old worst
		}
		top[pos] = c
	}
	out := make([]*model.User, 0, len(top))
	for _, c := range top {
		out = append(out, &model.User{Id: c.id, Username: c.name})
	}
	return out
}

// fetchMentions hits the channel-scoped autocomplete endpoint. teamID
// may be empty for DMs / group-DMs; the API tolerates it.
func (m Model) fetchMentions(teamID, channelID, query string, seq int) tea.Cmd {
	return func() tea.Msg {
		us, err := m.client.Autocomplete(m.ctx, teamID, channelID, query, mentionLimit)
		if err != nil {
			return mentionUsersMsg{seq: seq, err: err}
		}
		return mentionUsersMsg{seq: seq, users: us}
	}
}

// acceptMention replaces "@<query>" with "@<username> " at the captured
// position and closes the popup. Returns (cmd, true) on success — cmd
// persists the updated popularity counter — or (nil, false) when there's
// nothing usable to accept (caller falls through to the default handler).
func (m *Model) acceptMention() (tea.Cmd, bool) {
	if !m.mention.active || m.mention.idx < 0 || m.mention.idx >= len(m.mention.items) {
		return nil, false
	}
	u := m.mention.items[m.mention.idx]
	if u == nil || u.Username == "" {
		return nil, false
	}
	lines := strings.Split(m.input.Value(), "\n")
	if m.mention.line < 0 || m.mention.line >= len(lines) {
		return nil, false
	}
	runes := []rune(lines[m.mention.line])
	li := m.input.LineInfo()
	col := li.StartColumn + li.ColumnOffset
	if col > len(runes) {
		col = len(runes)
	}
	if m.mention.start > col {
		return nil, false
	}
	replaced := string(runes[:m.mention.start]) + "@" + u.Username + " " + string(runes[col:])
	lines[m.mention.line] = replaced
	// SetValue resets + reinserts, leaving the cursor at the end of the
	// value. Fine for the common single-line case; for a rare multi-line
	// edit the cursor still lands in a sensible spot to keep typing.
	m.history.checkpoint(m.composerContextKey(), m.input.Value())
	m.input.SetValue(strings.Join(lines, "\n"))
	m.syncInputHeight()
	m.userNames[u.Id] = u.Username
	bump := m.bumpMentionStat(u.Username)
	m.closeMention()
	return bump, true
}

// mentionPopupStyle is the dropdown frame; matches the focused-border
// vocab already used for the channels/messages panes.
var mentionPopupStyle = lipgloss.NewStyle().
	Border(border).
	BorderForeground(focusedColor).
	Padding(0, 1)

// renderMentionPopup returns the dropdown or "" if it shouldn't show.
func (m *Model) renderMentionPopup() string {
	if !m.mention.active || len(m.mention.items) == 0 {
		return ""
	}
	dim := lipgloss.NewStyle().Foreground(dimColor)
	rows := make([]string, 0, len(m.mention.items))
	for i, u := range m.mention.items {
		label := "@" + u.Username
		if u.Nickname != "" {
			label += "  " + dim.Render(u.Nickname)
		}
		if i == m.mention.idx {
			label = selectedRow.Render(label)
		}
		rows = append(rows, label)
	}
	return mentionPopupStyle.Render(strings.Join(rows, "\n"))
}
