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

// profileTestUser is a fully-populated author for the panel-content tests.
func profileTestUser() *model.User {
	cs, _ := json.Marshal(model.CustomStatus{Emoji: "palm_tree", Text: "on vacation"})
	return &model.User{
		Id:        "other",
		Username:  "jdoe",
		FirstName: "Jane",
		LastName:  "Doe",
		Position:  "Backend engineer",
		CreateAt:  time.Date(2019, 3, 12, 0, 0, 0, 0, time.UTC).UnixMilli(),
		Props:     model.StringMap{model.UserPropsKeyCustomStatus: string(cs)},
	}
}

// profileModel is infoTestModel with a selectable post by "other" and the maps
// the profile path writes into.
func profileModel() Model {
	m := infoTestModel()
	m.infoOpen = false
	m.focus = focusMessages
	m.posts = []*model.Post{pPost("a", 1000, "other")}
	m.postIdx = 0
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

// The command raises the info panel in profile mode aimed at the author,
// takes focus, and starts the fetch. A second run for the same user closes it.
func TestRunViewProfileRaisesPanel(t *testing.T) {
	m := profileModel()
	if cmd := runViewProfile(&m, ""); cmd == nil {
		t.Fatal("expected the fetch command")
	}
	if !m.infoOpen || m.infoMode != infoModeProfile || m.infoProfileUserID != "other" {
		t.Fatalf("panel not raised in profile mode for the author: open=%v mode=%v user=%q",
			m.infoOpen, m.infoMode, m.infoProfileUserID)
	}
	if m.focus != focusInfo {
		t.Errorf("focus = %v, want focusInfo", m.focus)
	}
	runViewProfile(&m, "")
	if m.infoOpen {
		t.Error("running the command again for the same user must close the panel")
	}
}

// The fetched profile lands in the panel and refreshes the name/presence
// caches; a stale result (panel closed meanwhile) still refreshes the caches
// but touches no panel state.
func TestApplyUserProfile(t *testing.T) {
	m := profileModel()
	runViewProfile(&m, "")
	m.applyUserProfile(userProfileMsg{userID: "other", user: profileTestUser(), status: model.StatusOnline})

	if !m.infoProfileLoaded || m.infoProfile == nil {
		t.Fatal("profile not applied to the open panel")
	}
	if m.userNames["other"] != "jdoe" || m.statuses["other"] != model.StatusOnline {
		t.Error("the fetch must refresh the name and presence caches")
	}

	m2 := profileModel()
	m2.applyUserProfile(userProfileMsg{userID: "other", user: profileTestUser(), status: model.StatusAway})
	if m2.infoProfileLoaded || m2.infoProfile != nil {
		t.Error("a stale result must not touch panel state")
	}
	if m2.userNames["other"] != "jdoe" {
		t.Error("a stale result must still refresh the name cache")
	}
}

// The panel content carries the profile's rows, drops empty ones, and ends on
// the DM action row; viewing yourself has no DM row.
func TestInfoProfileContent(t *testing.T) {
	m := profileModel()
	runViewProfile(&m, "")
	m.applyUserProfile(userProfileMsg{userID: "other", user: profileTestUser(), status: model.StatusOnline})

	lines, targets := m.infoProfileContent()
	body := strings.Join(lines, "\n")
	for _, want := range []string{"Jane Doe", "@jdoe", "online", "on vacation", "Backend engineer", "12 Mar 2019", "direct message"} {
		if !strings.Contains(body, want) {
			t.Errorf("profile content misses %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Email") {
		t.Error("an empty email must not render a row")
	}
	if len(targets) != 1 || targets[0].kind != infoTargetMember || targets[0].userID != "other" {
		t.Fatalf("targets = %+v, want one member target for the author", targets)
	}

	me := profileTestUser()
	me.Id = "me"
	m.infoProfileUserID = "me"
	m.applyUserProfile(userProfileMsg{userID: "me", user: me})
	_, targets = m.infoProfileContent()
	if len(targets) != 0 {
		t.Error("no DM row expected on your own profile")
	}
}

// A failed fetch renders in the panel, not the status line.
func TestApplyUserProfileError(t *testing.T) {
	m := profileModel()
	runViewProfile(&m, "")
	m.applyUserProfile(userProfileMsg{userID: "other", err: errors.New("boom")})
	lines, _ := m.infoProfileContent()
	if !strings.Contains(strings.Join(lines, "\n"), "boom") {
		t.Errorf("error not rendered in the panel: %q", lines)
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
