package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// fakeResolver records the calls resolveChannel makes and returns canned
// results, so resolution can be tested without a Mattermost server.
type fakeResolver struct {
	channel *model.Channel
	user    *model.User
	dm      *model.Channel
	err     error

	gotTeam, gotChannel string
	gotUsername         string
	gotDMa, gotDMb      string
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
	if f.err != nil {
		return nil, f.err
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
