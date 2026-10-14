package listen

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/control"
	"matterbox/internal/mm"
)

// TestFollowingCondition: `following` reads the follows answer, a nil answerer
// means "not following", and the answer isn't asked for when a cheaper
// condition has already rejected the post.
func TestFollowingCondition(t *testing.T) {
	p := &model.Post{Id: "p1", RootId: "r1", ChannelId: "c1", UserId: "u-bob"}
	ev := postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@bob"})
	yes := func() bool { return true }
	no := func() bool { return false }
	cases := []struct {
		name    string
		spec    MatchSpec
		follows func() bool
		want    bool
	}{
		{"following: true, followed", MatchSpec{Following: ptrBool(true)}, yes, true},
		{"following: true, not followed", MatchSpec{Following: ptrBool(true)}, no, false},
		{"following: false, not followed", MatchSpec{Following: ptrBool(false)}, no, true},
		{"following: true, no answerer", MatchSpec{Following: ptrBool(true)}, nil, false},
		{"unset ignores it", MatchSpec{}, no, true},
		{"not: following", MatchSpec{Not: &MatchSpec{Following: ptrBool(true)}}, yes, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := compileMatch(c.spec)
			if err != nil {
				t.Fatal(err)
			}
			if got := matchPost(ev, p, m, "u-me", "corne", "", nil, nil, control.Status{}, c.follows); got != c.want {
				t.Errorf("matchPost = %v, want %v", got, c.want)
			}
		})
	}

	m, _ := compileMatch(MatchSpec{Following: ptrBool(true), DM: ptrBool(true)})
	asked := false
	matchPost(ev, p, m, "u-me", "corne", "", nil, nil, control.Status{}, func() bool { asked = true; return true })
	if asked {
		t.Error("following was asked for a post the dm condition already rejected")
	}
}

// followServer serves the thread-membership route from follows and counts the
// lookups per root.
type followServer struct {
	mu      sync.Mutex
	follows map[string]bool
	lookups map[string]int
}

func (f *followServer) count(root string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookups[root]
}

func newFollowEngine(t *testing.T, follows map[string]bool) (*Engine, *followServer) {
	t.Helper()
	fs := &followServer{follows: follows, lookups: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/users/me/teams/{team}/threads/{root}", func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		root := r.PathValue("root")
		fs.lookups[root]++
		if !fs.follows[root] {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"id":"app.user.get_thread_membership_for_user.not_found","status_code":404}`))
			return
		}
		json.NewEncoder(w).Encode(model.ThreadResponse{PostId: root})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e := &Engine{client: mm.New(srv.URL, "tok"), log: log.New(io.Discard, "", 0), followed: map[string]bool{}}
	return e, fs
}

func replyEvent(t *testing.T, id, root string) (*model.Post, *model.WebSocketEvent) {
	t.Helper()
	p := &model.Post{Id: id, RootId: root, ChannelId: "c1", UserId: "u-bob"}
	return p, postedEvent(t, p, map[string]string{"channel_type": "O", "team_id": "team1"})
}

// TestThreadFollowingCache: a "yes" is cached; a "no" is re-asked on the next
// trigger (the server may have auto-followed you since) but reused across the
// rules evaluating one trigger.
func TestThreadFollowingCache(t *testing.T) {
	e, fs := newFollowEngine(t, map[string]bool{"followed": true})

	p, ev := replyEvent(t, "a", "followed")
	if !e.threadFollowing(ev, p) {
		t.Fatal("followed thread: following = false")
	}
	p, ev = replyEvent(t, "b", "followed")
	e.threadFollowing(ev, p)
	if n := fs.count("followed"); n != 1 {
		t.Errorf("followed thread looked up %d times, want 1 (cached)", n)
	}

	p, ev = replyEvent(t, "c", "other")
	if e.threadFollowing(ev, p) || e.threadFollowing(ev, p) {
		t.Fatal("unfollowed thread: following = true")
	}
	if n := fs.count("other"); n != 1 {
		t.Errorf("one trigger looked up %d times, want 1", n)
	}
	// A mention in the next reply auto-follows you server-side, with no event
	// before the `posted` one: the next trigger must ask again.
	fs.mu.Lock()
	fs.follows["other"] = true
	fs.mu.Unlock()
	p, ev = replyEvent(t, "d", "other")
	if !e.threadFollowing(ev, p) {
		t.Error("auto-followed thread: following = false (stale \"no\")")
	}
}

// TestThreadFollowEvents: thread_updated proves a follow without a lookup, and
// thread_follow_changed false drops the cached "yes".
func TestThreadFollowEvents(t *testing.T) {
	e, fs := newFollowEngine(t, map[string]bool{})

	upd := model.NewWebSocketEvent(model.WebsocketEventThreadUpdated, "team1", "", "u-me", nil, "")
	raw, _ := json.Marshal(model.ThreadResponse{PostId: "r1"})
	upd.Add("thread", string(raw))
	e.applyFollowEvent(upd)
	p, ev := replyEvent(t, "a", "r1")
	if !e.threadFollowing(ev, p) {
		t.Fatal("after thread_updated: following = false")
	}
	if n := fs.count("r1"); n != 0 {
		t.Errorf("looked up %d times after thread_updated, want 0", n)
	}

	off := model.NewWebSocketEvent(model.WebsocketEventThreadFollowChanged, "team1", "", "u-me", nil, "")
	off.Add("thread_id", "r1")
	off.Add("state", false)
	e.applyFollowEvent(off)
	p, ev = replyEvent(t, "b", "r1")
	if e.threadFollowing(ev, p) {
		t.Error("after unfollow: following = true")
	}
}

// TestProbeFollowing: `rules test --following` answers the condition without a
// server; a made-up post is otherwise not followed (and never looked up).
func TestProbeFollowing(t *testing.T) {
	rules, err := CompileRules([]RuleSpec{{Name: "followed", Match: MatchSpec{Following: ptrBool(true)}, Actions: []ActionSpec{{Type: "log"}}}})
	if err != nil {
		t.Fatal(err)
	}
	e, fs := newFollowEngine(t, map[string]bool{})
	e.applyRuleSet(rules)

	if ex := e.Explain(ProbeSpec{Message: "hi", Thread: true}); ex[0].Matched || ex[0].Why != "following" {
		t.Errorf("no --following: matched=%v why=%q, want a following miss", ex[0].Matched, ex[0].Why)
	}
	if ex := e.Explain(ProbeSpec{Message: "hi", Thread: true, Following: true}); !ex[0].Matched {
		t.Errorf("--following: rule didn't match (why %q)", ex[0].Why)
	}
	if n := fs.count("probe-root"); n != 0 {
		t.Errorf("probe looked up the made-up thread %d times", n)
	}
}
