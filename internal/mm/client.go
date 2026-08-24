package mm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"golang.org/x/sync/singleflight"
)

type Client struct {
	c         *model.Client4
	token     string
	serverURL string

	// usersSF collapses concurrent UsernamesByIDs lookups for the same
	// set of ids into a single API call. The UI fires a background
	// username fetch whenever it renders a post whose sender it can't
	// name yet (see resolveUnknownSenders); without this, a burst of
	// renders for the same unresolved set would each hit the server.
	usersSF singleflight.Group

	// emojiSF collapses concurrent CustomEmojiImage downloads for the same
	// emoji id into one request. Custom-emoji image fetches are render-driven
	// (a body, pill, or picker entry sights an unknown emoji and enqueues a
	// fetch), so the same id can be requested from several event loops before
	// the first download lands; singleflight makes that cost one GET.
	emojiSF singleflight.Group
}

// New builds a Client4 wrapper pointed at the given Mattermost server.
// The URL must include the scheme (https://… or http://…); the WS URL
// is derived by swapping http→ws / https→wss. Client4's default HTTP client is
// swapped for one that replays a read-only call whose connection dies (see
// retryOnce).
func New(serverURL, token string) *Client {
	c := model.NewAPIv4Client(serverURL)
	c.HTTPClient = &http.Client{Transport: retryOnce{delay: retryDelay}}
	c.SetToken(token)
	return &Client{c: c, token: token, serverURL: serverURL}
}

// DialWS opens a WebSocket connection to the server and starts the
// listener goroutine. Consume events from wsc.EventChannel.
func (c *Client) DialWS() (*model.WebSocketClient, error) {
	wsURL := c.serverURL
	switch {
	case strings.HasPrefix(wsURL, "https://"):
		wsURL = "wss://" + wsURL[len("https://"):]
	case strings.HasPrefix(wsURL, "http://"):
		wsURL = "ws://" + wsURL[len("http://"):]
	}
	wsc, err := model.NewWebSocketClient4(wsURL, c.token)
	if err != nil {
		return nil, fmt.Errorf("ws dial: %w", err)
	}
	wsc.Listen()
	return wsc, nil
}

func (c *Client) Me(ctx context.Context) (*model.User, error) {
	u, _, err := c.c.GetMe(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("get me: %w", err)
	}
	return u, nil
}

// LoginWithPassword authenticates with a login id (username or email) and
// password, returning the session token the server issues plus the
// authenticated user. A non-empty mfaToken is submitted alongside for servers
// that enforce two-factor (see MFARequired for detecting that a token is
// needed). The returned token has the same shape as the SSO MMAUTHTOKEN, so
// callers persist it identically; build the client with an empty token
// (mm.New(server, "")) since there isn't one yet.
func (c *Client) LoginWithPassword(ctx context.Context, loginID, password, mfaToken string) (string, *model.User, error) {
	var (
		u   *model.User
		err error
	)
	if mfaToken != "" {
		u, _, err = c.c.LoginWithMFA(ctx, loginID, password, mfaToken)
	} else {
		u, _, err = c.c.Login(ctx, loginID, password)
	}
	if err != nil {
		return "", nil, fmt.Errorf("login: %w", err)
	}
	// Login stores the issued session token on the underlying Client4; keep our
	// copy in sync so the same Client can immediately make authenticated calls.
	c.token = c.c.AuthToken
	return c.c.AuthToken, u, nil
}

// MFARequired reports whether err from LoginWithPassword means the server wants
// a two-factor token, so the caller can prompt for one and retry. Mattermost
// signals this with an AppError whose id (e.g. mfa.validate_token… /
// api.context.mfa_required…) mentions MFA; we match that case-insensitively
// rather than pinning one server error id that drifts between versions, and
// fall back to scanning the error text.
func MFARequired(err error) bool {
	if err == nil {
		return false
	}
	var appErr *model.AppError
	if errors.As(err, &appErr) && strings.Contains(strings.ToLower(appErr.Id), "mfa") {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "mfa")
}

func (c *Client) Teams(ctx context.Context, userID string) ([]*model.Team, error) {
	t, _, err := c.c.GetTeamsForUser(ctx, userID, "")
	if err != nil {
		return nil, fmt.Errorf("get teams: %w", err)
	}
	return t, nil
}

// AllChannels returns every channel the user is a member of across all
// teams plus DMs and group-DMs (DM channels carry an empty TeamId).
func (c *Client) AllChannels(ctx context.Context, userID string) ([]*model.Channel, error) {
	ch, _, err := c.c.GetChannelsForUserWithLastDeleteAt(ctx, userID, 0)
	if err != nil {
		return nil, fmt.Errorf("get channels: %w", err)
	}
	return ch, nil
}

func (c *Client) Posts(ctx context.Context, channelID string, perPage int) (*model.PostList, error) {
	pl, _, err := c.c.GetPostsForChannel(ctx, channelID, 0, perPage, "", false, false)
	if err != nil {
		return nil, fmt.Errorf("get posts: %w", err)
	}
	return pl, nil
}

// Post fetches a single post by id. Used to resolve a permalink
// (/<team>/pl/<postID>) to its channel when the target isn't cached, so a
// clicked permalink can be opened in the app instead of the browser.
func (c *Client) Post(ctx context.Context, postID string) (*model.Post, error) {
	p, _, err := c.c.GetPost(ctx, postID, "")
	if err != nil {
		return nil, fmt.Errorf("get post: %w", err)
	}
	return p, nil
}

// PostsAfter returns posts in the channel that were created after the
// given postID, oldest first as far as the page is concerned. Used to
// fill the gap between the newest cached post and the live state when
// reopening a channel.
func (c *Client) PostsAfter(ctx context.Context, channelID, postID string, perPage int) (*model.PostList, error) {
	pl, _, err := c.c.GetPostsAfter(ctx, channelID, postID, 0, perPage, "", false, false)
	if err != nil {
		return nil, fmt.Errorf("get posts after: %w", err)
	}
	return pl, nil
}

// PostsBefore returns posts in the channel that were created before the
// given postID. Used by the backfill indexer to walk a channel's
// history backward page by page until reaching a time cutoff.
func (c *Client) PostsBefore(ctx context.Context, channelID, postID string, perPage int) (*model.PostList, error) {
	pl, _, err := c.c.GetPostsBefore(ctx, channelID, postID, 0, perPage, "", false, false)
	if err != nil {
		return nil, fmt.Errorf("get posts before: %w", err)
	}
	return pl, nil
}

// PostsSince returns posts in the channel created at or after `since`
// (unix-ms). Used by the unread feed to pull exactly the messages newer
// than the user's last-viewed boundary for each unread channel.
func (c *Client) PostsSince(ctx context.Context, channelID string, since int64) (*model.PostList, error) {
	pl, _, err := c.c.GetPostsSince(ctx, channelID, since, false)
	if err != nil {
		return nil, fmt.Errorf("get posts since: %w", err)
	}
	return pl, nil
}

// ChannelMembers returns every channel-member record for the user
// across all teams, including msg/mention counters needed for the
// initial unread/mention badges. The server caps per_page at 200
// regardless of what we ask, so a member-heavy account (hundreds of
// channels) needs paging — otherwise the records past the first 200 are
// silently dropped and channels there never get an unread badge.
func (c *Client) ChannelMembers(ctx context.Context, userID string) (model.ChannelMembersWithTeamData, error) {
	const perPage = 200
	var all model.ChannelMembersWithTeamData
	for page := 0; ; page++ {
		batch, _, err := c.c.GetChannelMembersWithTeamData(ctx, userID, page, perPage)
		if err != nil {
			return nil, fmt.Errorf("get channel members: %w", err)
		}
		all = append(all, batch...)
		if len(batch) < perPage {
			break
		}
	}
	return all, nil
}

// PinnedPosts returns every pinned post in the channel, newest first.
// Used by the channel-info panel.
func (c *Client) PinnedPosts(ctx context.Context, channelID string) (*model.PostList, error) {
	pl, _, err := c.c.GetPinnedPosts(ctx, channelID, "")
	if err != nil {
		return nil, fmt.Errorf("get pinned posts: %w", err)
	}
	return pl, nil
}

// SavedPosts returns the current user's Mattermost saved messages, newest first.
func (c *Client) SavedPosts(ctx context.Context, userID string, page, perPage int) ([]*model.Post, error) {
	pl, _, err := c.c.GetFlaggedPostsForUser(ctx, userID, page, perPage)
	if err != nil {
		return nil, fmt.Errorf("get saved posts: %w", err)
	}
	out := make([]*model.Post, 0, len(pl.Order))
	for _, id := range pl.Order {
		if p := pl.Posts[id]; p != nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// ChannelUsers returns the user records of every member of the channel,
// paging until the list is exhausted (the server caps per_page). Unlike
// ChannelMembers (membership rows with counters) this carries full user
// profiles, so the channel-info panel can list members by name without a
// second lookup.
func (c *Client) ChannelUsers(ctx context.Context, channelID string) ([]*model.User, error) {
	const perPage = 200
	var all []*model.User
	for page := 0; ; page++ {
		batch, _, err := c.c.GetUsersInChannel(ctx, channelID, page, perPage, "")
		if err != nil {
			return nil, fmt.Errorf("get users in channel: %w", err)
		}
		all = append(all, batch...)
		if len(batch) < perPage {
			break
		}
	}
	return all, nil
}

// AddChannelMember joins a user to a channel (POST /channels/{id}/members).
// Adding someone who is already a member is a no-op server-side. The server
// posts a "added to the channel" system message and broadcasts a
// `user_added` WS event, so other clients (and our own message pane) learn of
// it without a refetch.
func (c *Client) AddChannelMember(ctx context.Context, channelID, userID string) error {
	if _, _, err := c.c.AddChannelMember(ctx, channelID, userID); err != nil {
		return fmt.Errorf("add channel member: %w", err)
	}
	return nil
}

// ViewChannel marks the channel as read for the user (updates
// LastViewedAt / MsgCount on the server-side ChannelMember).
func (c *Client) ViewChannel(ctx context.Context, userID, channelID string) error {
	_, _, err := c.c.ViewChannel(ctx, userID, &model.ChannelView{ChannelId: channelID})
	if err != nil {
		return fmt.Errorf("view channel: %w", err)
	}
	return nil
}

// SetChannelMuted mutes or unmutes a channel for the given user by
// patching its member notify props (mark_unread = mention when muted,
// all when not). The server broadcasts a `channel_member_updated` WS
// event so other clients learn of the change.
func (c *Client) SetChannelMuted(ctx context.Context, userID, channelID string, muted bool) error {
	level := model.ChannelMarkUnreadAll
	if muted {
		level = model.ChannelMarkUnreadMention
	}
	props := map[string]string{model.MarkUnreadNotifyProp: level}
	if _, err := c.c.UpdateChannelNotifyProps(ctx, channelID, userID, props); err != nil {
		return fmt.Errorf("update channel notify props: %w", err)
	}
	return nil
}

// PinPost pins a post so it appears in the channel's pinned-posts list.
func (c *Client) PinPost(ctx context.Context, postID string) error {
	if _, err := c.c.PinPost(ctx, postID); err != nil {
		return fmt.Errorf("pin post: %w", err)
	}
	return nil
}

// UnpinPost removes a post from the channel's pinned-posts list.
func (c *Client) UnpinPost(ctx context.Context, postID string) error {
	if _, err := c.c.UnpinPost(ctx, postID); err != nil {
		return fmt.Errorf("unpin post: %w", err)
	}
	return nil
}

// SavedPostIDs returns the ids of every post in the current user's saved
// messages — the "flagged_post" preference category, one preference per post.
func (c *Client) SavedPostIDs(ctx context.Context, userID string) ([]string, error) {
	prefs, _, err := c.c.GetPreferencesByCategory(ctx, userID, model.PreferenceCategoryFlaggedPost)
	if err != nil {
		return nil, fmt.Errorf("get saved post ids: %w", err)
	}
	ids := make([]string, 0, len(prefs))
	for _, p := range prefs {
		if p.Name != "" {
			ids = append(ids, p.Name)
		}
	}
	return ids, nil
}

// SavePost saves a post to the current user's Mattermost saved-messages list.
func (c *Client) SavePost(ctx context.Context, userID, postID string) error {
	prefs := model.Preferences{model.Preference{
		UserId:   userID,
		Category: model.PreferenceCategoryFlaggedPost,
		Name:     postID,
		Value:    "true",
	}}
	if _, err := c.c.UpdatePreferences(ctx, userID, prefs); err != nil {
		return fmt.Errorf("save post: %w", err)
	}
	return nil
}

// UnsavePost removes a post from the current user's Mattermost saved-messages list.
func (c *Client) UnsavePost(ctx context.Context, userID, postID string) error {
	prefs := model.Preferences{model.Preference{
		UserId:   userID,
		Category: model.PreferenceCategoryFlaggedPost,
		Name:     postID,
	}}
	if _, err := c.c.DeletePreferences(ctx, userID, prefs); err != nil {
		return fmt.Errorf("unsave post: %w", err)
	}
	return nil
}

// Send posts a new message to the given channel. If rootID is non-empty,
// the message is sent as a reply in that thread. fileIDs (may be empty)
// are previously-uploaded files to attach to the post.
func (c *Client) Send(ctx context.Context, channelID, rootID, message string, fileIDs []string) (*model.Post, error) {
	p, _, err := c.c.CreatePost(ctx, &model.Post{
		ChannelId: channelID,
		RootId:    rootID,
		Message:   message,
		FileIds:   fileIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("create post: %w", err)
	}
	return p, nil
}

// ExecuteCommand runs a server-side slash command (e.g. "/me waves") in the
// given channel and returns the server's response. teamID is required: the
// server can't infer it for a DM / group-DM, so callers pass the team the
// command should run under (a real team id, even for a DM). Commands typed in
// a regular channel may pass that channel's team id.
func (c *Client) ExecuteCommand(ctx context.Context, channelID, teamID, command string) (*model.CommandResponse, error) {
	resp, _, err := c.c.ExecuteCommandWithTeam(ctx, channelID, teamID, command)
	if err != nil {
		return nil, fmt.Errorf("execute command: %w", err)
	}
	return resp, nil
}

// AutocompleteCommands returns the team's autocomplete-enabled slash commands
// (built-in plus plugin/custom). The list is small and changes rarely, so the
// UI caches it per team and feeds it to the composer's "/" autocomplete popup.
func (c *Client) AutocompleteCommands(ctx context.Context, teamID string) ([]*model.Command, error) {
	cmds, _, err := c.c.ListAutocompleteCommands(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list autocomplete commands: %w", err)
	}
	return cmds, nil
}

// Drafts ---------------------------------------------------------------

// GetDrafts returns the user's saved message drafts for the given team.
// The server scopes the query to that team's channels *plus* every DM /
// group-DM (which carry an empty TeamId), so the same DM draft comes back
// for any team queried — callers dedupe by ChannelId. Each Draft carries
// its ChannelId and RootId ("" for a channel draft, the thread root for a
// reply draft).
func (c *Client) GetDrafts(ctx context.Context, userID, teamID string) ([]*model.Draft, error) {
	d, _, err := c.c.GetDrafts(ctx, userID, teamID)
	if err != nil {
		return nil, fmt.Errorf("get drafts: %w", err)
	}
	return d, nil
}

// UpsertDraft creates or replaces the draft for its ChannelId/RootId pair.
// The server stamps CreateAt/UpdateAt, so callers leave those zero. The
// message is keyed per (user, channel, root): one draft per channel, plus
// one per open thread.
func (c *Client) UpsertDraft(ctx context.Context, channelID, rootID, message string, fileIDs []string) (*model.Draft, error) {
	d, _, err := c.c.UpsertDraft(ctx, &model.Draft{
		ChannelId: channelID,
		RootId:    rootID,
		Message:   message,
		FileIds:   fileIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("upsert draft: %w", err)
	}
	return d, nil
}

// DeleteDraft removes the draft for the given channel/root pair (rootID
// "" targets the channel draft). It is idempotent — deleting a draft that
// no longer exists is not treated as an error by the caller.
func (c *Client) DeleteDraft(ctx context.Context, userID, channelID, rootID string) error {
	if _, _, err := c.c.DeleteDraft(ctx, userID, channelID, rootID); err != nil {
		return fmt.Errorf("delete draft: %w", err)
	}
	return nil
}

// DeletePost soft-deletes a post by Id. The server broadcasts a
// `post_deleted` WS event that the UI applies asynchronously.
func (c *Client) DeletePost(ctx context.Context, postID string) error {
	if _, err := c.c.DeletePost(ctx, postID); err != nil {
		return fmt.Errorf("delete post: %w", err)
	}
	return nil
}

// EditPost replaces the message body of an existing post. We use
// PatchPost so other fields (attachments, props, …) are left untouched.
func (c *Client) EditPost(ctx context.Context, postID, message string) (*model.Post, error) {
	p, _, err := c.c.PatchPost(ctx, postID, &model.PostPatch{Message: &message})
	if err != nil {
		return nil, fmt.Errorf("patch post: %w", err)
	}
	return p, nil
}

// Thread fetches every post in the thread rooted at postID.
func (c *Client) Thread(ctx context.Context, postID string) (*model.PostList, error) {
	pl, _, err := c.c.GetPostThread(ctx, postID, "", false)
	if err != nil {
		return nil, fmt.Errorf("get post thread: %w", err)
	}
	return pl, nil
}

// FileInfosForPost fetches the FileInfo records for a post's
// attachments — used to populate metadata on posts that arrived via
// WebSocket without it.
func (c *Client) FileInfosForPost(ctx context.Context, postID string) ([]*model.FileInfo, error) {
	infos, _, err := c.c.GetFileInfosForPost(ctx, postID, "")
	if err != nil {
		return nil, fmt.Errorf("get file infos: %w", err)
	}
	return infos, nil
}

// DownloadFile returns the raw bytes of an uploaded file.
func (c *Client) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	b, _, err := c.c.DownloadFile(ctx, fileID, true)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}
	return b, nil
}

// DownloadFilePreview returns the server-generated preview rendition of an
// image file — a downscaled (≤~1MP) JPEG/PNG, far smaller than the original.
// Only valid for image files whose FileInfo.HasPreviewImage is set; the server
// 404s otherwise. Used by the inline preview modal so opening a multi-megapixel
// photo doesn't have to transfer (and decode, and re-encode) the full original.
func (c *Client) DownloadFilePreview(ctx context.Context, fileID string) ([]byte, error) {
	b, _, err := c.c.DownloadFilePreview(ctx, fileID, true)
	if err != nil {
		return nil, fmt.Errorf("download file preview: %w", err)
	}
	return b, nil
}

// UploadFile uploads raw bytes to the given channel and returns the
// FileInfo for the resulting upload. The returned FileInfo.Id is the
// fileId to pass in Post.FileIds (via Send) when creating the post.
func (c *Client) UploadFile(ctx context.Context, channelID, filename string, data []byte) (*model.FileInfo, error) {
	resp, _, err := c.c.UploadFile(ctx, data, channelID, filename)
	if err != nil {
		return nil, fmt.Errorf("upload file: %w", err)
	}
	if resp == nil || len(resp.FileInfos) == 0 {
		return nil, fmt.Errorf("upload file: empty response")
	}
	return resp.FileInfos[0], nil
}

// Autocomplete returns users matching `query` for @-mention completion.
// Scoped to a channel (and its team, if known) so DMs and per-team
// channels return the most relevant members first; out-of-channel
// matches are appended after in-channel ones.
func (c *Client) Autocomplete(ctx context.Context, teamID, channelID, query string, limit int) ([]*model.User, error) {
	r, _, err := c.c.AutocompleteUsersInChannel(ctx, teamID, channelID, query, limit, "")
	if err != nil {
		return nil, fmt.Errorf("autocomplete users: %w", err)
	}
	out := make([]*model.User, 0, len(r.Users)+len(r.OutOfChannel))
	out = append(out, r.Users...)
	out = append(out, r.OutOfChannel...)
	return out, nil
}

// AddReaction posts an emoji reaction to a message. emojiName is a
// Mattermost emoji shortcode (no surrounding colons). The server
// broadcasts a `reaction_added` WS event the UI applies asynchronously.
func (c *Client) AddReaction(ctx context.Context, userID, postID, emojiName string) error {
	_, _, err := c.c.SaveReaction(ctx, &model.Reaction{
		UserId:    userID,
		PostId:    postID,
		EmojiName: emojiName,
	})
	if err != nil {
		return fmt.Errorf("save reaction: %w", err)
	}
	return nil
}

// RemoveReaction deletes the user's own reaction with the given emoji
// shortcode from a post.
func (c *Client) RemoveReaction(ctx context.Context, userID, postID, emojiName string) error {
	if _, err := c.c.DeleteReaction(ctx, &model.Reaction{
		UserId:    userID,
		PostId:    postID,
		EmojiName: emojiName,
	}); err != nil {
		return fmt.Errorf("delete reaction: %w", err)
	}
	return nil
}

// Reactions returns every reaction currently attached to a post. Used
// as a fallback when WS events fall behind or when the server didn't
// include them in the post's metadata.
func (c *Client) Reactions(ctx context.Context, postID string) ([]*model.Reaction, error) {
	r, _, err := c.c.GetReactions(ctx, postID)
	if err != nil {
		return nil, fmt.Errorf("get reactions: %w", err)
	}
	return r, nil
}

// DoPostAction fires the interactive action `actionID` on a post. Used
// for matterpoll buttons (vote0/vote1/addOption/endPoll/deletePoll) and
// other plugin-emitted action buttons. The server processes the action
// and broadcasts the resulting state change as a normal post_edited /
// open_dialog WS event.
func (c *Client) DoPostAction(ctx context.Context, postID, actionID string) error {
	if _, err := c.c.DoPostAction(ctx, postID, actionID); err != nil {
		return fmt.Errorf("do post action %s/%s: %w", postID, actionID, err)
	}
	return nil
}

// SubmitDialog submits an interactive-dialog response (the one that pops
// up after matterpoll's "Add Option" button, for example). `submission`
// is keyed by the dialog element's Name field.
func (c *Client) SubmitDialog(ctx context.Context, req *model.SubmitDialogRequest) (*model.SubmitDialogResponse, error) {
	resp, _, err := c.c.SubmitInteractiveDialog(ctx, *req)
	if err != nil {
		return nil, fmt.Errorf("submit dialog: %w", err)
	}
	return resp, nil
}

func (c *Client) UsersByIDs(ctx context.Context, ids []string) ([]*model.User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	u, _, err := c.c.GetUsersByIds(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("get users: %w", err)
	}
	return u, nil
}

// UsersStatuses resolves the given user ids to their presence, keyed
// userID → status (one of "online"/"away"/"dnd"/"offline"). This is the
// lightweight presence endpoint — it carries no profile/custom-status data,
// so it's cheap to poll. Ids that don't resolve are simply absent.
func (c *Client) UsersStatuses(ctx context.Context, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ss, _, err := c.c.GetUsersStatusesByIds(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("get statuses: %w", err)
	}
	out := make(map[string]string, len(ss))
	for _, s := range ss {
		out[s.UserId] = s.Status
	}
	return out, nil
}

// UpdateStatus sets the logged-in user's own presence to one of
// "online"/"away"/"dnd"/"offline". The server records API-set presence
// as manual, so it sticks until changed rather than flipping back to
// online on the next activity.
func (c *Client) UpdateStatus(ctx context.Context, userID, status string) error {
	_, _, err := c.c.UpdateUserStatus(ctx, userID, &model.Status{UserId: userID, Status: status})
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

// UpdateCustomStatus sets the user's custom status (emoji shortcode, no
// surrounding colons, plus free text). Other clients learn about it via
// the server's user_updated WS broadcast.
func (c *Client) UpdateCustomStatus(ctx context.Context, userID string, cs *model.CustomStatus) error {
	if _, _, err := c.c.UpdateUserCustomStatus(ctx, userID, cs); err != nil {
		return fmt.Errorf("update custom status: %w", err)
	}
	return nil
}

// ClearCustomStatus removes the user's custom status.
func (c *Client) ClearCustomStatus(ctx context.Context, userID string) error {
	if _, err := c.c.RemoveUserCustomStatus(ctx, userID); err != nil {
		return fmt.Errorf("clear custom status: %w", err)
	}
	return nil
}

// UsernamesByIDs resolves the given user ids to their usernames, keyed
// userID → username. Ids that don't resolve (deleted/unknown users) are
// simply absent from the result. Concurrent calls for the same set of
// ids are coalesced into one server request via singleflight, so the
// UI's render-driven background resolution (which may fire the same
// lookup from several event loops in quick succession) costs at most one
// in-flight API call per distinct set.
//
// The returned map is shared with any coalesced callers — treat it as
// read-only.
func (c *Client) UsernamesByIDs(ctx context.Context, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	// Sort a copy so the singleflight key is order-independent: the same
	// set of ids requested in any order collapses to one call.
	key := append([]string(nil), ids...)
	sort.Strings(key)

	v, err, _ := c.usersSF.Do(strings.Join(key, ","), func() (any, error) {
		us, _, err := c.c.GetUsersByIds(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("get usernames: %w", err)
		}
		names := make(map[string]string, len(us))
		for _, u := range us {
			names[u.Id] = u.Username
		}
		return names, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(map[string]string), nil
}

// CustomEmojisByNames bulk-resolves emoji shortcodes (no surrounding
// colons) to their server Emoji records. Names that aren't custom emoji
// are simply absent from the result — that's how the caller learns a
// `:name:` is not a server emoji (so it stays literal text). Old servers
// that lack the bulk endpoint surface an error; the caller can fall back
// to per-name resolution if needed.
func (c *Client) CustomEmojisByNames(ctx context.Context, names []string) ([]*model.Emoji, error) {
	if len(names) == 0 {
		return nil, nil
	}
	es, _, err := c.c.GetEmojisByNames(ctx, names)
	if err != nil {
		return nil, fmt.Errorf("get emojis by names: %w", err)
	}
	return es, nil
}

// CustomEmojiImage downloads the raw image bytes for a custom emoji by
// id. Concurrent calls for the same id are coalesced into one request via
// singleflight; the returned slice is shared with any coalesced callers,
// so treat it as read-only.
func (c *Client) CustomEmojiImage(ctx context.Context, emojiID string) ([]byte, error) {
	if emojiID == "" {
		return nil, fmt.Errorf("get emoji image: empty id")
	}
	v, err, _ := c.emojiSF.Do(emojiID, func() (any, error) {
		b, _, err := c.c.GetEmojiImage(ctx, emojiID)
		if err != nil {
			return nil, fmt.Errorf("get emoji image: %w", err)
		}
		return b, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

// AllCustomEmoji returns every custom emoji name (no colons) defined on
// the server, paging until the list is exhausted. Used to seed the
// `:`-picker index; the images themselves stay lazy.
func (c *Client) AllCustomEmoji(ctx context.Context) ([]string, error) {
	const perPage = 200
	var names []string
	for page := 0; ; page++ {
		batch, _, err := c.c.GetEmojiList(ctx, page, perPage)
		if err != nil {
			return nil, fmt.Errorf("get emoji list: %w", err)
		}
		for _, e := range batch {
			if e != nil && e.Name != "" {
				names = append(names, e.Name)
			}
		}
		if len(batch) < perPage {
			break
		}
	}
	return names, nil
}

// ChannelByName resolves a team-qualified channel to its record by URL
// slug — e.g. team "eng", channel "general" for the channel that lives
// at .../eng/channels/general. Used by the headless CLI to turn a
// "team/channel" spec into a channel id in a single request.
func (c *Client) ChannelByName(ctx context.Context, teamName, channelName string) (*model.Channel, error) {
	ch, _, err := c.c.GetChannelByNameForTeamName(ctx, channelName, teamName, "")
	if err != nil {
		return nil, fmt.Errorf("get channel %s/%s: %w", teamName, channelName, err)
	}
	return ch, nil
}

// UserByUsername resolves a username (no leading @) to its user record.
func (c *Client) UserByUsername(ctx context.Context, username string) (*model.User, error) {
	u, _, err := c.c.GetUserByUsername(ctx, username, "")
	if err != nil {
		return nil, fmt.Errorf("get user %s: %w", username, err)
	}
	return u, nil
}

// ChannelMember fetches the member record for a single user/channel pair.
// Used by the listen daemon to check LastViewedAt after a notify delay.
func (c *Client) ChannelMember(ctx context.Context, channelID, userID string) (*model.ChannelMember, error) {
	m, _, err := c.c.GetChannelMember(ctx, channelID, userID, "")
	if err != nil {
		return nil, fmt.Errorf("get channel member: %w", err)
	}
	return m, nil
}

// DirectChannel returns the direct-message channel between two users,
// creating it if it does not yet exist. The call is idempotent: an
// existing DM is returned unchanged.
func (c *Client) DirectChannel(ctx context.Context, userID1, userID2 string) (*model.Channel, error) {
	ch, _, err := c.c.CreateDirectChannel(ctx, userID1, userID2)
	if err != nil {
		return nil, fmt.Errorf("create direct channel: %w", err)
	}
	return ch, nil
}

// GroupChannel returns the group-DM channel shared by the given users,
// creating it if it does not yet exist. Mattermost group DMs hold 3–8
// users; the server adds the requesting user to the set if it isn't
// already listed. The call is idempotent: the same membership always
// maps to the same channel, so an existing group DM is returned
// unchanged rather than duplicated.
func (c *Client) GroupChannel(ctx context.Context, userIDs []string) (*model.Channel, error) {
	ch, _, err := c.c.CreateGroupChannel(ctx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("create group channel: %w", err)
	}
	return ch, nil
}

// CreateChannel creates a public or private channel and joins the calling
// user to it. TeamId, DisplayName, Name and Type must be set; the server
// owns everything else on the record (Id, CreatorId, timestamps). Direct
// and group channels are rejected here by the server — use DirectChannel /
// GroupChannel for those.
func (c *Client) CreateChannel(ctx context.Context, ch *model.Channel) (*model.Channel, error) {
	created, _, err := c.c.CreateChannel(ctx, ch)
	if err != nil {
		return nil, fmt.Errorf("create channel %s: %w", ch.Name, err)
	}
	return created, nil
}

// PatchChannel applies a partial update to a channel — only the patch's
// non-nil fields change. It carries the display name, URL slug, purpose and
// header; the channel's type is NOT patchable here (see UpdateChannelPrivacy).
// The updated record is returned.
func (c *Client) PatchChannel(ctx context.Context, channelID string, patch *model.ChannelPatch) (*model.Channel, error) {
	ch, _, err := c.c.PatchChannel(ctx, channelID, patch)
	if err != nil {
		return nil, fmt.Errorf("patch channel: %w", err)
	}
	return ch, nil
}

// UpdateChannelPrivacy converts a channel between public (O) and private (P).
// It's a dedicated endpoint rather than a PatchChannel field because each
// direction carries its own permission, and the server refuses to convert a
// team's default channel (town-square) at all.
func (c *Client) UpdateChannelPrivacy(ctx context.Context, channelID string, privacy model.ChannelType) (*model.Channel, error) {
	ch, _, err := c.c.UpdateChannelPrivacy(ctx, channelID, privacy)
	if err != nil {
		return nil, fmt.Errorf("update channel privacy: %w", err)
	}
	return ch, nil
}

// ArchiveChannel archives the channel. Mattermost's DELETE /channels/{id} is a
// soft archive: the history survives and a system admin can restore it — which
// is why the UI calls this "archive" rather than "delete".
func (c *Client) ArchiveChannel(ctx context.Context, channelID string) error {
	if _, err := c.c.DeleteChannel(ctx, channelID); err != nil {
		return fmt.Errorf("archive channel: %w", err)
	}
	return nil
}

// RemoveChannelMember removes a user from a channel. Passing your own user id
// is how you leave one: Mattermost has no separate leave endpoint. The server
// refuses for a team's default channel.
func (c *Client) RemoveChannelMember(ctx context.Context, channelID, userID string) error {
	if _, err := c.c.RemoveUserFromChannel(ctx, channelID, userID); err != nil {
		return fmt.Errorf("remove channel member: %w", err)
	}
	return nil
}

// PublicChannelsForTeam returns every open channel on the team, joined or not —
// the catalogue behind "Join a channel". Unlike AllChannels (membership only)
// this is the browsable directory; archived channels are excluded server-side.
// Paged, since the server caps per_page.
func (c *Client) PublicChannelsForTeam(ctx context.Context, teamID string) ([]*model.Channel, error) {
	const perPage = 200
	var all []*model.Channel
	for page := 0; ; page++ {
		batch, _, err := c.c.GetPublicChannelsForTeam(ctx, teamID, page, perPage, "")
		if err != nil {
			return nil, fmt.Errorf("get public channels: %w", err)
		}
		all = append(all, batch...)
		if len(batch) < perPage {
			break
		}
	}
	return all, nil
}
