package cli

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"matterbox/internal/config"
	"matterbox/internal/listen"
	"matterbox/internal/store"
)

// `matterbox rules` is the authoring loop for the listen daemon's rules. Before
// it, the only way to find out whether a rule worked was to edit the config,
// restart the daemon, persuade a colleague to say the magic word, and watch the
// log. These verbs answer the same questions without any of that: what loaded
// (list), would this message fire it and if not why not (test), is it firing at
// all (stats), and what has it remembered (state).

func newRulesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Inspect and test the `matterbox listen` rules",
		Long: "Work with the rules the `matterbox listen` daemon runs (the `rules:` block\n" +
			"in config.yaml).\n\n" +
			"  matterbox rules list                    what's configured, and when it fires\n" +
			"  matterbox rules test -m \"deploy prod\"    which rules a message would fire\n" +
			"  matterbox rules stats                   how often each rule has fired\n" +
			"  matterbox rules state                   the ledger rules read and write\n\n" +
			"All of them read the same config the daemon reads and run the same matcher,\n" +
			"so an answer here is the answer the daemon would give. Nothing is executed:\n" +
			"`test` never runs an action, posts a message, or writes the ledger.\n\n" +
			"After editing the config, reload the running daemon without dropping its\n" +
			"connection:\n\n" +
			"  systemctl --user reload matterbox-listen.service   # or: pkill -HUP -f 'matterbox listen'",
	}
	cmd.AddCommand(newRulesListCmd(), newRulesTestCmd(), newRulesStatsCmd(), newRulesStateCmd())
	return cmd
}

// loadRules reads and compiles the configured rules, so every verb reports a
// bad glob/regexp/action the same way the daemon does at startup.
func loadRules() (*config.Config, []listen.Rule, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	rules, err := listen.CompileRules(ruleSpecs(cfg.Rules))
	if err != nil {
		return nil, nil, fmt.Errorf("rules: %w", err)
	}
	return cfg, rules, nil
}

// ---------------------------------------------------------------- rules list

func newRulesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the configured rules, their triggers and their actions",
		Long: "Compile the `rules:` block and print what it contains: the events each rule\n" +
			"reacts to, the conditions it tests, and the actions it runs, in evaluation\n" +
			"order. A rule that can't compile is reported exactly as the daemon would\n" +
			"report it at startup, so this doubles as a config check.\n\n" +
			"With no rules configured it says so: the daemon then applies the built-in\n" +
			"mention/DM → Telegram rule.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, rules, err := loadRules()
			if err != nil {
				return err
			}
			return listRules(cmd.OutOrStdout(), cfg.Rules, rules, time.Now())
		},
	}
}

// listRules prints one block per rule: the header (order, name, triggers), the
// conditions as the user wrote them, and the actions in order.
func listRules(out io.Writer, cfgRules []config.RuleConfig, rules []listen.Rule, now time.Time) error {
	if len(rules) == 0 {
		fmt.Fprintln(out, "no rules configured — the daemon applies the built-in mention/DM → Telegram rule")
		return nil
	}
	for i, r := range rules {
		trigger := strings.Join(r.Kinds(), ", ")
		if s := r.ScheduleText(); s != "" {
			trigger += " (" + s + ")"
			if next, ok := r.NextRun(now); ok {
				trigger += ", next " + next.Format("Mon 2 Jan 15:04")
			}
		}
		stop := ""
		if r.Stop {
			stop = "  [stop]"
		}
		fmt.Fprintf(out, "%d. %s%s\n", i+1, r.Name, stop)
		fmt.Fprintf(out, "     on:      %s\n", trigger)
		if i < len(cfgRules) {
			if cond := matchSummary(cfgRules[i].Match); cond != "" {
				fmt.Fprintf(out, "     match:   %s\n", cond)
			}
		}
		fmt.Fprintf(out, "     actions: %s\n", strings.Join(r.ActionTypes(), " → "))
	}
	return nil
}

// matchSummary renders a rule's conditions as the compact "key=value" line the
// listing shows. It reads the config form (what the user wrote) rather than the
// compiled one, so a glob comes back as the glob.
func matchSummary(m config.RuleMatchConfig) string {
	var parts []string
	add := func(k string, v any) { parts = append(parts, fmt.Sprintf("%s=%v", k, v)) }
	addList := func(k string, v config.StringList) {
		if len(v) > 0 {
			add(k, strings.Join([]string(v), "|"))
		}
	}
	addList("channel", m.Channel)
	addList("team", m.Team)
	addList("author", m.Author)
	addList("channel_type", m.ChannelType)
	addList("emoji", m.Emoji)
	addList("reactor", m.Reactor)
	if m.Message != "" {
		add("message", strconv.Quote(m.Message))
	}
	if m.Mention {
		add("mention", true)
	}
	if m.HasFile {
		add("has_file", true)
	}
	for k, v := range map[string]*bool{"dm": m.DM, "from_me": m.FromMe, "from_bot": m.FromBot, "is_thread": m.IsThread, "viewing": m.Viewing} {
		if v != nil {
			add(k, *v)
		}
	}
	sort.Strings(parts) // the pointer loop above has no stable order
	if m.Time != nil {
		w := strings.TrimSpace(m.Time.After + "-" + m.Time.Before)
		if len(m.Time.Days) > 0 {
			w += " " + strings.Join([]string(m.Time.Days), ",")
		}
		parts = append(parts, "time="+strings.TrimPrefix(w, "-"))
	}
	if m.Frequency != nil {
		parts = append(parts, fmt.Sprintf("frequency=%dx/%s", m.Frequency.Count, m.Frequency.Within))
	}
	if m.Cooldown != nil {
		parts = append(parts, "cooldown="+m.Cooldown.Every)
	}
	for _, c := range m.State {
		parts = append(parts, "state="+c.Key)
	}
	if m.Not != nil {
		parts = append(parts, "not("+matchSummary(*m.Not)+")")
	}
	return strings.Join(parts, " ")
}

// ---------------------------------------------------------------- rules test

// probeFlags collects the `rules test` flags describing the trigger to try.
type probeFlags struct {
	message     string
	channel     string
	author      string
	team        string
	channelType string
	dm          bool
	fromMe      bool
	bot         bool
	file        bool
	thread      bool
	kind        string
	emoji       string
	reactor     string
	at          string
}

func newRulesTestCmd() *cobra.Command {
	var f probeFlags
	cmd := &cobra.Command{
		Use:   "test [post-id|permalink]",
		Short: "Show which rules a message would fire, and why the rest wouldn't",
		Long: "Evaluate the configured rules against a message and report, for each rule,\n" +
			"whether it matches — and when it doesn't, the first condition that failed.\n" +
			"Nothing runs: no action is executed, nothing is posted, and the ledger and\n" +
			"the rate gates are only read.\n\n" +
			"Describe a hypothetical message with flags, or pass a real post's id (or\n" +
			"permalink) to test the rules against a message that actually exists —\n" +
			"attachments, props and thread position included:\n\n" +
			"  matterbox rules test -m \"deploy prod\" --channel Ops --author bob\n" +
			"  matterbox rules test -m \"help\" --dm --author alice\n" +
			"  matterbox rules test 8x4k9y…                       (a real post)\n" +
			"  matterbox rules test 8x4k9y… --on reaction --emoji eyes --reactor bob\n" +
			"  matterbox rules test --on schedule                 (the timer rules)\n\n" +
			"--at moves the clock, which is how a `time:` window or a weekday condition\n" +
			"is tested without waiting for Tuesday.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			post := ""
			if len(args) == 1 {
				post = args[0]
			}
			return runRulesTest(cmd.Context(), cmd.OutOrStdout(), post, f)
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.message, "message", "m", "", "the message body to test")
	fl.StringVar(&f.channel, "channel", "", "channel display name (what `channel:` globs match)")
	fl.StringVar(&f.author, "author", "", "sender username (no @)")
	fl.StringVar(&f.team, "team", "", "team URL name (what `team:` globs match)")
	fl.StringVar(&f.channelType, "type", "", "channel type: public, private, dm, group (default public)")
	fl.BoolVar(&f.dm, "dm", false, "shorthand for --type dm")
	fl.BoolVar(&f.fromMe, "from-me", false, "the message is your own")
	fl.BoolVar(&f.bot, "bot", false, "the message comes from a bot or incoming webhook")
	fl.BoolVar(&f.file, "file", false, "the message has an attachment")
	fl.BoolVar(&f.thread, "thread", false, "the message is a thread reply")
	fl.StringVar(&f.kind, "on", "", "trigger kind: message (default), edit, delete, reaction, reaction_removed, schedule")
	fl.StringVar(&f.emoji, "emoji", "", "reaction shortcode, for --on reaction")
	fl.StringVar(&f.reactor, "reactor", "", "who reacted, for --on reaction")
	fl.StringVar(&f.at, "at", "", "pretend the trigger fired at this time (15:04, 2006-01-02 15:04, RFC3339)")
	return cmd
}

func runRulesTest(ctx context.Context, out io.Writer, postRef string, f probeFlags) error {
	cfg, rules, err := loadRules()
	if err != nil {
		return err
	}
	_, client, err := dial()
	if err != nil {
		return err
	}
	me, err := client.Me(ctx)
	if err != nil {
		return err
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

	if err := validateProbeFlags(f); err != nil {
		return err
	}
	at := time.Now()
	if f.at != "" {
		if at, err = parseProbeTime(f.at, time.Now()); err != nil {
			return err
		}
	}

	spec := listen.ProbeSpec{
		Kind:        f.kind,
		Message:     f.message,
		Channel:     f.channel,
		ChannelType: probeType(f),
		Team:        f.team,
		Author:      f.author,
		FromMe:      f.fromMe,
		Emoji:       f.emoji,
		Reactor:     f.reactor,
		HasFile:     f.file,
		Thread:      f.thread,
		Bot:         f.bot,
		At:          at,
	}

	// A real post carries the attachments, props and thread position that a
	// flag-built one can't, which is exactly what a rule about an integration's
	// alerts needs to be tested against.
	if postRef != "" {
		post, err := client.Post(ctx, postID(postRef))
		if err != nil {
			return fmt.Errorf("post %s: %w", postRef, err)
		}
		spec.Post = post
		spec.ChannelID = post.ChannelId
		if spec.Author == "" {
			if names, err := client.UsernamesByIDs(ctx, []string{post.UserId}); err == nil {
				spec.Author = names[post.UserId]
			}
		}
	}

	// The daemon's own options only matter here for the built-in rule the
	// engine synthesises when nothing is configured — the same fallback the
	// daemon would run.
	opts := listen.Options{
		ServerURL:       cfg.ServerURL,
		NotifyOnMention: cfg.Listen.NotifyOnMention != nil && *cfg.Listen.NotifyOnMention,
		NotifyDMs:       cfg.Listen.NotifyDMs != nil && *cfg.Listen.NotifyDMs,
		Rules:           rules,
	}
	eng := listen.New(client, st, nil, nil, me, opts, log.New(os.Stderr, "matterbox rules: ", 0))
	eng.Warm(ctx)
	return printExplanations(out, spec, eng.Explain(spec), at)
}

// validateProbeFlags rejects a probe that can't mean what it says, rather than
// quietly testing something else: an unknown trigger kind would make every rule
// look like it isn't listening, and a stray --emoji would simply be ignored.
func validateProbeFlags(f probeFlags) error {
	if !listen.ValidEventKind(f.kind) {
		return fmt.Errorf("unknown --on %q (want message, edit, delete, reaction, reaction_removed, or schedule)", f.kind)
	}
	if t := probeType(f); t != "" && !listen.ValidChannelType(t) {
		return fmt.Errorf("unknown --type %q (want public, private, dm, or group)", t)
	}
	if (f.emoji != "" || f.reactor != "") && !listen.IsReactionKind(f.kind) {
		return fmt.Errorf("--emoji/--reactor need --on reaction (or reaction_removed)")
	}
	return nil
}

// probeType resolves the channel-type flags: --dm is shorthand for --type dm.
func probeType(f probeFlags) string {
	if f.dm {
		return "dm"
	}
	return f.channelType
}

// postID accepts a bare post id or a permalink (…/team/pl/<id>) and returns the
// id, so a link copied from the UI can be pasted straight in.
func postID(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.LastIndex(ref, "/pl/"); i >= 0 {
		return strings.Trim(ref[i+len("/pl/"):], "/")
	}
	return ref
}

// parseProbeTime reads --at. A bare "15:04" means that time today (the common
// case when testing a `time:` window); everything else goes through the shared
// CLI time parser, so dates and RFC3339 work as they do on read/digest.
func parseProbeTime(s string, now time.Time) (time.Time, error) {
	if t, err := time.ParseInLocation("15:04", strings.TrimSpace(s), now.Location()); err == nil {
		y, m, d := now.Date()
		return time.Date(y, m, d, t.Hour(), t.Minute(), 0, 0, now.Location()), nil
	}
	ms, err := parseTimeArg(s, now, now.Location())
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms), nil
}

// printExplanations renders the verdicts: what was tested, then one line per
// rule — fired, blocked by a condition, or not listening for this kind at all.
func printExplanations(out io.Writer, spec listen.ProbeSpec, res []listen.Explanation, at time.Time) error {
	fmt.Fprintf(out, "%s\n\n", probeSummary(spec, at))
	if len(res) == 0 {
		fmt.Fprintln(out, "no rules configured")
		return nil
	}
	fired := 0
	for _, ex := range res {
		switch {
		case ex.Skipped:
			fmt.Fprintf(out, "  –  %-24s reacts to %s\n", ex.Rule, strings.Join(ex.Kinds, "/"))
		case !ex.Matched:
			fmt.Fprintf(out, "  ✗  %-24s %s doesn't match\n", ex.Rule, ex.Why)
		case ex.Gate != "":
			fmt.Fprintf(out, "  ⏸  %-24s matches, held by %s\n", ex.Rule, ex.Gate)
		default:
			fired++
			fmt.Fprintf(out, "  ✓  %-24s → %s%s\n", ex.Rule, strings.Join(ex.Actions, " → "), stopNote(ex.Stop))
		}
		if ex.Matched && ex.Stop && ex.Gate == "" {
			break // later rules never see this trigger
		}
	}
	fmt.Fprintf(out, "\n%d of %d rules would fire\n", fired, len(res))
	if spec.Kind == listen.EventSchedule && fired > 0 {
		// A schedule rule's conditions holding is only half the story: it still
		// fires when its timer says so, which `rules list` is the place to see.
		fmt.Fprintln(out, "(a scheduled rule fires when its timer is due — `matterbox rules list` shows when)")
	}
	return nil
}

func stopNote(stop bool) string {
	if stop {
		return "  [stop: later rules are skipped]"
	}
	return ""
}

// probeSummary describes the trigger being tested, so the verdicts below it are
// read against the right thing.
func probeSummary(spec listen.ProbeSpec, at time.Time) string {
	kind := spec.Kind
	if kind == "" {
		kind = listen.EventMessage
	}
	var b strings.Builder
	fmt.Fprintf(&b, "probe: %s at %s", kind, at.Format("Mon 2 Jan 15:04"))
	if spec.Channel != "" {
		fmt.Fprintf(&b, " in %q", spec.Channel)
	}
	if spec.Author != "" {
		fmt.Fprintf(&b, " from @%s", strings.TrimPrefix(spec.Author, "@"))
	}
	if spec.Emoji != "" {
		fmt.Fprintf(&b, " :%s:", spec.Emoji)
	}
	body := spec.Message
	if spec.Post != nil {
		body = spec.Post.Message
	}
	if body = strings.TrimSpace(body); body != "" {
		fmt.Fprintf(&b, " — %q", truncate(body, 60))
	}
	return b.String()
}

// truncate shortens s to at most n runes, with an ellipsis.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// --------------------------------------------------------------- rules stats

func newRulesStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show how often each rule has fired, and when it last did",
		Long: "Print the per-rule fire counters the daemon keeps in the local cache.\n\n" +
			"A rule that has never fired shows a dash — which, for a rule you expect to\n" +
			"be busy, is the fastest sign that its match is wrong (then `rules test` says\n" +
			"which condition). Counters persist across restarts and count firings, not\n" +
			"matches: a rule held back by its cooldown or frequency window doesn't count.\n\n" +
			"Counters are keyed by rule name, so name your rules — an unnamed one is\n" +
			"keyed by its position, and inserting a rule above it starts its count over.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, rules, err := loadRules()
			if err != nil {
				return err
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
			return printRuleStats(cmd.OutOrStdout(), st, rules)
		},
	}
}

// ruleStatReader is the slice of the store the stats verb needs, so the
// formatting can be tested without a database.
type ruleStatReader interface {
	GetMeta(key string) (string, bool, error)
}

func printRuleStats(out io.Writer, st ruleStatReader, rules []listen.Rule) error {
	if len(rules) == 0 {
		fmt.Fprintln(out, "no rules configured")
		return nil
	}
	fmt.Fprintf(out, "%-28s %8s  %s\n", "RULE", "FIRES", "LAST")
	for _, r := range rules {
		count := "–"
		if v, ok, err := st.GetMeta(listen.RuleStatKey(r.Name, "count")); err == nil && ok {
			count = v
		}
		last := "never"
		if v, ok, err := st.GetMeta(listen.RuleStatKey(r.Name, "last")); err == nil && ok {
			if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
				last = time.UnixMilli(ms).Format("2006-01-02 15:04")
			}
		}
		fmt.Fprintf(out, "%-28s %8s  %s\n", truncate(r.Name, 28), count, last)
	}
	return nil
}

// --------------------------------------------------------------- rules state

func newRulesStateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state [get|set|del] [key] [value]",
		Short: "Read and edit the ledger rules remember things in",
		Long: "Inspect the persistent key/value ledger the state_set / state_incr /\n" +
			"state_del actions write and `state:` conditions read.\n\n" +
			"  matterbox rules state              list every key\n" +
			"  matterbox rules state get <key>    print one value\n" +
			"  matterbox rules state set <key> <value>\n" +
			"  matterbox rules state del <key>    remove one key\n\n" +
			"Without this, a rule gated on a key that got stuck could only be unwedged\n" +
			"with sqlite3 — the ledger is the one piece of rule state with no other way\n" +
			"to see it.",
		Args: cobra.MaximumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := store.DefaultPath()
			if err != nil {
				return err
			}
			st, err := store.Open(p)
			if err != nil {
				return fmt.Errorf("open store: %w", err)
			}
			defer st.Close()
			return runRulesState(cmd.OutOrStdout(), st, args)
		},
	}
	return cmd
}

// ruleStateStore is the slice of the store the state verb needs.
type ruleStateStore interface {
	AllState() (map[string]string, error)
	GetState(key string) (string, bool, error)
	SetState(key, value string) error
	DeleteState(key string) error
}

func runRulesState(out io.Writer, st ruleStateStore, args []string) error {
	if len(args) == 0 {
		all, err := st.AllState()
		if err != nil {
			return err
		}
		if len(all) == 0 {
			fmt.Fprintln(out, "ledger is empty")
			return nil
		}
		keys := make([]string, 0, len(all))
		for k := range all {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(out, "%s = %s\n", k, all[k])
		}
		return nil
	}
	switch args[0] {
	case "get":
		if len(args) != 2 {
			return fmt.Errorf("usage: matterbox rules state get <key>")
		}
		v, ok, err := st.GetState(args[1])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no such key %q", args[1])
		}
		fmt.Fprintln(out, v)
		return nil
	case "set":
		if len(args) != 3 {
			return fmt.Errorf("usage: matterbox rules state set <key> <value>")
		}
		if err := st.SetState(args[1], args[2]); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s = %s\n", args[1], args[2])
		return nil
	case "del":
		if len(args) != 2 {
			return fmt.Errorf("usage: matterbox rules state del <key>")
		}
		if err := st.DeleteState(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(out, "deleted %s\n", args[1])
		return nil
	}
	return fmt.Errorf("unknown state command %q (want get, set, del, or nothing to list)", args[0])
}
