package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"matterbox/internal/auth"
	"matterbox/internal/mm"
	"matterbox/internal/ui"
)

func main() {
	token, err := auth.ReadToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "matterbox:", err)
		os.Exit(1)
	}

	client := mm.New(token)
	p := tea.NewProgram(ui.New(client), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "matterbox:", err)
		os.Exit(1)
	}
}
