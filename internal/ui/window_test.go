package ui

import (
	"fmt"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func windowPosts(n int) []*model.Post {
	posts := make([]*model.Post, n)
	for i := range posts {
		posts[i] = &model.Post{Id: fmt.Sprintf("p%d", i)}
	}
	return posts
}

func TestTrimPostWindowTail(t *testing.T) {
	n := maxLoadedPosts + 130
	m := Model{posts: windowPosts(n), postIdx: 40}
	sel := m.posts[m.postIdx]

	m.trimPostWindowTail()

	if len(m.posts) != maxLoadedPosts {
		t.Fatalf("len = %d; want %d", len(m.posts), maxLoadedPosts)
	}
	if m.posts[m.postIdx] != sel {
		t.Errorf("selection moved after tail trim")
	}
	if m.posts[0].Id != "p0" {
		t.Errorf("oldest post changed: got %s; want p0", m.posts[0].Id)
	}
}

func TestTrimPostWindowTailKeepsOptimisticStub(t *testing.T) {
	// An unpersisted optimistic stub (empty Id) sits at the newest end and
	// can't be recovered from the store, so trimming must halt before it
	// rather than drop it — even though that leaves the window above cap.
	n := maxLoadedPosts + 50
	posts := windowPosts(n)
	posts[n-1] = &model.Post{Id: ""} // stub at the very end
	m := Model{posts: posts, postIdx: 10}

	m.trimPostWindowTail()

	if got := m.posts[len(m.posts)-1].Id; got != "" {
		t.Errorf("optimistic stub was trimmed (tail id = %q)", got)
	}
	if len(m.posts) != n {
		t.Errorf("trimmed past the stub: len = %d; want %d", len(m.posts), n)
	}
}

func TestTrimPostWindowHead(t *testing.T) {
	n := maxLoadedPosts + 60
	m := Model{posts: windowPosts(n), postIdx: n - 1} // selection at bottom
	sel := m.posts[m.postIdx]

	m.trimPostWindowHead()

	if len(m.posts) != maxLoadedPosts {
		t.Fatalf("len = %d; want %d", len(m.posts), maxLoadedPosts)
	}
	if m.posts[m.postIdx] != sel {
		t.Errorf("selection not preserved after head trim")
	}
	if want := fmt.Sprintf("p%d", n-1); m.posts[len(m.posts)-1].Id != want {
		t.Errorf("newest post lost: got %s; want %s", m.posts[len(m.posts)-1].Id, want)
	}
}

func TestTrimPostWindowHeadClampsToSelection(t *testing.T) {
	// With the selection near the top, head-trim must not drop it even if
	// that leaves the window above cap; it pages down later.
	n := maxLoadedPosts + 60
	m := Model{posts: windowPosts(n), postIdx: 5}
	sel := m.posts[5]

	m.trimPostWindowHead()

	if m.postIdx < 0 || m.posts[m.postIdx] != sel {
		t.Errorf("selection dropped by head trim (postIdx = %d)", m.postIdx)
	}
}
