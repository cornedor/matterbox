package ui

import (
	"fmt"
	"strconv"
	"testing"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
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

// BenchmarkStyleMentions measures the self-mention highlighting that runs once
// per message line inside every search and feed bubble — 50-500 calls per
// render of a busy results list. The "match" case has the @self the regex
// looks for; "nomatch" exercises the common case (a line that mentions nobody)
// where the scan still runs end to end.
func BenchmarkStyleMentions(b *testing.B) {
	base := lipgloss.NewStyle()
	const self = "anders"
	cases := []struct{ name, body string }{
		{"nomatch", "shipped the fix, can someone review the diff when free?"},
		{"match", "hey @anders can you take a look — also cc @bob and @carol please"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = styleMentions(c.body, self, base)
			}
		})
	}
}

// BenchmarkTruncate measures the grapheme-width-aware truncation called
// hundreds of times per frame (channel names, bubble headers, message
// snippets, attachment names). "fits" is the early-out; "ascii" and "wide"
// hit the per-rune width-measuring loop, with wide runes (emoji/CJK) the
// costlier path.
func BenchmarkTruncate(b *testing.B) {
	cases := []struct {
		name string
		s    string
		n    int
	}{
		{"fits", "#general", 40},
		{"ascii", "engineering-platform-incidents-and-postmortems", 20},
		{"wide", "🚀 platform 事故 incidents 🔥 postmortem review 📊", 20},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = truncate(c.s, c.n)
			}
		})
	}
}

// benchVisibleModel builds a Model sitting on a team tab with n channels in
// that team, so visibleChannels exercises the real per-keystroke sidebar
// filter (lower-casing + channelLabel + substring match for every channel).
func benchVisibleModel(n int) Model {
	lists := map[string][]*model.Channel{"t1": make([]*model.Channel, n)}
	for i := 0; i < n; i++ {
		lists["t1"][i] = &model.Channel{
			Id:          "chan" + strconv.Itoa(i),
			TeamId:      "t1",
			Type:        model.ChannelTypeOpen,
			Name:        "channel-" + strconv.Itoa(i),
			DisplayName: "Channel " + strconv.Itoa(i),
		}
	}
	return Model{
		teams:     []*model.Team{{Id: "t1", Name: "team", DisplayName: "Team"}},
		teamIdx:   2, // Feed(0), Search(1), team(2) — land on the team tab
		channels:  lists,
		userNames: map[string]string{},
	}
}

// BenchmarkVisibleChannels measures the sidebar filter rerun on every keystroke
// while filtering the channel list. The empty filter is the unfiltered fast
// path (returns the slice as-is); the typed filter walks every channel.
func BenchmarkVisibleChannels(b *testing.B) {
	for _, n := range []int{50, 200, 800} {
		m := benchVisibleModel(n)
		b.Run(fmt.Sprintf("empty/chans=%d", n), func(b *testing.B) {
			m.filterValue = ""
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.visibleChannels()
			}
		})
		b.Run(fmt.Sprintf("typed/chans=%d", n), func(b *testing.B) {
			m.filterValue = "channel 1"
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.visibleChannels()
			}
		})
	}
}

// BenchmarkFeedBadgeCounts measures the Feed-tab badge tally recomputed on
// every render: a scan of the unread and mention maps, each entry checked
// against channelMuted (a linear members scan). n is the number of channels
// carrying unread/mention state.
func BenchmarkFeedBadgeCounts(b *testing.B) {
	for _, n := range []int{50, 200, 800} {
		m := Model{
			unread:   map[string]int{},
			mentions: map[string]int{},
		}
		for i := 0; i < n; i++ {
			id := "chan" + strconv.Itoa(i)
			m.unread[id] = 1
			if i%4 == 0 {
				m.mentions[id] = 1
			}
			m.members = append(m.members, model.ChannelMemberWithTeamData{
				ChannelMember: model.ChannelMember{ChannelId: id},
			})
		}
		m.rebuildMutedChannels()
		b.Run(fmt.Sprintf("chans=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = m.feedBadgeCounts()
			}
		})
	}
}

// BenchmarkExpandTabs measures the tab-normalisation helper renderMarkdown now
// runs once per (uncached) styling pass. Two inputs bracket the real range: a
// tab-free message (the overwhelming majority — hits the ContainsRune fast path
// and returns the input unchanged, zero alloc) and a tab-heavy cookie-dump
// paste (the worst case that motivated the fix).
func BenchmarkExpandTabs(b *testing.B) {
	tabFree := "Sure — here's the summary, a normal chat message with no tabs at all in it."
	tabHeavy := "_ga\tGA1.1.1388876972\t.justbrands.eu\t/\t2027-06-05\t30\tMedium\n" +
		"session\teyJhbGciOiJIUzI1NiJ9.aVeryLongUnbrokenToken\t.x.eu\t/\tLax\n" +
		"AWSALB\tH2T/NsIgUUWF9QJAv0lkE6Rwlkj\tbc.justbrands.eu\t/\t2026-05-08\t130\tMedium"
	b.Run("tab-free", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = expandTabs(tabFree, 4)
		}
	})
	b.Run("tab-heavy", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = expandTabs(tabHeavy, 4)
		}
	})
}
