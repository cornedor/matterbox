package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// fakeReactor records AddReaction calls so the react loop can be tested
// without a Mattermost server.
type fakeReactor struct {
	addErr error

	gotUser  string
	gotPost  string
	gotEmoji []string
}

func (f *fakeReactor) AddReaction(_ context.Context, userID, postID, emojiName string) error {
	f.gotUser = userID
	f.gotPost = postID
	f.gotEmoji = append(f.gotEmoji, emojiName)
	return f.addErr
}

func TestReact(t *testing.T) {
	me := &model.User{Id: "me123"}

	t.Run("adds a reaction", func(t *testing.T) {
		f := &fakeReactor{}
		var out bytes.Buffer
		if err := react(context.Background(), f, me, "post1", []string{"tada"}, &out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.gotUser != "me123" || f.gotPost != "post1" {
			t.Errorf("AddReaction(user=%q, post=%q), want (me123, post1)", f.gotUser, f.gotPost)
		}
		if len(f.gotEmoji) != 1 || f.gotEmoji[0] != "tada" {
			t.Errorf("emoji = %v, want [tada]", f.gotEmoji)
		}
		if !strings.Contains(out.String(), ":tada:") || !strings.Contains(out.String(), "post1") {
			t.Errorf("output %q should confirm :tada: on post1", out.String())
		}
	})

	t.Run("strips surrounding colons and whitespace", func(t *testing.T) {
		f := &fakeReactor{}
		var out bytes.Buffer
		if err := react(context.Background(), f, me, "post1", []string{" :+1: "}, &out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(f.gotEmoji) != 1 || f.gotEmoji[0] != "+1" {
			t.Errorf("emoji = %v, want [+1] (colons and spaces stripped)", f.gotEmoji)
		}
	})

	t.Run("adds several emoji in order", func(t *testing.T) {
		f := &fakeReactor{}
		var out bytes.Buffer
		if err := react(context.Background(), f, me, "post1", []string{"eyes", "rocket"}, &out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{"eyes", "rocket"}
		if len(f.gotEmoji) != len(want) || f.gotEmoji[0] != want[0] || f.gotEmoji[1] != want[1] {
			t.Errorf("emoji = %v, want %v", f.gotEmoji, want)
		}
	})

	t.Run("rejects an empty message id", func(t *testing.T) {
		f := &fakeReactor{}
		var out bytes.Buffer
		if err := react(context.Background(), f, me, "  ", []string{"tada"}, &out); err == nil {
			t.Fatal("expected error for empty message id, got nil")
		}
		if len(f.gotEmoji) != 0 {
			t.Errorf("nothing should be reacted for an empty id, got %v", f.gotEmoji)
		}
	})

	t.Run("rejects an emoji that is only colons", func(t *testing.T) {
		f := &fakeReactor{}
		var out bytes.Buffer
		if err := react(context.Background(), f, me, "post1", []string{"::"}, &out); err == nil {
			t.Fatal("expected error for empty emoji name, got nil")
		}
		if len(f.gotEmoji) != 0 {
			t.Errorf("nothing should be reacted for an empty emoji, got %v", f.gotEmoji)
		}
	})

	t.Run("stops at the first emoji the server rejects", func(t *testing.T) {
		sentinel := errors.New("no such emoji")
		f := &fakeReactor{addErr: sentinel}
		var out bytes.Buffer
		err := react(context.Background(), f, me, "post1", []string{"nope", "tada"}, &out)
		if !errors.Is(err, sentinel) {
			t.Errorf("error = %v, want it to wrap %v", err, sentinel)
		}
		// The loop stops at the first failure, so the second emoji is never tried.
		if len(f.gotEmoji) != 1 {
			t.Errorf("attempted %d emoji, want 1 (stop at first failure): %v", len(f.gotEmoji), f.gotEmoji)
		}
	})
}
