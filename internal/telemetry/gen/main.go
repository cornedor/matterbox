// Command gen writes docs/telemetry.md from the event catalogue.
//
// Run it through the directive in docs.go rather than by hand:
//
//	go generate ./internal/telemetry
//
// TestDocsAreCurrent fails when the checked-in file no longer matches the
// catalogue, so the published list of what matterbox sends cannot go stale
// without CI noticing.
package main

import (
	"flag"
	"fmt"
	"os"

	"matterbox/internal/telemetry"
)

func main() {
	out := flag.String("o", "../../docs/telemetry.md", "file to write")
	flag.Parse()
	if err := os.WriteFile(*out, []byte(telemetry.Markdown()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "wrote", *out)
}
