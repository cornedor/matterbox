package ui

import (
	"image/color"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/embed"
	"matterbox/internal/semindex"
	"matterbox/internal/store"
	"matterbox/internal/viewport"
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

// aiSearchQuery reports whether a raw search input is an AI question (it ends
// in "?" and has real text before the "?"), returning the trimmed raw query
// to hand to the agent. A query that is only modifiers or punctuation isn't
// treated as an AI question.
func aiSearchQuery(raw string) (string, bool) {
	q := strings.TrimSpace(raw)
	if !strings.HasSuffix(q, "?") {
		return "", false
	}
	body := strings.TrimSpace(strings.TrimRight(q, "?"))
	if parseSearchQuery(body).text == "" {
		return "", false
	}
	return q, true
}

// isAIQuery is the boolean half of aiSearchQuery, for render-time hints.
func isAIQuery(raw string) bool {
	_, ok := aiSearchQuery(raw)
	return ok
}

// semanticQuery reports whether a raw search input opted into semantic (hybrid)
// search with a leading "~", returning the remainder (the "~" stripped). Unlike
// the AI "?" trigger this fires live through the normal debounce path — the only
// extra cost is one query-embedding call per debounced search.
func semanticQuery(raw string) (string, bool) {
	q := strings.TrimSpace(raw)
	if rest, ok := strings.CutPrefix(q, "~"); ok {
		return strings.TrimSpace(rest), true
	}
	return raw, false
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

	// zones maps viewport visual rows to selectable items (hit index, -1 for
	// the AI answer box, len(hits) for the load-more row) for mouse
	// hit-testing; zonesTotal is the rendered list's full height. Both are
	// rebuilt by setBubbleViewport and cleared by renderSearchResults' non-list
	// states. See mouse.go.
	zones      []bubbleZone
	zonesTotal int
}

// newSearchState constructs the textinput / viewport used by the Search
// tab. Called once at startup from New().
func newSearchState(storeAvailable bool) searchState {
	ti := textinput.New()
	ti.Prompt = "🔎 "
	ti.Placeholder = "search… (~ semantic · ? ask AI · team:/in: to narrow)"
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
	ch := m.findChannel(m.openChannelID)
	if ch == nil {
		return ""
	}
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
	// A leading "~" requests semantic (hybrid) search; strip it before parsing
	// modifiers so team:/in: still work, e.g. `~auth bug in:platform`.
	body, semantic := semanticQuery(query)
	parsed := parseSearchQuery(body)
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

	// Semantic path: embed the query, then fuse keyword + vector rankings. Needs
	// the embeddings client; without it (not configured) "~" silently falls back
	// to keyword search on the stripped text.
	if semantic && m.embedClient != nil {
		client := m.embedClient
		modelTag := semindex.ModelTag(m.embedModel, m.embedDim)
		text := parsed.text
		ctx := m.ctx
		return func() tea.Msg {
			qvec, err := client.EmbedOne(ctx, embed.QueryText(text))
			if err != nil {
				return searchResultsMsg{seq: seq, query: query,
					err: "semantic search unavailable — is the embeddings server up? (see scripts/llama-embeddings.sh)"}
			}
			hits, _, err := st.SearchHybrid(text, qvec, modelTag, store.HybridScope{ChannelIDs: scope}, limit, 0, searchContextLines)
			return searchResultsMsg{seq: seq, query: query, hits: hits, err: errString(err)}
		}
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
	// A finished AI run installs its own keymap: up/down walk the answer box and
	// the surfaced hits, and typing feeds the in-box follow-up field.
	if m.aiSearch.phase == aiSearchDone {
		return m.handleAIDoneKey(msg)
	}
	switch {
	case msg.String() == "ctrl+c": // hardwired quit
		return m, tea.Quit
	case msg.String() == "esc": // hardwired clear/cancel
		// First esc cancels/clears an AI run but keeps the typed question so
		// it can be tweaked; a second esc then clears the input as usual.
		if m.aiSearch.active() {
			m.cancelAISearch()
			m.search.hits = nil
			m.search.query = ""
			m.search.idx = 0
			// A follow-up run blurs the main input in favour of the in-box
			// follow-up field; restore it so typing works after cancelling.
			m.search.input.Focus()
			m.renderSearchResults()
			return m, nil
		}
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
	case key.Matches(msg, m.keys.InputUp):
		// input_up is ↑/ctrl+p; ctrl+p is shadowed by the global switcher, so
		// in practice ↑ moves the selection here. ctrl+n (input_down) still
		// moves down (it has no such global owner).
		if m.search.idx > 0 {
			m.search.idx--
			m.renderSearchResults()
		}
		return m, nil
	case key.Matches(msg, m.keys.InputDown):
		maxIdx := len(m.search.hits) - 1
		if m.search.hasMore() {
			maxIdx = len(m.search.hits) // load-more pseudo-row
		}
		if m.search.idx < maxIdx {
			m.search.idx++
			m.renderSearchResults()
		}
		return m, nil
	case msg.String() == "enter":
		// While an AI run is in flight, enter is a no-op (it would restart it).
		if m.aiSearch.phase == aiSearchRunning {
			return m, nil
		}
		// A question (trailing "?") hands off to the agentic search.
		if raw, ok := aiSearchQuery(m.search.input.Value()); ok {
			return m, m.startAISearch(raw)
		}
		if m.search.onLoadMoreRow() {
			return m.expandSearch()
		}
		if m.search.idx < 0 || m.search.idx >= len(m.search.hits) {
			return m, nil
		}
		return m.openHitChannel(m.search.hits[m.search.idx])
	case msg.String() == "pgup":
		// Kept literal: PageUp's ctrl+u alias is an emacs editing key the
		// search input needs (delete-to-start), so don't claim it here.
		m.search.view.ScrollUp(m.search.view.Height() / 2)
		return m, nil
	case msg.String() == "pgdown":
		m.search.view.ScrollDown(m.search.view.Height() / 2)
		return m, nil
	case key.Matches(msg, m.keys.Tab):
		return m.cycleFocus(1)
	case key.Matches(msg, m.keys.ShiftTab):
		return m.cycleFocus(-1)
	case key.Matches(msg, m.keys.Paste):
		// Pull text from the system clipboard. The result lands as a
		// clipboardReadMsg, which the global handler routes back through
		// handlePaste — that's where the textinput is updated.
		return m, readClipboard()
	}

	var cmd tea.Cmd
	old := m.search.input.Value()
	m.search.input, cmd = m.search.input.Update(msg)
	if m.search.input.Value() != old {
		// Editing the query drops back to plain search, tearing down any AI
		// run or result still on screen.
		if m.aiSearch.active() {
			m.cancelAISearch()
		}
		if isAIQuery(m.search.input.Value()) {
			// Question mode: don't run FTS over the half-typed question; wait
			// for enter. Clear any stale FTS hits and prompt the user.
			m.search.seq++ // invalidate any pending debounce tick
			m.search.hits = nil
			m.search.query = ""
			m.search.idx = 0
			m.search.err = ""
			m.search.loading = false
			m.renderSearchResults()
			return m, cmd
		}
		debounceCmd := m.scheduleSearch()
		return m, tea.Batch(cmd, debounceCmd)
	}
	return m, cmd
}

// handleAIDoneKey owns keystrokes once an AI run has finished. The cursor lives
// at idx -1 (the answer box, where the follow-up field is) or 0..n-1 (a hit
// bubble). up/down move between them — so you can always walk back up to the
// summary — enter either runs the follow-up or opens the selected hit, and any
// other keypress edits the follow-up field while the answer box is selected.
func (m Model) handleAIDoneKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "ctrl+c": // hardwired quit
		return m, tea.Quit
	case msg.String() == "esc": // hardwired tear-down
		// Tear the AI result down and return to plain search, keeping the typed
		// question so it can be tweaked and re-asked.
		m.cancelAISearch()
		m.search.hits = nil
		m.search.query = ""
		m.search.idx = 0
		m.search.input.Focus()
		m.renderSearchResults()
		return m, nil
	case key.Matches(msg, m.keys.InputUp):
		// input_up is ↑/ctrl+p; ctrl+p is shadowed by the global switcher.
		if m.search.idx > -1 {
			m.search.idx--
			var cmd tea.Cmd
			if m.search.idx == -1 && m.aiSearch.err == nil {
				cmd = m.aiSearch.followup.Focus() // back on the answer box
			}
			m.renderSearchResults()
			return m, cmd
		}
		return m, nil
	case key.Matches(msg, m.keys.InputDown):
		// An errored run renders the banner only (no hit bubbles), so there's
		// nothing below the answer box to move onto.
		if m.aiSearch.err == nil && m.search.idx < len(m.search.hits)-1 {
			if m.search.idx == -1 {
				m.aiSearch.followup.Blur() // leaving the answer box for a hit
			}
			m.search.idx++
			m.renderSearchResults()
		}
		return m, nil
	case msg.String() == "enter":
		if m.search.idx <= -1 {
			return m, m.startAIFollowup()
		}
		if m.search.idx < len(m.search.hits) {
			return m.openHitChannel(m.search.hits[m.search.idx])
		}
		return m, nil
	case msg.String() == "pgup":
		m.search.view.ScrollUp(m.search.view.Height() / 2)
		return m, nil
	case msg.String() == "pgdown":
		m.search.view.ScrollDown(m.search.view.Height() / 2)
		return m, nil
	case key.Matches(msg, m.keys.Tab):
		return m.cycleFocus(1)
	case key.Matches(msg, m.keys.ShiftTab):
		return m.cycleFocus(-1)
	case key.Matches(msg, m.keys.Paste):
		return m, readClipboard()
	}
	// Anything else edits the follow-up field, but only while the answer box is
	// selected and the run succeeded (a failed run has no follow-up input).
	if m.search.idx <= -1 && m.aiSearch.err == nil {
		var cmd tea.Cmd
		m.aiSearch.followup, cmd = m.aiSearch.followup.Update(msg)
		m.renderSearchResults()
		return m, cmd
	}
	return m, nil
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
			// Opening a channel here bypasses openChannelLoadCmd; route the switch
			// through enterChannel so the pane can't desync from openChannelID. The
			// repoint matters beyond routing — the gap-fill below (and any live
			// update) only merges into the pane when its channel matches
			// openChannelID, and the read dwell is armed off that same match — so
			// leaving it stale drops them silently.
			draftCmd := m.enterChannel(ch.Id)
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
			return m, tea.Batch(saveCmd, draftCmd, gapCmd)
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
	// Non-list states (not-ready / AI trace / error / hints) carry no clickable
	// bubbles; setBubbleViewport repopulates these whenever it runs.
	m.search.zones, m.search.zonesTotal = nil, 0
	if m.search.notReady {
		m.search.view.SetContent(
			lipgloss.NewStyle().Foreground(dimColor).Render(
				"  local message cache is unavailable — search needs the SQLite store",
			),
		)
		return
	}
	// An active AI run owns the viewport: the live trace while it works, then
	// the answer banner + the hits it surfaced.
	switch m.aiSearch.phase {
	case aiSearchRunning:
		m.search.view.SetContent(m.renderAIWorking())
		return
	case aiSearchDone:
		m.renderAIResults()
		return
	}
	if m.search.err != "" {
		m.search.view.SetContent(
			lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("  search error: " + m.search.err),
		)
		return
	}
	// A search with no results yet but one in flight is "searching", not "no
	// matches" — semantic search in particular embeds the query and scans the
	// vectors, which can take a beat (longer while the backfill shares the GPU).
	if m.search.loading && len(m.search.hits) == 0 {
		msg := "  searching…"
		if _, ok := semanticQuery(m.search.input.Value()); ok {
			msg = "  searching… (embedding query + scanning vectors)"
		}
		m.search.view.SetContent(lipgloss.NewStyle().Foreground(dimColor).Render(msg))
		return
	}
	if len(m.search.hits) == 0 {
		var hint string
		raw := m.search.input.Value()
		parsed := parseSearchQuery(raw)
		switch {
		case strings.TrimSpace(raw) == "":
			hint = "  type to search · end with ? to ask the AI · team:/in: to narrow"
		case isAIQuery(raw):
			hint = "  press enter to ask the AI ✨  (drop the ? for a plain search)"
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
	m.setBubbleViewport(nil, m.search.hits, m.search.idx, m.search.hasMore())
}

// setBubbleViewport renders optional header lines (verbatim) followed by one
// bubble per hit into the search viewport, scrolling the selected hit into
// view. When showLoadMore is set, the load-more pseudo-row is appended and
// selIdx == len(hits) selects it. Shared by plain FTS results and the AI
// answer view (which passes its banner as the header).
func (m *Model) setBubbleViewport(header []string, hits []store.SearchHit, selIdx int, showLoadMore bool) {
	innerW := m.search.view.Width()
	if innerW < 10 {
		innerW = 10
	}
	allLines := append([]string(nil), header...)
	selStart, selEnd := -1, -1
	// zones map viewport visual rows back to selectable items for mouse
	// hit-testing; acc tracks the running visual-row count as the list grows.
	zones := make([]bubbleZone, 0, len(hits)+2)
	vw := m.search.view.Width()
	acc := 0
	// A header block is the AI answer box, selectable as idx -1. A negative
	// selIdx means it's the selected item: scroll it into view so the summary /
	// follow-up box stays reachable.
	if len(header) > 0 {
		zones = append(zones, bubbleZone{row0: 0, idx: -1})
		acc = visualRowsBefore(header, len(header), vw)
		if selIdx < 0 {
			selStart, selEnd = 0, len(header)
		}
	}
	for i, hit := range hits {
		bubble := m.renderSearchBubble(innerW-2, hit, i == selIdx)
		bubbleLines := strings.Split(bubble, "\n")
		zones = append(zones, bubbleZone{row0: acc, idx: i})
		if i == selIdx {
			selStart = len(allLines)
			selEnd = selStart + len(bubbleLines)
		}
		allLines = append(allLines, bubbleLines...)
		acc += visualRowsBefore(bubbleLines, len(bubbleLines), vw) + 1 // +1 blank separator
		allLines = append(allLines, "")                                // blank separator between bubbles
	}
	if showLoadMore {
		row := m.renderLoadMoreRow(innerW-2, selIdx == len(hits))
		zones = append(zones, bubbleZone{row0: acc, idx: len(hits)})
		if selIdx == len(hits) {
			selStart = len(allLines)
			selEnd = selStart + 1
		}
		allLines = append(allLines, row)
		acc += visualRowsBefore([]string{row}, 1, vw)
	}
	m.search.zones, m.search.zonesTotal = zones, acc
	m.search.view.SetContentLines(allLines)

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
// border is drawn in borderColor, the header in titleStyle. When selected,
// the header is rendered with inverted colours for visual contrast.
// Shared by the Search and Feed tabs so their bubbles stay visually identical.
func bubbleBox(inner int, header string, bodyLines []string, borderColor color.Color, selected bool) string {
	if inner < 6 {
		inner = 6
	}
	bs := lipgloss.NewStyle().Foreground(borderColor)

	// Top border: "┌─ <header> ──...──┐". The space on each side of the header
	// is rendered inside hdrStyle so its background covers the padding too.
	const topPrefix = "┌─"
	const hdrPad = " " // one space left and right, inside the header style
	prefixW := lipgloss.Width(topPrefix) + 2*lipgloss.Width(hdrPad)
	maxHdr := inner - prefixW - 1 // -1 reserves space for at least one fill char
	if maxHdr < 1 {
		maxHdr = 1
	}
	hdr := header
	if lipgloss.Width(hdr) > maxHdr {
		hdr = truncate(hdr, maxHdr)
	}
	used := lipgloss.Width(topPrefix) + lipgloss.Width(hdrPad+hdr+hdrPad)
	fillN := inner - used + 1
	if fillN < 0 {
		fillN = 0
	}
	hdrStyle := titleStyle
	if selected {
		hdrStyle = titleStyle.Reverse(true)
	}
	topRow := bs.Render(topPrefix) + hdrStyle.Render(hdrPad+hdr+hdrPad) + bs.Render(strings.Repeat("─", fillN)+"┐")

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
	return bubbleBox(inner, header, bodyLines, borderColor, selected)
}

// renderHitLine formats one post as a single line "user · time  message"
// suitable for the bubble interior. muted dims everything; match bolds
// the message body. Long messages are truncated to width with an ellipsis.
func (m Model) renderHitLine(p *model.Post, width int, muted, match bool) string {
	name := m.postAuthorName(p)
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
	// Collapse tabs to a space: this is a single-line preview, and lipgloss
	// measures a tab as zero cells while the terminal paints it wider, which
	// would defeat the width-based truncate below (see expandTabs).
	body = strings.ReplaceAll(body, "\t", " ")
	body = strings.TrimSpace(body)
	if width-prefixW > 1 {
		body = truncate(body, width-prefixW)
	} else {
		body = ""
	}
	self := ""
	if m.me != nil {
		self = m.me.Username
	}
	return prefix + styleMentions(body, self, msgStyle)
}

// styleMentions replaces @self with a red-bold mentionStyle while keeping
// the rest of the body styled with baseStyle. It preserves correct styling
// across segment boundaries so truncation and dim/bold wrapping work.
func styleMentions(body, self string, baseStyle lipgloss.Style) string {
	if self == "" || body == "" {
		return baseStyle.Render(body)
	}
	re := selfMentionRe(self)
	var out strings.Builder
	last := 0
	for _, loc := range re.FindAllStringIndex(body, -1) {
		start, end := loc[0], loc[1]
		matchStr := body[start:end]
		atUser := "@" + self
		idx := strings.Index(matchStr, atUser)
		if idx < 0 {
			continue
		}
		if start > last {
			if seg := body[last:start]; seg != "" {
				out.WriteString(baseStyle.Render(seg))
			}
		}
		if idx > 0 {
			if seg := matchStr[:idx]; seg != "" {
				out.WriteString(baseStyle.Render(seg))
			}
		}
		out.WriteString(mentionStyle.Render(atUser))
		trailing := start + idx + len(atUser)
		if end > trailing {
			if seg := body[trailing:end]; seg != "" {
				out.WriteString(baseStyle.Render(seg))
			}
		}
		last = end
	}
	if last < len(body) {
		if seg := body[last:]; seg != "" {
			out.WriteString(baseStyle.Render(seg))
		}
	}
	return out.String()
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
