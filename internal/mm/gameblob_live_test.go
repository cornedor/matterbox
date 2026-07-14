package mm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"matterbox/internal/auth"
	"matterbox/internal/config"
	"matterbox/internal/game"
)

// TestGameBlobSurvivesTheServer is the load-bearing assumption behind the
// in-message game transport: that Mattermost stores and returns a post body
// byte-for-byte, including the invisible variation selectors the game state
// rides in. If the server normalizes (NFC would fold them), sanitizes, or the
// database collation mangles them, the whole design has to fall back to base64
// in a code fence — so this is checked against a real server, not a mock.
//
// It is gated on MATTERBOX_LIVE because it posts to the live Mattermost the
// user is logged into. It posts to their own self-DM, which nobody else can see,
// and deletes the post afterwards.
//
//	MATTERBOX_LIVE=1 go test ./internal/mm -run TestGameBlobSurvivesTheServer -v
func TestGameBlobSurvivesTheServer(t *testing.T) {
	if os.Getenv("MATTERBOX_LIVE") != "1" {
		t.Skip("live server test; set MATTERBOX_LIVE=1 to run")
	}

	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	token, err := auth.ReadToken()
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	c := New(cfg.ServerURL, token)

	me, err := c.Me(ctx)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	// A self-DM: the one channel where junk test posts bother nobody.
	ch, _, err := c.c.CreateDirectChannel(ctx, me.Id, me.Id)
	if err != nil {
		t.Fatalf("open self-DM: %v", err)
	}
	t.Logf("server=%s user=%s self-DM=%s", cfg.ServerURL, me.Username, ch.Id)

	// Every byte value, so a server that mangles any single one of the 256
	// selectors is caught rather than a lucky subset passing.
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}
	body := func(p []byte, note string) string {
		return "\U0001F4A3 matterbox game transport probe — " + note + "\n" +
			"```\n(this post carries an invisible binary payload)\n```\n" +
			game.Encode(p)
	}

	post, err := c.Send(ctx, ch.Id, "", body(payload, "create"), nil)
	if err != nil {
		t.Fatalf("send probe post: %v", err)
	}
	t.Cleanup(func() {
		if err := c.DeletePost(context.Background(), post.Id); err != nil {
			t.Logf("cleanup: delete probe post %s: %v", post.Id, err)
		}
	})

	// The create response is the server echoing our own request back, so it can
	// pass while stored data is mangled. Re-fetch to see what was actually kept.
	got, err := c.Post(ctx, post.Id)
	if err != nil {
		t.Fatalf("re-fetch after create: %v", err)
	}
	assertBlob(t, "after create+fetch", got.Message, payload)

	// The game streams frames with EditPost (PATCH), not Send. Prove the same
	// bytes survive that path — a PATCH can normalize where a POST did not.
	edited := make([]byte, 256)
	for i := range edited {
		edited[i] = byte(255 - i) // a different payload, so a stale read can't pass
	}
	if _, err := c.EditPost(ctx, post.Id, body(edited, "edit")); err != nil {
		t.Fatalf("edit probe post: %v", err)
	}
	got, err = c.Post(ctx, post.Id)
	if err != nil {
		t.Fatalf("re-fetch after edit: %v", err)
	}
	assertBlob(t, "after edit+fetch", got.Message, edited)

	// The visible part must come back intact too — the fenced ASCII board is what
	// users on the official clients see.
	if want := "matterbox game transport probe"; !bytes.Contains([]byte(got.Message), []byte(want)) {
		t.Errorf("visible text was lost; body = %q", got.Message)
	}

	// Sanity-check the streaming budget: the banana flight edits this post ~30×/s.
	// This measures round-trip PATCH latency, not throughput, but if a single edit
	// takes longer than a frame we know to lower the tick rate or fire and forget.
	const frames = 10
	start := time.Now()
	for i := range frames {
		if _, err := c.EditPost(ctx, post.Id, body(edited, fmt.Sprintf("frame %d", i))); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	per := time.Since(start) / frames
	t.Logf("edit round-trip: %v/frame (%.1f fps ceiling if serialized)", per, float64(time.Second)/float64(per))
	if per > 33*time.Millisecond {
		t.Logf("NOTE: slower than a 30fps frame budget — stream frames without awaiting the response, or drop the tick rate")
	}
}

func assertBlob(t *testing.T, stage, body string, want []byte) {
	t.Helper()
	got, ok := game.Decode(body)
	if !ok {
		t.Fatalf("%s: no payload survived; the server ate the blob.\nbody = %q", stage, body)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: payload corrupted.\n got %d bytes: %v\nwant %d bytes: %v", stage, len(got), got, len(want), want)
	}
	t.Logf("%s: all %d bytes round-tripped intact", stage, len(want))
}
