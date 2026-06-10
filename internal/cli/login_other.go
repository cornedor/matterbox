//go:build !linux

package cli

import (
	"context"
	"fmt"
	"io"
)

// startURLHandlerCapture is a no-op on platforms without the Linux
// x-scheme-handler integration; `login` falls back to pasting the link.
func startURLHandlerCapture(_ context.Context, _ chan<- string) (cleanup func(), enabled bool) {
	return func() {}, false
}

// runURLHandler exists only so the hidden `url-handler` command compiles on
// every platform; the OS never invokes it off Linux.
func runURLHandler(_ []string) error { return nil }

// registerSchemeHandler is a no-op off Linux: only the freedesktop
// x-scheme-handler mechanism is implemented. `make install` guards on the
// OS too, so this is reached only if invoked by hand.
func registerSchemeHandler(out io.Writer) error {
	fmt.Fprintln(out, "mmauth:// handler registration is only supported on Linux — skipping")
	return nil
}
