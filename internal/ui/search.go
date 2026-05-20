package ui

import (
	"image/color"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/store"
)

// searchDebounce is how long we wait after the last keystroke before
// issuing the FTS query. Keeps typing snappy without flooding SQLite
// for transient queries like "h" → "he" → "hel".
const searchDebounce = 120 * time.Millisecond

// searchPageSize is how many hits we ask for on the first batch and on
// each "load more" expansion. Bigger pages cost more SQLite work for
// each render; smaller pages mean more "load more" trips for the user.
const searchPageSize = 30

// modifierRe matches one team:<value> or in:<value> token. Value can be
// a bareword (no whitespace) or a double-quoted string for names with
// spaces. Case-insensitive on the key so TEAM:foo also works.
var modifierRe = regexp.MustCompile(`(?i)\b(team|in):("[^"]*"|\S+)`)

// parsedQuery splits a raw search input into its FTS text and optional
// team:/in: modifiers. Empty modifiers mean "no filter".
type parsedQuery struct {
	text string
	team string
	in   string
}

// parseSearchQuery extracts team:/in: modifiers from anywhere in the
// input and returns the remaining text along with the modifier values.
// Quoted values keep their interior verbatim (without the quotes); the
// last occurrence of each modifier wins.
func parseSearchQuery(s string) parsedQuery {
	var p parsedQuery
	cleaned := modifierRe.ReplaceAllStringFunc(s, func(match string) string {
		parts := strings.SplitN(match, ":", 2)
		key := strings.ToLower(parts[0])
		val := strings.Trim(parts[1], `"`)
		switch key {
		case "team":
			p.team = val
		case "in":
			p.in = val
		}
		return ""
	})
	p.text = strings.TrimSpace(strings.Join(strings.Fields(cleaned), " "))
	return p
}

// searchContextLines is how many posts before / after the match we
// include in each bubble. Two on each side matches the spec.
const searchContextLines = 2

// searchState owns the live-search UI on the synthetic Search tab.
type searchState struct {
	input    textinput.Model
	view     viewport.Model
	query    string // last query we actually ran (used to drop stale results)
	pending  string // most recently typed value (debounced)
	seq      int    // bumps on every keystroke; used to drop stale debounce/result msgs
	hits     []store.SearchHit
	idx      int    // selected hit (or len(hits) when the load-more row is selected)
	limit    int    // current store cap — grows by searchPageSize on each "load more"
	loading  bool   // a search is currently in flight (load-more spinner-less indicator)
	err      string // error from the last search (cleared when a new search runs)
	notReady bool   // store unavailable — show a friendly message instead of searching
}

// newSearchState constructs the textinput / viewport used by the Search
// tab. Called once at startup from New().
func newSearchState(storeAvailable bool) searchState {
	ti := textinput.New()
	ti.Prompt = "🔎 "
	ti.Placeholder = "search… (team:<name> in:<channel> optional)"
	ti.CharLimit = 256

	vp := viewport.New()
	vp.SoftWrap = true

	return searchState{
		input:    ti,
		view:     vp,
		limit:    searchPageSize,
		notReady: !storeAvailable,
	}
}

// hasMore reports whether the last search returned a full page, i.e.
// there might be more hits the store didn't include. Used to decide
// when to render the "load more" pseudo-row at the bottom of the list.
func (s searchState) hasMore() bool {
	return s.limit > 0 && len(s.hits) >= s.limit
}

// loadMoreIdx returns the synthetic idx value that selects the load-more
// row. Equals len(hits) when hasMore() is true; otherwise -1 (no row).
func (s searchState) loadMoreIdx() int {
	if !s.hasMore() {
		return -1
	}
	return len(s.hits)
}

// onLoadMoreRow reports whether the load-more pseudo-row is currently
// selected (so Enter should expand the page rather than open a channel).
func (s searchState) onLoadMoreRow() bool {
	return s.hasMore() && s.idx == len(s.hits)
}

// openSearchTab switches to the synthetic Search tab and focuses the
// input. Idempotent — calling it while already on Search just re-focuses
// the input so F is a reliable "give me the search bar" key.
func (m *Model) openSearchTab() tea.Cmd {
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if kind, _, _ := m.tabAt(i); kind == tabSearch {
			m.teamIdx = i
			break
		}
	}
	// Close anything else that might be modal-like.
	m.filterMode = false
	m.filter.SetValue("")
	m.filter.Blur()
	m.input.Blur()
	m.focus = focusSearch
	m.search.input.Focus()
	return nil
}

// openSearchHere opens the Search tab like openSearchTab, but prefills
// the input with the current channel's scope: "team:<team> in:<channel>"
// for a regular channel, or just "in:<partner>" for a DM / group-DM
// (which have no team). A trailing space leaves the cursor ready for the
// search term. Falls back to an empty box when there's no resolvable
// current channel (e.g. on the Feed tab).
func (m *Model) openSearchHere() tea.Cmd {
	// Compute the scope before openSearchTab moves teamIdx onto the
	// synthetic Search tab, which would change visibleChannels().
	prefix := m.searchScopePrefix()
	cmd := m.openSearchTab()
	if prefix != "" {
		m.search.input.SetValue(prefix)
		m.search.input.CursorEnd()
	}
	return cmd
}

// searchScopePrefix builds the "team:… in:…" prefix for the currently
// selected channel, suitable for prefilling the search box. Returns ""
// when there's no resolvable current channel.
func (m Model) searchScopePrefix() string {
	vis := m.visibleChannels()
	if m.channelIdx < 0 || m.channelIdx >= len(vis) {
		return ""
	}
	ch := vis[m.channelIdx]
	in := m.searchInValue(ch)
	if in == "" {
		return ""
	}
	switch ch.Type {
	case model.ChannelTypeDirect, model.ChannelTypeGroup:
		return "in:" + quoteModifier(in) + " "
	}
	team := m.teamName(ch.TeamId)
	if team == "" {
		return "in:" + quoteModifier(in) + " "
	}
	return "team:" + quoteModifier(team) + " in:" + quoteModifier(in) + " "
}

// searchInValue returns the value an in: modifier needs to scope search to
// channel c, matching the resolution rules in channelMatchesIn: the
// partner username for a DM, the display name for a group-DM, and the
// channel slug (Name) otherwise. Returns "" for an unresolvable DM partner.
func (m Model) searchInValue(c *model.Channel) string {
	switch c.Type {
	case model.ChannelTypeDirect:
		for _, id := range strings.Split(c.Name, "__") {
			if id == "" || (m.me != nil && id == m.me.Id) {
				continue
			}
			if n, ok := m.userNames[id]; ok && n != "" {
				return n
			}
		}
		return ""
	case model.ChannelTypeGroup:
		return c.DisplayName
	default:
		return c.Name
	}
}

// quoteModifier double-quotes a team:/in: value when it contains
// whitespace so parseSearchQuery keeps it as a single token (its grammar
// is `"[^"]*"|\S+`). Barewords (slugs, usernames) pass through unchanged.
func quoteModifier(v string) string {
	if strings.ContainsAny(v, " \t") {
		return `"` + v + `"`
	}
	return v
}

// onSearchTab reports whether the synthetic Search tab is currently
// selected.
func (m *Model) onSearchTab() bool {
	kind, _, _ := m.tabAt(m.teamIdx)
	return kind == tabSearch
}

// scheduleSearch bumps the seq, resets pagination to a single page, and
// emits a debounce tick. The actual store.Search call only runs if the
// seq still matches when the tick fires — typing fast enough chains the
// ticks together without spawning queries.
func (m *Model) scheduleSearch() tea.Cmd {
	m.search.seq++
	m.search.pending = m.search.input.Value()
	// New query = paginate from the first page again. "Load more" goes
	// through a separate path that does not reset the limit.
	m.search.limit = searchPageSize
	seq := m.search.seq
	return tea.Tick(searchDebounce, func(_ time.Time) tea.Msg {
		return searchDebounceMsg{seq: seq}
	})
}

// runSearch issues the store.Search on a worker goroutine. Empty
// queries clear results immediately without touching the store. The
// team:/in: modifiers are parsed and resolved to a channel-id scope
// here so the worker goroutine doesn't need to touch the UI state.
func (m Model) runSearch(seq int, query string, limit int) tea.Cmd {
	st := m.store
	if st == nil {
		return func() tea.Msg {
			return searchResultsMsg{seq: seq, query: query}
		}
	}
	parsed := parseSearchQuery(query)
	if parsed.text == "" {
		return func() tea.Msg {
			return searchResultsMsg{seq: seq, query: query}
		}
	}
	var scope []string
	if parsed.team != "" || parsed.in != "" {
		scope = m.resolveSearchScope(parsed.team, parsed.in)
	}
	if limit <= 0 {
		limit = searchPageSize
	}
	return func() tea.Msg {
		hits, err := st.Search(parsed.text, scope, limit, searchContextLines)
		return searchResultsMsg{seq: seq, query: query, hits: hits, err: errString(err)}
	}
}

// resolveSearchScope turns optional team:/in: modifier values into a
// channel-id slice suitable for store.Search. Returns nil if both
// modifiers are empty (no scope restriction); returns an empty
// non-nil slice when the modifiers are set but resolve to zero
// channels — store.Search treats that as "return no hits", so we
// short-circuit FTS instead of running it across the whole corpus.
//
// Matching rules:
//   - team:X matches teams whose Name or DisplayName equals X (case-
//     insensitive). DMs and group-DMs have no team and are excluded
//     when team: is set.
//   - in:X matches public/private channels whose Name or DisplayName
//     equals X, group-DMs whose DisplayName equals X, and direct
//     messages whose resolved partner username equals X. Useful for
//     querying multiple channels of the same name (e.g. an
//     "off-topic" room that lives in several teams).
func (m Model) resolveSearchScope(teamFilter, inFilter string) []string {
	if teamFilter == "" && inFilter == "" {
		return nil
	}

	var allowedTeamIDs map[string]struct{}
	if teamFilter != "" {
		allowedTeamIDs = map[string]struct{}{}
		for _, t := range m.teams {
			if strings.EqualFold(t.DisplayName, teamFilter) || strings.EqualFold(t.Name, teamFilter) {
				allowedTeamIDs[t.Id] = struct{}{}
			}
		}
		if len(allowedTeamIDs) == 0 {
			return []string{} // explicit empty: filter active, no match
		}
	}

	ids := make([]string, 0, 8)
	seen := map[string]struct{}{}
	for _, list := range m.channels {
		for _, c := range list {
			if _, dup := seen[c.Id]; dup {
				continue
			}
			if allowedTeamIDs != nil {
				if _, ok := allowedTeamIDs[c.TeamId]; !ok {
					continue
				}
			}
			if inFilter != "" && !m.channelMatchesIn(c, inFilter) {
				continue
			}
			seen[c.Id] = struct{}{}
			ids = append(ids, c.Id)
		}
	}
	if len(ids) == 0 {
		return []string{}
	}
	return ids
}

// channelMatchesIn reports whether the channel's name (public/private),
// display name (group-DM), or DM partner username matches `in`
// (case-insensitive). The match is exact, not substring — `in:off`
// won't accidentally pick up #offtopic-anything.
func (m Model) channelMatchesIn(c *model.Channel, in string) bool {
	switch c.Type {
	case model.ChannelTypeDirect:
		for _, id := range strings.Split(c.Name, "__") {
			if id == "" {
				continue
			}
			if m.me != nil && id == m.me.Id {
				continue
			}
			if n, ok := m.userNames[id]; ok && strings.EqualFold(n, in) {
				return true
			}
		}
		return false
	case model.ChannelTypeGroup:
		return strings.EqualFold(c.DisplayName, in)
	default:
		return strings.EqualFold(c.Name, in) || strings.EqualFold(c.DisplayName, in)
	}
}

// handleSearchKey owns every keystroke while focus == focusSearch.
// Up/down navigate the hit list; enter opens the selected hit; esc
// clears the query (or returns to teams if already empty); typing
// updates the input and triggers a debounced search.
func (m Model) handleSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.search.input.Value() != "" {
			m.search.input.SetValue("")
			m.search.hits = nil
			m.search.query = ""
			m.search.idx = 0
			m.search.err = ""
			m.renderSearchResults()
			return m, nil
		}
		m.search.input.Blur()
		m.focus = focusTeams
		return m, nil
	case "up", "ctrl+p":
		if m.search.idx > 0 {
			m.search.idx--
			m.renderSearchResults()
		}
		return m, nil
	case "down", "ctrl+n":
		maxIdx := len(m.search.hits) - 1
		if m.search.hasMore() {
			maxIdx = len(m.search.hits) // load-more pseudo-row
		}
		if m.search.idx < maxIdx {
			m.search.idx++
			m.renderSearchResults()
		}
		return m, nil
	case "enter":
		if m.search.onLoadMoreRow() {
			return m.expandSearch()
		}
		if m.search.idx < 0 || m.search.idx >= len(m.search.hits) {
			return m, nil
		}
		return m.openHitChannel(m.search.hits[m.search.idx])
	case "pgup":
		m.search.view.ScrollUp(m.search.view.Height() / 2)
		return m, nil
	case "pgdown":
		m.search.view.ScrollDown(m.search.view.Height() / 2)
		return m, nil
	case "tab":
		return m.cycleFocus(1)
	case "shift+tab":
		return m.cycleFocus(-1)
	case "ctrl+v":
		// Pull text from the system clipboard. The result lands as a
		// clipboardReadMsg, which the global handler routes back through
		// handlePaste — that's where the textinput is updated.
		return m, readClipboard()
	}

	var cmd tea.Cmd
	old := m.search.input.Value()
	m.search.input, cmd = m.search.input.Update(msg)
	if m.search.input.Value() != old {
		debounceCmd := m.scheduleSearch()
		return m, tea.Batch(cmd, debounceCmd)
	}
	return m, cmd
}

// applySearchDebounce runs once a typing-debounce tick fires. If the
// seq still matches the current state, we kick off the real store query.
// An input that only contains modifiers (e.g. `team:foo` with no text)
// is treated as empty — the FTS engine needs at least one term.
func (m Model) applySearchDebounce(msg searchDebounceMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.search.seq {
		return m, nil
	}
	q := m.search.pending
	if parseSearchQuery(q).text == "" {
		m.search.hits = nil
		m.search.query = ""
		m.search.idx = 0
		m.search.err = ""
		m.search.loading = false
		m.renderSearchResults()
		return m, nil
	}
	m.search.loading = true
	m.renderSearchResults()
	return m, m.runSearch(msg.seq, q, m.search.limit)
}

// expandSearch grows the result cap by another page and re-issues the
// store query. Selection stays on the current row (the load-more row),
// which becomes the first new hit once results arrive — so pressing
// enter twice naturally navigates into the freshly-loaded content.
func (m Model) expandSearch() (tea.Model, tea.Cmd) {
	if m.search.loading {
		return m, nil
	}
	q := m.search.query
	if parseSearchQuery(q).text == "" {
		return m, nil
	}
	m.search.seq++ // invalidate any still-pending debounce ticks
	m.search.limit += searchPageSize
	m.search.loading = true
	m.renderSearchResults()
	return m, m.runSearch(m.search.seq, q, m.search.limit)
}

// applySearchResults installs hits from a completed store.Search if the
// response is still fresh (matches the latest seq). Out-of-order results
// are dropped silently so the UI never flickers backwards through stale
// data. Selection is preserved across same-query reloads (load-more);
// reset to the first hit when the query itself changed.
func (m Model) applySearchResults(msg searchResultsMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.search.seq {
		return m, nil
	}
	sameQuery := msg.query != "" && msg.query == m.search.query
	m.search.query = msg.query
	m.search.hits = msg.hits
	m.search.err = msg.err
	m.search.loading = false
	if !sameQuery {
		m.search.idx = 0
		m.search.view.GotoTop()
	} else if m.search.idx > len(msg.hits) {
		// Same query but the selection now points past the end (e.g.
		// load-more returned fewer rows than expected). Clamp to last.
		m.search.idx = len(msg.hits) - 1
		if m.search.idx < 0 {
			m.search.idx = 0
		}
	}
	m.renderSearchResults()
	return m, nil
}

// gotoSearchHit moves the search-result cursor by step and reopens the
// hit at the new index, letting n/N cycle matches from the messages pane
// (vim-style "next match"). No-op with a hint when there are no results.
func (m Model) gotoSearchHit(step int) (tea.Model, tea.Cmd) {
	n := len(m.search.hits)
	if n == 0 {
		m.status = "no active search"
		return m, nil
	}
	idx := m.search.idx + step
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	m.search.idx = idx
	return m.openHitChannel(m.search.hits[idx])
}

// openHitChannel switches to the hit's channel and centers the messages
// pane on the matched post. The pivot window is pulled from the local
// cache (PostsAround); if the cache can't satisfy that, we fall back to
// the standard channel-open path with a queued jump-id that the loader
// applies once posts arrive.
func (m Model) openHitChannel(hit store.SearchHit) (tea.Model, tea.Cmd) {
	if hit.Match == nil {
		return m, nil
	}
	ch := m.findChannel(hit.Match.ChannelId)
	if ch == nil {
		m.status = "channel not in the local list"
		return m, nil
	}
	// Hop to the channel's home team so isCurrentChannel keeps tracking
	// the open channel. Clear any sidebar filter so the target is visible.
	m.switchToChannelHomeTeam(ch)
	m.filterValue = ""
	m.filter.SetValue("")
	m.focus = focusMessages
	m.search.input.Blur()
	saveCmd := m.bumpChannelStat(ch.Id)

	// Preferred path: pull a window of posts around the match straight
	// from the cache so the user lands on the message with context above
	// and below — even when the match is older than what RecentForChannel
	// would return.
	if m.store != nil {
		around, err := m.store.PostsAround(ch.Id, hit.Match.Id, 30, 30)
		if err == nil && len(around) > 0 {
			m.posts = around
			m.postIdx = len(around) - 1
			for i, p := range around {
				if p.Id == hit.Match.Id {
					m.postIdx = i
					break
				}
			}
			m.pendingJumpPostID = ""
			m.status = ""
			m.loading = false
			delete(m.unread, ch.Id)
			delete(m.mentions, ch.Id)
			m.renderMessages()
			// Gap-fill from the newest post we currently have cached so
			// the user can scroll forward to live without an extra step.
			gapID, _ := m.store.LatestPostID(ch.Id)
			var gapCmd tea.Cmd
			if gapID != "" {
				gapCmd = m.fetchPostsAfter(ch.Id, gapID)
			}
			return m, tea.Batch(saveCmd, gapCmd)
		}
	}

	// Fallback: take the standard open path and let jumpToPendingPost
	// position the selection if the loaded page happens to include the
	// matched id.
	m.pendingJumpPostID = hit.Match.Id
	loadCmd := m.openChannelLoadCmd(ch.Id)
	return m, tea.Batch(loadCmd, saveCmd)
}

// findChannel returns the *model.Channel with the given id, looking
// across every bucket. Returns nil if the user isn't a member of it
// (which can happen if the cache contains posts from a channel the user
// has since left).
func (m Model) findChannel(channelID string) *model.Channel {
	for _, list := range m.channels {
		for _, c := range list {
			if c.Id == channelID {
				return c
			}
		}
	}
	return nil
}

// sizeSearchView keeps the search viewport in sync with the body area.
// width is the pane's outer width (border included); height is the
// pane's inner content height (border already subtracted by the caller).
func (m *Model) sizeSearchView(width, height int) {
	innerW := width - 2 // strip the pane's left/right border
	if innerW < 10 {
		innerW = 10
	}
	m.search.input.SetWidth(innerW - 4)
	// Header rows inside the pane:
	//   1 — titleRow
	//   2 — inputBox (top border + the textinput line)
	//   1 — rule
	const headerRows = 4
	bodyH := height - headerRows
	if bodyH < 1 {
		bodyH = 1
	}
	m.search.view.SetWidth(innerW)
	m.search.view.SetHeight(bodyH)
}

// renderSearchResults populates the viewport with rendered bubbles, one
// per hit. Selection is drawn by giving the focused bubble the focused
// border color. The selected bubble is scrolled into view.
func (m *Model) renderSearchResults() {
	if m.search.notReady {
		m.search.view.SetContent(
			lipgloss.NewStyle().Foreground(dimColor).Render(
				"  local message cache is unavailable — search needs the SQLite store",
			),
		)
		return
	}
	if m.search.err != "" {
		m.search.view.SetContent(
			lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("  search error: " + m.search.err),
		)
		return
	}
	if len(m.search.hits) == 0 {
		var hint string
		raw := m.search.input.Value()
		parsed := parseSearchQuery(raw)
		switch {
		case strings.TrimSpace(raw) == "":
			hint = "  type to search · narrow with team:<name> or in:<channel>"
		case parsed.text == "":
			hint = "  add a search term — modifiers alone don't match anything"
		case m.search.query == "":
			hint = "  …"
		default:
			hint = "  no matches for " + lipgloss.NewStyle().Italic(true).Render(parsed.text)
			if parsed.team != "" || parsed.in != "" {
				var scope []string
				if parsed.team != "" {
					scope = append(scope, "team="+parsed.team)
				}
				if parsed.in != "" {
					scope = append(scope, "in="+parsed.in)
				}
				hint += "  (" + strings.Join(scope, " · ") + ")"
			}
		}
		m.search.view.SetContent(lipgloss.NewStyle().Foreground(dimColor).Render(hint))
		return
	}

	// Clamp idx to the valid range, including the synthetic load-more row.
	maxIdx := len(m.search.hits) - 1
	if m.search.hasMore() {
		maxIdx = len(m.search.hits)
	}
	if m.search.idx > maxIdx {
		m.search.idx = maxIdx
	}
	if m.search.idx < 0 {
		m.search.idx = 0
	}

	innerW := m.search.view.Width()
	if innerW < 10 {
		innerW = 10
	}
	var allLines []string
	selStart, selEnd := -1, -1
	for i, hit := range m.search.hits {
		bubble := m.renderSearchBubble(innerW-2, hit, i == m.search.idx)
		bubbleLines := strings.Split(bubble, "\n")
		if i == m.search.idx {
			selStart = len(allLines)
			selEnd = selStart + len(bubbleLines)
		}
		allLines = append(allLines, bubbleLines...)
		allLines = append(allLines, "") // blank separator between bubbles
	}
	if m.search.hasMore() {
		row := m.renderLoadMoreRow(innerW-2, m.search.idx == len(m.search.hits))
		if m.search.idx == len(m.search.hits) {
			selStart = len(allLines)
			selEnd = selStart + 1
		}
		allLines = append(allLines, row)
	}
	m.search.view.SetContent(strings.Join(allLines, "\n"))

	if h := m.search.view.Height(); h > 0 && selStart >= 0 {
		visStart := visualRowsBefore(allLines, selStart, m.search.view.Width())
		visEnd := visualRowsBefore(allLines, selEnd, m.search.view.Width())
		off := m.search.view.YOffset()
		switch {
		case visStart < off:
			off = visStart
		case visEnd > off+h:
			off = visEnd - h
		}
		if off < 0 {
			off = 0
		}
		m.search.view.SetYOffset(off)
	}
}

// renderLoadMoreRow draws the pseudo-row appended below the bubbles when
// the last page came back full. Selected gets the focused color; the
// label flips while a load-more reload is in flight so the user sees
// that pressing enter did something.
func (m Model) renderLoadMoreRow(width int, selected bool) string {
	label := "↓ load more (enter)"
	if m.search.loading {
		label = "↓ loading more…"
	}
	color := dimColor
	if selected {
		color = focusedColor
	}
	style := lipgloss.NewStyle().Foreground(color)
	if selected {
		style = style.Bold(true)
	}
	rendered := style.Render(label)
	w := lipgloss.Width(rendered)
	pad := width - w
	if pad < 0 {
		pad = 0
	}
	return rendered + strings.Repeat(" ", pad)
}

// bubbleBox draws a bordered box whose top border carries `header` and
// whose interior is `bodyLines`. inner is the box's interior width (the
// outer width minus the two border columns); each body line is expected
// to already be rendered to at most inner-2 columns (the interior minus
// the single-space left/right padding) and is padded out here. The
// border is drawn in borderColor, the header in titleStyle. Shared by
// the Search and Feed tabs so their bubbles stay visually identical.
func bubbleBox(inner int, header string, bodyLines []string, borderColor color.Color) string {
	if inner < 6 {
		inner = 6
	}
	bs := lipgloss.NewStyle().Foreground(borderColor)

	// Top border: "┌─ <header> ──...──┐". If the header is too long, truncate.
	const topPrefix = "┌─ "
	const topMidGap = " "
	prefixW := lipgloss.Width(topPrefix) + lipgloss.Width(topMidGap)
	maxHdr := inner - prefixW - 1 // -1 reserves space for at least one fill char
	if maxHdr < 1 {
		maxHdr = 1
	}
	hdr := header
	if lipgloss.Width(hdr) > maxHdr {
		hdr = truncate(hdr, maxHdr)
	}
	used := lipgloss.Width(topPrefix) + lipgloss.Width(hdr) + lipgloss.Width(topMidGap)
	fillN := inner - used + 1
	if fillN < 0 {
		fillN = 0
	}
	topRow := bs.Render(topPrefix) + titleStyle.Render(hdr) + bs.Render(topMidGap+strings.Repeat("─", fillN)+"┐")

	contentW := inner - 2
	if contentW < 1 {
		contentW = 1
	}
	rendered := make([]string, 0, len(bodyLines)+2)
	rendered = append(rendered, topRow)
	for _, ln := range bodyLines {
		w := lipgloss.Width(ln)
		pad := contentW - w
		if pad < 0 {
			pad = 0
		}
		rendered = append(rendered, bs.Render("│")+" "+ln+strings.Repeat(" ", pad)+" "+bs.Render("│"))
	}
	rendered = append(rendered, bs.Render("└"+strings.Repeat("─", inner)+"┘"))
	return strings.Join(rendered, "\n")
}

// renderSearchBubble draws one hit as a bordered box whose top border
// carries the channel breadcrumb + match timestamp. Body lines are the
// before-context (dim), the match (highlighted), and the after-context
// (dim).
func (m Model) renderSearchBubble(outerW int, hit store.SearchHit, selected bool) string {
	if hit.Match == nil {
		return ""
	}
	borderColor := dimColor
	if selected {
		borderColor = focusedColor
	}
	if outerW < 8 {
		outerW = 8
	}
	inner := outerW - 2

	chLabel := "?"
	if ch := m.findChannel(hit.Match.ChannelId); ch != nil {
		chLabel = m.channelBreadcrumb(ch)
	}
	ts := time.UnixMilli(hit.Match.CreateAt).Local().Format("2006-01-02 15:04")
	header := chLabel + " · " + ts

	// Body lines: context-before (muted), match (emphasised), context-after
	// (muted). Each is rendered to inner-2 columns (interior width minus the
	// single-space left/right padding).
	contentW := inner - 2
	if contentW < 1 {
		contentW = 1
	}
	var bodyLines []string
	for _, p := range hit.Before {
		bodyLines = append(bodyLines, m.renderHitLine(p, contentW, true, false))
	}
	bodyLines = append(bodyLines, m.renderHitLine(hit.Match, contentW, false, true))
	for _, p := range hit.After {
		bodyLines = append(bodyLines, m.renderHitLine(p, contentW, true, false))
	}
	return bubbleBox(inner, header, bodyLines, borderColor)
}

// renderHitLine formats one post as a single line "user · time  message"
// suitable for the bubble interior. muted dims everything; match bolds
// the message body. Long messages are truncated to width with an ellipsis.
func (m Model) renderHitLine(p *model.Post, width int, muted, match bool) string {
	name := m.userNames[p.UserId]
	if name == "" {
		name = p.UserId
		if len(name) > 8 {
			name = name[:8]
		}
	}
	ts := formatPostTime(p.CreateAt)
	// Compose: <name>  <hh:mm>  <message>
	var nameStyle, timeStyle2, msgStyle lipgloss.Style
	switch {
	case muted:
		nameStyle = lipgloss.NewStyle().Foreground(dimColor)
		timeStyle2 = lipgloss.NewStyle().Foreground(dimColor)
		msgStyle = lipgloss.NewStyle().Foreground(dimColor)
	case match:
		nameStyle = userStyle
		timeStyle2 = lipgloss.NewStyle().Foreground(dimColor)
		msgStyle = lipgloss.NewStyle().Bold(true)
	default:
		nameStyle = userStyle
		timeStyle2 = lipgloss.NewStyle().Foreground(dimColor)
		msgStyle = lipgloss.NewStyle()
	}
	prefix := nameStyle.Render(name) + "  " + timeStyle2.Render(ts) + "  "
	prefixW := lipgloss.Width(prefix)
	body := strings.ReplaceAll(p.Message, "\n", " ↵ ")
	body = strings.TrimSpace(body)
	if width-prefixW > 1 {
		body = truncate(body, width-prefixW)
	} else {
		body = ""
	}
	return prefix + msgStyle.Render(body)
}

// renderSearchPane composes the entire body of the Search tab: title,
// input row, separator, then the result viewport.
func (m Model) renderSearchPane(height, width int) string {
	innerH := height - 1 // border (bottom only; top connects to tab strip)
	if innerH < 1 {
		innerH = 1
	}
	if width < 10 {
		width = 10
	}

	dim := lipgloss.NewStyle().Foreground(dimColor)
	title := titleStyle.Render("Search")
	hits := ""
	if !m.search.notReady && len(m.search.hits) > 0 {
		count := plural(len(m.search.hits), "match", "matches")
		if m.search.hasMore() {
			// "+" hints that more hits exist beyond what's loaded; the
			// real total isn't known without paginating.
			count = itoa(len(m.search.hits)) + "+ matches"
		}
		hits = dim.Render("  " + count)
	}
	titleRow := title + hits

	inputBorder := dimColor
	if m.focus == focusSearch {
		inputBorder = focusedColor
	}
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(inputBorder).
		Width(width - 2).
		Render(m.search.input.View())

	rule := dim.Render(strings.Repeat("─", width-2))

	body := m.search.view.View()
	rows := []string{titleRow, inputBox, rule, body}

	style := lipgloss.NewStyle().
		Border(border).
		UnsetBorderTop().
		Width(width).
		Height(innerH)
	if m.focus == focusSearch {
		style = style.BorderForeground(focusedColor)
	} else {
		style = style.BorderForeground(dimColor)
	}
	return style.Render(strings.Join(rows, "\n"))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

// itoa is a tiny strconv.Itoa stand-in to avoid pulling strconv into this
// file just for the count formatter above.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// jumpToPendingPost positions m.postIdx on m.pendingJumpPostID (if set
// and present in m.posts), clears the pending field, and re-renders the
// messages viewport. No-op when the pending id is empty or not found.
func (m *Model) jumpToPendingPost() {
	if m.pendingJumpPostID == "" {
		return
	}
	for i, p := range m.posts {
		if p.Id == m.pendingJumpPostID {
			m.postIdx = i
			m.pendingJumpPostID = ""
			m.renderMessages()
			return
		}
	}
}
