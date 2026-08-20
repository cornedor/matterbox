package ui

import (
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"matterbox/internal/forge"
)

// Inline change-request badges: when a message links a merge/pull request, the
// raw URL is replaced by a pill showing its state and CI verdict. Sightings are
// recorded during rendering, fetches fire from Update, and a result invalidates
// the post-line cache so the badge appears — the same shape as the custom-emoji
// image manager. Provider-neutral throughout: an entry remembers which forge it
// came from, so a feed mixing GitLab and GitHub badges is no different from one.

// changeStatusState is the fetch lifecycle for one change request.
type changeStatusState int

const (
	changeStatusPending  changeStatusState = iota // sighted on screen, awaiting a fetch
	changeStatusFetching                          // fetch in flight
	changeStatusReady                             // data available; badge renderable
	changeStatusFailed                            // fetch failed; render as a plain number
)

// changeStatusEntry holds the minimal badge data for one change request. Only
// the fields needed for rendering the inline pill are stored — the full
// forge.Change lives in the ref panel's refChange field.
type changeStatusEntry struct {
	state       changeStatusState
	changeState string // forge.StateOpen / StateMerged / StateClosed / StateLocked
	draft       bool
	checkStatus string // normalized CI status, or "" when there is no CI
	postIDs     []string
}

// changeRef is a sighted change request: which provider it belongs to, and
// where.
type changeRef struct {
	provider int
	repo     string
	number   int
}

// changeStatusLoadedMsg carries the result of a background badge fetch.
type changeStatusLoadedMsg struct {
	ref    changeRef
	change *forge.Change
	err    error
}

// changeFetchSettleMsg fires after the debounce delay; gen guards against stale
// ticks.
type changeFetchSettleMsg struct{ gen int }

// changeFetchSettleDelay is how long after the last navigation event we wait
// before firing pending fetches. Long enough to skip fetches while the user is
// scrolling quickly, short enough that pausing feels instant.
const changeFetchSettleDelay = 150 * time.Millisecond

// changeFetchConcurrencyMax caps how many fetches we dispatch in one batch, so
// rapid scroll through a dense feed doesn't flood a forge's API.
const changeFetchConcurrencyMax = 5

// changeStatusManager tracks inline badge state for every change request sighted
// during rendering.
type changeStatusManager struct {
	mu      sync.Mutex
	entries map[string]*changeStatusEntry // key: provider|repo#number
}

func newChangeStatusManager() *changeStatusManager {
	return &changeStatusManager{entries: make(map[string]*changeStatusEntry)}
}

// changeKey is the map key for one change request. The provider index leads it,
// so two forges hosting the same owner/repo path never collide.
func changeKey(r changeRef) string {
	return strconv.Itoa(r.provider) + "|" + r.repo + "#" + strconv.Itoa(r.number)
}

// parseChangeKey reverses changeKey. A malformed key (impossible in practice)
// yields a zero changeRef, which fetches nothing.
func parseChangeKey(k string) changeRef {
	bar := strings.IndexByte(k, '|')
	hash := strings.LastIndexByte(k, '#')
	if bar < 0 || hash < bar {
		return changeRef{}
	}
	provider, err := strconv.Atoi(k[:bar])
	if err != nil {
		return changeRef{}
	}
	number, err := strconv.Atoi(k[hash+1:])
	if err != nil {
		return changeRef{}
	}
	return changeRef{provider: provider, repo: k[bar+1 : hash], number: number}
}

// sighted records that this change request appeared during a render of postID.
// If we already have data or a fetch is in flight, it just makes sure the postID
// is tracked for invalidation.
func (s *changeStatusManager) sighted(r changeRef, postID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := changeKey(r)
	e, ok := s.entries[k]
	if !ok {
		e = &changeStatusEntry{state: changeStatusPending}
		s.entries[k] = e
	}
	// Track which posts reference this change request so we can invalidate them
	// when the fetch completes.
	if postID != "" {
		for _, id := range e.postIDs {
			if id == postID {
				return
			}
		}
		e.postIDs = append(e.postIDs, postID)
	}
}

// status returns the entry for this change request (ready or failed only), or
// nil when a fetch is still pending/in-flight. The caller uses nil to render a
// plain number without a status pill.
func (s *changeStatusManager) status(r changeRef) *changeStatusEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[changeKey(r)]
	if e == nil || e.state == changeStatusPending || e.state == changeStatusFetching {
		return nil
	}
	return e
}

// postIDsFor returns the set of post IDs that referenced the given change
// request, for invalidating the post-line cache when the fetch completes.
func (s *changeStatusManager) postIDsFor(r changeRef) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[changeKey(r)]
	if e == nil {
		return nil
	}
	out := make([]string, len(e.postIDs))
	copy(out, e.postIDs)
	return out
}

// drainPending returns at most n pending references, marking them as fetching.
// Called from Update to build the batch of background fetch commands.
func (s *changeStatusManager) drainPending(n int) []changeRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []changeRef
	for k, e := range s.entries {
		if e.state != changeStatusPending {
			continue
		}
		e.state = changeStatusFetching
		out = append(out, parseChangeKey(k))
		if len(out) >= n {
			break
		}
	}
	return out
}

// markReady installs a successful fetch result. The caller must invalidate the
// relevant post-line cache entries separately (see postIDsFor).
func (s *changeStatusManager) markReady(r changeRef, ch *forge.Change) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(changeKey(r))
	e.state = changeStatusReady
	e.changeState = ch.State
	e.draft = ch.Draft
	if ch.Checks != nil {
		e.checkStatus = ch.Checks.Status
	}
}

// markFailed records a failed fetch. The entry stays in the map so we don't
// retry on every subsequent render.
func (s *changeStatusManager) markFailed(r changeRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(changeKey(r)).state = changeStatusFailed
}

// entry returns the entry for k, creating it if the fetch outlived a cache
// eviction. Callers hold the lock.
func (s *changeStatusManager) entry(k string) *changeStatusEntry {
	e := s.entries[k]
	if e == nil {
		e = &changeStatusEntry{}
		s.entries[k] = e
	}
	return e
}

// --- rendering ---------------------------------------------------------------

// changeChipBg is the same subtle adaptive background used for reaction chips,
// so the inline badges look visually consistent with the rest of the feed.
var changeChipBg = reactionChipBg // shared constant from reactions.go

var changeChipCapStyle = lipgloss.NewStyle().Foreground(changeChipBg)

// renderChangeBadge returns the inline pill for one change request. When the
// status is not yet available, it returns the plain styled reference ("!42",
// "#42") with an OSC 8 link. When ready, it renders "#42 open ●" inside a
// powerline-capped pill.
func (s *changeStatusManager) renderChangeBadge(r changeRef, p forge.Provider) string {
	sigil := "#"
	if p != nil {
		sigil = p.Sigil()
	}
	numStr := sigil + strconv.Itoa(r.number)
	webURL := ""
	if p != nil {
		webURL = p.WebURL(r.repo, r.number)
	}

	e := s.status(r)
	if e == nil || e.state == changeStatusFailed {
		// No data yet (or fetch failed) — plain reference link, no pill.
		label := refKeyStyle.Render(numStr)
		if webURL != "" {
			label = osc8Link(webURL, label)
		}
		return label
	}

	// Build the chip content: each part sets its own chip background so that
	// ANSI resets between parts don't leave unstyled gaps.
	bg := changeChipBg
	numPart := refKeyStyle.Background(bg).Render(numStr)

	stateLabel, stateCol := changeStateStyle(e)
	statePart := stateCol.Background(bg).Render(" " + stateLabel)

	checkPart := ""
	if e.checkStatus != "" {
		g, gs := checkGlyph(e.checkStatus)
		checkPart = gs.Background(bg).Render(" " + g)
	}

	inner := numPart + statePart + checkPart
	pill := changeChipCapStyle.Render(reactionCapLeft) +
		inner +
		changeChipCapStyle.Render(reactionCapRight)

	if webURL != "" {
		pill = osc8Link(webURL, pill)
	}
	return pill
}

// changeStateStyle returns the display label and colour for a change request's
// state.
func changeStateStyle(e *changeStatusEntry) (string, lipgloss.Style) {
	if e.draft {
		return "draft", fgYellow
	}
	switch e.changeState {
	case forge.StateOpen:
		return "open", fgGreen
	case forge.StateMerged:
		return "merged", fgPurple
	case forge.StateClosed:
		return "closed", fgRed
	case forge.StateLocked:
		return "locked", refDimStyle
	}
	return e.changeState, refDimStyle
}

// --- tea.Cmd wiring ----------------------------------------------------------

// fetchPendingChangeStatus drains pending sightings and dispatches background
// fetch goroutines for them, capped at changeFetchConcurrencyMax. Returns nil
// when no forge is configured, nothing is pending, or the scroll debounce is
// still active (changeFetchGen != changeFetchSettledGen).
func (m *Model) fetchPendingChangeStatus() tea.Cmd {
	if m.changeStatus == nil || !m.anyForgeEnabled() {
		return nil
	}
	// Debounce: suppress fetches while the user is scrolling quickly. Navigation
	// keys bump changeFetchGen; changeFetchSettleMsg restores the match when
	// scrolling pauses.
	if m.changeFetchGen != m.changeFetchSettledGen {
		return nil
	}
	refs := m.changeStatus.drainPending(changeFetchConcurrencyMax)
	if len(refs) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(refs))
	for _, r := range refs {
		p, ref, ctx := m.forgeAt(r.provider), r, m.ctx
		if p == nil {
			continue
		}
		cmds = append(cmds, func() tea.Msg {
			ch, err := p.Get(ctx, ref.repo, ref.number)
			return changeStatusLoadedMsg{ref: ref, change: ch, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// handleChangeStatusLoaded installs a finished background fetch. On success it
// invalidates the cached post lines for every post that referenced this change
// request and triggers a re-render so the badge appears.
func (m Model) handleChangeStatusLoaded(msg changeStatusLoadedMsg) (Model, tea.Cmd) {
	if m.changeStatus == nil {
		return m, nil
	}
	postIDs := m.changeStatus.postIDsFor(msg.ref)
	if msg.err != nil {
		m.changeStatus.markFailed(msg.ref)
		return m, nil
	}
	m.changeStatus.markReady(msg.ref, msg.change)
	if len(postIDs) == 0 {
		return m, nil
	}
	for _, id := range postIDs {
		m.invalidatePostLines(id)
	}
	m.renderMessages()
	m.renderThread()
	return m, nil
}

// changeFetchSettleCmd schedules the debounce tick. gen lets the handler drop
// stale ticks fired during rapid scroll.
func changeFetchSettleCmd(gen int) tea.Cmd {
	return tea.Tick(changeFetchSettleDelay, func(time.Time) tea.Msg {
		return changeFetchSettleMsg{gen: gen}
	})
}

// bumpChangeFetch increments changeFetchGen and returns a settle cmd. Call from
// every messages-pane navigation handler so that rapid scrolling defers badge
// fetches until the user pauses.
func (m *Model) bumpChangeFetch() tea.Cmd {
	if m.changeStatus == nil {
		return nil
	}
	m.changeFetchGen++
	return changeFetchSettleCmd(m.changeFetchGen)
}

// buildChangeInlineFn returns the changeInlineFn closure for a post. When no
// forge is configured it returns nil, disabling badge substitution.
func (m *Model) buildChangeInlineFn(postID string) changeInlineFn {
	if m.changeStatus == nil || !m.anyForgeEnabled() {
		return nil
	}
	return func(rawURL string) (string, bool) {
		r, p, ok := m.matchChangeURL(rawURL)
		if !ok {
			return "", false
		}
		m.changeStatus.sighted(r, postID)
		return m.changeStatus.renderChangeBadge(r, p), true
	}
}

// matchChangeURL asks each configured forge whether a raw URL is one of its
// change requests, first match winning. Providers only claim links on their own
// host, so at most one can answer.
func (m *Model) matchChangeURL(rawURL string) (changeRef, forge.Provider, bool) {
	for i, p := range m.forges {
		if !p.Enabled() {
			continue
		}
		refs := p.Refs(rawURL)
		if len(refs) == 0 {
			continue
		}
		return changeRef{provider: i, repo: refs[0].Repo, number: refs[0].Number}, p, true
	}
	return changeRef{}, nil, false
}

// anyForgeEnabled reports whether at least one configured forge can fetch.
func (m *Model) anyForgeEnabled() bool {
	for _, p := range m.forges {
		if p.Enabled() {
			return true
		}
	}
	return false
}
