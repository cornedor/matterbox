package mm

import (
	"context"
	"fmt"
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
}

// New builds a Client4 wrapper pointed at the given Mattermost server.
// The URL must include the scheme (https://… or http://…); the WS URL
// is derived by swapping http→ws / https→wss.
func New(serverURL, token string) *Client {
	c := model.NewAPIv4Client(serverURL)
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
// initial unread/mention badges.
func (c *Client) ChannelMembers(ctx context.Context, userID string) (model.ChannelMembersWithTeamData, error) {
	m, _, err := c.c.GetChannelMembersWithTeamData(ctx, userID, 0, 500)
	if err != nil {
		return nil, fmt.Errorf("get channel members: %w", err)
	}
	return m, nil
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
