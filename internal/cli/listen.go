package cli

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"matterbox/internal/chat"
	"matterbox/internal/listen"
	"matterbox/internal/store"
	"matterbox/internal/telegram"
)

func newListenCmd() *cobra.Command {
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
			"Intended to run under a process supervisor. A sample systemd user unit lives\n" +
			"at scripts/matterbox-listen.service:\n\n" +
			"  systemctl --user enable --now matterbox-listen.service\n" +
			"  journalctl --user -u matterbox-listen -f",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListen(cmd.Context(), cmd.ErrOrStderr())
		},
	}
	return cmd
}

func runListen(ctx context.Context, out io.Writer) error {
	cfg, client, err := dial()
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

	logger := log.New(out, "", log.LstdFlags)

	// Summarize only when enabled and a chat endpoint is configured; otherwise
	// notifications carry the raw message text.
	summarize := cfg.Listen.Summarize != nil && *cfg.Listen.Summarize
	var chatClient *chat.Client
	if summarize && cfg.Summary.Endpoint != "" {
		chatClient = chat.New(cfg.Summary.Endpoint, cfg.Summary.APIKey, cfg.Summary.Model)
	}

	// Deliver to Telegram only when a bot token is set; otherwise the daemon
	// still warms the cache and logs mentions.
	var tgClient *telegram.Client
	if cfg.Telegram.BotToken != "" {
		tgClient = telegram.New(cfg.Telegram.BotToken)
	}

	opts := listen.Options{
		ServerURL:       cfg.ServerURL,
		NotifyOnMention: cfg.Listen.NotifyOnMention != nil && *cfg.Listen.NotifyOnMention,
		Summarize:       chatClient != nil,
		NotifyPrompt:    cfg.Listen.NotifyPrompt,
		TelegramChatID:  cfg.Telegram.ChatID,
	}

	logger.Printf("matterbox listen: starting as @%s on %s", me.Username, cfg.ServerURL)
	logger.Printf("matterbox listen: cache=%s notify_on_mention=%t summarize=%t telegram=%s",
		p, opts.NotifyOnMention, opts.Summarize, telegramState(tgClient, cfg.Telegram.ChatID))

	eng := listen.New(client, st, chatClient, tgClient, me, opts, logger)

	// SIGINT/SIGTERM trigger a graceful shutdown: Run drains in-flight
	// notifications, then returns ctx.Err().
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := eng.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	logger.Printf("matterbox listen: stopped")
	return nil
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
