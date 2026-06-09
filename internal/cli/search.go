package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/spf13/cobra"

	"matterbox/internal/embed"
	"matterbox/internal/semindex"
	"matterbox/internal/store"
)

// defaultSearchLimit is how many matches `search` shows when --limit is not
// given. A page, not a firehose — page further with --offset.
const defaultSearchLimit = 20

func newSearchCmd() *cobra.Command {
	var (
		channel  string
		from     string
		since    string
		until    string
		limit    int
		offset   int
		contextN int
		semantic bool
		asJSONFn func() (bool, error)
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search cached messages over the local store (keyword or semantic)",
		Long: "Search the locally-cached message corpus — the same store the TUI Search\n" +
			"tab and `digest` use — and print the matches, best-ranked first. This is a\n" +
			"fast local query: it only sees what matterbox has cached, and (apart from\n" +
			"--semantic) makes no Mattermost API calls beyond resolving labels.\n\n" +
			"By default it is a keyword (FTS5) search: every word must appear, prefix-\n" +
			"matched, so `tweak` matches `tweakwise`. With --semantic the query is\n" +
			"embedded and blended with the keyword ranking (hybrid) so conceptually\n" +
			"similar messages surface even without a shared word — this needs the\n" +
			"embeddings server up (see scripts/llama-embeddings.sh) and vectors built\n" +
			"(matterbox embed).\n\n" +
			"--channel (team/channel or @user) and --from (@user) narrow the scope;\n" +
			"--since / --until bound it by time (now, today, yesterday, 7d, 2h,\n" +
			"2006-01-02; --until exclusive). Page large result sets with --offset.\n\n" +
			"  matterbox search tweakwise\n" +
			"  matterbox search \"release plan\" --channel eng/general --since 7d\n" +
			"  matterbox search deadline --from @alice --json\n" +
			"  matterbox search \"how do we deploy\" --semantic",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := asJSONFn()
			if err != nil {
				return err
			}
			sinceMs, untilMs, err := parseSinceUntil(since, until, time.Now(), time.Local)
			if err != nil {
				return err
			}
			return runSearch(cmd.Context(), searchOpts{
				query:    strings.Join(args, " "),
				channel:  channel,
				from:     from,
				sinceMs:  sinceMs,
				untilMs:  untilMs,
				limit:    limit,
				offset:   offset,
				contextN: contextN,
				semantic: semantic,
				asJSON:   asJSON,
			}, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&channel, "channel", "c", "", "restrict to one channel (team/channel or @user)")
	cmd.Flags().StringVarP(&from, "from", "f", "", "restrict to one author (@user)")
	cmd.Flags().StringVar(&since, "since", "", "only messages at or after this time (e.g. yesterday, 7d, 2026-06-08)")
	cmd.Flags().StringVar(&until, "until", "", "only messages before this time (exclusive)")
	cmd.Flags().IntVarP(&limit, "limit", "n", defaultSearchLimit, "maximum matches to show")
	cmd.Flags().IntVar(&offset, "offset", 0, "skip this many top-ranked matches (paging)")
	cmd.Flags().IntVar(&contextN, "context", 0, "show this many surrounding messages around each match (text output only)")
	cmd.Flags().BoolVar(&semantic, "semantic", false, "blend semantic similarity with keyword matching (needs the embeddings server)")
	asJSONFn = addOutputFlags(cmd)
	return cmd
}

// searchOpts bundles the resolved search parameters so runSearch's signature
// stays readable.
type searchOpts struct {
	query    string
	channel  string
	from     string
	sinceMs  int64
	untilMs  int64
	limit    int
	offset   int
	contextN int
	semantic bool
	asJSON   bool
}

func runSearch(ctx context.Context, o searchOpts, out io.Writer) error {
	if strings.TrimSpace(o.query) == "" {
		return fmt.Errorf("empty query")
	}
	if o.limit <= 0 {
		o.limit = defaultSearchLimit
	}

	cfg, client, err := dial()
	if err != nil {
		return err
	}
	me, err := client.Me(ctx)
	if err != nil {
		return err
	}

	// Optional scopes. nil = "no scope" (search everywhere); see store.Search
	// for the nil-vs-empty convention the store relies on.
	var channelIDs, authorIDs []string
	if o.channel != "" {
		ch, err := resolveChannel(ctx, client, me, o.channel)
		if err != nil {
			return err
		}
		channelIDs = []string{ch.Id}
	}
	if o.from != "" {
		u, err := client.UserByUsername(ctx, strings.TrimPrefix(o.from, "@"))
		if err != nil {
			return fmt.Errorf("no user %q: %w", o.from, err)
		}
		authorIDs = []string{u.Id}
	}

	p, err := store.DefaultPath()
	if err != nil {
		return err
	}
	st, err := store.Open(p)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	var (
		hits   []store.SearchHit
		total  int
		capped bool
	)
	if o.semantic {
		ec := cfg.Embeddings
		ecl := embed.New(ec.Endpoint, ec.APIKey, ec.Model, ec.Dim)
		vec, eerr := ecl.EmbedOne(ctx, embed.QueryText(o.query))
		if eerr != nil {
			return fmt.Errorf("embed query: %w (is the embeddings server up? see scripts/llama-embeddings.sh)", eerr)
		}
		scope := store.HybridScope{ChannelIDs: channelIDs, AuthorIDs: authorIDs, After: o.sinceMs, Before: o.untilMs}
		hits, total, err = st.SearchHybrid(o.query, vec, semindex.ModelTag(ec.Model, ec.Dim), scope, o.limit, o.offset, o.contextN)
	} else {
		spec := store.SearchSpec{
			AllOf:      strings.Fields(o.query),
			ChannelIDs: channelIDs,
			AuthorIDs:  authorIDs,
			After:      o.sinceMs,
			Before:     o.untilMs,
		}
		hits, total, err = st.SearchSpec(spec, o.limit, o.offset, o.contextN)
		capped = total >= store.MatchCountCap // keyword total saturates at the cap
	}
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		fmt.Fprintln(os.Stderr, "matterbox: no matches")
		return nil
	}

	// Labels (channel breadcrumbs, usernames) are best-effort metadata: degrade
	// to raw ids rather than failing a search whose hits are already in hand.
	channels, _ := client.AllChannels(ctx, me.Id)
	chByID := make(map[string]*model.Channel, len(channels))
	for _, c := range channels {
		chByID[c.Id] = c
	}
	teamSlug := map[string]string{}
	if teams, terr := client.Teams(ctx, me.Id); terr == nil {
		for _, t := range teams {
			teamSlug[t.Id] = t.Name
		}
	}
	names, _ := client.UsernamesByIDs(ctx, searchUserIDs(hits, chByID, me.Id, o.contextN > 0))
	if names == nil {
		names = map[string]string{}
	}
	lbl := labeler{meID: me.Id, teamSlug: teamSlug, channels: chByID, names: names}

	if o.asJSON {
		matches := make([]*model.Post, len(hits))
		for i, h := range hits {
			matches[i] = h.Match
		}
		if err := writeJSONPosts(out, lbl.header, names, matches); err != nil {
			return err
		}
	} else {
		printSearchHits(out, lbl, names, hits, o.contextN > 0)
	}

	printSearchSummary(os.Stderr, len(hits), total, capped, o.offset, o.limit)
	return nil
}

// searchUserIDs collects every id a search render needs a username for: each
// match's author, the DM partner of any direct-message channel a match lands
// in, and (when context is shown) the authors of the surrounding posts.
func searchUserIDs(hits []store.SearchHit, chByID map[string]*model.Channel, meID string, withContext bool) []string {
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, h := range hits {
		add(h.Match.UserId)
		if withContext {
			for _, p := range h.Before {
				add(p.UserId)
			}
			for _, p := range h.After {
				add(p.UserId)
			}
		}
		if ch := chByID[h.Match.ChannelId]; ch != nil && ch.Type == model.ChannelTypeDirect {
			add(dmPartnerID(ch, meID))
		}
	}
	return ids
}

// printSearchHits renders each hit under its channel breadcrumb. The matched
// post is flush-left; when context is requested the surrounding posts are
// indented so the match still stands out. Timestamps carry the date since hits
// span channels and time.
func printSearchHits(out io.Writer, lbl labeler, names map[string]string, hits []store.SearchHit, withContext bool) {
	for i, h := range hits {
		fmt.Fprintf(out, "%s\n", lbl.header(h.Match.ChannelId))
		if withContext {
			io.WriteString(out, indentLines(formatPostsLayout(h.Before, names, "Jan 2 15:04"), "  "))
		}
		io.WriteString(out, formatPostsLayout([]*model.Post{h.Match}, names, "Jan 2 15:04"))
		if withContext {
			io.WriteString(out, indentLines(formatPostsLayout(h.After, names, "Jan 2 15:04"), "  "))
		}
		if i < len(hits)-1 {
			io.WriteString(out, "\n")
		}
	}
}

// printSearchSummary writes the shown/total line (and a paging hint when more
// matches remain) to stderr, so it never pollutes the stdout data stream. capped
// marks a keyword total saturated at store.MatchCountCap, rendered "500+".
func printSearchSummary(w io.Writer, shown, total int, capped bool, offset, limit int) {
	totalStr := fmt.Sprintf("%d", total)
	if capped {
		totalStr += "+"
	}
	fmt.Fprintf(w, "matterbox: showing %d of %s match%s\n", shown, totalStr, pluralS(total))
	if total > offset+shown {
		fmt.Fprintf(w, "matterbox: more — page with --offset %d\n", offset+limit)
	}
}

// pluralS returns the plural suffix for n ("" for 1, "es" for match→matches).
func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}

// indentLines prefixes every non-empty line of s with prefix, preserving the
// trailing newline. An empty string stays empty.
func indentLines(s, prefix string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(prefix)
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}
