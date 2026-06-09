package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/store"
)

func TestNormalizeChannelArg(t *testing.T) {
	cases := map[string]string{
		"#frontend":             "frontend",
		"frontend":              "frontend",
		"Acme › #acme":  "acme",
		"Ops > #infra-platform": "infra-platform",
		"DMs › @alice":          "alice",
		"  🔒secret  ":           "secret",
		"team/channel-name":     "channel-name",
		"":                      "",
		"·group dm":             "group dm",
	}
	for in, want := range cases {
		if got := normalizeChannelArg(in); got != want {
			t.Errorf("normalizeChannelArg(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestAISearchQuery(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"the new cms acme uses?", "the new cms acme uses?", true},
		{"  what is storyblok ?  ", "what is storyblok ?", true},
		{"plain search", "", false},
		{"?", "", false},       // no text before ?
		{"in:foo?", "", false}, // only a modifier
		{"team:bar what changed?", "team:bar what changed?", true},
	}
	for _, tc := range cases {
		got, ok := aiSearchQuery(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("aiSearchQuery(%q) = (%q, %v); want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

// testCatalog builds a small in-memory catalog without a Model.
func testCatalog() searchCatalog {
	chs := []catChannel{
		{id: "c1", name: "acme-project", displayName: "Acme Project", purpose: "Acme platform work", typ: model.ChannelTypeOpen, teamID: "tAcme"},
		{id: "c2", name: "frontend", displayName: "Frontend", purpose: "headless CMS and web UI", typ: model.ChannelTypeOpen, teamID: "tDev"},
		{id: "c3", name: "backend", displayName: "Backend", purpose: "APIs", typ: model.ChannelTypeOpen, teamID: "tDev"},
		{id: "c4", typ: model.ChannelTypeDirect, dmPartner: "alice", name: "me__alice"},
	}
	cat := searchCatalog{
		byID:      map[string]catChannel{},
		teamNames: map[string]string{"tAcme": "Acme", "tDev": "Dev"},
		userNames: map[string]string{"u1": "kevin"},
		teams: []*model.Team{
			{Id: "tAcme", Name: "acme", DisplayName: "Acme"},
			{Id: "tDev", Name: "dev", DisplayName: "Dev"},
		},
		channels: chs,
	}
	for _, c := range chs {
		cat.byID[c.id] = c
	}
	return cat
}

func TestResolveScope(t *testing.T) {
	cat := testCatalog()
	cases := []struct {
		name        string
		team, chn   string
		wantIDs     []string
		wantReq     bool
		wantMatched bool
	}{
		{"none", "", "", nil, false, false},
		{"exact channel", "", "frontend", []string{"c2"}, true, true},
		{"channel with breadcrumb", "", "Dev › #frontend", []string{"c2"}, true, true},
		{"substring channel", "", "front", []string{"c2"}, true, true},
		{"team only", "Dev", "", []string{"c2", "c3"}, true, true},
		{"team + channel", "Dev", "frontend", []string{"c2"}, true, true},
		{"dm partner", "", "alice", []string{"c4"}, true, true},
		{"no match", "", "nonexistent-zzz", nil, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids, req, matched := cat.resolveScope(tc.team, tc.chn)
			if req != tc.wantReq || matched != tc.wantMatched {
				t.Fatalf("resolveScope(%q,%q) flags = (req=%v, matched=%v); want (%v,%v)",
					tc.team, tc.chn, req, matched, tc.wantReq, tc.wantMatched)
			}
			if !sameStringSet(ids, tc.wantIDs) {
				t.Errorf("resolveScope(%q,%q) ids = %v; want %v", tc.team, tc.chn, ids, tc.wantIDs)
			}
		})
	}
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestFormatHitUsesUsernames(t *testing.T) {
	tools := aiSearchTools{catalog: testCatalog()}
	p := &model.Post{
		ChannelId: "c1",
		UserId:    "u1",
		CreateAt:  time.Date(2026, 4, 12, 9, 0, 0, 0, time.Local).UnixMilli(),
		Message:   "We're migrating Acme to Storyblok\nas the new CMS",
	}
	got := tools.formatHit(p)
	if !strings.Contains(got, "@kevin") {
		t.Errorf("formatHit should resolve the username: %q", got)
	}
	if !strings.Contains(got, "Acme › #acme-project") {
		t.Errorf("formatHit should include the breadcrumb: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("formatHit should be a single line: %q", got)
	}
}

// TestAISearchLoopE2E drives the real agent loop against a live local server.
// Opt-in: set MATTERBOX_LLM_E2E=1 (and have the server from config running).
// It plants messages in a temp store and checks the agent finds them.
func TestAISearchLoopE2E(t *testing.T) {
	if os.Getenv("MATTERBOX_LLM_E2E") == "" {
		t.Skip("set MATTERBOX_LLM_E2E=1 to run the live agent-loop test")
	}
	endpoint := envOr("MATTERBOX_LLM_ENDPOINT", "http://127.0.0.1:8321")
	mdl := envOr("MATTERBOX_LLM_MODEL", "gemma-4-E4B-it-UD-Q4_K_XL.gguf")

	st, err := store.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	now := time.Now().UnixMilli()
	// Note: none of these messages contain "Acme" — that name only lives
	// in the channel title (testCatalog's c1 = #acme-project). The agent
	// must search by topic (CMS / Storyblok), not by the project name.
	posts := []*model.Post{
		{Id: "p1", ChannelId: "c1", UserId: "u1", CreateAt: now - 5000, Message: "we're migrating to Storyblok as the new headless CMS"},
		{Id: "p2", ChannelId: "c1", UserId: "u1", CreateAt: now - 4000, Message: "Storyblok onboarding call scheduled next week"},
		{Id: "p3", ChannelId: "c2", UserId: "u1", CreateAt: now - 3000, Message: "anyone integrated Storyblok with Next.js before?"},
		{Id: "p4", ChannelId: "c3", UserId: "u1", CreateAt: now - 2000, Message: "deploy pipeline is green again"},
	}
	if err := st.UpsertMany(posts); err != nil {
		t.Fatalf("seed posts: %v", err)
	}

	tools := aiSearchTools{store: st, catalog: testCatalog(), refs: newHitRefTable()}
	system := "You are a search agent. Search by topic, not by project name (it lives in the channel title, not the messages). search_messages is keyword search: put topic words and synonyms in 'any_of'; if there are too many matches narrow with 'all_of'/'phrase'/'none_of', if zero then loosen. Then call finish with a short answer naming the channel. Never answer from your own knowledge."
	ch := make(chan aiSearchUpdate, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	messages := []aiMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: "what is the new CMS Acme uses?"},
	}
	go runAISearchLoop(ctx, endpoint, "", mdl, messages, 6, tools, ch)

	var answer string
	var hits []store.SearchHit
	steps := 0
	for u := range ch {
		switch {
		case u.hasStep:
			steps++
			t.Logf("step: %s %q in=%q -> %s", u.step.tool, u.step.detail, u.step.scope, u.step.result)
		case u.done:
			if u.err != nil {
				t.Fatalf("loop error: %v", u.err)
			}
			answer = u.answer
			hits = u.hits
		}
	}
	t.Logf("answer: %s", answer)
	t.Logf("collected %d hits over %d tool steps", len(hits), steps)
	if steps == 0 {
		t.Errorf("expected the agent to call at least one tool")
	}
	if !strings.Contains(strings.ToLower(answer), "storyblok") {
		t.Errorf("expected the answer to mention Storyblok; got: %q", answer)
	}
	if len(hits) == 0 {
		t.Errorf("expected the agent to surface at least one hit bubble")
	}
}

// TestExecSearch exercises the tool implementations offline (no LLM): the new
// precision/recall params, the legacy 'queries' fallback, and read_around ref
// resolution. It seeds a temp store against testCatalog's channels.
func TestExecSearch(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "exec.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	now := time.Now().UnixMilli()
	posts := []*model.Post{
		{Id: "x1aaaaaaaaaaaaaaaaaaaaaaaa", ChannelId: "c1", UserId: "u1", CreateAt: now - 3000, Message: "migrating to Storyblok as the new headless CMS"},
		{Id: "x2aaaaaaaaaaaaaaaaaaaaaaaa", ChannelId: "c1", UserId: "u1", CreateAt: now - 2000, Message: "Storyblok onboarding call next week"},
		{Id: "x3aaaaaaaaaaaaaaaaaaaaaaaa", ChannelId: "c2", UserId: "u1", CreateAt: now - 1000, Message: "deploy pipeline is green again"},
	}
	if err := st.UpsertMany(posts); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tools := aiSearchTools{store: st, catalog: testCatalog(), refs: newHitRefTable()}

	t.Run("any_of returns hits", func(t *testing.T) {
		out, step, hits := tools.execSearch(`{"any_of":["storyblok","cms"]}`)
		if step.tool != "search" || len(hits) == 0 {
			t.Fatalf("expected search hits; step=%+v hits=%d out=%q", step, len(hits), out)
		}
	})

	t.Run("all_of narrows a broad any_of", func(t *testing.T) {
		_, _, broad := tools.execSearch(`{"any_of":["storyblok"]}`)
		_, _, narrow := tools.execSearch(`{"any_of":["storyblok"],"all_of":["onboarding"]}`)
		if len(narrow) >= len(broad) {
			t.Errorf("all_of should narrow: broad=%d narrow=%d", len(broad), len(narrow))
		}
	})

	t.Run("legacy queries param still works", func(t *testing.T) {
		if _, _, hits := tools.execSearch(`{"queries":["storyblok"]}`); len(hits) == 0 {
			t.Error("legacy 'queries' should fall back to any_of")
		}
	})

	t.Run("no terms gives a helpful message", func(t *testing.T) {
		out, step, hits := tools.execSearch(`{}`)
		if len(hits) != 0 || step.result != "no terms" || !strings.Contains(out, "any_of") {
			t.Errorf("empty args: out=%q step=%+v hits=%d", out, step, len(hits))
		}
	})

	t.Run("read_around resolves a ref from a prior search", func(t *testing.T) {
		_, _, hits := tools.execSearch(`{"any_of":["storyblok"]}`)
		if len(hits) == 0 {
			t.Fatal("need a hit to get a ref")
		}
		ref := tools.refs.byPost[hits[0].Match.Id]
		if ref == "" {
			t.Fatal("hit was not assigned a ref")
		}
		out, step := tools.execReadAround(`{"message":"` + ref + `"}`)
		if step.tool != "read" || !strings.Contains(out, "Context in") {
			t.Errorf("read_around: out=%q step=%+v", out, step)
		}
	})

	t.Run("read_around rejects an unknown ref", func(t *testing.T) {
		out, step := tools.execReadAround(`{"message":"m999"}`)
		if step.result != "unknown ref" || !strings.Contains(out, "Unknown message ref") {
			t.Errorf("expected unknown-ref handling; out=%q step=%+v", out, step)
		}
	})

	t.Run("offset pages and reports the window", func(t *testing.T) {
		// "storyblok" matches x1 and x2 (2 total); offset 1 shows the second.
		out, _, hits := tools.execSearch(`{"any_of":["storyblok"],"offset":1}`)
		if len(hits) != 1 || !strings.Contains(out, "Showing matches 2") {
			t.Errorf("offset 1: out=%q hits=%d", out, len(hits))
		}
		out2, step2, hits2 := tools.execSearch(`{"any_of":["storyblok"],"offset":5}`)
		if len(hits2) != 0 || step2.result != "0 hits" || !strings.Contains(out2, "No more matches") {
			t.Errorf("offset past end: out=%q step=%+v hits=%d", out2, step2, len(hits2))
		}
	})

	t.Run("scope label marks team vs channel", func(t *testing.T) {
		cases := []struct{ args, want string }{
			{`{"any_of":["storyblok"],"team":"Acme"}`, "Acme"},         // bare name = team
			{`{"any_of":["storyblok"],"channel":"frontend"}`, "#frontend"}, // # = channel
			{`{"any_of":["storyblok"],"team":"Acme","channel":"acme-project"}`, "Acme › #acme-project"},
			{`{"any_of":["storyblok"],"channel":"nonexistent-zzz"}`, "#nonexistent-zzz (no match → all)"},
		}
		for _, tc := range cases {
			_, step, _ := tools.execSearch(tc.args)
			if step.scope != tc.want {
				t.Errorf("scope for %s = %q; want %q", tc.args, step.scope, tc.want)
			}
		}
	})

	t.Run("author and date narrowing show in the trace", func(t *testing.T) {
		cases := []struct{ args, want string }{
			// kevin (u1) authored every seeded post → filter applies.
			{`{"any_of":["storyblok"],"author":"kevin"}`, "by kevin"},
			// A leading @ is tolerated and stripped.
			{`{"any_of":["storyblok"],"author":"@kevin"}`, "by kevin"},
			// An unknown author is dropped, but the trace still shows it was tried.
			{`{"any_of":["storyblok"],"author":"ghost"}`, "by ghost (no match)"},
			// Dates render as passed, author + dates compose in order.
			{`{"any_of":["storyblok"],"after":"2020-01-01"}`, "after 2020-01-01"},
			{`{"any_of":["storyblok"],"author":"kevin","after":"2020-01-01","before":"2030-01-01"}`, "by kevin after 2020-01-01 before 2030-01-01"},
			// No author/date filter → empty.
			{`{"any_of":["storyblok"]}`, ""},
			// An unparseable date is silently dropped (not shown).
			{`{"any_of":["storyblok"],"after":"last tuesday"}`, ""},
		}
		for _, tc := range cases {
			_, step, _ := tools.execSearch(tc.args)
			if step.filters != tc.want {
				t.Errorf("filters for %s = %q; want %q", tc.args, step.filters, tc.want)
			}
		}
	})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
