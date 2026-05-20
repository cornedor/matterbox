package ui

import (
	"strconv"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// indexPerPage is the page size used for backfill fetches. Larger than
// initialRenderLimit because the indexer prioritises throughput over UI
// responsiveness — these results never paint, only persist.
const indexPerPage = 200

// indexerState tracks an in-flight backfill of one channel's history.
// Only one indexer runs at a time; a second invocation while active
// surfaces a status hint instead of clobbering the current run.
type indexerState struct {
	active    bool
	channelID string
	label     string
	cutoffMs  int64 // posts older than this end the run
	count     int   // total posts persisted in this run
	oldestMs  int64 // create_at of the oldest post seen (0 = none yet)
	spinner   spinner.Model
	seq       int // bumps each start so stale results can be ignored
}

// indexResultMsg is the unified pagination output for the indexer.
// posts is oldest-first within the page. nextCursor is the postID to
// pass to the next PostsBefore call (empty = stop fetching).
type indexResultMsg struct {
	seq        int
	posts      []*model.Post
	nextCursor string
	err        error
}

// startIndexer kicks off a background backfill of channelID down to
// (now - days*24h). Returns the Cmd that issues the first fetch plus
// the spinner tick. Already-running indexer is left alone with a hint.
func (m *Model) startIndexer(channelID, label string, days int) tea.Cmd {
	if m.indexer.active {
		m.status = "indexer already running on " + m.indexer.label
		return nil
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	m.indexer = indexerState{
		active:    true,
		channelID: channelID,
		label:     label,
		cutoffMs:  cutoff,
		seq:       m.indexer.seq + 1,
		spinner:   sp,
	}
	m.status = "indexing " + label + "…"
	return tea.Batch(m.indexer.spinner.Tick, m.indexSeedCmd(m.indexer.seq))
}

// indexSeedCmd produces the first fetch of an indexer run: it page-back
// from the oldest cached post if any, otherwise it fetches the newest
// page to seed the cursor.
func (m Model) indexSeedCmd(seq int) tea.Cmd {
	channelID := m.indexer.channelID
	cutoff := m.indexer.cutoffMs
	client := m.client
	st := m.store
	ctx := m.ctx
	return func() tea.Msg {
		if st != nil {
			id, createAt, err := st.OldestPost(channelID)
			if err == nil && id != "" {
				if createAt <= cutoff {
					// Cache already covers (or exceeds) the requested window.
					return indexResultMsg{seq: seq}
				}
				pl, err := client.PostsBefore(ctx, channelID, id, indexPerPage)
				if err != nil {
					return indexResultMsg{seq: seq, err: err}
				}
				return materializeIndexPage(seq, pl)
			}
		}
		// Cold cache: fetch the newest page first to seed the cursor.
		pl, err := client.Posts(ctx, channelID, indexPerPage)
		if err != nil {
			return indexResultMsg{seq: seq, err: err}
		}
		return materializeIndexPage(seq, pl)
	}
}

// indexNextCmd issues the next PostsBefore page.
func (m Model) indexNextCmd(seq int, cursorID string) tea.Cmd {
	channelID := m.indexer.channelID
	client := m.client
	ctx := m.ctx
	return func() tea.Msg {
		pl, err := client.PostsBefore(ctx, channelID, cursorID, indexPerPage)
		if err != nil {
			return indexResultMsg{seq: seq, err: err}
		}
		return materializeIndexPage(seq, pl)
	}
}

// materializeIndexPage flips a PostList from newest-first into oldest-
// first and picks the oldest id as the next backward cursor.
func materializeIndexPage(seq int, pl *model.PostList) indexResultMsg {
	if pl == nil {
		return indexResultMsg{seq: seq}
	}
	ordered := make([]*model.Post, 0, len(pl.Order))
	for i := len(pl.Order) - 1; i >= 0; i-- {
		if p, ok := pl.Posts[pl.Order[i]]; ok {
			ordered = append(ordered, p)
		}
	}
	var cursor string
	if len(ordered) > 0 {
		cursor = ordered[0].Id
	}
	return indexResultMsg{seq: seq, posts: ordered, nextCursor: cursor}
}

// applyIndexResult merges a page into the cache and decides whether to
// keep paging. Stale results (different seq) are dropped silently.
func (m *Model) applyIndexResult(msg indexResultMsg) tea.Cmd {
	if !m.indexer.active || msg.seq != m.indexer.seq {
		return nil
	}
	if msg.err != nil {
		m.status = "indexer " + m.indexer.label + ": " + msg.err.Error()
		m.indexer.active = false
		return nil
	}
	if len(msg.posts) == 0 {
		// No more history available on the server.
		m.status = m.indexerDoneStatus(true)
		m.indexer.active = false
		return nil
	}
	cmds := []tea.Cmd{m.persistPosts(msg.posts...)}
	m.indexer.count += len(msg.posts)
	oldest := msg.posts[0].CreateAt
	if m.indexer.oldestMs == 0 || oldest < m.indexer.oldestMs {
		m.indexer.oldestMs = oldest
	}
	if oldest <= m.indexer.cutoffMs {
		m.status = m.indexerDoneStatus(false)
		m.indexer.active = false
		return tea.Batch(cmds...)
	}
	if msg.nextCursor == "" {
		m.status = m.indexerDoneStatus(true)
		m.indexer.active = false
		return tea.Batch(cmds...)
	}
	m.status = m.indexerProgressStatus()
	cmds = append(cmds, m.indexNextCmd(m.indexer.seq, msg.nextCursor))
	return tea.Batch(cmds...)
}

func (m Model) indexerProgressStatus() string {
	s := m.indexer.spinner.View() + " indexing " + m.indexer.label +
		" · " + strconv.Itoa(m.indexer.count) + " posts"
	if m.indexer.oldestMs > 0 {
		s += " · oldest " + time.UnixMilli(m.indexer.oldestMs).Local().Format("2006-01-02 15:04")
	}
	return s
}

// indexerDoneStatus returns the final status line. hitStart=true means
// we ran out of server history before reaching the cutoff (channel
// older history is exhausted), not that the cutoff was reached.
func (m Model) indexerDoneStatus(hitStart bool) string {
	s := "indexed " + m.indexer.label + " · " + strconv.Itoa(m.indexer.count) + " posts"
	if m.indexer.oldestMs > 0 {
		ts := time.UnixMilli(m.indexer.oldestMs).Local().Format("2006-01-02 15:04")
		if hitStart {
			s += " · channel start " + ts
		} else {
			s += " · back to " + ts
		}
	}
	return s
}
