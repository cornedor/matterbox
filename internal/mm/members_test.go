package mm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// fakeAdder resolves any username to "u_"+name unless it's listed in unknown,
// and records every AddChannelMember call. Users in refuse fail to be added.
type fakeAdder struct {
	unknown map[string]bool
	refuse  map[string]bool
	added   []string // user ids, in call order
}

func (f *fakeAdder) UserByUsername(_ context.Context, name string) (*model.User, error) {
	if f.unknown[name] {
		return nil, errors.New("404 not found")
	}
	return &model.User{Id: "u_" + name, Username: name}, nil
}

func (f *fakeAdder) AddChannelMember(_ context.Context, _, userID string) error {
	if f.refuse[strings.TrimPrefix(userID, "u_")] {
		return errors.New("not on this team")
	}
	f.added = append(f.added, userID)
	return nil
}

// TestAddMembers covers the spec syntax (optional @, spaces, dedupe) and the
// happy path: every named user is added once, in the order given.
func TestAddMembers(t *testing.T) {
	f := &fakeAdder{}
	added, err := AddMembers(context.Background(), f, "c1", " @alice, bob ,@alice")
	if err != nil {
		t.Fatalf("AddMembers: %v", err)
	}
	if want := []string{"alice", "bob"}; !equal(added, want) {
		t.Errorf("added = %v, want %v (duplicate @alice collapsed)", added, want)
	}
	if want := []string{"u_alice", "u_bob"}; !equal(f.added, want) {
		t.Errorf("AddChannelMember calls = %v, want %v", f.added, want)
	}
}

// TestAddMembersUnknownUserAddsNobody: resolution happens up front, so a typo
// in the last name must not leave the earlier ones already added.
func TestAddMembersUnknownUserAddsNobody(t *testing.T) {
	f := &fakeAdder{unknown: map[string]bool{"nobody": true}}
	added, err := AddMembers(context.Background(), f, "c1", "@alice, @nobody")
	if err == nil {
		t.Fatal("AddMembers with an unknown user: err = nil, want an error")
	}
	if !strings.Contains(err.Error(), "@nobody") {
		t.Errorf("err = %v, want it to name @nobody", err)
	}
	if len(added) != 0 || len(f.added) != 0 {
		t.Errorf("added = %v / calls = %v, want nobody added", added, f.added)
	}
}

// TestAddMembersPartialFailure: a user who resolves but can't be added is
// reported by name, while the others still join.
func TestAddMembersPartialFailure(t *testing.T) {
	f := &fakeAdder{refuse: map[string]bool{"bob": true}}
	added, err := AddMembers(context.Background(), f, "c1", "@alice, @bob")
	if err == nil {
		t.Fatal("err = nil, want the refused user reported")
	}
	if !strings.Contains(err.Error(), "@bob") {
		t.Errorf("err = %v, want it to name @bob", err)
	}
	if want := []string{"alice"}; !equal(added, want) {
		t.Errorf("added = %v, want %v", added, want)
	}
}

// TestAddMembersEmptySpec rejects a blank name rather than resolving "".
func TestAddMembersEmptySpec(t *testing.T) {
	f := &fakeAdder{}
	if _, err := AddMembers(context.Background(), f, "c1", "@alice,"); err == nil {
		t.Fatal("trailing comma: err = nil, want an error")
	}
	if len(f.added) != 0 {
		t.Errorf("calls = %v, want nobody added", f.added)
	}
}

// TestClientAddChannelMember pins the wire call: POST to the channel's members
// route with a {"user_id": …} body.
func TestClientAddChannelMember(t *testing.T) {
	var gotPath, gotMethod, gotUserID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotUserID = body["user_id"]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&model.ChannelMember{ChannelId: "c1", UserId: gotUserID})
	}))
	defer srv.Close()

	if err := New(srv.URL, "tok").AddChannelMember(context.Background(), "c1", "u1"); err != nil {
		t.Fatalf("AddChannelMember: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v4/channels/c1/members" {
		t.Errorf("%s %s, want POST /api/v4/channels/c1/members", gotMethod, gotPath)
	}
	if gotUserID != "u1" {
		t.Errorf("body user_id = %q, want %q", gotUserID, "u1")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
