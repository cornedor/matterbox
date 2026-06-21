package ui

import (
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
