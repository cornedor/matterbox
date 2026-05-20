package main

import (
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"

	tea "charm.land/bubbletea/v2"
	emoji "github.com/kyokomi/emoji/v2"

	"matterbox/internal/auth"
	"matterbox/internal/config"
	"matterbox/internal/mm"
	"matterbox/internal/ui"
)

func main() {
	pprofAddr := flag.String("pprof", "", "if set (e.g. localhost:6060), serve net/http/pprof on this address")
	flag.Parse()

	if *pprofAddr != "" {
		runtime.SetBlockProfileRate(1)
		runtime.SetMutexProfileFraction(1)
		go func() {
			if err := http.ListenAndServe(*pprofAddr, nil); err != nil {
				fmt.Fprintln(os.Stderr, "matterbox: pprof server:", err)
			}
		}()
	}

	emoji.ReplacePadding = ""

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "matterbox:", err)
		os.Exit(1)
	}

	token, err := auth.ReadToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "matterbox:", err)
		os.Exit(1)
	}

	client := mm.New(cfg.ServerURL, token)
	// v2 drops tea.WithAltScreen(); each tea.View opts in via
	// v.AltScreen = true (set in Model.View). v2 always requests the
	// kitty "disambiguate escape codes" flag, which makes shift+enter
	// arrive as a distinct keypress on capable terminals.
	p := tea.NewProgram(ui.New(client, cfg))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "matterbox:", err)
		os.Exit(1)
	}
}
