package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"matterbox/internal/config"
	"matterbox/internal/embed"
	"matterbox/internal/semindex"
	"matterbox/internal/store"
)

func newEmbedCmd() *cobra.Command {
	var batch int
	cmd := &cobra.Command{
		Use:   "embed",
		Short: "Backfill semantic-search embeddings for cached messages",
		Long: "Embed every locally-cached message that doesn't yet have a vector, for\n" +
			"semantic search. Reads the embeddings server from config.yaml (the\n" +
			"`embeddings` section) and writes vectors into the local message store.\n\n" +
			"This is the on-demand counterpart to the TUI's background indexer: run it\n" +
			"to grind through history in one go. It only touches the local store and\n" +
			"the embeddings server — no Mattermost API calls — and is safe to re-run\n" +
			"or interrupt (each batch is committed as it completes).\n\n" +
			"Start the embeddings server first (see scripts/llama-embeddings.sh).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEmbed(cmd.Context(), batch, cmd.ErrOrStderr())
		},
	}
	cmd.Flags().IntVar(&batch, "batch", semindex.DefaultBatch,
		"posts per embedding request")
	return cmd
}

func runEmbed(ctx context.Context, batch int, out io.Writer) error {
	cfg, err := config.Load()
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

	ec := cfg.Embeddings
	client := embed.New(ec.Endpoint, ec.APIKey, ec.Model, ec.Dim)
	ix := semindex.New(st, client, ec.Model, ec.Dim, batch)

	// Report current coverage up front so the user knows how much is left.
	already, _ := st.VectorCount(ix.Tag())
	fmt.Fprintf(out, "matterbox: %d messages already embedded (model %s)\n", already, ix.Tag())
	if pending, err := st.PostsMissingVectorsCount(ix.Tag()); err == nil {
		fmt.Fprintf(out, "matterbox: %d messages to embed\n", pending)
	}

	// Ctrl+C cancels between batches; committed batches survive, so a re-run
	// resumes. (cmd.Context() isn't signal-bound by default, so bind it here.)
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	total, err := ix.Backfill(ctx, func(total int) {
		fmt.Fprintf(out, "\rmatterbox: embedded %d…", total)
	})
	if total > 0 {
		fmt.Fprintln(out) // close the \r progress line
	}
	switch {
	case ctx.Err() != nil:
		// Interrupted by SIGINT. The cancellation can surface as an opaque
		// wrapped request error rather than context.Canceled, so key off the
		// context, not err. Committed batches survive, so a re-run resumes.
		fmt.Fprintf(out, "matterbox: interrupted after %d (re-run to continue)\n", total)
		return nil
	case err != nil:
		return fmt.Errorf("embed: %w (is the embeddings server up? see scripts/llama-embeddings.sh)", err)
	}
	fmt.Fprintf(out, "matterbox: done, %d newly embedded\n", total)
	return nil
}
