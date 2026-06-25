package ui

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/viewport"
)

// benchTableBodies returns message bodies of which a configurable fraction are
// GFM tables, so renderPostLines / renderMessages does table layout work that
// mirrors selecting through a channel full of tables.
func benchTableBodies() []string {
	table := "| Service | Status | Latency | Owner |\n" +
		"| --- | --- | --- | --- |\n" +
		"| auth | up | 12ms | alice |\n" +
		"| billing | degraded | 240ms | bob |\n" +
		"| search | up | 33ms | carol |\n" +
		"| notify | down | n/a | dave |\n" +
		"| cache | up | 4ms | erin |\n" +
		"| gateway | up | 18ms | frank |"
	return []string{
		table,
		"hey, can you take a look at this when you get a chance?",
		table,
		"Sure — here's the **summary**:\n- point one\n- point two",
		table,
		"shorter one",
	}
}

// benchTablePosts builds n posts where ~half carry a GFM table.
func benchTablePosts(n int) ([]*model.Post, map[string]string) {
	posts := make([]*model.Post, n)
	names := map[string]string{}
	bodies := benchTableBodies()
	for i := 0; i < n; i++ {
		uid := "user" + strconv.Itoa(i%12)
		names[uid] = "person" + strconv.Itoa(i%12)
		posts[i] = &model.Post{
			Id:       fmt.Sprintf("tpost-%06d", i),
			UserId:   uid,
			Message:  bodies[i%len(bodies)],
			CreateAt: int64(1_700_000_000_000 + i*60_000),
			UpdateAt: int64(1_700_000_000_000 + i*60_000),
		}
	}
	return posts, names
}

func newTableSelBenchModel(n int) Model {
	posts, names := benchTablePosts(n)
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

// BenchmarkRenderMessagesTables mirrors BenchmarkRenderMessages but with a
// channel half-full of GFM tables: one renderMessages call per arrow keypress
// while the selection moves through table-heavy history. Compare directly
// against BenchmarkRenderMessages to isolate the marginal cost of tables on the
// selection hot path.
func BenchmarkRenderMessagesTables(b *testing.B) {
	for _, n := range []int{60, 240, 600, 1200} {
		b.Run(fmt.Sprintf("posts=%d", n), func(b *testing.B) {
			m := newTableSelBenchModel(n)
			// Warm the post-line cache so we measure steady-state selection,
			// not first-paint markdown/table rendering.
			m.renderMessages()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
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
