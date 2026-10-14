package mm

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/mattermost/mattermost/server/public/model"
)

// Thread follow state is the per-user membership collapsed reply threads
// notify on: the server follows you into a thread when you post in it, are
// mentioned in it, or started it, and unfollowing is the only per-thread mute.
//
// The routes are team-scoped, but the server reads and writes the membership by
// user and thread alone; the team only labels the thread_follow_changed event
// it broadcasts. A DM thread has no team, so teamID "" means "any team of mine"
// (see anyTeam).

// ThreadFollowing reports whether the reader follows the thread rooted at
// rootID. A thread never followed, or unfollowed, is a 404 on this route, which
// is an answer and not an error.
func (c *Client) ThreadFollowing(ctx context.Context, teamID, rootID string) (bool, error) {
	teamID, err := c.anyTeam(ctx, teamID)
	if err != nil {
		return false, err
	}
	_, resp, err := c.c.GetUserThread(ctx, "me", teamID, rootID, false)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, fmt.Errorf("get thread membership: %w", err)
	}
	return true, nil
}

// SetThreadFollowing follows (true) or unfollows (false) the thread rooted at
// rootID.
func (c *Client) SetThreadFollowing(ctx context.Context, teamID, rootID string, follow bool) error {
	teamID, err := c.anyTeam(ctx, teamID)
	if err != nil {
		return err
	}
	if _, err := c.c.UpdateThreadFollowForUser(ctx, "me", teamID, rootID, follow); err != nil {
		return fmt.Errorf("update thread follow: %w", err)
	}
	return nil
}

// anyTeam returns teamID, or for "" (a DM thread) the id of a team the reader
// belongs to, fetched once and then reused.
func (c *Client) anyTeam(ctx context.Context, teamID string) (string, error) {
	if teamID != "" {
		return teamID, nil
	}
	c.teamMu.Lock()
	defer c.teamMu.Unlock()
	if c.anyTeamID != "" {
		return c.anyTeamID, nil
	}
	teams, _, err := c.c.GetTeamsForUser(ctx, "me", "")
	if err != nil {
		return "", fmt.Errorf("get teams: %w", err)
	}
	if len(teams) == 0 {
		return "", errors.New("thread follow needs a team, and you belong to none")
	}
	c.anyTeamID = teams[0].Id
	return c.anyTeamID, nil
}

// Channel fetches one channel by id.
func (c *Client) Channel(ctx context.Context, channelID string) (*model.Channel, error) {
	ch, _, err := c.c.GetChannel(ctx, channelID)
	if err != nil {
		return nil, fmt.Errorf("get channel: %w", err)
	}
	return ch, nil
}
