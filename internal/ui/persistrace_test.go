package ui

import (
	"path/filepath"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/store"
)

// TestPersistPostsSnapshots: the persist cmd runs off the UI goroutine while
// Update keeps mutating the same *Post in place (reactions, file infos). It
// must work from a snapshot taken when the cmd was built — run under -race.
func TestPersistPostsSnapshots(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "race.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	p := &model.Post{Id: "p1", ChannelId: "c1", UserId: "u1", Message: "hi", CreateAt: 1, Metadata: &model.PostMetadata{}}
	m := Model{store: st}
	cmd := m.persistPosts(p)

	done := make(chan struct{})
	go func() { cmd(); close(done) }()
	for i := 0; i < 200; i++ {
		p.Metadata.Reactions = append(p.Metadata.Reactions, &model.Reaction{UserId: "u2", PostId: "p1", EmojiName: "+1"})
	}
	<-done

	got, err := st.Post("p1")
	if err != nil || got == nil {
		t.Fatalf("load: %v %v", got, err)
	}
	if got.Metadata != nil && len(got.Metadata.Reactions) != 0 {
		n := len(got.Metadata.Reactions)
		t.Errorf("persisted %d reactions added after the cmd was built; want a snapshot (0)", n)
	}
}
