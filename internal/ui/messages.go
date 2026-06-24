package ui

import (
	"context"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/aisearch"
	"matterbox/internal/store"
)

type meLoadedMsg struct{ user *model.User }
type teamsLoadedMsg struct{ teams []*model.Team }
type channelsLoadedMsg struct {
	channels       []*model.Channel
	userNames      map[string]string             // pre-resolved usernames for DM partners
	customStatuses map[string]model.CustomStatus // DM partners' custom statuses (captured with the name fetch)
}

// statusesLoadedMsg carries a batch of DM partner presence (userID → status:
// online/away/dnd; offline/unknown absent) from fetchStatuses. A nil/empty
// map (e.g. a swallowed poll error) merges nothing, leaving the last-known
// dots in place.
type statusesLoadedMsg struct{ statuses map[string]string }

// statusPollMsg fires on the recurring presence-poll tick; its handler kicks
// off the next fetchStatuses and reschedules the tick (a single chain).
type statusPollMsg struct{}
type postsLoadedMsg struct {
	channelID string
	posts     []*model.Post
	users     map[string]string
}

// postsGapFilledMsg carries a page of `channelID`'s posts (oldest→newest)
// to reconcile into the open channel. Its handler merges them by Id and
// create_at rather than appending, so the page may fill a gap *inside* the
// loaded range as well as extend it — see fetchRecent (warm-open recent-
// window reconcile) and fetchPostsAfter (forward fill from a cached post).
// Empty `posts` means there was nothing to reconcile.
type postsGapFilledMsg struct {
	channelID string
	posts     []*model.Post
	users     map[string]string
}

// deletionsSyncedMsg reports posts that were removed while matterbox was away,
// found by syncChannelDeletions (a PostsSince sweep — the only API that returns
// deleted posts). They're already persisted as tombstones; the handler also
// flips any matching live post in the open transcript so the marker shows
// without a reopen. Empty `deleted` means nothing was missed.
type deletionsSyncedMsg struct {
	channelID string
	deleted   []*model.Post
}

// olderPostsMsg carries a server-fetched page of posts strictly older than
// the top of the loaded window (see fetchOlder), merged in when the user
// scrolls past it. atChannelStart is true when the server reports nothing
// older — the genuine beginning of the channel, not just the cache floor —
// so the UI can say "beginning of channel" only when it's actually true.
type olderPostsMsg struct {
	channelID      string
	posts          []*model.Post
	users          map[string]string
	atChannelStart bool
}

// newerPostsMsg is the forward mirror of olderPostsMsg: a server-fetched
// page strictly newer than the bottom of the loaded window (see
// fetchNewer). atChannelEnd is true when the page reaches the channel's
// newest post.
type newerPostsMsg struct {
	channelID    string
	posts        []*model.Post
	users        map[string]string
	atChannelEnd bool
}

// markViewedMsg fires after a channel has been open for the configured
// dwell. gen is the viewGen captured when the tick was scheduled; the
// handler ignores the message if the generation (or open channel) has
// since changed — i.e. the user switched or refocused before the dwell
// elapsed. See Model.scheduleMarkViewed.
type markViewedMsg struct {
	channelID string
	gen       int
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

// infoMembersLoadedMsg / infoPinnedLoadedMsg carry the channel-info panel's
// async fetches (member profiles and pinned posts). channelID is checked
// against m.infoChannelID so a response for a channel the user already
// navigated away from is discarded. err carries a fetch failure to show in
// the panel rather than the global status line.
type infoMembersLoadedMsg struct {
	channelID string
	members   []*model.User
	err       error
}

type infoPinnedLoadedMsg struct {
	channelID string
	posts     []*model.Post
	users     map[string]string
	err       error
}

type fileInfosLoadedMsg struct {
	postID string
	infos  []*model.FileInfo
}

type attachmentOpenedMsg struct {
	name string
	err  error
}

// searchDebounceMsg fires after searchDebounce; if seq still matches the
// current state, the handler kicks off the actual store.Search call.
type searchDebounceMsg struct{ seq int }

// searchResultsMsg carries hits from a completed store.Search. Stale
// responses are dropped when seq no longer matches m.search.seq. err is
// the stringified database error if any (kept as a string so the msg
// stays cheaply copyable across the bubbletea tick boundary).
type searchResultsMsg struct {
	seq   int
	query string
	hits  []store.SearchHit
	err   string
}

// feedLoadedMsg carries the assembled unread-feed entries from a
// completed buildFeed run. Stale responses are dropped when seq no
// longer matches m.feed.seq. users carries any newly-resolved sender
// usernames so the bubbles render real names. members is the freshly
// fetched channel-member snapshot (nil if the refresh failed) so the
// model's read state stays current.
type feedLoadedMsg struct {
	seq     int
	entries []feedEntry
	users   map[string]string
	members model.ChannelMembersWithTeamData
}

// summaryGatheredMsg carries the channel-path transcript assembled by the
// worker. Stale responses are dropped when seq no longer matches
// m.summary.seq. count is the number of messages included.
type summaryGatheredMsg struct {
	seq        int
	transcript string
	count      int
	// latestMs is the create-time (unix-ms) of the channel's most recent
	// real message, probed only when count == 0 so the empty-window result
	// can tell the user how far back the last activity was.
	latestMs int64
	err      error
}

// summaryStreamOpenedMsg hands the UI the live chunk channel + cancel
// handle for an opened streaming request. Dropped (and cancelled) when seq
// no longer matches m.summary.seq.
type summaryStreamOpenedMsg struct {
	seq    int
	ch     chan summaryChunkMsg
	cancel context.CancelFunc
}

// summaryChunkMsg is one streamed delta (answer content and/or reasoning),
// a terminal done marker, or a terminal error. Dropped when seq no longer
// matches m.summary.seq.
type summaryChunkMsg struct {
	seq      int
	content  string
	thinking string
	done     bool
	err      error
}

// aiSearchOpenedMsg hands the UI the update channel + cancel handle for a
// started agentic-search run. Dropped (and cancelled) when seq no longer
// matches m.aiSearch.seq.
type aiSearchOpenedMsg struct {
	seq    int
	ch     chan aisearch.Update
	cancel context.CancelFunc
}

// aiSearchUpdateMsg carries one update from the search agent: a trace step or
// the terminal answer/error. Dropped when seq no longer matches
// m.aiSearch.seq.
type aiSearchUpdateMsg struct {
	seq int
	u   aisearch.Update
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

// usersResolvedMsg carries the result of the background username fetch
// kicked off by resolveUnknownSenders. `ids` is the exact set requested
// (so the handler can clear them from inflightSenders on error); `users`
// maps the ids that resolved to their username. On error `users` is nil.
type usersResolvedMsg struct {
	ids   []string
	users map[string]string
	err   error
}
