//go:build eval

// Live agent eval against the *configured* agent: it reads the user's real
// config.yaml (chat endpoint/model/key, ai_search prompt, embeddings) and the
// real Mattermost account (teams, channels, DM partners, usernames), then runs
// the agent loop over the real message cache — exactly the setup the Search tab
// drives. Build-tagged so `make test` never touches it:
//
//	go test -tags eval -run TestLiveCatalog ./internal/aisearch/ -v
//	MATTERBOX_EVAL_Q='did alice ship the checkout fix for bergtoys?' \
//	  go test -tags eval -run TestLiveAgent ./internal/aisearch/ -v -timeout 30m
//
// Unlike TestAgentEval (which pins a local llama.cpp server and a channels
// dump), this one is about *routing*: does the agent aim at the right channel
// or DM, or does it dump the person's name into the keyword terms?
package aisearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/auth"
	"matterbox/internal/config"
	"matterbox/internal/embed"
	"matterbox/internal/mm"
	"matterbox/internal/store"
)

// liveEnv is everything one eval run needs, assembled from the user's own
// config + account, so the agent under test is the configured one.
type liveEnv struct {
	cfg     Config
	cat     Catalog
	system  string
	store   *store.Store
	names   map[string]string
	chans   []*model.Channel
	teams   []*model.Team
	meID    string
	dmNames map[string]string // channelID → partner username
}

func setupLive(t *testing.T) *liveEnv {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cf, err := config.Load()
	if err != nil {
		t.Skipf("config: %v", err)
	}
	token, err := auth.ReadToken()
	if err != nil {
		t.Skipf("token: %v", err)
	}
	client := mm.New(cf.ServerURL, token)
	me, err := client.Me(ctx)
	if err != nil {
		t.Skipf("me: %v", err)
	}
	teams, err := client.Teams(ctx, me.Id)
	if err != nil {
		t.Skipf("teams: %v", err)
	}
	chans, err := client.AllChannels(ctx, me.Id)
	if err != nil {
		t.Skipf("channels: %v", err)
	}

	p, err := store.DefaultPath()
	if err != nil {
		t.Fatalf("store path: %v", err)
	}
	st, err := store.Open(p)
	if err != nil {
		t.Skipf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// Exactly what production does now: one shared resolver for both front-ends.
	people := ResolvePeople(ctx, client, me.Id, chans, st)
	cat := BuildCatalog(me.Id, teams, chans, people)
	if counts, err := st.ChannelPostCounts(); err == nil {
		cat = cat.WithVolumes(counts)
	}
	names := map[string]string{}
	for id, p := range people {
		names[id] = p.Username
	}
	dmNames := map[string]string{}
	for _, c := range chans {
		if c.Type != model.ChannelTypeDirect {
			continue
		}
		for _, id := range strings.Split(c.Name, "__") {
			if id != "" && id != me.Id && names[id] != "" {
				dmNames[c.Id] = names[id]
			}
		}
	}

	ec := cf.Embeddings
	acfg := Config{
		Store:       st,
		Endpoint:    cf.Summary.Endpoint,
		APIKey:      cf.Summary.APIKey,
		Model:       cf.Summary.Model,
		MaxSteps:    cf.AISearch.MaxSteps,
		EmbedClient: embed.New(ec.Endpoint, ec.APIKey, ec.Model, ec.Dim),
		EmbedModel:  ec.Model,
		EmbedDim:    ec.Dim,
	}
	// Model/endpoint overrides so one run can compare two configured agents.
	if v := os.Getenv("MATTERBOX_EVAL_MODEL"); v != "" {
		acfg.Model = v
	}
	if v := os.Getenv("MATTERBOX_EVAL_ENDPOINT"); v != "" {
		acfg.Endpoint = v
	}

	// Mirrors ui.Model.buildAISearchSystem.
	prompt := cf.AISearch.Prompt
	if f := os.Getenv("MATTERBOX_EVAL_PROMPT_FILE"); f != "" {
		b, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			t.Fatalf("prompt file: %v", err)
		}
		prompt = strings.TrimSpace(string(b))
	}
	system := prompt +
		"\n\nTeams you can search: " + strings.Join(cat.TeamNames(), ", ") + "." +
		"\nToday is " + time.Now().Local().Format("Monday, January 2, 2006") + "."

	return &liveEnv{
		cfg: acfg, cat: cat, system: system, store: st,
		names: names, chans: chans, teams: teams, meID: me.Id, dmNames: dmNames,
	}
}

// TestLiveCatalog prints what the agent can actually see: teams, how many
// channels of each kind, and — crucially — whether DMs are discoverable via
// list_channels at all.
func TestLiveCatalog(t *testing.T) {
	env := setupLive(t)
	var pub, priv, dm, gm int
	for _, c := range env.chans {
		switch c.Type {
		case model.ChannelTypeOpen:
			pub++
		case model.ChannelTypePrivate:
			priv++
		case model.ChannelTypeDirect:
			dm++
		case model.ChannelTypeGroup:
			gm++
		}
	}
	fmt.Printf("teams=%d public=%d private=%d dm=%d group=%d users=%d\n",
		len(env.teams), pub, priv, dm, gm, len(env.names))
	fmt.Printf("teams: %s\n", strings.Join(env.cat.TeamNames(), ", "))

	// What does list_channels do with a person's name?
	tools := Tools{catalog: env.cat, memo: newCallMemo(), refs: newHitRefTable()}
	for _, f := range strings.Split(os.Getenv("MATTERBOX_EVAL_FILTERS"), ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		out, step := tools.execListChannels(`{"filter":` + jsonStr(f) + `}`)
		fmt.Printf("\nlist_channels %q → %s\n%s\n", f, step.Result(), out)
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

// TestLiveAgent runs the configured agent over the questions in
// MATTERBOX_EVAL_Q (one per line, or ';'-separated) and prints the full trace
// plus where the hits landed, so mis-routing is visible.
func TestLiveAgent(t *testing.T) {
	env := setupLive(t)
	qs := evalQuestions(t)

	for _, q := range qs {
		var trace []TraceStep
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		start := time.Now()
		res, err := Ask(ctx, env.cfg, env.cat, []Message{
			{Role: "system", Content: env.system},
			{Role: "user", Content: q},
		}, func(s TraceStep) { trace = append(trace, s) })
		cancel()

		fmt.Printf("\n=== Q: %s   (%.1fs, %d calls)\n", q, time.Since(start).Seconds(), len(trace))
		for _, s := range trace {
			fmt.Printf("   · %-90s → %s\n", trunc2(s.Label(), 90), s.Result())
		}
		if err != nil {
			fmt.Printf("   ! %v\n", err)
			continue
		}
		fmt.Printf("   ANSWER: %s\n", strings.ReplaceAll(res.Answer, "\n", " "))
		seen := map[string]int{}
		for _, h := range res.Hits {
			if h.Match != nil {
				seen[env.cat.breadcrumb(h.Match.ChannelId)]++
			}
		}
		var rows []string
		for k, n := range seen {
			rows = append(rows, fmt.Sprintf("%s×%d", k, n))
		}
		sort.Strings(rows)
		fmt.Printf("   HITS(%d): %s\n", len(res.Hits), strings.Join(rows, ", "))
	}
}

func evalQuestions(t *testing.T) []string {
	t.Helper()
	raw := os.Getenv("MATTERBOX_EVAL_Q")
	if f := os.Getenv("MATTERBOX_EVAL_QFILE"); f != "" {
		b, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			t.Fatalf("qfile: %v", err)
		}
		raw = string(b)
	}
	if strings.TrimSpace(raw) == "" {
		t.Skip("set MATTERBOX_EVAL_Q (or MATTERBOX_EVAL_QFILE) to the questions to run")
	}
	var out []string
	for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == ';' }) {
		if s := strings.TrimSpace(line); s != "" && !strings.HasPrefix(s, "#") {
			out = append(out, s)
		}
	}
	return out
}

func trunc2(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// TestLiveTools drives the tools directly (no model) to establish what the
// tool surface can and cannot do — the counterfactual behind a mis-routed run.
func TestLiveTools(t *testing.T) {
	env := setupLive(t)
	mk := func() Tools {
		return Tools{store: env.store, catalog: env.cat, refs: newHitRefTable(), memo: newCallMemo(),
			embedClient: env.cfg.EmbedClient, embedModel: env.cfg.EmbedModel, embedDim: env.cfg.EmbedDim,
			ctx: context.Background()}
	}
	for _, args := range strings.Split(os.Getenv("MATTERBOX_EVAL_CALLS"), "\n") {
		args = strings.TrimSpace(args)
		if args == "" {
			continue
		}
		out, step, hits := mk().execSearch(args)
		fmt.Printf("\n>>> %s\n    %s → %s (%d hits)\n%s\n",
			args, step.Label(), step.Result(), len(hits), indent(out))
	}
	// Author resolution: username vs. full name.
	for _, who := range strings.Split(os.Getenv("MATTERBOX_EVAL_AUTHORS"), ",") {
		who = strings.TrimSpace(who)
		if who == "" {
			continue
		}
		ids := env.cat.resolveAuthor(who)
		var got []string
		for _, id := range ids {
			got = append(got, env.names[id])
		}
		sort.Strings(got)
		fmt.Printf("resolveAuthor(%-22q) → %d: %s\n", who, len(ids), strings.Join(got, ", "))
	}
}

func indent(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		b.WriteString("      " + l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
