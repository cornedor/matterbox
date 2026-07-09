package mm

import (
	"context"
	"fmt"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

// Recipients is the slice of the Mattermost client that direct- and
// group-message resolution needs. *Client satisfies it; callers that want to
// exercise resolution without a server can pass a fake.
type Recipients interface {
	UserByUsername(ctx context.Context, name string) (*model.User, error)
	DirectChannel(ctx context.Context, userID1, userID2 string) (*model.Channel, error)
	GroupChannel(ctx context.Context, userIDs []string) (*model.Channel, error)
}

// ResolveRecipients turns an @-prefixed spec into a DM or group-DM channel,
// creating it if it does not yet exist. A single @user opens the direct
// channel with that user; a comma-separated list (@a,@b[,@c…]) opens the
// group DM shared by the current user and everyone named.
//
// meID is the current user's id; they are always a member and are added
// first. Mattermost group DMs hold 3–8 people total, so 2–7 distinct others
// may be listed alongside you. The current user is implicit — naming yourself
// is harmless (it's deduped) but unnecessary. Duplicate names collapse, so
// `@a,@a` is just a DM with a. The leading "@" and surrounding spaces on each
// name are optional, so this is shared verbatim between the CLI (where users
// type the spec) and the TUI command palette.
func ResolveRecipients(ctx context.Context, r Recipients, meID, spec string) (*model.Channel, error) {
	// ids accumulates the membership with me first; seen dedupes (including
	// me, so naming yourself doesn't inflate the count). names tracks the
	// distinct others purely for readable error messages.
	ids := []string{meID}
	seen := map[string]bool{meID: true}
	var names []string
	parsed, err := parseUsernames(spec)
	if err != nil {
		return nil, err
	}
	for _, name := range parsed {
		u, err := r.UserByUsername(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("no user %q: %w", "@"+name, err)
		}
		if seen[u.Id] {
			continue
		}
		seen[u.Id] = true
		ids = append(ids, u.Id)
		names = append(names, name)
	}

	others := len(ids) - 1
	switch {
	case others == 0:
		// Only the current user survived (e.g. "@me" or "@a,@a" where a is me).
		return nil, fmt.Errorf("no other users in %q", spec)

	case others == 1:
		ch, err := r.DirectChannel(ctx, meID, ids[1])
		if err != nil {
			return nil, fmt.Errorf("open DM with @%s: %w", names[0], err)
		}
		return ch, nil

	case others+1 > model.ChannelGroupMaxUsers:
		return nil, fmt.Errorf("group DM holds at most %d people; %q names %d (you + %d others)",
			model.ChannelGroupMaxUsers, spec, others+1, others)

	default:
		ch, err := r.GroupChannel(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("open group DM with @%s: %w", strings.Join(names, ", @"), err)
		}
		return ch, nil
	}
}
