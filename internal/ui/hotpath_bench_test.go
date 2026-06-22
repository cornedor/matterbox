package ui

import (
	"fmt"
	"strconv"
	"testing"

	"charm.land/bubbles/v2/textinput"
	"github.com/mattermost/mattermost/server/public/model"
)

// BenchmarkRenderMarkdown measures one renderMarkdown call — the per-message
// styling pass that runs (uncached) on first paint of every post and on every
// edit. It feeds the same realistic body mix benchPosts uses (plain text,
// bullet lists, links, fenced code, quotes + inline code), so the number is a
// representative average cost across the kinds of messages a channel holds.
// emojiImg / mr are nil and self is empty: this isolates the core markdown work
// from custom-emoji and MR-badge lookups.
func BenchmarkRenderMarkdown(b *testing.B) {
	bodies := []string{
		"hey, can you take a look at this when you get a chance?",
		"Sure — here's the **summary**:\n- point one\n- point two\n- a third, longer point that wraps across the viewport width comfortably",
		"see https://example.com/some/long/path?query=1&other=2 for details",
		"```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```",
		"> quoted reply\nand a follow-up line with `inline code` and *emphasis*",
		"shorter one",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = renderMarkdown(bodies[i%len(bodies)], nil, nil, "")
	}
}

// benchSwitcherModel builds a Model holding n open channels across a few teams,
// plus an empty switcher textinput, so switcherResults exercises the real
// per-channel fuzzy/label/sort work the global switcher (ctrl+k) runs on every
// keystroke.
func benchSwitcherModel(n int) Model {
	sw := textinput.New()
	lists := map[string][]*model.Channel{}
	for i := 0; i < n; i++ {
		team := "team" + strconv.Itoa(i%4)
		lists[team] = append(lists[team], &model.Channel{
			Id:          "chan" + strconv.Itoa(i),
			TeamId:      team,
			Type:        model.ChannelTypeOpen,
			Name:        "channel-" + strconv.Itoa(i),
			DisplayName: "Channel " + strconv.Itoa(i),
		})
	}
	return Model{
		switcher:  sw,
		channels:  lists,
		mentions:  map[string]int{},
		unread:    map[string]int{},
		openStats: map[string]channelStat{},
	}
}

// BenchmarkSwitcherResults measures one switcherResults call — the fuzzy filter
// + multi-key sort the channel switcher reruns per keystroke as the query
// grows. The empty-needle case is the bare ctrl+k list (everything matches, so
// the sort dominates); the typed case narrows via fuzzyScore first.
func BenchmarkSwitcherResults(b *testing.B) {
	for _, n := range []int{50, 200, 800} {
		m := benchSwitcherModel(n)
		b.Run(fmt.Sprintf("empty/chans=%d", n), func(b *testing.B) {
			m.switcher.SetValue("")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.switcherResults()
			}
		})
		b.Run(fmt.Sprintf("typed/chans=%d", n), func(b *testing.B) {
			m.switcher.SetValue("chan1")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.switcherResults()
			}
		})
	}
}

// benchMentionModel builds a Model whose userNames cache holds n usernames plus
// the mentionUsage popularity map localMentionMatches reads while ranking.
func benchMentionModel(n int) *Model {
	m := &Model{
		userNames:    map[string]string{},
		mentionUsage: map[string]int{},
	}
	for i := 0; i < n; i++ {
		name := "person" + strconv.Itoa(i)
		m.userNames["user"+strconv.Itoa(i)] = name
		if i%5 == 0 {
			m.mentionUsage[name] = i
		}
	}
	return m
}

// BenchmarkLocalMentionMatches measures the local @-autocomplete prefilter that
// runs on every keystroke after `@`: a fuzzyScore pass over the whole userNames
// cache, then a ranked sort. The empty query is the moment the popup first opens
// (everything matches); the typed query is steady-state filtering.
func BenchmarkLocalMentionMatches(b *testing.B) {
	for _, n := range []int{100, 500, 2000} {
		m := benchMentionModel(n)
		b.Run(fmt.Sprintf("empty/users=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.localMentionMatches("")
			}
		})
		b.Run(fmt.Sprintf("typed/users=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.localMentionMatches("person1")
			}
		})
	}
}

// BenchmarkFuzzyScore micro-benchmarks the scorer that backs both the switcher
// and @-mention filters — it's called once per candidate per keystroke, so its
// per-call cost multiplies across the lists above. Each sub-case hits a
// different branch: exact/prefix/substring share the fast strings.Index path,
// while subsequence falls back to the rune-by-rune scan, and "miss" is the
// worst case (a full scan that still rejects).
func BenchmarkFuzzyScore(b *testing.B) {
	const haystack = "engineering-platform-incidents"
	cases := []struct{ name, needle string }{
		{"exact", haystack},
		{"prefix", "engineer"},
		{"substring", "platform"},
		{"subsequence", "egpltfm"},
		{"miss", "zzzqqq"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _, _ = fuzzyScore(haystack, c.needle)
			}
		})
	}
}
