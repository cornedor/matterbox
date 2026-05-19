package ui

import (
	"github.com/mattermost/mattermost/server/public/model"
)

type meLoadedMsg struct{ user *model.User }
type teamsLoadedMsg struct{ teams []*model.Team }
type channelsLoadedMsg struct {
	channels  []*model.Channel
	userNames map[string]string // pre-resolved usernames for DM partners
}
type postsLoadedMsg struct {
	channelID string
	posts     []*model.Post
	users     map[string]string
}
type errMsg struct{ err error }

type wsConnectedMsg struct{ ws *model.WebSocketClient }
type wsEventMsg struct{ ev *model.WebSocketEvent }
type wsClosedMsg struct{ err error }
type wsReconnectMsg struct{}

type postSentMsg struct {
	channelID string
	post      *model.Post
}

// threadLoadedMsg carries a freshly-fetched thread (root + replies) so
// the sidebar can render it. Stale responses (the user already closed
// or swapped threads) are discarded by checking rootID against
// m.threadRootID.
type threadLoadedMsg struct {
	rootID string
	posts  []*model.Post
	users  map[string]string
}

type membersLoadedMsg struct {
	members model.ChannelMembersWithTeamData
}

type fileInfosLoadedMsg struct {
	postID string
	infos  []*model.FileInfo
}

type attachmentOpenedMsg struct {
	name string
	err  error
}

// mentionDebounceMsg fires after mentionDebounce; if `seq` still matches
// the current state, the handler kicks off fetchMentions.
type mentionDebounceMsg struct{ seq int }

// mentionUsersMsg carries the autocomplete response (or its error).
// Stale responses are dropped when `seq` no longer matches.
type mentionUsersMsg struct {
	seq   int
	users []*model.User
	err   error
}
