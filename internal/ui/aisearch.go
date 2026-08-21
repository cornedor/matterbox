package ui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/aisearch"
)

// AI search turns a natural-language question typed into the Search box
// (anything ending in "?") into an agentic loop: the model is given a small
// set of tools and drives them itself — searching the local message cache,
// discovering which channels a topic lives in, and narrowing down — until it
// can answer. The messages it surfaces are collected and rendered as the same
// clickable hit bubbles as a normal search, with the model's answer on top.
//
// The agent loop, tools, and catalog live in the UI-free internal/aisearch
// package (shared with the `matterbox listen` daemon). This file is only the
// bubbletea wiring: it snapshots the channel/team/user metadata into an
// aisearch.Catalog, runs aisearch.Run on a goroutine, and reports back over a
// channel — mirroring the streaming pattern in summary.go.

// aiSearchHTTPTimeout is the fallback bound on the whole agentic loop (all tool
// rounds, not a single request) used when config.yaml sets no timeout_minutes.
// Generous because each round waits on a local model.
const aiSearchHTTPTimeout = 4 * time.Minute

type aiSearchPhase int

const (
	aiSearchOff     aiSearchPhase = iota // not active
	aiSearchRunning                      // agent loop in flight
	aiSearchDone                         // final answer (or error) shown
)

// aiSearchState owns the agentic-search run on the Search tab. Only one runs
// at a time; starting a new one resets this wholesale. The collected hits are
// installed into m.search.hits when the run finishes, so the existing bubble
// navigation (up/down/enter) works on them unchanged.
type aiSearchState struct {
	phase     aiSearchPhase
	seq       int // bumps per run so stale goroutine messages are dropped
	query     string
	trace     []aisearch.TraceStep
	answer    string
	tentative bool // answer is an unconfirmed best guess, not a confirmed one
	spinner   spinner.Model

	// history is the full chat transcript (system + user + assistant + tool
	// turns) as it stood when the run finished, so a follow-up can continue the
	// same conversation instead of starting cold. Empty until a run completes.
	history []aisearch.Message
	// followup is the "↳ ask a follow-up…" input rendered inside the answer box.
	// It owns keystrokes while the answer box is the selected row (idx == -1).
	followup textinput.Model

	stream chan aisearch.Update
	cancel context.CancelFunc
	err    error
}

// newAISearchState builds a fresh, inactive state with an empty follow-up input.
func newAISearchState() aiSearchState {
	ti := textinput.New()
	ti.Prompt = "↳ "
	ti.Placeholder = "ask a follow-up…"
	ti.CharLimit = 256
	return aiSearchState{followup: ti}
}

// active reports whether AI search is running or showing a result, i.e.
// whether it currently owns the Search viewport.
func (s aiSearchState) active() bool { return s.phase != aiSearchOff }

// aiPeopleTTL is how long a resolved people directory is reused before the
// worker refetches it. Long, because it only changes when someone joins, is
// renamed, or a new DM appears.
const aiPeopleTTL = 30 * time.Minute

// catalogInput is the update-loop snapshot the worker turns into a Catalog.
// Everything in it is copied, so the worker never reads live Model state.
type catalogInput struct {
	meID     string
	teams    []*model.Team
	channels []*model.Channel
	// people is the cached directory; empty means the worker should resolve it.
	people map[string]aisearch.Person
	// fallback names the worker uses if the directory fetch fails, so a search
	// still runs (without real-name matching) rather than losing every author.
	fallback map[string]string
}

// buildSearchCatalogInput snapshots the current channel/team/user metadata so
// the worker goroutine can build the catalog without racing the update loop.
func (m Model) buildSearchCatalogInput() catalogInput {
	in := catalogInput{fallback: make(map[string]string, len(m.userNames))}
	if m.me != nil {
		in.meID = m.me.Id
	}
	in.teams = append(in.teams, m.teams...)
	for _, list := range m.channels {
		in.channels = append(in.channels, list...)
	}
	for id, name := range m.userNames {
		in.fallback[id] = name
	}
	if time.Since(m.aiPeopleAt) < aiPeopleTTL {
		in.people = m.aiPeople
	}
	return in
}

// buildCatalog turns the snapshot into the Catalog the tools resolve against,
// resolving the people directory (one batched user fetch) when the cached one
// has expired. Runs on the worker goroutine. Returns the directory too, so the
// update loop can cache it for the next run.
func (m Model) buildCatalog(ctx context.Context, in catalogInput) (aisearch.Catalog, map[string]aisearch.Person) {
	people := in.people
	if len(people) == 0 {
		people = aisearch.ResolvePeople(ctx, m.client, in.meID, in.channels, m.store)
	}
	if len(people) == 0 {
		people = aisearch.PeopleFromUsernames(in.fallback)
	}
	cat := aisearch.BuildCatalog(in.meID, in.teams, in.channels, people)
	// Cached volume per channel ranks the channel and people listings, so the
	// agent is shown the conversations that actually hold something first.
	if m.store != nil {
		if counts, err := m.store.ChannelPostCounts(); err == nil {
			cat = cat.WithVolumes(counts)
		}
	}
	return cat, people
}

// ---- bubbletea wiring ----------------------------------------------------

// startAISearch kicks off an agentic search for the given raw query (which
// still has its trailing "?"). Returns a Cmd that starts the worker, or sets
// a status and returns nil if prerequisites are missing.
func (m *Model) startAISearch(rawQuery string) tea.Cmd {
	query := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(rawQuery), "?"))
	if query == "" {
		return nil
	}
	if m.store == nil {
		m.status = "AI search needs the local message cache"
		return nil
	}
	if strings.TrimSpace(m.summaryEndpoint) == "" || strings.TrimSpace(m.summaryModel) == "" {
		m.status = "AI search: no model endpoint configured"
		return nil
	}
	maxSteps := m.aiSearchMaxSteps
	if maxSteps <= 0 {
		maxSteps = 32
	}

	prev := m.aiSearch.seq
	m.aiSearch = newAISearchState()
	m.aiSearch.seq = prev + 1
	m.aiSearch.phase = aiSearchRunning
	m.aiSearch.query = query
	m.beginAISearchSpinner()

	// Clear any FTS results behind us so the trace owns the viewport.
	m.search.hits = nil
	m.search.idx = 0
	m.search.query = ""
	m.search.err = ""
	m.search.loading = false
	m.renderSearchResults()

	// The agentic search is the most expensive thing on this tab and the least
	// evidenced: it runs a whole tool-calling loop against a local model, and
	// nobody knows whether the answers get opened. Timed from here; reported
	// when the run terminates.
	m.armSearch("ai", "all", searchTerms(query), false)

	system := m.buildAISearchSystem()
	catalog := m.buildSearchCatalogInput()
	messages := []aisearch.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: query},
	}
	return tea.Batch(m.aiSearch.spinner.Tick, m.openAISearchCmd(m.aiSearch.seq, messages, maxSteps, catalog))
}

// startAIFollowup continues the finished run with the text in the follow-up box,
// seeding the agent with the prior transcript so references like "that" resolve.
// Returns nil (and does nothing) when there's no completed run or empty input.
func (m *Model) startAIFollowup() tea.Cmd {
	text := strings.TrimSpace(m.aiSearch.followup.Value())
	if text == "" || m.aiSearch.phase != aiSearchDone || m.aiSearch.err != nil || len(m.aiSearch.history) == 0 {
		return nil
	}
	maxSteps := m.aiSearchMaxSteps
	if maxSteps <= 0 {
		maxSteps = 32
	}
	// Seed = prior transcript + the new question. Copy the history so the
	// append can't clobber the slice we still hold for a possible retry.
	seed := append(m.aiSearch.history[:len(m.aiSearch.history):len(m.aiSearch.history)],
		aisearch.Message{Role: "user", Content: text})

	m.aiSearch.seq++
	seq := m.aiSearch.seq
	m.aiSearch.phase = aiSearchRunning
	m.aiSearch.query = text
	m.aiSearch.trace = nil
	m.aiSearch.answer = ""
	m.aiSearch.tentative = false
	m.aiSearch.err = nil
	m.aiSearch.followup.SetValue("")
	m.aiSearch.followup.Blur()
	m.beginAISearchSpinner()
	m.armSearch("ai", "all", searchTerms(text), false)

	// Clear the previous result set so the live trace owns the viewport again.
	m.search.hits = nil
	m.search.idx = -1
	m.search.query = ""
	m.renderSearchResults()

	catalog := m.buildSearchCatalogInput()
	return tea.Batch(m.aiSearch.spinner.Tick, m.openAISearchCmd(seq, seed, maxSteps, catalog))
}

// beginAISearchSpinner installs a fresh focused-colour dot spinner.
func (m *Model) beginAISearchSpinner() {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(focusedColor)
	m.aiSearch.spinner = sp
}

// buildAISearchSystem appends a tiny orientation (team names, current scope)
// to the configured agent prompt. We deliberately do NOT dump the channel
// catalog — the model discovers channels via list_channels, which keeps the
// prompt small for the local model's limited per-slot context.
func (m Model) buildAISearchSystem() string {
	system := m.aiSearchPrompt
	var teamNames []string
	for _, t := range m.teams {
		if n := displayTeam(t); n != "" {
			teamNames = append(teamNames, n)
		}
	}
	if len(teamNames) > 0 {
		system += "\n\nTeams you can search: " + strings.Join(teamNames, ", ") + "."
	}
	system += "\nToday is " + time.Now().Local().Format("Monday, January 2, 2006") + "."
	return system
}

// openAISearchCmd starts the worker goroutine and hands the UI the update
// channel + cancel handle.
func (m Model) openAISearchCmd(seq int, messages []aisearch.Message, maxSteps int, in catalogInput) tea.Cmd {
	cfg := aisearch.Config{
		Store:       m.store,
		Endpoint:    m.summaryEndpoint,
		APIKey:      m.summaryAPIKey,
		Model:       m.summaryModel,
		MaxSteps:    maxSteps,
		EmbedClient: m.embedClient,
		EmbedModel:  m.embedModel,
		EmbedDim:    m.embedDim,
	}
	timeout := m.aiSearchTimeout
	if timeout <= 0 {
		timeout = aiSearchHTTPTimeout
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		catalog, people := m.buildCatalog(ctx, in)
		ch := make(chan aisearch.Update, 8)
		go aisearch.Run(ctx, cfg, catalog, messages, ch)
		return aiSearchOpenedMsg{seq: seq, ch: ch, cancel: cancel, people: people}
	}
}

// waitAISearchUpdate blocks for the next worker update. A closed channel
// (goroutine returned without a terminal message) yields a done update.
func waitAISearchUpdate(seq int, ch <-chan aisearch.Update) tea.Cmd {
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return aiSearchUpdateMsg{seq: seq, u: aisearch.Update{Done: true}}
		}
		return aiSearchUpdateMsg{seq: seq, u: u}
	}
}

// applyAISearchOpened stores the channel + cancel and schedules the first
// read. A stale open is cancelled immediately.
func (m *Model) applyAISearchOpened(msg aiSearchOpenedMsg) tea.Cmd {
	// Cache the directory even for a stale run — resolving it cost a fetch, and
	// it is the same for every run.
	if len(msg.people) > 0 {
		m.aiPeople = msg.people
		m.aiPeopleAt = time.Now()
	}
	if msg.seq != m.aiSearch.seq || m.aiSearch.phase != aiSearchRunning {
		msg.cancel()
		return nil
	}
	m.aiSearch.stream = msg.ch
	m.aiSearch.cancel = msg.cancel
	return waitAISearchUpdate(msg.seq, msg.ch)
}

// applyAISearchUpdate folds one worker update into the state: append a trace
// step and keep reading, or finalize on a terminal update.
func (m *Model) applyAISearchUpdate(msg aiSearchUpdateMsg) tea.Cmd {
	if msg.seq != m.aiSearch.seq || m.aiSearch.phase != aiSearchRunning {
		return nil
	}
	u := msg.u
	if u.HasStep {
		m.aiSearch.trace = append(m.aiSearch.trace, u.Step)
		m.renderSearchResults()
		return waitAISearchUpdate(m.aiSearch.seq, m.aiSearch.stream)
	}
	// Terminal update.
	m.finishAISearch()
	m.recordSearchRun(len(u.Hits), u.Err != nil)
	m.aiSearch.answer = u.Answer
	m.aiSearch.tentative = u.Tentative
	m.aiSearch.err = u.Err
	m.aiSearch.history = u.History
	// Install the agent's hits as the search result set so the existing
	// bubble navigation (up/down/enter to jump to a message) works on them.
	m.search.hits = u.Hits
	m.search.query = m.aiSearch.query
	// Land on the answer box (idx -1), not the first hit, so the summary is
	// fully in view and the follow-up input is ready. up/down walks the hits.
	m.search.idx = -1
	m.search.view.GotoTop()
	var cmd tea.Cmd
	if u.Err == nil {
		// The answer box owns input now; hand the cursor to the follow-up field
		// and take it off the main search box so there's only one caret.
		m.search.input.Blur()
		cmd = m.aiSearch.followup.Focus()
	}
	m.renderSearchResults()
	return cmd
}

// finishAISearch releases the request and moves to the done phase.
func (m *Model) finishAISearch() {
	if m.aiSearch.cancel != nil {
		m.aiSearch.cancel()
		m.aiSearch.cancel = nil
	}
	m.aiSearch.stream = nil
	m.aiSearch.phase = aiSearchDone
}

// cancelAISearch tears down an in-flight or finished AI run and returns the
// Search tab to plain FTS. Safe to call when nothing is active.
func (m *Model) cancelAISearch() {
	if m.aiSearch.cancel != nil {
		m.aiSearch.cancel()
	}
	prev := m.aiSearch.seq
	m.aiSearch = newAISearchState()
	m.aiSearch.seq = prev + 1 // invalidate any in-flight goroutine messages
}

// ---- rendering -----------------------------------------------------------

// renderAIWorking draws the live trace into the Search viewport while the
// agent runs: a spinner header plus one line per tool call so far.
func (m *Model) renderAIWorking() string {
	dim := lipgloss.NewStyle().Foreground(dimColor)
	accent := lipgloss.NewStyle().Foreground(focusedColor)
	var lines []string
	lines = append(lines, accent.Render("✨ "+m.aiSearch.spinner.View())+" searching: "+
		lipgloss.NewStyle().Italic(true).Render(truncate(m.aiSearch.query, maxInt(10, m.search.view.Width()-16))))
	lines = append(lines, "")
	for _, s := range m.aiSearch.trace {
		// Don't truncate: the row carries ANSI styling (truncate is not
		// escape-aware), and the viewport soft-wraps long lines anyway.
		row := "  " + accent.Render("▸") + " " + s.Label()
		if r := s.Result(); r != "" {
			row += dim.Render("  → " + r)
		}
		lines = append(lines, row)
	}
	lines = append(lines, "", dim.Render("  (esc to cancel)"))
	return strings.Join(lines, "\n")
}

// renderAIBanner builds the "✨ AI answer" box, sized to match a hit bubble's
// outer width so it stacks flush above the bubbles.
func (m *Model) renderAIBanner(innerW int) []string {
	outerW := innerW - 2
	if outerW < 8 {
		outerW = 8
	}
	inner := outerW - 2
	contentW := inner - 2
	if contentW < 1 {
		contentW = 1
	}

	header := "✨ AI answer"
	body := m.aiSearch.answer
	// The answer box is "selected" when the cursor is above the first hit
	// (idx -1); show that with the focused border, like a selected hit bubble.
	borderColor := dimColor
	if m.search.idx < 0 {
		borderColor = focusedColor
	}
	switch {
	case m.aiSearch.err != nil:
		header = "✨ AI search — error"
		body = m.aiSearch.err.Error()
		borderColor = lipgloss.Color("9") // red
	case m.aiSearch.tentative:
		// Ran out of search steps: the answer is a best guess, not confirmed.
		header = "✨ AI answer — best guess (unconfirmed)"
		borderColor = lipgloss.Color("11") // yellow
	}
	wrapped := lipgloss.NewStyle().Width(contentW).Render(strings.TrimSpace(body))
	bodyLines := strings.Split(wrapped, "\n")
	// On a successful run, tack a follow-up input onto the bottom of the same
	// box, under a separator rule, so the conversation can continue in place.
	if m.aiSearch.err == nil {
		sep := lipgloss.NewStyle().Foreground(dimColor).Render(strings.Repeat("─", contentW))
		m.aiSearch.followup.SetWidth(contentW - 2)
		bodyLines = append(bodyLines, sep, m.aiSearch.followup.View())
	}
	return strings.Split(bubbleBox(inner, header, bodyLines, borderColor, m.search.idx < 0), "\n")
}

// renderAIResults draws the finished AI run: the answer banner, then the
// collected messages as clickable hit bubbles (or a "found nothing" note).
func (m *Model) renderAIResults() {
	innerW := m.search.view.Width()
	if innerW < 10 {
		innerW = 10
	}
	banner := m.renderAIBanner(innerW)
	if m.aiSearch.err != nil {
		m.search.view.SetContentLines(banner)
		m.search.view.GotoTop()
		return
	}
	if len(m.search.hits) == 0 {
		// No hits, but the answer box (with its follow-up input) is still the
		// selected, scrollable header — keep idx pinned to it.
		m.search.idx = -1
		note := lipgloss.NewStyle().Foreground(dimColor).Render("  no matching messages to show")
		m.setBubbleViewport(append(banner, "", note), nil, -1, false)
		return
	}
	// idx -1 selects the answer box; 0..n-1 select hits. Clamp into that range.
	if m.search.idx < -1 {
		m.search.idx = -1
	}
	if m.search.idx >= len(m.search.hits) {
		m.search.idx = len(m.search.hits) - 1
	}
	m.setBubbleViewport(append(banner, ""), m.search.hits, m.search.idx, false)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
