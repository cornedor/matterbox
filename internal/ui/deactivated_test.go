package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textinput"

	"github.com/mattermost/mattermost/server/public/model"
)

// dmModel builds a Model whose DM bucket holds direct channels with "me" and
// the given partner ids, one channel per partner (id "dm-"+partner).
func dmModel(partners ...string) *Model {
	m := &Model{
		me:               &model.User{Id: "me"},
		teams:            []*model.Team{},
		unread:           map[string]int{},
		mentions:         map[string]int{},
		deactivatedUsers: map[string]bool{},
		userNames:        map[string]string{},
		hasDMs:           true,
	}
	chs := make([]*model.Channel, 0, len(partners))
	for _, p := range partners {
		m.userNames[p] = p
		chs = append(chs, &model.Channel{Id: "dm-" + p, Type: model.ChannelTypeDirect, Name: "me__" + p})
	}
	m.bucketChannels(chs)
	return m
}

func TestSidebarHidesDeactivatedDMs(t *testing.T) {
	m := dmModel("alice", "bob", "carol")
	if got, want := visibleIDs(m), "dm-alice,dm-bob,dm-carol"; got != want {
		t.Fatalf("baseline visible = %q, want %q", got, want)
	}
	m.deactivatedUsers["bob"] = true
	if got, want := visibleIDs(m), "dm-alice,dm-carol"; got != want {
		t.Fatalf("visible = %q, want %q", got, want)
	}
}

func TestSidebarKeepsDeactivatedDMWhenOpenOrUnread(t *testing.T) {
	m := dmModel("alice", "bob", "carol")
	m.deactivatedUsers["bob"] = true
	m.deactivatedUsers["carol"] = true

	m.openChannelID = "dm-bob"
	m.mentions["dm-carol"] = 1
	if got, want := visibleIDs(m), "dm-alice,dm-bob,dm-carol"; got != want {
		t.Fatalf("visible = %q, want all three kept (%q)", got, want)
	}

	// Once read and closed, both drop out.
	m.openChannelID = ""
	m.mentions["dm-carol"] = 0
	if got, want := visibleIDs(m), "dm-alice"; got != want {
		t.Fatalf("visible = %q, want %q", got, want)
	}
}

// A group DM is never hidden: only a one-to-one DM has a single partner whose
// account can be gone.
func TestSidebarKeepsGroupDMs(t *testing.T) {
	m := &Model{
		me:               &model.User{Id: "me"},
		unread:           map[string]int{},
		mentions:         map[string]int{},
		deactivatedUsers: map[string]bool{"bob": true},
		hasDMs:           true,
	}
	m.bucketChannels([]*model.Channel{
		{Id: "g", Type: model.ChannelTypeGroup, Name: "me__bob", DisplayName: "group"},
		{Id: "dm-bob", Type: model.ChannelTypeDirect, Name: "me__bob"},
	})
	if got, want := visibleIDs(m), "g"; got != want {
		t.Fatalf("visible = %q, want %q", got, want)
	}
}

// The filter must not allocate when there is nothing to hide — it runs on
// every View.
func TestWithoutDeactivatedDMsReturnsInputWhenNothingHidden(t *testing.T) {
	m := dmModel("alice", "bob")
	in := m.channels[dmTeamID]
	if out := m.withoutDeactivatedDMs(in); &out[0] != &in[0] {
		t.Fatal("withoutDeactivatedDMs copied the slice with nothing to hide")
	}
}

func TestKeepActiveFiltersAndRecords(t *testing.T) {
	m := &Model{deactivatedUsers: map[string]bool{"stale": true}}
	got := m.keepActive([]*model.User{
		{Id: "a", Username: "alice"},
		{Id: "b", Username: "bob", DeleteAt: 123},
		nil,
		{Id: "stale", Username: "stale"},
	})
	if len(got) != 2 || got[0].Id != "a" || got[1].Id != "stale" {
		t.Fatalf("keepActive = %v, want the two active users", got)
	}
	if !m.deactivatedUsers["b"] {
		t.Error("deactivated user not recorded")
	}
	if m.deactivatedUsers["stale"] {
		t.Error("reactivated user still flagged")
	}
}

// The profile's status line says "deactivated" instead of a presence dot, the
// frozen custom status is dropped, and the fact rows date the deactivation.
func TestProfileShowsDeactivated(t *testing.T) {
	m := profileModel()
	runViewProfile(&m, "")
	u := profileTestUser()
	u.DeleteAt = time.Date(2025, 4, 2, 0, 0, 0, 0, time.UTC).UnixMilli()
	m.applyUserProfile(userProfileMsg{userID: "other", user: u, status: model.StatusOnline})

	body := strings.Join(firstOf(m.infoProfileContent()), "\n")
	for _, want := range []string{"deactivated account", "2 Apr 2025"} {
		if !strings.Contains(body, want) {
			t.Errorf("profile misses %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"online", "on vacation"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("deactivated profile still shows %q:\n%s", unwanted, body)
		}
	}
	if !m.deactivatedUsers["other"] {
		t.Error("the profile fetch did not record the deactivation")
	}
}

// An active profile keeps its presence line.
func TestProfileKeepsPresenceWhenActive(t *testing.T) {
	m := profileModel()
	runViewProfile(&m, "")
	m.applyUserProfile(userProfileMsg{userID: "other", user: profileTestUser(), status: model.StatusOnline})
	body := strings.Join(firstOf(m.infoProfileContent()), "\n")
	if !strings.Contains(body, "online") || strings.Contains(body, "deactivated") {
		t.Errorf("active profile = %s", body)
	}
}

func firstOf(lines []string, _ []infoTarget) []string { return lines }

// The picker keeps a deactivated DM (it is how you reach one once the sidebar
// has dropped it) and marks it behind the name.
func TestSwitcherMarksDeactivated(t *testing.T) {
	sw := textinput.New()
	m := dmModel("alice", "bob")
	m.switcher = &sw
	m.openStats = map[string]channelStat{}
	m.width, m.height = 120, 40
	m.switcherMode = true
	m.deactivatedUsers["bob"] = true

	out := m.renderSwitcher(30)
	if !strings.Contains(out, "@bob") {
		t.Fatalf("picker dropped the deactivated DM:\n%s", out)
	}
	if !strings.Contains(out, deactivatedLabel) {
		t.Errorf("picker did not mark the deactivated DM:\n%s", out)
	}
	if strings.Count(out, deactivatedLabel) != 1 {
		t.Errorf("marker on more than the deactivated row:\n%s", out)
	}
}
