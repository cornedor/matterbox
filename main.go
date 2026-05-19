package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	emoji "github.com/kyokomi/emoji/v2"

	"matterbox/internal/auth"
	"matterbox/internal/mm"
	"matterbox/internal/ui"
)

func main() {
	emoji.ReplacePadding = ""

	token, err := auth.ReadToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "matterbox:", err)
		os.Exit(1)
	}

	client := mm.New(token)
	// v2 drops tea.WithAltScreen(); each tea.View opts in via
	// v.AltScreen = true (set in Model.View). v2 always requests the
	// kitty "disambiguate escape codes" flag, which makes shift+enter
	// arrive as a distinct keypress on capable terminals.
	p := tea.NewProgram(ui.New(client))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "matterbox:", err)
		os.Exit(1)
	}
}
