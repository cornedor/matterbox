package mm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

// ChannelMemberAdder is the slice of the Mattermost client that AddMembers
// needs. *Client satisfies it; callers that want to exercise the resolution
// without a server can pass a fake.
type ChannelMemberAdder interface {
	UserByUsername(ctx context.Context, name string) (*model.User, error)
	AddChannelMember(ctx context.Context, channelID, userID string) error
}

// AddMembers adds the users named in an @-prefixed, comma-separated spec to
// the channel. It shares its spec syntax with ResolveRecipients, so `@alice,
// bob` reads the same wherever a user list is typed.
//
// Every name is resolved before anyone is added, so a typo fails the whole
// call rather than half-applying it. Resolved users are then added one at a
// time rather than through the bulk endpoint: that endpoint collapses the
// batch into a single error, while per-user calls let us name exactly who
// couldn't be added (usually "not on this team") and still report the ones who
// were. Duplicate names collapse; adding an existing member is a server-side
// no-op. Returns the usernames added, in the order given, alongside a
// (possibly non-nil) error covering the ones that failed.
func AddMembers(ctx context.Context, c ChannelMemberAdder, channelID, spec string) ([]string, error) {
	names, err := parseUsernames(spec)
	if err != nil {
		return nil, err
	}
	type target struct{ name, id string }
	targets := make([]target, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		u, err := c.UserByUsername(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("no user %q: %w", "@"+name, err)
		}
		if seen[u.Id] {
			continue
		}
		seen[u.Id] = true
		targets = append(targets, target{name: name, id: u.Id})
	}

	var added []string
	var errs []error
	for _, t := range targets {
		if err := c.AddChannelMember(ctx, channelID, t.id); err != nil {
			errs = append(errs, fmt.Errorf("@%s: %w", t.name, err))
			continue
		}
		added = append(added, t.name)
	}
	return added, errors.Join(errs...)
}

// parseUsernames splits an @-prefixed, comma-separated user spec into bare
// usernames. The leading "@" and the spaces around each name are optional.
func parseUsernames(spec string) ([]string, error) {
	parts := strings.Split(spec, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimPrefix(strings.TrimSpace(part), "@")
		if name == "" {
			return nil, fmt.Errorf("empty username in %q", spec)
		}
		out = append(out, name)
	}
	return out, nil
}
