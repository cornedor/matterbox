package aisearch

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/store"
)

// TestResolvePeopleByName covers the lookup that turns a name in a question
// into someone to filter by. A question says "Alice Jansen", never "alice" —
// resolving only usernames silently dropped the filter and searched everything.
func TestResolvePeopleByName(t *testing.T) {
	cat := testCatalog()
	cases := []struct {
		in   string
		want []string
	}{
		{"kevin", []string{"kevin"}},
		{"@kevin", []string{"kevin"}},
		{"Alice Jansen", []string{"alice"}},   // real name
		{"alice jansen", []string{"alice"}},   // case-insensitive
		{"Ali", []string{"alice"}},            // nickname, exact
		{"Kevin de Vries", []string{"kevin"}}, // real name with particles
		{"jansen", []string{"alice"}},         // substring of a real name
		{"nobody at all", nil},
	}
	for _, c := range cases {
		var got []string
		for _, p := range cat.resolvePeople(c.in) {
			got = append(got, p.Username)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("resolvePeople(%q) = %v; want %v", c.in, got, c.want)
		}
	}
}

// TestResolvePeopleExactBeatsSubstring: an exact username must not drag in
// everyone whose name merely contains it, or an author filter for a common
// first name stops narrowing anything.
func TestResolvePeopleExactBeatsSubstring(t *testing.T) {
	cat := testCatalog()
	cat.people["u3"] = Person{ID: "u3", Username: "kevinsmith", FullName: "Kevin Smith"}
	got := cat.resolvePeople("kevin")
	if len(got) != 1 || got[0].Username != "kevin" {
		t.Fatalf("resolvePeople(kevin) = %+v; want just @kevin", got)
	}
	// With no exact match, substring is the useful fallback.
	if got := cat.resolvePeople("kevins"); len(got) != 1 || got[0].Username != "kevinsmith" {
		t.Errorf("resolvePeople(kevins) = %+v; want @kevinsmith", got)
	}
}

// TestDMChannelsWith checks the link from a person to the conversations a
// team-scoped search can never reach.
func TestDMChannelsWith(t *testing.T) {
	cat := testCatalog()
	// Both the one-to-one DM (matched by user id) and the group DM (matched by
	// the usernames in its display name, the only handle a group DM exposes).
	got := cat.dmChannelsWith(cat.resolvePeople("Alice Jansen"))
	if strings.Join(got, ",") != "c4,c6" {
		t.Fatalf("dmChannelsWith(alice) = %v; want [c4 c6]", got)
	}
	if got := cat.dmChannelsWith(nil); got != nil {
		t.Errorf("dmChannelsWith(nil) = %v; want nil", got)
	}
}

// execSearchStore seeds a store where the answer to "did alice do the migration
// for Acme" lives in the DM with alice — not in Acme's own channel. That is the
// real shape the eval exposed: scoping to the client hides the answer.
func execSearchStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "routing.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().UnixMilli()
	posts := []*model.Post{
		{Id: "r1aaaaaaaaaaaaaaaaaaaaaaaa", ChannelId: "c1", UserId: "u1", CreateAt: now - 5000, Message: "standup notes for the week"},
		{Id: "r2aaaaaaaaaaaaaaaaaaaaaaaa", ChannelId: "c4", UserId: "u2", CreateAt: now - 4000, Message: "finished the storyblok migration, all green"},
		{Id: "r3aaaaaaaaaaaaaaaaaaaaaaaa", ChannelId: "c4", UserId: "u2", CreateAt: now - 3000, Message: "one more storyblok fix and we are done"},
	}
	if err := st.UpsertMany(posts); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return st
}

// TestSearchTeamScopeIncludesAuthorDMs is the core regression: naming both a
// person and a team must not AND them into an empty result. A team holds no
// direct messages, so the scope has to widen to the DMs with that person.
func TestSearchTeamScopeIncludesAuthorDMs(t *testing.T) {
	st := execSearchStore(t)
	tools := Tools{store: st, catalog: testCatalog(), refs: newHitRefTable(), memo: newCallMemo()}

	out, step, hits := tools.execSearch(`{"query":"the storyblok migration","terms":["storyblok"],"team":"Acme","author":"Alice Jansen"}`)
	if len(hits) == 0 {
		t.Fatalf("team+author search found nothing; step=%+v out=%q", step, out)
	}
	if !strings.Contains(out, "no direct messages") {
		t.Errorf("result should say the DMs were searched too; got %q", out)
	}
	if !strings.Contains(step.scope, "+ DMs") {
		t.Errorf("trace scope = %q; want it to show the DM widening", step.scope)
	}
	if !strings.Contains(step.filters, "@alice") {
		t.Errorf("trace filters = %q; want the resolved username", step.filters)
	}
}

// TestSearchAuthorResolvesRealName: a real name must actually filter, and the
// trace must name who it resolved to rather than echoing the question.
func TestSearchAuthorResolvesRealName(t *testing.T) {
	st := execSearchStore(t)
	tools := Tools{store: st, catalog: testCatalog(), refs: newHitRefTable(), memo: newCallMemo()}
	_, step, hits := tools.execSearch(`{"query":"migration","terms":["storyblok"],"author":"Alice Jansen"}`)
	if len(hits) == 0 {
		t.Fatal("author search by real name found nothing")
	}
	if step.filters != "by @alice" {
		t.Errorf("filters = %q; want \"by @alice\"", step.filters)
	}
}

// TestSearchUnknownAuthorPointsAtFindPeople: an unmatched name silently
// searched everything, so the agent never learned it had guessed wrong.
func TestSearchUnknownAuthorPointsAtFindPeople(t *testing.T) {
	st := execSearchStore(t)
	tools := Tools{store: st, catalog: testCatalog(), refs: newHitRefTable(), memo: newCallMemo()}
	out, _, _ := tools.execSearch(`{"query":"migration","terms":["storyblok"],"author":"Someone Else"}`)
	if !strings.Contains(out, "find_people") {
		t.Errorf("an unmatched author should point at find_people; got %q", out)
	}
}

// TestSearchHintsAtDMs: an unscoped search whose answer sits in a DM must say
// so. Channel chatter can out-rank the one message that matters, and without
// the hint the agent has no way to learn the conversation exists.
func TestSearchHintsAtDMs(t *testing.T) {
	st := execSearchStore(t)
	tools := Tools{store: st, catalog: testCatalog(), refs: newHitRefTable(), memo: newCallMemo()}
	out, _, _ := tools.execSearch(`{"query":"the migration","terms":["storyblok"],"channel":"acme-project"}`)
	if !strings.Contains(out, "Also in your direct messages") || !strings.Contains(out, "@alice") {
		t.Errorf("scoped search should point at the DM carrying the term; got %q", out)
	}
	// Already searching that DM: no point telling the agent to go there.
	out2, _, _ := tools.execSearch(`{"query":"the migration","terms":["storyblok"],"channel":"@alice"}`)
	if strings.Contains(out2, "Also in your direct messages") {
		t.Errorf("hint should be suppressed when the DM is already in scope; got %q", out2)
	}
}

// TestListChannelsFindsTeamsAndDMs: a client name usually names a TEAM, and a
// person names a DM — neither of which the old substring-over-channel-names
// match could ever find.
func TestListChannelsFindsTeamsAndDMs(t *testing.T) {
	tools := Tools{catalog: testCatalog(), refs: newHitRefTable(), memo: newCallMemo()}

	out, step, _ := tools.listChannelsT(`{"filter":"Dev"}`)
	if !strings.Contains(out, "#frontend") || !strings.Contains(out, "#backend") {
		t.Errorf("team filter should list the team's channels; got %q (%s)", out, step.Result())
	}

	out, _, _ = tools.listChannelsT(`{"filter":"Alice Jansen"}`)
	if !strings.Contains(out, "DMs › @alice") {
		t.Errorf("a person's real name should find their DM; got %q", out)
	}

	out, _, _ = tools.listChannelsT(`{"filter":""}`)
	if !strings.Contains(out, "Teams:") || !strings.Contains(out, "DMs › @alice") {
		t.Errorf("empty filter should orient with teams and the busiest conversations; got %q", out)
	}
	// Busiest first: the 900-message DM outranks the 40-message channel.
	if i, j := strings.Index(out, "@alice"), strings.Index(out, "#acme-project"); i < 0 || j < 0 || i > j {
		t.Errorf("channels should be listed busiest first; got %q", out)
	}
}

// TestFindPeople covers the directory lookup: the username to filter by, the
// real name that confirms it, and whether a DM with them is worth searching.
func TestFindPeople(t *testing.T) {
	tools := Tools{catalog: testCatalog(), refs: newHitRefTable(), memo: newCallMemo()}

	out, step, _ := tools.findPeopleT(`{"name":"Alice Jansen"}`)
	if !strings.Contains(out, "@alice") || !strings.Contains(out, "900") {
		t.Errorf("find_people should give the username and DM size; got %q", out)
	}
	if !strings.Contains(out, `channel:"@alice"`) {
		t.Errorf("find_people should show how to search the DM; got %q", out)
	}
	if step.Result() != "1 people" {
		t.Errorf("result summary = %q", step.Result())
	}

	out, _, _ = tools.findPeopleT(`{"name":"nobody"}`)
	if !strings.Contains(out, "Nobody matches") {
		t.Errorf("unknown name should say so; got %q", out)
	}
}

// listChannelsT / findPeopleT adapt the two-value tool signatures to the
// three-value shape the tests above read against.
func (t Tools) listChannelsT(args string) (string, TraceStep, []store.SearchHit) {
	out, step := t.execListChannels(args)
	return out, step, nil
}

func (t Tools) findPeopleT(args string) (string, TraceStep, []store.SearchHit) {
	out, step := t.execFindPeople(args)
	return out, step, nil
}
