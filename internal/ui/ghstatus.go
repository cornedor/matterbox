package ui

import (
	"strconv"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"matterbox/internal/github"
)

// ghStatusState is the fetch lifecycle for one GitHub issue / PR badge.
type ghStatusState int

const (
	ghStatusPending  ghStatusState = iota // sighted on screen, awaiting a fetch
	ghStatusFetching                      // fetch in flight
	ghStatusReady                         // data available; badge renderable
	ghStatusFailed                        // fetch failed; render as plain #N
)

// ghStatusEntry holds the minimal badge data for one GitHub issue / PR.
type ghStatusEntry struct {
	state   ghStatusState
	ghState string // "open" / "closed" (issues); PRs may also be "merged" via state+merged
	draft   bool
	isPull  bool
	postIDs []string
}

// ghStatusLoadedMsg carries the result of a background GitHub status fetch.
type ghStatusLoadedMsg struct {
	repo   string
	number int
	item   *github.Item
	err    error
}

// ghStatusManager tracks inline GitHub badge state for issue/PR URLs sighted
// during rendering. Same pattern as mrStatusManager: sightings during render,
// fetches from Update, results invalidate the post-line cache.
type ghStatusManager struct {
	mu      sync.Mutex
	entries map[string]*ghStatusEntry // key = "repo#number"
}

func newGHStatusManager() *ghStatusManager {
	return &ghStatusManager{
		entries: make(map[string]*ghStatusEntry),
	}
}

func ghKey(repo string, number int) string {
	return repo + "#" + strconv.Itoa(number)
}

// sighted records that this GitHub ref appeared during a render of postID.
// isPullFromURL is a hint from the link path (/pull/ vs /issues/) used only
// until the API resolves the real type.
func (s *ghStatusManager) sighted(repo string, number int, isPullFromURL bool, postID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := ghKey(repo, number)
	e, ok := s.entries[k]
	if !ok {
		e = &ghStatusEntry{state: ghStatusPending, isPull: isPullFromURL}
		s.entries[k] = e
	} else if e.state == ghStatusPending || e.state == ghStatusFetching {
		// Keep the URL hint until the API resolves it.
		if isPullFromURL {
			e.isPull = true
		}
	}
	if postID != "" {
		for _, id := range e.postIDs {
			if id == postID {
				return
			}
		}
		e.postIDs = append(e.postIDs, postID)
	}
}

// status returns the entry when ready/failed, or nil while still pending.
func (s *ghStatusManager) status(repo string, number int) *ghStatusEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[ghKey(repo, number)]
	if e == nil || e.state == ghStatusPending || e.state == ghStatusFetching {
		return nil
	}
	return e
}

func (s *ghStatusManager) postIDsFor(repo string, number int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[ghKey(repo, number)]
	if e == nil {
		return nil
	}
	out := make([]string, len(e.postIDs))
	copy(out, e.postIDs)
	return out
}

// drainPending returns at most n pending refs, marking them as fetching.
func (s *ghStatusManager) drainPending(n int) []github.Ref {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []github.Ref
	for k, e := range s.entries {
		if e.state != ghStatusPending {
			continue
		}
		e.state = ghStatusFetching
		repo, number := splitGHKey(k)
		out = append(out, github.Ref{Repo: repo, Number: number})
		if len(out) >= n {
			break
		}
	}
	return out
}

func splitGHKey(k string) (repo string, number int) {
	i := lastHash(k)
	if i < 0 {
		return k, 0
	}
	repo = k[:i]
	number, _ = strconv.Atoi(k[i+1:])
	return repo, number
}

func lastHash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '#' {
			return i
		}
	}
	return -1
}

func (s *ghStatusManager) markReady(repo string, number int, item *github.Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := ghKey(repo, number)
	e := s.entries[k]
	if e == nil {
		e = &ghStatusEntry{}
		s.entries[k] = e
	}
	e.state = ghStatusReady
	e.ghState = item.State
	e.draft = item.Draft
	e.isPull = item.IsPull
}

func (s *ghStatusManager) markFailed(repo string, number int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := ghKey(repo, number)
	e := s.entries[k]
	if e == nil {
		e = &ghStatusEntry{}
		s.entries[k] = e
	}
	e.state = ghStatusFailed
}

// --- rendering ---------------------------------------------------------------

// GitHub badges reuse the same chip chrome as GitLab MR badges so they sit
// naturally in the feed, but the id form is GitHub-native (#N / PR#N) so the
// two providers stay visually distinct from GitLab's !N.
func (s *ghStatusManager) renderGHBadge(repo string, number int, ghClient *github.Client) string {
	e := s.status(repo, number)

	isPull := false
	if e != nil {
		isPull = e.isPull
	} else {
		// Pending/in-flight: peek at the entry for the URL-path hint.
		s.mu.Lock()
		if pe := s.entries[ghKey(repo, number)]; pe != nil {
			isPull = pe.isPull
		}
		s.mu.Unlock()
	}

	idStr := "#" + strconv.Itoa(number)
	if isPull {
		idStr = "PR#" + strconv.Itoa(number)
	}

	webURL := ""
	if ghClient != nil {
		webURL = ghClient.WebURL(repo, number, isPull)
	}

	if e == nil || e.state == ghStatusFailed {
		label := refKeyStyle.Render(idStr)
		if webURL != "" {
			label = osc8Link(webURL, label)
		}
		return label
	}

	bg := mrChipBg
	idPart := refKeyStyle.Background(bg).Render(idStr)
	stateLabel, stateCol := ghStateStyle(e)
	statePart := stateCol.Background(bg).Render(" " + stateLabel)

	inner := idPart + statePart
	pill := mrChipCapStyle.Render(reactionCapLeft) +
		inner +
		mrChipCapStyle.Render(reactionCapRight)

	if webURL != "" {
		pill = osc8Link(webURL, pill)
	}
	return pill
}

func ghStateStyle(e *ghStatusEntry) (string, lipgloss.Style) {
	if e.draft {
		return "draft", glYellow
	}
	switch e.ghState {
	case "open":
		return "open", glGreen
	case "merged":
		return "merged", glPurple
	case "closed":
		return "closed", glRed
	}
	return e.ghState, refDimStyle
}

// --- tea.Cmd wiring ----------------------------------------------------------

func (m *Model) fetchPendingGHStatus() tea.Cmd {
	if m.ghStatus == nil || !m.ghClient.Enabled() {
		return nil
	}
	// Share the MR scroll debounce so rapid scrolling doesn't flood either API.
	if m.mrFetchGen != m.mrFetchSettledGen {
		return nil
	}
	refs := m.ghStatus.drainPending(mrFetchConcurrencyMax)
	if len(refs) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(refs))
	for _, r := range refs {
		repo, number := r.Repo, r.Number
		client, ctx := m.ghClient, m.ctx
		cmds = append(cmds, func() tea.Msg {
			item, err := client.Get(ctx, repo, number)
			return ghStatusLoadedMsg{repo: repo, number: number, item: item, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func (m Model) handleGHStatusLoaded(msg ghStatusLoadedMsg) (Model, tea.Cmd) {
	if m.ghStatus == nil {
		return m, nil
	}
	postIDs := m.ghStatus.postIDsFor(msg.repo, msg.number)
	if msg.err != nil {
		m.ghStatus.markFailed(msg.repo, msg.number)
		return m, nil
	}
	m.ghStatus.markReady(msg.repo, msg.number, msg.item)
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

// parseGitHubURL returns repo/number when rawURL is a GitHub issue or PR link
// for the configured instance.
func parseGitHubURL(rawURL, baseURL string) (repo string, number int, isPull bool, ok bool) {
	refs := github.Refs(rawURL, baseURL)
	if len(refs) == 0 {
		return "", 0, false, false
	}
	return refs[0].Repo, refs[0].Number, refs[0].IsPull, true
}
