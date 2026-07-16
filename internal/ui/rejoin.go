package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/game"
	"matterbox/internal/game/kurve"
)

// runRejoin is the `> Rejoin game` command: step back into a game you closed.
//
// Both games post their whole match into the channel — Gorillas' world post and
// Kurve's — and that post names its players: the author is always the host, and the
// payload carries the joiners. So a game you left is a game you can walk back into.
// This finds the newest live one in the open channel that you play, works out which
// side you were, and hands off to that game's resume. Nothing was lost when the
// modal closed; the posts held the match the whole time.
func runRejoin(m *Model, _ string) tea.Cmd {
	if m.gorillas.active || m.kurve.active {
		m.status = "rejoin: a game is already open"
		return nil
	}
	// Newest first: the game you just closed is the one you want back.
	for i := len(m.posts) - 1; i >= 0; i-- {
		p := m.posts[i]
		if p == nil || p.DeleteAt != 0 || p.UserId == "" {
			continue
		}
		if cmd, ok := m.rejoinGorillas(p); ok {
			return cmd
		}
		if cmd, ok := m.rejoinKurve(p); ok {
			return cmd
		}
	}
	m.status = "rejoin: no game of yours to rejoin in this channel"
	return nil
}

// rejoinGorillas resumes post as a Gorillas game if it is a live one this client
// plays, returning (nil,false) otherwise.
func (m *Model) rejoinGorillas(post *model.Post) (tea.Cmd, bool) {
	st, role, ok := gorillasResumeRole(post, m.me.Id)
	if !ok {
		return nil, false
	}
	return m.gorillasResume(post, st, role), true
}

// rejoinKurve resumes post as a Kurve game if it is a live one this client plays.
func (m *Model) rejoinKurve(post *model.Post) (tea.Cmd, bool) {
	st, role, ok := kurveResumeRole(post, m.me.Id)
	if !ok {
		return nil, false
	}
	return m.kurveResume(post, st, role), true
}

// gorillasResumeRole decodes post and reports the role meID would rejoin a live
// Gorillas game as — 0 host, 1 joiner — or ok=false if it is not a resumable
// Gorillas game for that user. A finished match is not resumable.
func gorillasResumeRole(post *model.Post, meID string) (*game.State, int, bool) {
	payload, ok := game.Decode(post.Message)
	if !ok {
		return nil, 0, false
	}
	st, err := game.UnmarshalState(payload)
	if err != nil || st.Phase == game.PhaseOver {
		return nil, 0, false
	}
	switch {
	case post.UserId == meID:
		return st, 0, true
	case st.Joiner == meID:
		return st, 1, true
	}
	return nil, 0, false
}

// kurveResumeRole is gorillasResumeRole's counterpart: the role meID would rejoin a
// live Kurve game as, or ok=false if it is not a resumable Kurve game for them.
func kurveResumeRole(post *model.Post, meID string) (*kurve.State, int, bool) {
	payload, ok := kurve.Decode(post.Message)
	if !ok {
		return nil, 0, false
	}
	st, err := kurve.UnmarshalState(payload)
	if err != nil || st.Phase == kurve.PhaseOver {
		return nil, 0, false
	}
	if post.UserId == meID {
		return st, 0, true
	}
	for _, id := range st.Joiners {
		if id == meID {
			return st, 1, true
		}
	}
	return nil, 0, false
}
