package aisearch

import (
	"context"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/store"
)

// peopleAuthorCap bounds how many distinct cached-message authors are resolved
// for the directory. Ordered by most-recent activity by DistinctUserIDs, so the
// cap drops the long tail of people who last spoke years ago.
const peopleAuthorCap = 4000

// peopleChunk is the batch size for the users-by-ids endpoint.
const peopleChunk = 100

// PeopleFetcher resolves user ids to full profiles. *mm.Client satisfies it;
// the interface keeps this package free of a Mattermost-client dependency.
type PeopleFetcher interface {
	UsersByIDs(ctx context.Context, ids []string) ([]*model.User, error)
}

// ResolvePeople builds the directory a Catalog needs: the reader, everyone they
// have a direct message with, and the authors of cached messages — resolved to
// full profiles so a question can name someone by their real name.
//
// Every step degrades rather than fails: a fetch error just yields a thinner
// directory, because a search with unnamed authors still beats no search. Both
// front-ends use this, so the TUI's agent and the listen daemon's see the same
// people.
func ResolvePeople(ctx context.Context, f PeopleFetcher, meID string, channels []*model.Channel, st *store.Store) map[string]Person {
	if f == nil {
		return nil
	}
	idSet := map[string]struct{}{}
	if meID != "" {
		idSet[meID] = struct{}{}
	}
	for _, c := range channels {
		if c == nil || c.Type != model.ChannelTypeDirect {
			continue
		}
		for _, id := range dmMemberIDs(c.Name, meID) {
			idSet[id] = struct{}{}
		}
	}
	if st != nil {
		if authors, err := st.DistinctUserIDs(peopleAuthorCap); err == nil {
			for _, id := range authors {
				idSet[id] = struct{}{}
			}
		}
	}

	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	people := make(map[string]Person, len(ids))
	for i := 0; i < len(ids); i += peopleChunk {
		end := min(i+peopleChunk, len(ids))
		users, err := f.UsersByIDs(ctx, ids[i:end])
		if err != nil {
			continue
		}
		for id, p := range PeopleFromUsers(users) {
			people[id] = p
		}
	}
	return people
}

// dmMemberIDs pulls the other party's id(s) out of a direct channel's
// "userid__userid" name. A self-DM (both halves equal) yields nothing beyond
// meID, which is already in the set.
func dmMemberIDs(name, meID string) []string {
	var out []string
	for _, id := range strings.Split(name, "__") {
		if id != "" && id != meID {
			out = append(out, id)
		}
	}
	return out
}
