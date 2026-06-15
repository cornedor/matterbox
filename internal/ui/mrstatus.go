package ui

import (
	"strconv"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"matterbox/internal/gitlab"
)

// mrStatusState is the fetch lifecycle for one merge request.
type mrStatusState int

const (
	mrStatusPending  mrStatusState = iota // sighted on screen, awaiting a fetch
	mrStatusFetching                      // fetch in flight
	mrStatusReady                         // data available; badge renderable
	mrStatusFailed                        // fetch failed; render as plain !iid
)

// mrStatusEntry holds the minimal badge data for one MR. Only the fields needed
// for rendering the inline pill are stored — the full gitlab.MR lives in the
// ref panel's glMR field.
type mrStatusEntry struct {
	state       mrStatusState
	mrState     string // "opened" / "merged" / "closed" / "locked"
	draft       bool
	pipeStatus  string // pipeline status or "" when no pipeline
	postIDs     []string
}

// mrStatusLoadedMsg carries the result of a background MR status fetch.
type mrStatusLoadedMsg struct {
	project string
	iid     int
	mr      *gitlab.MR
	err     error
}

// mrFetchSettleMsg fires after the debounce delay; gen guards against stale ticks.
type mrFetchSettleMsg struct{ gen int }

// mrFetchSettleDelay is how long after the last navigation event we wait before
// firing pending MR fetches. Long enough to skip fetches while the user is
// scrolling quickly, short enough that pausing feels instant.
const mrFetchSettleDelay = 150 * time.Millisecond

// mrFetchConcurrencyMax caps how many MR fetches we dispatch in one batch, so
// rapid scroll through a dense feed doesn't flood the GitLab API.
const mrFetchConcurrencyMax = 5

// mrStatusManager tracks inline MR badge state for all merge requests sighted
// during rendering. Mirrors the emojiImages pattern: sightings are recorded
// during render, fetches fire from Update, results invalidate the post-line
// cache and trigger a re-render.
type mrStatusManager struct {
	mu      sync.Mutex
	entries map[string]*mrStatusEntry // key = "project!iid"
	baseURL string
}

func newMRStatusManager(baseURL string) *mrStatusManager {
	return &mrStatusManager{
		entries: make(map[string]*mrStatusEntry),
		baseURL: baseURL,
	}
}

func mrKey(project string, iid int) string {
	return project + "!" + strconv.Itoa(iid)
}

// sighted records that this MR appeared during a render of postID. If we
// already have data or a fetch is in flight, just ensure the postID is tracked
// for invalidation. Returns false when the entry is already ready/failed (so
// the caller knows a pill can be rendered immediately without re-queuing).
func (s *mrStatusManager) sighted(project string, iid int, postID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := mrKey(project, iid)
	e, ok := s.entries[k]
	if !ok {
		e = &mrStatusEntry{state: mrStatusPending}
		s.entries[k] = e
	}
	// Track which posts reference this MR so we can invalidate them when the
	// fetch completes.
	if postID != "" {
		for _, id := range e.postIDs {
			if id == postID {
				return
			}
		}
		e.postIDs = append(e.postIDs, postID)
	}
}

// status returns the entry for this MR (ready or failed only), or nil when a
// fetch is still pending/in-flight. The caller uses nil to render a plain !iid
// without a status pill.
func (s *mrStatusManager) status(project string, iid int) *mrStatusEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[mrKey(project, iid)]
	if e == nil || e.state == mrStatusPending || e.state == mrStatusFetching {
		return nil
	}
	return e
}

// postIDsFor returns the set of post IDs that referenced the given MR, for
// invalidating the post-line cache when the fetch completes.
func (s *mrStatusManager) postIDsFor(project string, iid int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[mrKey(project, iid)]
	if e == nil {
		return nil
	}
	out := make([]string, len(e.postIDs))
	copy(out, e.postIDs)
	return out
}

// drainPending returns at most n pending MR refs, marking them as fetching.
// Called from Update to build the batch of background fetch commands.
func (s *mrStatusManager) drainPending(n int) []gitlab.Ref {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []gitlab.Ref
	for k, e := range s.entries {
		if e.state != mrStatusPending {
			continue
		}
		e.state = mrStatusFetching
		// Parse project and iid back from the "project!iid" key.
		var project string
		var iid int
		if i := lastExclamation(k); i >= 0 {
			project = k[:i]
			iid, _ = strconv.Atoi(k[i+1:])
		}
		out = append(out, gitlab.Ref{Project: project, IID: iid})
		if len(out) >= n {
			break
		}
	}
	return out
}

// lastExclamation returns the index of the last '!' in s, or -1.
func lastExclamation(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '!' {
			return i
		}
	}
	return -1
}

// markReady installs a successful fetch result. The caller must invalidate the
// relevant post-line cache entries separately (see postIDsFor).
func (s *mrStatusManager) markReady(project string, iid int, mr *gitlab.MR) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := mrKey(project, iid)
	e := s.entries[k]
	if e == nil {
		e = &mrStatusEntry{}
		s.entries[k] = e
	}
	e.state = mrStatusReady
	e.mrState = mr.State
	e.draft = mr.Draft
	if mr.Pipeline != nil {
		e.pipeStatus = mr.Pipeline.Status
	}
}

// markFailed records a failed fetch. The entry stays in the map so we don't
// retry on every subsequent render.
func (s *mrStatusManager) markFailed(project string, iid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := mrKey(project, iid)
	e := s.entries[k]
	if e == nil {
		e = &mrStatusEntry{}
		s.entries[k] = e
	}
	e.state = mrStatusFailed
}

// --- rendering ---------------------------------------------------------------

// mrChipBg is the same subtle adaptive background used for reaction chips, so
// the MR inline badges look visually consistent with the rest of the feed.
var mrChipBg = reactionChipBg // shared constant from reactions.go

var (
	mrChipStyle    = lipgloss.NewStyle().Background(mrChipBg)
	mrChipCapStyle = lipgloss.NewStyle().Foreground(mrChipBg)
)

// renderMRBadge returns the inline pill for one MR. When the status is not yet
// available, it returns a plain styled "!iid" with an OSC 8 link. When ready,
// it renders "!iid state ●" (or similar) inside powerline-capped pill.
func (s *mrStatusManager) renderMRBadge(project string, iid int, glClient *gitlab.Client) string {
	iidStr := "!" + strconv.Itoa(iid)
	webURL := ""
	if glClient != nil {
		webURL = glClient.WebURL(project, iid)
	}

	e := s.status(project, iid)
	if e == nil || e.state == mrStatusFailed {
		// No data yet (or fetch failed) — plain !iid link, no pill.
		label := refKeyStyle.Render(iidStr)
		if webURL != "" {
			label = osc8Link(webURL, label)
		}
		return label
	}

	// Build the chip content: each part sets its own chip background so that
	// ANSI resets between parts don't leave unstyled gaps.
	bg := mrChipBg
	iidPart := refKeyStyle.Background(bg).Render(iidStr)

	stateLabel, stateCol := mrStateStyle(e)
	statePart := stateCol.Background(bg).Render(" " + stateLabel)

	pipePart := ""
	if e.pipeStatus != "" {
		g, gs := glStatusGlyph(e.pipeStatus)
		pipePart = gs.Background(bg).Render(" " + g)
	}

	inner := iidPart + statePart + pipePart
	pill := mrChipCapStyle.Render(reactionCapLeft) +
		inner +
		mrChipCapStyle.Render(reactionCapRight)

	if webURL != "" {
		pill = osc8Link(webURL, pill)
	}
	return pill
}

// mrStateStyle returns the display label and colour for an MR's state.
func mrStateStyle(e *mrStatusEntry) (string, lipgloss.Style) {
	if e.draft {
		return "draft", glYellow
	}
	switch e.mrState {
	case "opened":
		return "open", glGreen
	case "merged":
		return "merged", glPurple
	case "closed":
		return "closed", glRed
	case "locked":
		return "locked", refDimStyle
	}
	return e.mrState, refDimStyle
}

// --- tea.Cmd wiring ----------------------------------------------------------

// fetchPendingMRStatus drains pending MR sightings and dispatches background
// fetch goroutines for them, capped at mrFetchConcurrencyMax. Returns nil when
// GitLab is not configured, nothing is pending, or the scroll debounce is still
// active (mrFetchGen != mrFetchSettledGen).
func (m Model) fetchPendingMRStatus() tea.Cmd {
	if m.mrStatus == nil || !m.glClient.Enabled() {
		return nil
	}
	// Debounce: suppress fetches while the user is scrolling quickly. Navigation
	// keys bump mrFetchGen; mrFetchSettleMsg restores the match when scrolling pauses.
	if m.mrFetchGen != m.mrFetchSettledGen {
		return nil
	}
	refs := m.mrStatus.drainPending(mrFetchConcurrencyMax)
	if len(refs) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(refs))
	for _, r := range refs {
		project, iid := r.Project, r.IID
		client, ctx := m.glClient, m.ctx
		cmds = append(cmds, func() tea.Msg {
			mr, err := client.Get(ctx, project, iid)
			return mrStatusLoadedMsg{project: project, iid: iid, mr: mr, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// handleMRStatusLoaded installs a finished background fetch. On success it
// invalidates the cached post lines for every post that referenced this MR and
// triggers a re-render so the badge appears.
func (m Model) handleMRStatusLoaded(msg mrStatusLoadedMsg) (Model, tea.Cmd) {
	if m.mrStatus == nil {
		return m, nil
	}
	postIDs := m.mrStatus.postIDsFor(msg.project, msg.iid)
	if msg.err != nil {
		m.mrStatus.markFailed(msg.project, msg.iid)
		return m, nil
	}
	m.mrStatus.markReady(msg.project, msg.iid, msg.mr)
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

// mrFetchSettleCmd schedules the debounce tick. gen lets the handler drop stale
// ticks fired during rapid scroll.
func mrFetchSettleCmd(gen int) tea.Cmd {
	return tea.Tick(mrFetchSettleDelay, func(time.Time) tea.Msg {
		return mrFetchSettleMsg{gen: gen}
	})
}

// bumpMRFetch increments mrFetchGen and returns a settle cmd. Call from every
// messages-pane navigation handler so that rapid scrolling defers MR status
// fetches until the user pauses.
func (m *Model) bumpMRFetch() tea.Cmd {
	if m.mrStatus == nil {
		return nil
	}
	m.mrFetchGen++
	return mrFetchSettleCmd(m.mrFetchGen)
}

// buildMRInlineFn returns the mrInlineFn closure for a post. When GitLab is not
// configured it returns nil, disabling MR badge substitution.
func (m *Model) buildMRInlineFn(postID string) mrInlineFn {
	if m.mrStatus == nil || !m.glClient.Enabled() {
		return nil
	}
	baseURL := m.glClient.BaseURL()
	return func(rawURL string) (string, bool) {
		project, iid, ok := parseMRURL(rawURL, baseURL)
		if !ok {
			return "", false
		}
		m.mrStatus.sighted(project, iid, postID)
		return m.mrStatus.renderMRBadge(project, iid, m.glClient), true
	}
}
