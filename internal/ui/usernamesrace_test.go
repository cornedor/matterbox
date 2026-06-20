package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
)

// TestUserNamesNoRaceWithCmdClosures is the regression test for issue #2:
// `m.userNames` was read inside tea.Cmd closures (which Bubble Tea runs on
// worker goroutines) while Update wrote the same map on the main loop, which
// Go promotes to a hard panic ("concurrent map read and map write").
//
// It mirrors Bubble Tea's execution model precisely: a single "update loop"
// goroutine (here, the test goroutine) builds the Cmds and mutates the model —
// those two never race each other because they're serialized — and each Cmd's
// closure runs on its own worker goroutine. The fix snapshots m.userNames on
// the update-loop goroutine before the closure is handed off, so the worker
// reads a private copy instead of the live map.
//
// Run under `-race`. Before the fix this trips the detector (the worker's map
// read overlaps the update loop's writes); after it, it's clean. The msg
// assertions keep the test honest — if the fake server 404'd, the fetch would
// bail with errMsg before resolveSenderNames ever touched the map, and the
// test would pass vacuously.
func TestUserNamesNoRaceWithCmdClosures(t *testing.T) {
	mustJSON := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return b
	}
	// author1 is deliberately absent from the seeded userNames below so every
	// fetch's resolveSenderNames builds a non-empty "need" set and also makes
	// the UsersByIDs round-trip — keeping the worker alive (and reading the
	// map) for longer, which widens the window a regression would race in.
	postListJSON := mustJSON(model.PostList{
		Order: []string{"p1"},
		Posts: map[string]*model.Post{
			"p1": {Id: "p1", UserId: "author1", ChannelId: "c", CreateAt: 1000, Message: "hi"},
		},
	})
	usersJSON := mustJSON([]*model.User{{Id: "author1", Username: "resolved"}})
	membersJSON := mustJSON(model.ChannelMembersWithTeamData{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/users/ids"):
			_, _ = w.Write(usersJSON)
		case strings.HasSuffix(r.URL.Path, "/channel_members"):
			_, _ = w.Write(membersJSON)
		case strings.HasSuffix(r.URL.Path, "/posts"):
			_, _ = w.Write(postListJSON)
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	m := Model{
		client:    mm.New(srv.URL, "token"),
		ctx:       context.Background(),
		me:        &model.User{Id: "me", Username: "me"},
		userNames: map[string]string{"me": "me"},
		// store stays nil: fetchFeed's persist/context-load branches are guarded
		// by `if m.store != nil`, so the feed path still reaches the snapshotted
		// userNames read without needing a database.
	}

	const iters = 200
	var wg sync.WaitGroup
	msgs := make(chan tea.Msg, iters*2)

	run := func(cmd tea.Cmd) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msgs <- cmd()
		}()
	}

	// The test goroutine plays the single Update loop: it serially takes the
	// snapshot (inside each fetch*) and writes m.userNames, then hands each
	// closure to a worker. With the bug present, the workers' map reads race
	// these writes.
	feedTargets := []feedTarget{{channelID: "c", lastViewedAt: 1, unreadCount: 1}}
	for i := 0; i < iters; i++ {
		run(m.fetchPosts("c"))
		run(m.fetchFeed(i, feedTargets))
		m.userNames[fmt.Sprintf("late%d", i)] = "v"
	}

	go func() { wg.Wait(); close(msgs) }()

	var posts, feeds int
	for msg := range msgs {
		switch v := msg.(type) {
		case errMsg:
			t.Errorf("fetch failed (fake server misrouted?): %v", v.err)
		case postsLoadedMsg:
			posts++
			if v.users["author1"] != "resolved" {
				t.Errorf("sender not resolved from snapshot: %+v", v.users)
			}
		case feedLoadedMsg:
			feeds++
		}
	}
	if posts == 0 || feeds == 0 {
		t.Fatalf("expected both fetch paths to run; got posts=%d feeds=%d", posts, feeds)
	}
}
