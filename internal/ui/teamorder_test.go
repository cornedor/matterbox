package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func teamNames(ts []*model.Team) string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = displayTeam(t)
	}
	return strings.Join(out, ",")
}

func threeTeams() []*model.Team {
	return []*model.Team{
		{Id: "t1", DisplayName: "Engineering", Name: "eng"},
		{Id: "t2", DisplayName: "Design", Name: "design"},
		{Id: "t3", DisplayName: "Marketing", Name: "mktg"},
	}
}

func TestApplyTeamOrder(t *testing.T) {
	cases := []struct {
		name  string
		order []string
		want  string
	}{
		{
			name:  "empty order is alphabetical",
			order: nil,
			want:  "Design,Engineering,Marketing",
		},
		{
			name:  "full order is honoured verbatim",
			order: []string{"Marketing", "Engineering", "Design"},
			want:  "Marketing,Engineering,Design",
		},
		{
			name:  "unlisted teams trail alphabetically",
			order: []string{"Marketing"},
			want:  "Marketing,Design,Engineering",
		},
		{
			name:  "match is case-insensitive and accepts url name",
			order: []string{"design", "eng"},
			want:  "Design,Engineering,Marketing",
		},
		{
			name:  "unknown entries are ignored",
			order: []string{"Sales", "Marketing"},
			want:  "Marketing,Design,Engineering",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{teams: threeTeams(), teamOrder: tc.order}
			m.applyTeamOrder()
			if got := teamNames(m.teams); got != tc.want {
				t.Fatalf("applyTeamOrder() = %q, want %q", got, tc.want)
			}
		})
	}
}

// withTeams builds a Model whose tab bar has the synthetic Feed/Search
// tabs (always present) followed by the given teams, with the active tab
// set to the team at teamPos (0-based among teams).
func withTeams(teams []*model.Team, teamPos int) Model {
	m := Model{teams: teams}
	m.teamIdx = m.firstTeamTabIdx() + teamPos
	return m
}

func TestMoveTeam(t *testing.T) {
	t.Run("move right swaps and follows selection", func(t *testing.T) {
		m := withTeams(threeTeams(), 0) // Engineering active
		if !m.moveTeam(1) {
			t.Fatal("moveTeam(1) returned false, want true")
		}
		if got := teamNames(m.teams); got != "Design,Engineering,Marketing" {
			t.Fatalf("order = %q", got)
		}
		// teamIdx should still point at Engineering (now position 1).
		if _, _, name := m.tabAt(m.teamIdx); name != "Engineering" {
			t.Fatalf("selection followed to %q, want Engineering", name)
		}
		// teamOrder is persisted by URL name, not display name.
		if got := strings.Join(m.teamOrder, ","); got != "design,eng,mktg" {
			t.Fatalf("teamOrder = %q", got)
		}
	})

	t.Run("move left at the first team is a no-op", func(t *testing.T) {
		m := withTeams(threeTeams(), 0)
		if m.moveTeam(-1) {
			t.Fatal("moveTeam(-1) at leftmost team returned true, want false")
		}
		if got := teamNames(m.teams); got != "Engineering,Design,Marketing" {
			t.Fatalf("order changed to %q", got)
		}
	})

	t.Run("move right at the last team is a no-op", func(t *testing.T) {
		m := withTeams(threeTeams(), 2)
		if m.moveTeam(1) {
			t.Fatal("moveTeam(1) at rightmost team returned true, want false")
		}
	})

	t.Run("no-op when a synthetic tab is active", func(t *testing.T) {
		m := Model{teams: threeTeams()}
		m.teamIdx = 0 // Unread (first synthetic tab; no DMs)
		if m.moveTeam(1) {
			t.Fatal("moveTeam on a synthetic tab returned true, want false")
		}
	})
}
