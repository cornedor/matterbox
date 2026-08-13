package aisearch

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
		"Acme › #acme":          "acme",
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

// testCatalog builds a small in-memory catalog without a Model.
func testCatalog() Catalog {
	chs := []channel{
		{id: "c1", name: "acme-project", displayName: "Acme Project", purpose: "Acme platform work", typ: model.ChannelTypeOpen, teamID: "tAcme"},
		{id: "c2", name: "frontend", displayName: "Frontend", purpose: "headless CMS and web UI", typ: model.ChannelTypeOpen, teamID: "tDev"},
		{id: "c3", name: "backend", displayName: "Backend", purpose: "APIs", typ: model.ChannelTypeOpen, teamID: "tDev"},
		{id: "c4", typ: model.ChannelTypeDirect, dmPartner: "alice", name: "me__alice"},
	}
	cat := Catalog{
		byID:      map[string]channel{},
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
	tools := Tools{catalog: testCatalog()}
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

	system := "You are a search agent. Search by topic, not by project name (it lives in the channel title, not the messages). Call search_messages with a short 'query' describing what you want and the likely literal words in 'terms'. Then call finish with a short answer naming the channel. Never answer from your own knowledge."
	ch := make(chan Update, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	messages := []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: "what is the new CMS Acme uses?"},
	}
	cfg := Config{Store: st, Endpoint: endpoint, Model: mdl, MaxSteps: 6}
	go Run(ctx, cfg, testCatalog(), messages, ch)

	var answer string
	var hits []store.SearchHit
	steps := 0
	for u := range ch {
		switch {
		case u.HasStep:
			steps++
			t.Logf("step: %s %q in=%q -> %s", u.Step.tool, u.Step.detail, u.Step.scope, u.Step.result)
		case u.Done:
			if u.Err != nil {
				t.Fatalf("loop error: %v", u.Err)
			}
			answer = u.Answer
			hits = u.Hits
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

// TestExecSearch exercises the tool implementations offline (no LLM): the
// query/terms inputs, the tolerated legacy spellings, repeat suppression, and
// read_around ref resolution. It seeds a temp store against testCatalog's
// channels.
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
	newTools := func() Tools {
		return Tools{store: st, catalog: testCatalog(), refs: newHitRefTable(), memo: newCallMemo()}
	}
	tools := newTools()

	t.Run("terms return hits", func(t *testing.T) {
		out, step, hits := tools.execSearch(`{"query":"the new cms","terms":["storyblok","cms"]}`)
		if step.tool != "search" || len(hits) == 0 {
			t.Fatalf("expected search hits; step=%+v hits=%d out=%q", step, len(hits), out)
		}
	})

	t.Run("query alone still searches", func(t *testing.T) {
		// No 'terms': the keyword half falls back to the query's content words,
		// so the call must not dead-end the way the old schema's did.
		out, step, hits := newTools().execSearch(`{"query":"storyblok headless cms"}`)
		if len(hits) == 0 {
			t.Fatalf("query-only search should work; step=%+v out=%q", step, out)
		}
	})

	t.Run("legacy param spellings still work", func(t *testing.T) {
		for _, args := range []string{
			`{"queries":["storyblok"]}`,
			`{"any_of":["storyblok"]}`,
			`{"keywords":"storyblok"}`,
			`{"text":"storyblok"}`,
		} {
			if _, _, hits := newTools().execSearch(args); len(hits) == 0 {
				t.Errorf("%s should still search", args)
			}
		}
	})

	t.Run("sort recent reorders and is announced", func(t *testing.T) {
		// x1 and x2 both match "storyblok"; x2 is the newer of the two.
		out, step, hits := newTools().execSearch(`{"query":"cms","terms":["storyblok"],"sort":"recent"}`)
		if len(hits) < 2 {
			t.Fatalf("need both hits to check ordering; got %d (%q)", len(hits), out)
		}
		if hits[0].Match.CreateAt < hits[1].Match.CreateAt {
			t.Errorf("sort recent should put the newer post first: %d then %d",
				hits[0].Match.CreateAt, hits[1].Match.CreateAt)
		}
		if !strings.Contains(out, "newest first") {
			t.Errorf("result header should say the order changed: %q", out)
		}
		if !strings.Contains(step.Label(), "newest first") {
			t.Errorf("trace should show the order: %q", step.Label())
		}
	})

	t.Run("an unknown sort value falls back to relevance", func(t *testing.T) {
		out, _, hits := newTools().execSearch(`{"query":"cms","terms":["storyblok"],"sort":"banana"}`)
		if len(hits) == 0 || strings.Contains(out, "newest first") {
			t.Errorf("bad sort should behave as relevance: %q", out)
		}
	})

	t.Run("empty args give a helpful message", func(t *testing.T) {
		out, step, hits := tools.execSearch(`{}`)
		if len(hits) != 0 || step.result != "no query" || !strings.Contains(out, "terms") {
			t.Errorf("empty args: out=%q step=%+v hits=%d", out, step, len(hits))
		}
	})

	t.Run("an identical repeat is short-circuited", func(t *testing.T) {
		tl := newTools()
		if _, step, hits := tl.execSearch(`{"query":"cms","terms":["storyblok"]}`); len(hits) == 0 {
			t.Fatalf("first call should search; step=%+v", step)
		}
		out, step, hits := tl.execSearch(`{"query":"cms","terms":["storyblok"]}`)
		if step.result != "repeat" || len(hits) != 0 || !strings.Contains(out, "already ran") {
			t.Errorf("repeat: out=%q step=%+v hits=%d", out, step, len(hits))
		}
		// A different search still goes through.
		if _, step, _ := tl.execSearch(`{"query":"deploys","terms":["pipeline"]}`); step.result == "repeat" {
			t.Error("a different search must not be treated as a repeat")
		}
	})

	t.Run("read_around does not repeat itself", func(t *testing.T) {
		tl := newTools()
		_, _, hits := tl.execSearch(`{"query":"cms","terms":["storyblok"]}`)
		if len(hits) == 0 {
			t.Fatal("need a hit to get a ref")
		}
		ref := tl.refs.byPost[hits[0].Match.Id]
		if _, step := tl.execReadAround(`{"message":"` + ref + `"}`); step.result == "repeat" {
			t.Fatal("first read must not be a repeat")
		}
		out, step := tl.execReadAround(`{"message":"` + ref + `"}`)
		if step.result != "repeat" || !strings.Contains(out, "already read") {
			t.Errorf("second read: out=%q step=%+v", out, step)
		}
	})

	t.Run("read_around resolves a ref from a prior search", func(t *testing.T) {
		_, _, hits := newTools().execSearch(`{"query":"cms","terms":["storyblok"]}`)
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
		out, _, hits := tools.execSearch(`{"query":"cms","terms":["storyblok"],"offset":1}`)
		if len(hits) != 1 || !strings.Contains(out, "Best matches 2") {
			t.Errorf("offset 1: out=%q hits=%d", out, len(hits))
		}
		out2, step2, hits2 := tools.execSearch(`{"query":"cms","terms":["storyblok"],"offset":5}`)
		if len(hits2) != 0 || step2.result != "0 hits" || !strings.Contains(out2, "No more matches") {
			t.Errorf("offset past end: out=%q step=%+v hits=%d", out2, step2, len(hits2))
		}
	})

	t.Run("scope label marks team vs channel", func(t *testing.T) {
		cases := []struct{ args, want string }{
			{`{"query":"cms","terms":["storyblok"],"team":"Acme"}`, "Acme"},             // bare name = team
			{`{"query":"cms","terms":["storyblok"],"channel":"frontend"}`, "#frontend"}, // # = channel
			{`{"query":"cms","terms":["storyblok"],"team":"Acme","channel":"acme-project"}`, "Acme › #acme-project"},
			{`{"query":"cms","terms":["storyblok"],"channel":"nonexistent-zzz"}`, "#nonexistent-zzz (no match → all)"},
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
			{`{"query":"cms","terms":["storyblok"],"author":"kevin"}`, "by kevin"},
			// A leading @ is tolerated and stripped.
			{`{"query":"cms","terms":["storyblok"],"author":"@kevin"}`, "by kevin"},
			// An unknown author is dropped, but the trace still shows it was tried.
			{`{"query":"cms","terms":["storyblok"],"author":"ghost"}`, "by ghost (no match)"},
			// Dates render as passed, author + dates compose in order.
			{`{"query":"cms","terms":["storyblok"],"after":"2020-01-01"}`, "after 2020-01-01"},
			{`{"query":"cms","terms":["storyblok"],"author":"kevin","after":"2020-01-01","before":"2030-01-01"}`, "by kevin after 2020-01-01 before 2030-01-01"},
			// No author/date filter → empty.
			{`{"query":"cms","terms":["storyblok"]}`, ""},
			// An unparseable date is silently dropped (not shown).
			{`{"query":"cms","terms":["storyblok"],"after":"last tuesday"}`, ""},
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
