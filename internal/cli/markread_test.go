package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// fakeViewer is a fakeResolver that also records ViewChannel calls, so the
// mark-read loop can be tested without a Mattermost server.
type fakeViewer struct {
	fakeResolver
	viewErr  error
	gotUser  string
	viewedCh []string
}

func (f *fakeViewer) ViewChannel(_ context.Context, userID, channelID string) error {
	f.gotUser = userID
	f.viewedCh = append(f.viewedCh, channelID)
	return f.viewErr
}

func TestMarkRead(t *testing.T) {
	me := &model.User{Id: "me123"}

	t.Run("marks a team channel read", func(t *testing.T) {
		f := &fakeViewer{}
		f.channel = &model.Channel{Id: "chan1"}
		var out bytes.Buffer
		if err := markRead(context.Background(), f, me, []string{"eng/general"}, &out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(f.viewedCh) != 1 || f.viewedCh[0] != "chan1" {
			t.Errorf("viewed channels = %v, want [chan1]", f.viewedCh)
		}
		if f.gotUser != "me123" {
			t.Errorf("ViewChannel user = %q, want me123", f.gotUser)
		}
		if !strings.Contains(out.String(), "eng/general") {
			t.Errorf("output %q should confirm eng/general", out.String())
		}
	})

	t.Run("marks a DM read", func(t *testing.T) {
		f := &fakeViewer{}
		f.user = &model.User{Id: "u456"}
		f.dm = &model.Channel{Id: "dm1"}
		var out bytes.Buffer
		if err := markRead(context.Background(), f, me, []string{"@alice"}, &out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(f.viewedCh) != 1 || f.viewedCh[0] != "dm1" {
			t.Errorf("viewed channels = %v, want [dm1]", f.viewedCh)
		}
	})

	t.Run("marks several channels read in order", func(t *testing.T) {
		f := &fakeViewer{}
		f.channel = &model.Channel{Id: "chan1"}
		var out bytes.Buffer
		if err := markRead(context.Background(), f, me, []string{"eng/general", "eng/random"}, &out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(f.viewedCh) != 2 {
			t.Errorf("viewed %d channels, want 2 (%v)", len(f.viewedCh), f.viewedCh)
		}
	})

	t.Run("stops at the first unresolved spec", func(t *testing.T) {
		f := &fakeViewer{}
		f.err = errors.New("no such channel")
		var out bytes.Buffer
		if err := markRead(context.Background(), f, me, []string{"eng/nope", "eng/general"}, &out); err == nil {
			t.Fatal("expected error for unresolved spec, got nil")
		}
		if len(f.viewedCh) != 0 {
			t.Errorf("nothing should be viewed when resolution fails, got %v", f.viewedCh)
		}
	})

	t.Run("propagates a view error", func(t *testing.T) {
		sentinel := errors.New("boom")
		f := &fakeViewer{viewErr: sentinel}
		f.channel = &model.Channel{Id: "chan1"}
		var out bytes.Buffer
		err := markRead(context.Background(), f, me, []string{"eng/general"}, &out)
		if !errors.Is(err, sentinel) {
			t.Errorf("error = %v, want it to wrap %v", err, sentinel)
		}
	})
}
