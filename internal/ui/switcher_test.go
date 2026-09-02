package ui

import (
	"strconv"
	"testing"

	"charm.land/bubbles/v2/textinput"
	"github.com/mattermost/mattermost/server/public/model"
)

// fuzzyScore's band is the primary switcher ranking key: a stronger
// textual match (lower band) always outranks a weaker one, so typing a
// name jumps to it. Within a band, attention/usage decide — that part is
// exercised via switcherResults elsewhere; here we pin the classification.
func TestFuzzyScoreBands(t *testing.T) {
	cases := []struct {
		haystack, needle string
		wantBand         int
		wantOK           bool
	}{
		{"general", "", 0, true},        // empty needle → everyone in band 0
		{"general", "general", 0, true}, // exact
		{"general", "gen", 1, true},     // prefix
		{"x-general", "gen", 2, true},   // interior substring
		{"agenda", "gnd", 3, true},      // subsequence (g..n..d)
		{"general", "xyz", 0, false},    // no match
	}
	for _, c := range cases {
		band, _, ok := fuzzyScore(c.haystack, c.needle)
		if ok != c.wantOK {
			t.Errorf("fuzzyScore(%q,%q) ok=%v, want %v", c.haystack, c.needle, ok, c.wantOK)
			continue
		}
		if ok && band != c.wantBand {
			t.Errorf("fuzzyScore(%q,%q) band=%d, want %d", c.haystack, c.needle, band, c.wantBand)
		}
	}
}

// Within a single band the finer score still orders by earliest match
// position, so it remains a sensible last-resort tiebreaker.
func TestFuzzyScoreWithinBandPosition(t *testing.T) {
	_, early, _ := fuzzyScore("ab-team", "team")  // substring at index 3
	_, late, _ := fuzzyScore("abcd-team", "team") // substring at index 5
	if !(early < late) {
		t.Errorf("expected earlier substring to score lower: early=%d late=%d", early, late)
	}
}

// A directory match for someone we have no DM channel with gets its own row
// below the channel matches, so a freshly-added colleague is reachable from
// ctrl+p before any DM exists.
func TestSwitcherRowsIncludeDirectoryUsers(t *testing.T) {
	sw := textinput.New()
	m := Model{
		switcher:  &sw,
		me:        &model.User{Id: "me"},
		channels:  map[string][]*model.Channel{},
		mentions:  map[string]int{},
		unread:    map[string]int{},
		openStats: map[string]channelStat{},
		userNames: map[string]string{"u1": "newbie"},
		switcherUsers: []*model.User{
			{Id: "u1", Username: "newbie"},
		},
	}
	m.switcher.SetValue("newb")
	rows := m.switcherRows()
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].user == nil || rows[0].user.Id != "u1" {
		t.Errorf("row 0 = %+v, want directory user u1", rows[0])
	}
}

// Yourself and users you already have a DM channel with are dropped — that
// channel is already among the channel matches.
func TestSwitcherUserMatchesSkipsKnown(t *testing.T) {
	sw := textinput.New()
	m := Model{
		switcher: &sw,
		me:       &model.User{Id: "me"},
		channels: map[string][]*model.Channel{
			dmTeamID: {{Id: "dm1", Type: model.ChannelTypeDirect, Name: "me__u1"}},
		},
		switcherUsers: []*model.User{
			{Id: "u1", Username: "known"},
			{Id: "me", Username: "self"},
			{Id: "u2", Username: "fresh"},
		},
	}
	got := m.switcherUserMatches()
	if len(got) != 1 || got[0].Id != "u2" {
		t.Fatalf("got %+v, want only u2", got)
	}
}

// Directory rows are kept even when the channel matches would fill the popup.
func TestSwitcherRowsReserveRoomForUsers(t *testing.T) {
	sw := textinput.New()
	lists := map[string][]*model.Channel{}
	for i := 0; i < switcherLimit+5; i++ {
		lists["t1"] = append(lists["t1"], &model.Channel{
			Id: "c" + strconv.Itoa(i), TeamId: "t1", Type: model.ChannelTypeOpen,
			Name: "fresh-" + strconv.Itoa(i), DisplayName: "fresh " + strconv.Itoa(i),
		})
	}
	m := Model{
		switcher:      &sw,
		me:            &model.User{Id: "me"},
		channels:      lists,
		mentions:      map[string]int{},
		unread:        map[string]int{},
		openStats:     map[string]channelStat{},
		switcherUsers: []*model.User{{Id: "u2", Username: "fresh"}},
	}
	m.switcher.SetValue("fresh")
	rows := m.switcherRows()
	if len(rows) != switcherLimit {
		t.Fatalf("got %d rows, want %d", len(rows), switcherLimit)
	}
	last := rows[len(rows)-1]
	if last.user == nil || last.user.Id != "u2" {
		t.Errorf("last row = %+v, want the directory user", last)
	}
}
