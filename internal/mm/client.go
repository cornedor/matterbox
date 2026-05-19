package mm

import (
	"context"
	"fmt"

	"github.com/mattermost/mattermost/server/public/model"
)

const ServerURL = "https://mattermost.example.com"

type Client struct {
	c     *model.Client4
	token string
}

func New(token string) *Client {
	c := model.NewAPIv4Client(ServerURL)
	c.SetToken(token)
	return &Client{c: c, token: token}
}

// DialWS opens a WebSocket connection to the server and starts the
// listener goroutine. Consume events from wsc.EventChannel.
func (c *Client) DialWS() (*model.WebSocketClient, error) {
	wsURL := "wss" + ServerURL[len("https"):]
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
