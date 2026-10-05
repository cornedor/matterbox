package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// TestReactionReachesThreadCopy: the thread pane fetches its own copies of
// posts, so a reaction must land on every copy, not just the transcript's.
func TestReactionReachesThreadCopy(t *testing.T) {
	m := Model{
		posts:       []*model.Post{{Id: "r1", ChannelId: "c1"}},
		threadPosts: []*model.Post{{Id: "r1", ChannelId: "c1"}},
	}
	m.addLocalReaction("r1", "u2", "+1")
	for name, p := range map[string]*model.Post{"transcript": m.posts[0], "thread": m.threadPosts[0]} {
		if p.Metadata == nil || len(p.Metadata.Reactions) != 1 {
			t.Errorf("%s copy missed the reaction", name)
		}
	}
	m.removeLocalReaction("r1", "u2", "+1")
	for name, p := range map[string]*model.Post{"transcript": m.posts[0], "thread": m.threadPosts[0]} {
		if len(p.Metadata.Reactions) != 0 {
			t.Errorf("%s copy kept the removed reaction", name)
		}
	}
}
