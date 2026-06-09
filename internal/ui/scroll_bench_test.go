package ui

import (
	"fmt"
	"strconv"
	"testing"

	"charm.land/bubbles/v2/viewport"
	"github.com/mattermost/mattermost/server/public/model"
)

// benchPosts builds n realistic-ish posts with varied bodies (some
// markdown, some multi-line) so renderPostLines does representative work.
func benchPosts(n int) ([]*model.Post, map[string]string) {
	posts := make([]*model.Post, n)
	names := map[string]string{}
	bodies := []string{
		"hey, can you take a look at this when you get a chance?",
		"Sure — here's the **summary**:\n- point one\n- point two\n- a third, longer point that wraps across the viewport width comfortably",
		"see https://example.com/some/long/path?query=1&other=2 for details",
		"```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```",
		"> quoted reply\nand a follow-up line with `inline code` and *emphasis*",
		"shorter one",
	}
	for i := 0; i < n; i++ {
		uid := "user" + strconv.Itoa(i%12)
		names[uid] = "person" + strconv.Itoa(i%12)
		posts[i] = &model.Post{
			Id:       fmt.Sprintf("post-%06d", i),
			UserId:   uid,
			Message:  bodies[i%len(bodies)],
			CreateAt: int64(1_700_000_000_000 + i*60_000),
			UpdateAt: int64(1_700_000_000_000 + i*60_000),
		}
	}
	return posts, names
}

func newScrollBenchModel(n int) Model {
	posts, names := benchPosts(n)
	vp := viewport.New()
	vp.SoftWrap = true
	vp.SetWidth(80)
	vp.SetHeight(40)
	return Model{
		posts:     posts,
		postIdx:   n - 1,
		userNames: names,
		focus:     focusMessages,
		width:     100,
		height:    44,
		msgsView:  vp,
	}
}

// BenchmarkRenderMessages measures the cost of a single renderMessages
// call — i.e. one arrow keypress while scrolling — as the number of
// loaded posts grows. Scrolling up prepends 60 cached posts at a time
// (loadOlderFromStore), so m.posts keeps growing during a session; this
// shows how per-keystroke cost scales with it.
func BenchmarkRenderMessages(b *testing.B) {
	for _, n := range []int{60, 240, 600, 1200, 3000} {
		b.Run(fmt.Sprintf("posts=%d", n), func(b *testing.B) {
			m := newScrollBenchModel(n)
			// Warm the post-line cache so we measure steady-state scrolling,
			// not first-paint markdown rendering.
			m.renderMessages()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Move the selection like the Up key does, then re-render.
				if m.postIdx > 0 {
					m.postIdx--
				} else {
					m.postIdx = n - 1
				}
				m.renderMessages()
			}
		})
	}
}
