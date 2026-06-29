//go:build !linux

package mmauth

import "context"

// Capture mirrors the Linux type so callers compile everywhere. Off Linux the
// channel never fires (StartCapture returns enabled=false) and Close is a no-op.
type Capture struct {
	URL   <-chan string
	Close func()
}

// StartCapture is a no-op off Linux (only the freedesktop x-scheme-handler
// integration is implemented); callers fall back to pasting the link. Close is
// non-nil so it can be deferred unconditionally.
func StartCapture(_ context.Context) (Capture, bool) {
	return Capture{Close: func() {}}, false
}

// HandleURL exists so the hidden `url-handler` command compiles on every
// platform; the OS never invokes it off Linux.
func HandleURL(_ string) error { return nil }

// RegisterHandler is a no-op off Linux: only the freedesktop x-scheme-handler
// mechanism is implemented. It returns an empty path so callers can report that
// registration was skipped.
func RegisterHandler() (string, error) { return "", nil }
