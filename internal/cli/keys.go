package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"matterbox/internal/config"
	"matterbox/internal/ui"
)

func newKeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keys",
		Short: "List keyboard actions, their default keys, and your overrides",
		Long: "Print every rebindable keyboard action with the keys it answers to and\n" +
			"whether you've overridden it in config.yaml. Handy when editing the\n" +
			"`keybindings.bindings:` section — the action id in the first column is\n" +
			"exactly what you put on the left of an override.\n\n" +
			"An override row is marked with `*` and shows the built-in default it\n" +
			"replaced. ctrl+c always quits and esc always cancels in modals; those are\n" +
			"hardwired and never listed.\n\n" +
			"  matterbox keys\n" +
			"  matterbox keys | grep delete",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKeys(cmd.OutOrStdout())
		},
	}
}

func runKeys(out io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	return writeKeys(out, cfg)
}

// writeKeys renders the keybindings table for the given config. Split from
// runKeys (which loads config) so the formatting is unit-testable without a
// config file on disk.
func writeKeys(out io.Writer, cfg *config.Config) error {
	binds, err := ui.KeybindingsList(cfg)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Rebind in config.yaml under keybindings.bindings (action: key, or [keys]; [] unbinds).\n")
	fmt.Fprintf(out, "nav_modifier=%s  vim_nav=%s   (* = overridden in your config)\n\n",
		cfg.Keybindings.NavModifier, cfg.Keybindings.VimNav)

	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "\tACTION\tKEYS\tDESCRIPTION")
	for _, b := range binds {
		mark := " "
		if b.Overridden {
			mark = "*"
		}
		keys := strings.Join(b.Keys, "  ")
		if keys == "" {
			keys = "(unbound)"
		}
		desc := b.Desc
		if b.Overridden {
			def := strings.Join(b.Default, "  ")
			if def == "" {
				def = "(unbound)"
			}
			desc = fmt.Sprintf("%s  (default: %s)", b.Desc, def)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mark, b.ID, keys, desc)
	}
	return tw.Flush()
}
