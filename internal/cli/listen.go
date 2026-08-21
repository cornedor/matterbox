package cli

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"matterbox/internal/chat"
	"matterbox/internal/config"
	"matterbox/internal/embed"
	"matterbox/internal/listen"
	"matterbox/internal/store"
	"matterbox/internal/telegram"
	"matterbox/internal/telemetry"
)

func newListenCmd() *cobra.Command {
	var notifySelf bool
	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Run a background daemon that keeps the cache warm and bridges mentions",
		Long: "Run a long-lived daemon that holds a single Mattermost WebSocket connection,\n" +
			"writes every incoming message into the local cache (so the TUI reopens warm\n" +
			"and `search`/`digest` stay fresh without launching the UI), and forwards your\n" +
			"direct @mentions and DMs to Telegram — summarizing the surrounding\n" +
			"conversation with the chat model first.\n\n" +
			"It is safe to run alongside the TUI: both write the same idempotent rows into\n" +
			"the WAL-mode store.\n\n" +
			"Configure the bridge in config.yaml (the `telegram` and `listen` sections):\n" +
			"set telegram.bot_token (from @BotFather) and telegram.chat_id. Summaries use\n" +
			"the same chat server as the `summary` command and fall back to the raw\n" +
			"message text when it is down. With no telegram.bot_token the daemon only\n" +
			"keeps the cache warm.\n\n" +
			"What the daemon does is driven by rules (the `rules:` config block):\n" +
			"match on channel/author/text/mention and run actions — notify, run a\n" +
			"local command, POST a webhook, react, mark read. A rule reacts to new\n" +
			"messages by default; `on:` widens that to edits, deletions, reactions,\n" +
			"or the clock (`on: schedule`). With no rules configured the Telegram\n" +
			"bridge above is applied as the default rule.\n\n" +
			"`matterbox rules list/test/stats/state` inspect and dry-run them, and a\n" +
			"SIGHUP (systemctl --user reload matterbox-listen) swaps an edited\n" +
			"ruleset in without dropping the connection. See docs/rules.md.\n\n" +
			"Intended to run under a process supervisor. `make install` drops a\n" +
			"(disabled) service for your OS — systemd --user on Linux, a launchd\n" +
			"LaunchAgent on macOS — which you then enable once configured:\n\n" +
			"  # Linux\n" +
			"  systemctl --user enable --now matterbox-listen.service\n" +
			"  journalctl --user -u matterbox-listen -f\n\n" +
			"  # macOS\n" +
			"  launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.matterbox.listen.plist\n" +
			"  tail -f ~/Library/Logs/matterbox-listen.log",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListen(cmd.Context(), cmd.ErrOrStderr(), notifySelf)
		},
	}
	cmd.Flags().BoolVar(&notifySelf, "notify-self", false,
		"also notify on your own messages — post in your self-DM to test the bridge")
	return cmd
}

func runListen(ctx context.Context, out io.Writer, notifySelf bool) error {
	cfg, client, err := dial()
	if err != nil {
		return err
	}
	logger := log.New(out, "", log.LstdFlags)

	// Deliver to Telegram only when a bot token is set; otherwise the daemon
	// still warms the cache and logs mentions. Built before the first API call
	// so a startup auth failure can be reported over Telegram too.
	var tgClient *telegram.Client
	if cfg.Telegram.BotToken != "" {
		tgClient = telegram.New(cfg.Telegram.BotToken)
	}

	me, err := client.Me(ctx)
	if err != nil {
		if listen.IsUnauthorized(err) {
			if tgClient != nil && cfg.Telegram.ChatID != "" {
				_ = tgClient.SendMessage(ctx, cfg.Telegram.ChatID,
					"⚠️ matterbox: your Mattermost session expired. Run `matterbox login` on the host and restart the daemon.")
			}
			// Exit cleanly so a supervisor doesn't restart-loop on a dead token.
			logger.Printf("matterbox listen: session expired — run `matterbox login` and restart")
			return nil
		}
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

	// Summarize only when enabled and a chat endpoint is configured; otherwise
	// notifications carry the raw message text.
	summarize := cfg.Listen.Summarize != nil && *cfg.Listen.Summarize
	var chatClient *chat.Client
	if summarize && cfg.Summary.Endpoint != "" {
		chatClient = chat.New(cfg.Summary.Endpoint, cfg.Summary.APIKey, cfg.Summary.Model)
	}

	// The /ask agentic search reuses the summary chat server (independent of
	// whether mention summaries are on) plus the optional embeddings server for
	// semantic/hybrid modes — mirroring how the TUI wires AI search.
	var embedClient *embed.Client
	if cfg.Embeddings.Endpoint != "" && cfg.Embeddings.Model != "" {
		embedClient = embed.New(cfg.Embeddings.Endpoint, cfg.Embeddings.APIKey, cfg.Embeddings.Model, cfg.Embeddings.Dim)
	}

	notifyDelay := 0
	if cfg.Listen.NotifyDelaySeconds != nil {
		notifyDelay = *cfg.Listen.NotifyDelaySeconds
	}

	// Compile the user's rules up front so a bad glob/regexp/action is a
	// startup error, not a rule that silently never fires. Empty leaves
	// opts.Rules nil — the daemon then synthesises the built-in notification
	// rule from the Notify* options below.
	rules, err := listen.CompileRules(ruleSpecs(cfg.Rules))
	if err != nil {
		return fmt.Errorf("rules: %w", err)
	}

	opts := listen.Options{
		ServerURL:          cfg.ServerURL,
		NotifyOnMention:    cfg.Listen.NotifyOnMention != nil && *cfg.Listen.NotifyOnMention,
		Summarize:          chatClient != nil,
		NotifyPrompt:       cfg.Listen.NotifyPrompt,
		TelegramChatID:     cfg.Telegram.ChatID,
		NotifySelf:         notifySelf,
		NotifyDMs:          cfg.Listen.NotifyDMs != nil && *cfg.Listen.NotifyDMs,
		NotifyDelaySeconds: notifyDelay,
		RespectMutes:       cfg.Listen.RespectMutes != nil && *cfg.Listen.RespectMutes,
		RespectDND:         cfg.Listen.RespectDND != nil && *cfg.Listen.RespectDND,
		QuietHours:         cfg.Listen.QuietHours,
		TwoWay:             cfg.Listen.TwoWay != nil && *cfg.Listen.TwoWay,
		Rules:              rules,

		AskEndpoint: cfg.Summary.Endpoint,
		AskAPIKey:   cfg.Summary.APIKey,
		AskModel:    cfg.Summary.Model,
		AskPrompt:   cfg.AISearch.Prompt,
		AskMaxSteps: cfg.AISearch.MaxSteps,
		AskTimeout:  time.Duration(cfg.AISearch.TimeoutMinutes) * time.Minute,
		EmbedClient: embedClient,
		EmbedModel:  cfg.Embeddings.Model,
		EmbedDim:    cfg.Embeddings.Dim,
	}

	logger.Printf("matterbox listen: starting as @%s on %s", me.Username, cfg.ServerURL)
	logger.Printf("matterbox listen: cache=%s notify_on_mention=%t notify_dms=%t notify_delay=%ds summarize=%t notify_self=%t respect_mutes=%t respect_dnd=%t two_way=%t ask=%t quiet_hours=%q telegram=%s rules=%s",
		p, opts.NotifyOnMention, opts.NotifyDMs, opts.NotifyDelaySeconds, opts.Summarize, opts.NotifySelf, opts.RespectMutes, opts.RespectDND, opts.TwoWay,
		opts.AskEndpoint != "" && opts.AskModel != "", cfg.Listen.QuietHours, telegramState(tgClient, cfg.Telegram.ChatID), rulesState(len(rules)))

	eng := listen.New(client, st, chatClient, tgClient, me, opts, logger)
	// Where the daemon looks for a TUI to ask "are you reading this?" — the one
	// thing about the viewing gate that isn't visible from the outside when it
	// silently never matches (wrong machine, wrong config dir).
	logger.Printf("matterbox listen: tui_socket=%s", eng.TUISocketPath())

	// Anonymous telemetry, if the user opted in. Started here rather than left
	// to reportCommand, which only runs once the verb returns — for a daemon
	// that is weeks later, so every rule_fired in between would have nowhere to
	// go. ModeDaemon also buys a longer flush budget on the way out, since
	// there is no interactive exit to hold up.
	telemetry.StartMode(cfg, telemetry.ModeDaemon)
	defer telemetry.Close()
	stamp := readBuildStamp()
	telemetry.SetBuild(versionName(stamp), stamp.tags)
	eng.ReportStart(versionName(stamp))
	// A daemon that has been running an old build for months is the most likely
	// reason a fixed bug is still being reported.
	telemetry.CheckVersion(st, versionName(stamp))

	// SIGINT/SIGTERM trigger a graceful shutdown: Run drains in-flight
	// notifications, then returns ctx.Err().
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// SIGHUP re-reads the config and swaps the ruleset in place. Restarting to
	// pick up a rule edit drops the websocket, re-runs catch-up and re-warms
	// every cache, which is a lot of ceremony for a changed regexp — and enough
	// friction that rules don't get iterated on. A bad config leaves the running
	// rules untouched: a typo must never disarm a working daemon.
	go watchReloads(ctx, eng, logger)

	if err := eng.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	logger.Printf("matterbox listen: stopped")
	return nil
}

// watchReloads swaps the daemon's rules on every SIGHUP until ctx is cancelled.
func watchReloads(ctx context.Context, eng *listen.Engine, logger *log.Logger) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			reloadRules(eng, logger)
		}
	}
}

// reloadRules recompiles the config's rules and installs them. Every failure
// path keeps the current ruleset and says so, so a half-written config can't
// leave the daemon running no rules at all.
func reloadRules(eng *listen.Engine, logger *log.Logger) {
	cfg, err := config.Load()
	if err != nil {
		logger.Printf("matterbox listen: reload failed, rules unchanged: %v", err)
		return
	}
	rules, err := listen.CompileRules(ruleSpecs(cfg.Rules))
	if err != nil {
		logger.Printf("matterbox listen: reload failed, rules unchanged: rules: %v", err)
		return
	}
	eng.SetRules(rules)
	logger.Printf("matterbox listen: reloaded — rules=%s", rulesState(len(rules)))
}

// ruleSpecs maps the config's YAML rule structs to the listen package's
// pre-compilation specs, keeping the config package a leaf (no dependency on
// internal/listen).
func ruleSpecs(cfg []config.RuleConfig) []listen.RuleSpec {
	specs := make([]listen.RuleSpec, len(cfg))
	for i, r := range cfg {
		actions := make([]listen.ActionSpec, len(r.Actions))
		for j, a := range r.Actions {
			actions[j] = listen.ActionSpec{
				Type:      a.Type,
				Summarize: a.Summarize,
				Urgent:    a.Urgent,
				ChatID:    a.ChatID,
				Command:   a.Command,
				URL:       a.URL,
				Headers:   a.Headers,
				Emoji:     a.Emoji,
				Text:      a.Text,
				Channel:   a.Channel,
				Thread:    a.Thread,
				Key:       a.Key,
				Value:     a.Value,
				By:        a.By,
			}
		}
		specs[i] = listen.RuleSpec{
			Name:    r.Name,
			Stop:    r.Stop,
			On:      []string(r.On),
			Match:   matchSpec(r.Match),
			Actions: actions,
		}
		if r.Schedule != nil {
			specs[i].Schedule = &listen.ScheduleSpec{Cron: r.Schedule.Cron, Every: r.Schedule.Every}
		}
	}
	return specs
}

// matchSpec maps a config match (recursing into not:) to the listen form.
func matchSpec(m config.RuleMatchConfig) listen.MatchSpec {
	spec := listen.MatchSpec{
		Channels:     []string(m.Channel),
		Teams:        []string(m.Team),
		Authors:      []string(m.Author),
		Message:      m.Message,
		Mention:      m.Mention,
		DM:           m.DM,
		FromMe:       m.FromMe,
		HasFile:      m.HasFile,
		IsThread:     m.IsThread,
		Viewing:      m.Viewing,
		Emoji:        []string(m.Emoji),
		Reactors:     []string(m.Reactor),
		ChannelTypes: []string(m.ChannelType),
		FromBot:      m.FromBot,
	}
	if m.Time != nil {
		spec.Time = &listen.TimeSpec{After: m.Time.After, Before: m.Time.Before, Days: []string(m.Time.Days)}
	}
	if m.Not != nil {
		not := matchSpec(*m.Not)
		spec.Not = &not
	}
	if m.Frequency != nil {
		spec.Frequency = &listen.FrequencySpec{
			Count:  m.Frequency.Count,
			Within: m.Frequency.Within,
			By:     m.Frequency.By,
		}
	}
	if m.Cooldown != nil {
		spec.Cooldown = &listen.CooldownSpec{
			Every: m.Cooldown.Every,
			By:    m.Cooldown.By,
		}
	}
	if len(m.State) > 0 {
		spec.State = make([]listen.StateCondSpec, len(m.State))
		for i, c := range m.State {
			spec.State[i] = listen.StateCondSpec{
				Key:    c.Key,
				Exists: c.Exists,
				Eq:     c.Eq,
				Ne:     c.Ne,
				Gt:     c.Gt,
				Gte:    c.Gte,
				Lt:     c.Lt,
				Lte:    c.Lte,
			}
		}
	}
	return spec
}

// rulesState renders the rule count for the startup log: "default (built-in
// notify)" when the user configured none, else "N configured".
func rulesState(n int) string {
	if n == 0 {
		return "default (built-in notify)"
	}
	return fmt.Sprintf("%d configured", n)
}

// telegramState renders the Telegram configuration for the startup log without
// leaking the bot token.
func telegramState(tg *telegram.Client, chatID string) string {
	if tg == nil {
		return "disabled (no bot_token)"
	}
	if chatID == "" {
		return "bot set but no chat_id — delivery will fail"
	}
	return "→ " + chatID
}
