package mm

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestUnreadCounts(t *testing.T) {
	tests := []struct {
		name              string
		ch                model.Channel
		mb                model.ChannelMember
		wantUnread, wantM int
	}{
		{
			name:       "fully read",
			ch:         model.Channel{TotalMsgCount: 10, TotalMsgCountRoot: 8},
			mb:         model.ChannelMember{MsgCount: 10, MsgCountRoot: 8},
			wantUnread: 0, wantM: 0,
		},
		{
			name:       "root posts unread",
			ch:         model.Channel{TotalMsgCount: 12, TotalMsgCountRoot: 10},
			mb:         model.ChannelMember{MsgCount: 9, MsgCountRoot: 7, MentionCount: 2, MentionCountRoot: 2},
			wantUnread: 3, wantM: 2,
		},
		{
			// The f.loermans regression: the only unread is a thread reply,
			// so the root counters sit flush while the all-posts pair — and
			// in a DM the all-mentions count — carry the real state.
			name:       "thread reply only",
			ch:         model.Channel{TotalMsgCount: 2992, TotalMsgCountRoot: 2943},
			mb:         model.ChannelMember{MsgCount: 2991, MsgCountRoot: 2943, MentionCount: 1, MentionCountRoot: 0},
			wantUnread: 1, wantM: 1,
		},
		{
			// A server whose legacy pair lags (tracks itself to a ~0 diff)
			// must still surface unread through the root floor.
			name:       "legacy counters lag",
			ch:         model.Channel{TotalMsgCount: 50, TotalMsgCountRoot: 40},
			mb:         model.ChannelMember{MsgCount: 50, MsgCountRoot: 35, MentionCountRoot: 1},
			wantUnread: 5, wantM: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unread, mentions := UnreadCounts(&tt.ch, &tt.mb)
			if unread != tt.wantUnread || mentions != tt.wantM {
				t.Errorf("UnreadCounts() = (%d, %d), want (%d, %d)", unread, mentions, tt.wantUnread, tt.wantM)
			}
		})
	}
}
