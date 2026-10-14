package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

type fakeThreads struct {
	posts    map[string]*model.Post
	channels map[string]*model.Channel
	follows  map[string]bool
	teams    []string // teamID of every follow call, in order
}

func (f *fakeThreads) Post(_ context.Context, id string) (*model.Post, error) {
	return f.posts[id], nil
}

func (f *fakeThreads) Channel(_ context.Context, id string) (*model.Channel, error) {
	return f.channels[id], nil
}

func (f *fakeThreads) ThreadFollowing(_ context.Context, teamID, rootID string) (bool, error) {
	f.teams = append(f.teams, teamID)
	return f.follows[rootID], nil
}

func (f *fakeThreads) SetThreadFollowing(_ context.Context, teamID, rootID string, follow bool) error {
	f.teams = append(f.teams, teamID)
	f.follows[rootID] = follow
	return nil
}

// TestThreadVerbs: a reply resolves to its root and its channel's team; a DM
// root resolves to no team; status reflects follow/unfollow.
func TestThreadVerbs(t *testing.T) {
	ctx := context.Background()
	f := &fakeThreads{
		posts: map[string]*model.Post{
			"reply": {Id: "reply", RootId: "root", ChannelId: "c1"},
			"dm":    {Id: "dm", ChannelId: "d1"},
		},
		channels: map[string]*model.Channel{"c1": {Id: "c1", TeamId: "t1"}, "d1": {Id: "d1"}},
		follows:  map[string]bool{},
	}

	root, team, err := threadOf(ctx, f, "reply")
	if err != nil || root != "root" || team != "t1" {
		t.Fatalf("reply: root=%q team=%q err=%v", root, team, err)
	}
	if root, team, _ := threadOf(ctx, f, "dm"); root != "dm" || team != "" {
		t.Fatalf("dm root: root=%q team=%q", root, team)
	}

	var out bytes.Buffer
	if err := setThreadFollow(ctx, f, "root", "t1", true, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	threadStatus(ctx, f, "root", "t1", &out)
	if out.String() != "following\n" {
		t.Errorf("after follow: status %q", out.String())
	}
	setThreadFollow(ctx, f, "root", "t1", false, &out)
	out.Reset()
	threadStatus(ctx, f, "root", "t1", &out)
	if out.String() != "not following\n" {
		t.Errorf("after unfollow: status %q", out.String())
	}
}
