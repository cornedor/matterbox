package mm

import (
	"context"
	"os"
	"testing"
	"time"

	"matterbox/internal/auth"
	"matterbox/internal/config"
	"matterbox/internal/game"
)

// TestGorillasStreamsAShotThroughTheServer plays a real banana through a real
// Mattermost, one PATCH per frame, and reads the result back off the server.
//
// This is the end-to-end proof the TUI cannot give: that a whole shot's worth of
// state survives the round trip, that the crater the host carved is visible to
// anyone who fetches the post, and that the frame rate the wire actually
// sustains is the one the game assumes (gorillasFrameDelay = 33ms).
//
// Gated on MATTERBOX_LIVE. It posts to the user's own self-DM, which nobody else
// can see, and deletes the post afterwards.
//
//	MATTERBOX_LIVE=1 go test ./internal/mm -run TestGorillasStreams -v
func TestGorillasStreamsAShotThroughTheServer(t *testing.T) {
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
	ch, _, err := c.c.CreateDirectChannel(ctx, me.Id, me.Id)
	if err != nil {
		t.Fatalf("open self-DM: %v", err)
	}

	// A match, and a shot aimed to actually land on something.
	mt := game.NewMatch(42)
	mt.Join(me.Id)

	body := func() string {
		return "🎮 **Gorillas** — live stream test\n" +
			game.ASCIIBoard(mt.World, mt.Shot, 64, 18) + "\n" +
			game.Encode(game.MarshalState(mt.State))
	}

	post, err := c.Send(ctx, ch.Id, "", body(), nil)
	if err != nil {
		t.Fatalf("post the game: %v", err)
	}
	t.Cleanup(func() {
		if err := c.DeletePost(context.Background(), post.Id); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})

	// A shot that lands. A miss would stream just as well, but it carves no
	// crater — and the crater is the only destructible state on the wire, so a
	// miss would leave the most important half of the format untested.
	mt.Launch(0, 45, 40)

	frames := 0
	start := time.Now()
	var last game.Event
	for range 500 {
		last = mt.Step(0.05)
		if last.Kind == game.EvNothing {
			break
		}
		if _, err := c.EditPost(ctx, post.Id, body()); err != nil {
			t.Fatalf("frame %d: %v", frames, err)
		}
		frames++
		if last.Kind != game.EvFlying {
			break
		}
	}
	elapsed := time.Since(start)

	if frames == 0 {
		t.Fatal("the shot streamed no frames at all")
	}
	t.Logf("streamed %d frames in %v (%.1f fps sustained) — shot ended as %v",
		frames, elapsed.Round(time.Millisecond), float64(frames)/elapsed.Seconds(), last.Kind)

	// The game paces itself at gorillasFrameDelay (33ms). If the wire cannot even
	// sustain that when hammered flat out, the flight will run in slow motion.
	if per := elapsed / time.Duration(frames); per > 33*time.Millisecond {
		t.Errorf("the wire sustained only %v/frame; the game asks for 33ms", per.Round(time.Millisecond))
	}

	// Now the part that matters: what a *reader* of that post sees. Fetch it back
	// cold and rebuild the world from nothing but the body.
	got, err := c.Post(ctx, post.Id)
	if err != nil {
		t.Fatalf("re-fetch: %v", err)
	}
	payload, ok := game.Decode(got.Message)
	if !ok {
		t.Fatal("the streamed post carries no readable state")
	}
	st, err := game.UnmarshalState(payload)
	if err != nil {
		t.Fatalf("a reader cannot parse the streamed state: %v", err)
	}

	if st.Seed != mt.State.Seed {
		t.Errorf("seed came back %d, want %d", st.Seed, mt.State.Seed)
	}
	if len(st.Craters) != len(mt.State.Craters) {
		t.Errorf("the reader sees %d craters, the host carved %d",
			len(st.Craters), len(mt.State.Craters))
	}
	if st.Scores != mt.State.Scores {
		t.Errorf("scores came back %v, want %v", st.Scores, mt.State.Scores)
	}

	// A reader rebuilds the same city, crater for crater, from a seed and a few
	// hundred bytes. That is the whole design in one assertion.
	rw := st.World()
	hw := mt.World
	if len(rw.Buildings) != len(hw.Buildings) {
		t.Fatalf("the reader rebuilt %d buildings, the host has %d",
			len(rw.Buildings), len(hw.Buildings))
	}
	diff := 0
	for y := 0; y < game.FieldH; y += 2 {
		for x := 0; x < game.FieldW; x += 2 {
			if rw.Solid(x, y) != hw.Solid(x, y) {
				diff++
			}
		}
	}
	if diff != 0 {
		t.Fatalf("the reader's city differs from the host's at %d sampled points", diff)
	}
	t.Logf("a cold reader rebuilt the host's city exactly, from %d bytes of post body", len(payload))
}
