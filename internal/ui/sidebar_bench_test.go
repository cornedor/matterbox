package ui

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// benchSidebarModel builds a Model with n DM channels plus the presence /
// custom-status / username state the sidebar row helpers read, so a benchmark
// exercises the same per-row work renderChannelsPane and channelsFingerprint
// do for real.
func benchSidebarModel(n int) (*Model, []*model.Channel) {
	m := &Model{
		me:               &model.User{Id: "me0000000000000000000000aa"},
		statuses:         map[string]string{},
		customStatuses:   map[string]model.CustomStatus{},
		userNames:        map[string]string{},
		mentions:         map[string]int{},
		unread:           map[string]int{},
		showCustomStatus: true,
	}
	vis := make([]*model.Channel, 0, n)
	for i := 0; i < n; i++ {
		uid := ("user" + strconv.Itoa(i) + "0000000000000000000000000")[:26]
		vis = append(vis, &model.Channel{
			Id:   "chan" + strconv.Itoa(i),
			Type: model.ChannelTypeDirect,
			Name: m.me.Id + "__" + uid,
		})
		m.statuses[uid] = "online"
		m.customStatuses[uid] = model.CustomStatus{Emoji: "rocket", Text: "shipping"}
		m.userNames[uid] = "user" + strconv.Itoa(i)
		if i%3 == 0 {
			m.unread[vis[i].Id] = i
		}
	}
	return m, vis
}

// BenchmarkChannelsFingerprint measures the sidebar cache-key computation that
// runs on every View. It calls dmPartnerID / dmCustomStatus / channelLabel per
// visible row, which are the helpers under perf scrutiny.
func BenchmarkChannelsFingerprint(b *testing.B) {
	m, vis := benchSidebarModel(40)
	const listH, innerH = 39, 40
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.channelsFingerprint(vis, 0, listH, innerH, "header")
	}
}

// BenchmarkRenderChannelsPane measures the sidebar repaint on a cache miss — the
// full per-row re-style (truncate + lipgloss render + presence/custom-status
// dots) that runs whenever anything the fingerprint covers changes: an unread
// count ticks, a partner's presence flips, the selection moves. The cache hit is
// the trivial string return measured indirectly by BenchmarkChannelsFingerprint;
// this is the work that hit avoids. Render is windowed to listH rows, so the
// channel-count sweep mostly grows the fingerprint scan and visibleChannels
// filter on top of a fixed render floor — i.e. it's the cost of one sidebar
// repaint as the DM list grows.
func BenchmarkRenderChannelsPane(b *testing.B) {
	for _, n := range []int{40, 200, 800} {
		m, vis := benchSidebarModel(n)
		m.hasDMs = true
		m.teamIdx = 0 // tabAt(0) → the DMs tab, so visibleChannels returns vis
		m.channels = map[string][]*model.Channel{dmTeamID: vis}
		m.vcache = &viewCache{}
		b.Run(fmt.Sprintf("dms=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.vcache.sidebar.valid = false // force the miss path every iteration
				_ = m.renderChannelsPane(40)
			}
		})
	}
}
