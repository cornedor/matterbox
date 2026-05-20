package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

// resolver is the slice of the Mattermost client that channel resolution
// needs. *mm.Client satisfies it; tests use a fake so resolution can be
// exercised without a server.
type resolver interface {
	ChannelByName(ctx context.Context, team, channel string) (*model.Channel, error)
	UserByUsername(ctx context.Context, name string) (*model.User, error)
	DirectChannel(ctx context.Context, userID1, userID2 string) (*model.Channel, error)
}

// resolveChannel turns a CLI channel spec into a channel record.
//
//	team/channel  → the channel at .../team/channels/channel (by URL slug)
//	@username     → the direct-message channel with that user
//
// me is the current user, needed to open DMs. Group DMs (generated names)
// are not addressable here.
func resolveChannel(ctx context.Context, r resolver, me *model.User, spec string) (*model.Channel, error) {
	spec = strings.TrimSpace(spec)
	switch {
	case spec == "":
		return nil, fmt.Errorf("empty channel")

	case strings.HasPrefix(spec, "@"):
		name := strings.TrimPrefix(spec, "@")
		if name == "" {
			return nil, fmt.Errorf("empty username in %q", spec)
		}
		u, err := r.UserByUsername(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("no user %q: %w", spec, err)
		}
		ch, err := r.DirectChannel(ctx, me.Id, u.Id)
		if err != nil {
			return nil, fmt.Errorf("open DM with %q: %w", spec, err)
		}
		return ch, nil

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
