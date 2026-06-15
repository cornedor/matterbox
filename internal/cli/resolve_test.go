package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// fakeResolver records the calls resolveChannel makes and returns canned
// results, so resolution can be tested without a Mattermost server.
type fakeResolver struct {
	channel *model.Channel
	user    *model.User
	dm      *model.Channel
	group   *model.Channel
	err     error

	gotTeam, gotChannel string
	gotUsername         string // last username looked up
	gotUsernames        []string
	gotDMa, gotDMb      string
	gotGroupIDs         []string

	// users maps username → user for multi-user (group) resolution; when
	// set it takes precedence over the single `user` field so each @name in
	// a group spec can resolve to a distinct id.
	users map[string]*model.User
}

func (f *fakeResolver) ChannelByName(_ context.Context, team, channel string) (*model.Channel, error) {
	f.gotTeam, f.gotChannel = team, channel
	if f.err != nil {
		return nil, f.err
	}
	return f.channel, nil
}

func (f *fakeResolver) UserByUsername(_ context.Context, name string) (*model.User, error) {
	f.gotUsername = name
	f.gotUsernames = append(f.gotUsernames, name)
	if f.err != nil {
		return nil, f.err
	}
	if f.users != nil {
		if u, ok := f.users[name]; ok {
			return u, nil
		}
		return nil, errors.New("no such user")
	}
	return f.user, nil
}

func (f *fakeResolver) DirectChannel(_ context.Context, a, b string) (*model.Channel, error) {
	f.gotDMa, f.gotDMb = a, b
	if f.err != nil {
		return nil, f.err
	}
	return f.dm, nil
}

func (f *fakeResolver) GroupChannel(_ context.Context, ids []string) (*model.Channel, error) {
	f.gotGroupIDs = ids
	if f.err != nil {
		return nil, f.err
	}
	return f.group, nil
}

func TestResolveChannel(t *testing.T) {
	me := &model.User{Id: "me123"}

	t.Run("team-qualified channel", func(t *testing.T) {
		f := &fakeResolver{channel: &model.Channel{Id: "chan1"}}
		ch, err := resolveChannel(context.Background(), f, me, "eng/general")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ch.Id != "chan1" {
			t.Errorf("channel id = %q, want chan1", ch.Id)
		}
		if f.gotTeam != "eng" || f.gotChannel != "general" {
			t.Errorf("ChannelByName(%q, %q), want (eng, general)", f.gotTeam, f.gotChannel)
		}
	})

	t.Run("direct message", func(t *testing.T) {
		f := &fakeResolver{user: &model.User{Id: "u456"}, dm: &model.Channel{Id: "dm1"}}
		ch, err := resolveChannel(context.Background(), f, me, "@alice")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ch.Id != "dm1" {
			t.Errorf("channel id = %q, want dm1", ch.Id)
		}
		if f.gotUsername != "alice" {
			t.Errorf("UserByUsername(%q), want alice", f.gotUsername)
		}
		if f.gotDMa != "me123" || f.gotDMb != "u456" {
			t.Errorf("DirectChannel(%q, %q), want (me123, u456)", f.gotDMa, f.gotDMb)
		}
	})

	t.Run("group DM with two others", func(t *testing.T) {
		f := &fakeResolver{
			users: map[string]*model.User{
				"alice": {Id: "ualice"},
				"bob":   {Id: "ubob"},
			},
			group: &model.Channel{Id: "grp1"},
		}
		ch, err := resolveChannel(context.Background(), f, me, "@alice,@bob")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ch.Id != "grp1" {
			t.Errorf("channel id = %q, want grp1", ch.Id)
		}
		// me first, then the named others in order.
		want := []string{"me123", "ualice", "ubob"}
		if len(f.gotGroupIDs) != len(want) {
			t.Fatalf("GroupChannel ids = %v, want %v", f.gotGroupIDs, want)
		}
		for i := range want {
			if f.gotGroupIDs[i] != want[i] {
				t.Errorf("GroupChannel ids = %v, want %v", f.gotGroupIDs, want)
				break
			}
		}
	})

	t.Run("tolerates spaces and @ noise in group spec", func(t *testing.T) {
		f := &fakeResolver{
			users: map[string]*model.User{"alice": {Id: "ualice"}, "bob": {Id: "ubob"}},
			group: &model.Channel{Id: "grp1"},
		}
		if _, err := resolveChannel(context.Background(), f, me, "@alice, @bob"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(f.gotGroupIDs) != 3 {
			t.Errorf("GroupChannel ids = %v, want 3 members", f.gotGroupIDs)
		}
	})

	t.Run("duplicate names collapse to a DM", func(t *testing.T) {
		f := &fakeResolver{
			users: map[string]*model.User{"alice": {Id: "ualice"}},
			dm:    &model.Channel{Id: "dm1"},
		}
		ch, err := resolveChannel(context.Background(), f, me, "@alice,@alice")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ch.Id != "dm1" {
			t.Errorf("channel id = %q, want dm1 (deduped to a DM)", ch.Id)
		}
		if f.gotGroupIDs != nil {
			t.Errorf("GroupChannel should not be called when names dedupe to one other")
		}
	})

	t.Run("rejects naming only yourself", func(t *testing.T) {
		f := &fakeResolver{users: map[string]*model.User{"me": {Id: "me123"}}}
		if _, err := resolveChannel(context.Background(), f, me, "@me"); err == nil {
			t.Fatal("expected error when the spec names no other users, got nil")
		}
	})

	t.Run("rejects too-large group", func(t *testing.T) {
		// 8 others + me = 9 > ChannelGroupMaxUsers (8).
		users := map[string]*model.User{}
		var parts []string
		for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
			users[n] = &model.User{Id: "u" + n}
			parts = append(parts, "@"+n)
		}
		f := &fakeResolver{users: users, group: &model.Channel{Id: "grp"}}
		if _, err := resolveChannel(context.Background(), f, me, strings.Join(parts, ",")); err == nil {
			t.Fatal("expected error for an oversized group, got nil")
		}
		if f.gotGroupIDs != nil {
			t.Error("GroupChannel should not be called for an oversized group")
		}
	})

	t.Run("empty username inside group spec errors", func(t *testing.T) {
		f := &fakeResolver{users: map[string]*model.User{"alice": {Id: "ualice"}}}
		if _, err := resolveChannel(context.Background(), f, me, "@alice,@"); err == nil {
			t.Fatal("expected error for empty username in group spec, got nil")
		}
	})

	t.Run("rejects bare name without team", func(t *testing.T) {
		f := &fakeResolver{}
		if _, err := resolveChannel(context.Background(), f, me, "general"); err == nil {
			t.Fatal("expected error for un-qualified channel, got nil")
		}
		if f.gotChannel != "" {
			t.Error("ChannelByName should not be called for a malformed spec")
		}
	})

	t.Run("rejects empty spec", func(t *testing.T) {
		if _, err := resolveChannel(context.Background(), &fakeResolver{}, me, "   "); err == nil {
			t.Fatal("expected error for empty spec, got nil")
		}
	})

	t.Run("rejects bare @", func(t *testing.T) {
		if _, err := resolveChannel(context.Background(), &fakeResolver{}, me, "@"); err == nil {
			t.Fatal("expected error for empty username, got nil")
		}
	})

	t.Run("wraps lookup error", func(t *testing.T) {
		sentinel := errors.New("boom")
		f := &fakeResolver{err: sentinel}
		_, err := resolveChannel(context.Background(), f, me, "eng/general")
		if !errors.Is(err, sentinel) {
			t.Errorf("error = %v, want it to wrap %v", err, sentinel)
		}
	})
}
