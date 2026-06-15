package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
)

// resolver is the slice of the Mattermost client that channel resolution
// needs. *mm.Client satisfies it; tests use a fake so resolution can be
// exercised without a server. The last three methods also satisfy
// mm.Recipients, so the @-spec path delegates to the shared resolver.
type resolver interface {
	ChannelByName(ctx context.Context, team, channel string) (*model.Channel, error)
	UserByUsername(ctx context.Context, name string) (*model.User, error)
	DirectChannel(ctx context.Context, userID1, userID2 string) (*model.Channel, error)
	GroupChannel(ctx context.Context, userIDs []string) (*model.Channel, error)
}

// resolveChannel turns a CLI channel spec into a channel record.
//
//	team/channel   → the channel at .../team/channels/channel (by URL slug)
//	@username      → the direct-message channel with that user
//	@a,@b[,@c…]    → the group-DM channel shared by you and everyone named
//
// me is the current user, needed to open DMs and group DMs.
func resolveChannel(ctx context.Context, r resolver, me *model.User, spec string) (*model.Channel, error) {
	spec = strings.TrimSpace(spec)
	switch {
	case spec == "":
		return nil, fmt.Errorf("empty channel")

	case strings.HasPrefix(spec, "@"):
		// DM / group-DM resolution is shared with the TUI command palette.
		return mm.ResolveRecipients(ctx, r, me.Id, spec)

	default:
		team, channel, ok := strings.Cut(spec, "/")
		if !ok || team == "" || channel == "" {
			return nil, fmt.Errorf("channel %q must be team/channel (e.g. eng/general) or @user", spec)
		}
		ch, err := r.ChannelByName(ctx, team, channel)
		if err != nil {
			return nil, fmt.Errorf("no channel %q: %w", spec, err)
		}
		return ch, nil
	}
}
