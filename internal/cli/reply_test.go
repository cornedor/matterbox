package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/hidden"
	"matterbox/internal/replyto"
)

// fakeReplier records Thread/Send calls so the reply threading logic can be
// tested without a Mattermost server. thread is the PostList returned by Thread.
type fakeReplier struct {
	thread    *model.PostList
	threadErr error
	sendErr   error

	gotThreadID string
	gotChannel  string
	gotRoot     string
	gotMessage  string
}

func (f *fakeReplier) Thread(_ context.Context, postID string) (*model.PostList, error) {
	f.gotThreadID = postID
	return f.thread, f.threadErr
}

func (f *fakeReplier) Send(_ context.Context, channelID, rootID, message string, _ []string) (*model.Post, error) {
	f.gotChannel, f.gotRoot, f.gotMessage = channelID, rootID, message
	return &model.Post{Id: "new"}, f.sendErr
}

// plWith builds a single-post PostList keyed by id, as Thread returns it.
func plWith(p *model.Post) *model.PostList {
	return &model.PostList{Order: []string{p.Id}, Posts: map[string]*model.Post{p.Id: p}}
}

func TestReply(t *testing.T) {
	t.Run("roots the reply at a top-level message", func(t *testing.T) {
		// A root post (no RootId): the reply's root is the post's own id.
		f := &fakeReplier{thread: plWith(&model.Post{Id: "root1", ChannelId: "chanA"})}
		var out bytes.Buffer
		if err := reply(context.Background(), f, "root1", "on it", &out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.gotChannel != "chanA" {
			t.Errorf("sent to channel %q, want chanA", f.gotChannel)
		}
		if f.gotRoot != "root1" {
			t.Errorf("reply root = %q, want root1 (the message itself)", f.gotRoot)
		}
		if f.gotMessage != "on it" {
			t.Errorf("message = %q, want \"on it\"", f.gotMessage)
		}
	})

	t.Run("joins the existing thread when replying to a reply", func(t *testing.T) {
		// The target is itself a reply (RootId set): the new reply reuses that
		// root, so it lands in the same thread rather than nesting off the reply.
		f := &fakeReplier{thread: plWith(&model.Post{Id: "reply9", RootId: "root1", ChannelId: "chanA"})}
		var out bytes.Buffer
		if err := reply(context.Background(), f, "reply9", "me too", &out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.gotRoot != "root1" {
			t.Errorf("reply root = %q, want root1 (the existing thread root)", f.gotRoot)
		}
	})

	t.Run("rejects an empty message id", func(t *testing.T) {
		f := &fakeReplier{}
		if err := reply(context.Background(), f, "  ", "hi", &bytes.Buffer{}); err == nil {
			t.Fatal("expected error for empty message id, got nil")
		}
		if f.gotThreadID != "" {
			t.Error("Thread should not be called for an empty id")
		}
	})

	t.Run("errors when the post is not found", func(t *testing.T) {
		f := &fakeReplier{thread: &model.PostList{Posts: map[string]*model.Post{}}}
		if err := reply(context.Background(), f, "ghost", "hi", &bytes.Buffer{}); err == nil {
			t.Fatal("expected error for an unknown post, got nil")
		}
	})

	t.Run("propagates a send error", func(t *testing.T) {
		sentinel := errors.New("boom")
		f := &fakeReplier{
			thread:  plWith(&model.Post{Id: "root1", ChannelId: "chanA"}),
			sendErr: sentinel,
		}
		err := reply(context.Background(), f, "root1", "hi", &bytes.Buffer{})
		if !errors.Is(err, sentinel) {
			t.Errorf("error = %v, want it to wrap %v", err, sentinel)
		}
	})

	t.Run("confirms the thread it replied in", func(t *testing.T) {
		f := &fakeReplier{thread: plWith(&model.Post{Id: "root1", ChannelId: "chanA"})}
		var out bytes.Buffer
		if err := reply(context.Background(), f, "root1", "hi", &out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out.String(), "root1") {
			t.Errorf("output %q should name the thread root", out.String())
		}
	})
}

// Replying to a reply records which reply it answers, so a matterbox reader can
// nest it — while the visible body stays exactly what the caller wrote.
func TestReplyToAReplyRecordsItsParent(t *testing.T) {
	f := &fakeReplier{thread: plWith(&model.Post{Id: "reply9", RootId: "root1", ChannelId: "chanA"})}
	if err := reply(context.Background(), f, "reply9", "on it", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if f.gotRoot != "root1" {
		t.Fatalf("reply root = %q, want root1", f.gotRoot)
	}
	if got, ok := replyto.Parse(f.gotMessage); !ok || got != "reply9" {
		t.Fatalf("parent = %q, %v; want reply9", got, ok)
	}
	if vis := hidden.Strip(f.gotMessage); vis != "on it" {
		t.Fatalf("visible body = %q, want it unchanged", vis)
	}
}

// Replying to a thread root does not: RootId already says it, and a redundant
// reference is one more thing that can go stale.
func TestReplyToARootRecordsNothing(t *testing.T) {
	f := &fakeReplier{thread: plWith(&model.Post{Id: "root1", ChannelId: "chanA"})}
	if err := reply(context.Background(), f, "root1", "on it", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if f.gotMessage != "on it" {
		t.Fatalf("body = %q, want it untouched", f.gotMessage)
	}
}
