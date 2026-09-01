package ui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/telemetry"
)

// profileModel is a renderable channel "c" with one post by "other", ready for
// the view-profile path.
func profileModel() Model {
	m := pagingModel([]*model.Post{pPost("a", 1000, "other")}, 0)
	m.channels = map[string][]*model.Channel{
		"t1": {{Id: "c", Name: "general", DisplayName: "General"}},
	}
	m.statuses = map[string]string{}
	m.customStatuses = map[string]model.CustomStatus{}
	return m
}

// The entry is offered while a conversation is open, carries a catalogued
// telemetry id (the palette counter drops anything else), and is absent with
// no channel open.
func TestViewProfileCommandListed(t *testing.T) {
	m := profileModel()
	cmd, ok := m.viewProfileCommand()
	if !ok {
		t.Fatal("expected the view-profile entry with a channel open")
	}
	catalogued := false
	for _, id := range telemetry.PaletteIDs {
		if id == cmd.tid {
			catalogued = true
		}
	}
	if !catalogued {
		t.Errorf("entry id %q is not in telemetry.PaletteIDs", cmd.tid)
	}
	m.openChannelID = ""
	if _, ok := m.viewProfileCommand(); ok {
		t.Error("no entry expected with no channel open")
	}
}

// Running the entry with no selection reports it rather than fetching.
func TestRunViewProfileWithoutSelection(t *testing.T) {
	m := profileModel()
	m.focus = focusInput // selectedPost() follows focus: nothing selected here
	if cmd := runViewProfile(&m, ""); cmd != nil {
		t.Error("expected no command with no message selected")
	}
	if m.status != "no message selected" {
		t.Errorf("status = %q, want %q", m.status, "no message selected")
	}
}

// A fetched profile opens the popup with the profile's rows and refreshes the
// name/presence caches on the way.
func TestApplyUserProfileOpensPopup(t *testing.T) {
	m := profileModel()
	cs, _ := json.Marshal(model.CustomStatus{Emoji: "palm_tree", Text: "on vacation"})
	u := &model.User{
		Id:        "other",
		Username:  "jdoe",
		FirstName: "Jane",
		LastName:  "Doe",
		Position:  "Backend engineer",
		CreateAt:  time.Date(2019, 3, 12, 0, 0, 0, 0, time.UTC).UnixMilli(),
		Props:     model.StringMap{model.UserPropsKeyCustomStatus: string(cs)},
	}
	m.applyUserProfile(userProfileMsg{user: u, status: model.StatusOnline})

	if !m.textPopup.active {
		t.Fatal("expected the text popup to open")
	}
	if m.userNames["other"] != "jdoe" || m.statuses["other"] != model.StatusOnline {
		t.Error("the fetch must refresh the name and presence caches")
	}
	body := m.renderUserProfile(u, model.StatusOnline)
	for _, want := range []string{"Jane Doe", "@jdoe", "online", "on vacation", "Backend engineer", "12 Mar 2019"} {
		if !strings.Contains(body, want) {
			t.Errorf("profile body misses %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Email") {
		t.Error("an empty email must not render a row")
	}
}

// A failed fetch lands on the status line; no popup.
func TestApplyUserProfileError(t *testing.T) {
	m := profileModel()
	m.applyUserProfile(userProfileMsg{err: errors.New("boom")})
	if m.textPopup.active {
		t.Error("no popup expected on a failed fetch")
	}
	if !strings.Contains(m.status, "profile:") {
		t.Errorf("status = %q, want a profile error", m.status)
	}
}

// An expired custom status is dropped, exactly like the sidebar's rule.
func TestProfileCustomStatusExpiry(t *testing.T) {
	m := profileModel()
	cs, _ := json.Marshal(model.CustomStatus{Emoji: "x", Text: "gone", ExpiresAt: time.Now().Add(-time.Hour)})
	u := &model.User{Id: "other", Username: "jdoe", Props: model.StringMap{model.UserPropsKeyCustomStatus: string(cs)}}
	if _, ok := m.profileCustomStatus(u); ok {
		t.Error("an expired custom status must not show")
	}
}
