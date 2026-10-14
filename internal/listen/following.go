package listen

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// followLookupTimeout bounds the membership lookup a `following` condition may
// make. It runs on the ingest goroutine, so a hung server must not stall it.
const followLookupTimeout = 3 * time.Second

// threadFollowing answers the `following` condition: does the reader follow the
// thread this post belongs to (its root, or the post itself when it is one)?
//
// A "yes" is cached, because the server announces every way it can end: an
// unfollow from any client is a thread_follow_changed event (applyFollowEvent).
// A "no" is not, because the server auto-follows you into a thread when a reply
// mentions you, or when you reply yourself, and it does so before it sends the
// `posted` event and without a follow event. So a "no" is re-asked on every
// trigger and reused only across the rules evaluating that one trigger (keyed
// by its event), which is where the server's answer can't have changed.
//
// Failing towards "not following" matches what a 404 means; the error is logged.
func (e *Engine) threadFollowing(ev *model.WebSocketEvent, p *model.Post) bool {
	if e.client == nil || p == nil {
		return false
	}
	root := p.RootId
	if root == "" {
		root = p.Id
	}
	if root == "" {
		return false
	}
	e.followMu.Lock()
	if e.followed[root] {
		e.followMu.Unlock()
		return true
	}
	if e.notFollowedEv == ev && e.notFollowedRoot == root {
		e.followMu.Unlock()
		return false
	}
	e.followMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), followLookupTimeout)
	defer cancel()
	ok, err := e.client.ThreadFollowing(ctx, eventStr(ev, "team_id"), root)
	if err != nil {
		e.log.Printf("following: thread %s: %v", root, err)
	}
	e.noteFollowing(ev, root, ok)
	return ok
}

// noteFollowing records one answer: a "yes" for good, a "no" for this trigger.
func (e *Engine) noteFollowing(ev *model.WebSocketEvent, root string, ok bool) {
	e.followMu.Lock()
	defer e.followMu.Unlock()
	if ok {
		if e.followed == nil {
			e.followed = map[string]bool{}
		}
		e.followed[root] = true
	} else {
		e.notFollowedEv, e.notFollowedRoot = ev, root
	}
}

// applyFollowEvent keeps the "yes" cache current. thread_follow_changed is an
// explicit follow or unfollow from any of the reader's clients; thread_updated
// goes only to a thread's followers, so it proves a follow.
func (e *Engine) applyFollowEvent(ev *model.WebSocketEvent) {
	var root string
	following := true
	switch ev.EventType() {
	case model.WebsocketEventThreadFollowChanged:
		root = eventStr(ev, "thread_id")
		following, _ = ev.GetData()["state"].(bool)
	case model.WebsocketEventThreadUpdated:
		var th model.ThreadResponse
		if json.Unmarshal([]byte(eventStr(ev, "thread")), &th) != nil {
			return
		}
		root = th.PostId
	}
	if root == "" {
		return
	}
	e.followMu.Lock()
	defer e.followMu.Unlock()
	if following {
		if e.followed == nil {
			e.followed = map[string]bool{}
		}
		e.followed[root] = true
	} else {
		delete(e.followed, root)
	}
	if e.notFollowedRoot == root {
		e.notFollowedEv, e.notFollowedRoot = nil, ""
	}
}

// resetFollowed forgets every cached follow. Called on (re)connect: an unfollow
// announced while the socket was down never reaches us.
func (e *Engine) resetFollowed() {
	e.followMu.Lock()
	defer e.followMu.Unlock()
	e.followed = map[string]bool{}
	e.notFollowedEv, e.notFollowedRoot = nil, ""
}
