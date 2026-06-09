package cli

import (
	"bytes"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestDigestPartnerIDs(t *testing.T) {
	chans := map[string]*model.Channel{
		"open": {Id: "open", Type: model.ChannelTypeOpen},
		"dm1":  {Id: "dm1", Type: model.ChannelTypeDirect, Name: "me__alice"},
		"dm2":  {Id: "dm2", Type: model.ChannelTypeDirect, Name: "bob__me"},
	}
	posts := []*model.Post{
		{ChannelId: "open", UserId: "me"},
		{ChannelId: "dm1", UserId: "me"},
		{ChannelId: "dm1", UserId: "me"}, // dup channel → partner not added twice
		{ChannelId: "dm2", UserId: "me"},
		{ChannelId: "missing", UserId: "me"}, // unknown channel → ignored
	}
	got := digestPartnerIDs(posts, chans, "me")
	want := []string{"alice", "bob"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestPrintDigest(t *testing.T) {
	chans := map[string]*model.Channel{
		"c1": {Id: "c1", Type: model.ChannelTypeOpen, TeamId: "t1", Name: "general"},
		"dm": {Id: "dm", Type: model.ChannelTypeDirect, Name: "me__u2"},
	}
	lbl := labeler{
		meID:     "me",
		teamSlug: map[string]string{"t1": "eng"},
		channels: chans,
		names:    map[string]string{"me": "corne", "u2": "bob"},
	}
	names := map[string]string{"me": "corne", "u2": "bob"}

	// Two channels; the DM is more recently active (09:30 > 09:10), so it
	// heads the digest. Posts within a group stay oldest→newest.
	posts := []*model.Post{
		{ChannelId: "c1", UserId: "me", Message: "ship it", CreateAt: ms(t, "09:00")},
		{ChannelId: "c1", UserId: "me", Message: "done", CreateAt: ms(t, "09:10")},
		{ChannelId: "dm", UserId: "me", Message: "hey", CreateAt: ms(t, "09:30")},
	}

	var buf bytes.Buffer
	printDigest(&buf, lbl, names, posts)

	got := buf.String()
	// DM group first (more recent), then the channel group, blank line between.
	for _, want := range []string{
		"@bob · 1 message\n",
		"eng/general · 2 messages\n",
		"@corne  hey",
		"@corne  ship it",
		"@corne  done",
	} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Errorf("digest missing %q in:\n%s", want, got)
		}
	}
	// Ordering: DM header appears before the channel header.
	if i, j := bytes.Index([]byte(got), []byte("@bob ·")), bytes.Index([]byte(got), []byte("eng/general ·")); i < 0 || j < 0 || i > j {
		t.Errorf("want @bob group before eng/general group:\n%s", got)
	}
}
