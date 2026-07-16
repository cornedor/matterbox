package mm

import "github.com/mattermost/mattermost/server/public/model"

// UnreadCounts returns the member's unread-message and mention counts for
// the channel, combining both counter families the server keeps.
//
// The root counters (TotalMsgCountRoot/MsgCountRoot/MentionCountRoot) only
// move for root posts: a thread reply never bumps them, so a channel whose
// only unread is a reply looks fully read through them alone. This is very
// common in DMs, where every incoming message — including a reply — counts
// as a mention. The legacy all-posts counters (TotalMsgCount/MsgCount/
// MentionCount) do include replies.
//
// matterbox renders replies inline in the channel rather than in a separate
// Threads view, so an unread reply is genuinely unread here. Take the max
// of the two families: on a healthy server the all-posts diff is a superset
// of the root diff (both are reset together by channel view), and the root
// counters stay as a floor for servers where the legacy pair lags.
func UnreadCounts(ch *model.Channel, mb *model.ChannelMember) (unread, mentions int) {
	unread = int(max(ch.TotalMsgCountRoot-mb.MsgCountRoot, ch.TotalMsgCount-mb.MsgCount))
	mentions = int(max(mb.MentionCountRoot, mb.MentionCount))
	return unread, mentions
}
