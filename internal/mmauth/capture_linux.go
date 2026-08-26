//go:build linux

package mmauth

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// readTimeout bounds how long a connection to the login socket may take to
// deliver its URL. A var so tests don't have to wait it out.
var readTimeout = 5 * time.Second

// Capture is an in-progress auto-capture. URL receives the captured mmauth://
// link exactly once and is then closed; reading it again yields ("", false).
// Close releases the socket and stops the listener — it is always non-nil (a
// no-op when capture isn't enabled), so callers can defer it unconditionally.
type Capture struct {
	URL   <-chan string
	Close func()
}

// SocketPath is the Unix socket the hidden url-handler dials to hand the
// captured mmauth:// URL back to the waiting process. A fixed path under the
// runtime dir is fine because only one interactive sign-in runs at a time.
func SocketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "matterbox-login.sock")
}

// StartCapture registers matterbox as the mmauth:// scheme handler and listens
// on the login socket. When the browser opens mmauth://callback?… after SSO,
// the OS launches `matterbox url-handler`, which connects to this socket; the
// URL it sends is forwarded to c.URL. Registration/socket failures are
// non-fatal — the caller falls back to a pasted link — so they return
// enabled=false with a hint on stderr. The returned Close is always safe to
// call (and closes c.URL, so a waiter on it never leaks).
func StartCapture(ctx context.Context) (c Capture, enabled bool) {
	if _, err := RegisterHandler(); err != nil {
		fmt.Fprintf(os.Stderr, "matterbox: couldn't register the mmauth:// handler (%v); falling back to copy-paste\n", err)
		return Capture{Close: func() {}}, false
	}

	path := SocketPath()
	_ = os.Remove(path) // clear a stale socket left by an aborted run
	ln, err := net.Listen("unix", path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "matterbox: couldn't open login socket (%v); falling back to copy-paste\n", err)
		return Capture{Close: func() {}}, false
	}
	// Whatever lands on this socket is stored as a session token, so only we
	// may connect to it. XDG_RUNTIME_DIR is already private, but the TempDir
	// fallback is not: without this any local user could hand us an
	// mmauth://callback of their choosing. Linux enforces the mode on connect.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		os.Remove(path)
		fmt.Fprintf(os.Stderr, "matterbox: couldn't secure the login socket (%v); falling back to copy-paste\n", err)
		return Capture{Close: func() {}}, false
	}

	ch := make(chan string, 1)
	go func() {
		// Closing ch when the goroutine exits (URL delivered, or the listener
		// closed by Close) unblocks any waiter so it never hangs.
		defer close(ch)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed by Close
			}
			// A peer that connects and then says nothing would otherwise hold
			// the accept loop forever, so the real handler never gets through.
			_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
			data, _ := io.ReadAll(io.LimitReader(conn, 8192))
			conn.Close()
			if s := strings.TrimSpace(string(data)); s != "" {
				select {
				case ch <- s:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	return Capture{
		URL: ch,
		Close: func() {
			ln.Close()
			os.Remove(path)
		},
	}, true
}

// HandleURL is the body of the hidden `url-handler` command: dial the login
// socket and write the URL it was launched with. If no capture is in progress
// (socket absent), it exits quietly — there is nothing to deliver to.
func HandleURL(rawURL string) error {
	if rawURL == "" {
		return nil
	}
	conn, err := net.Dial("unix", SocketPath())
	if err != nil {
		return nil // no sign-in waiting; nothing to deliver
	}
	defer conn.Close()
	_, err = io.WriteString(conn, rawURL)
	return err
}

// RegisterHandler writes a .desktop entry mapping the mmauth:// scheme to
// `matterbox url-handler %u` and makes it the default handler, returning the
// path of the written entry. The entry is rewritten every run so its Exec path
// tracks the current binary location (so moving or reinstalling matterbox
// self-heals).
//
// The .desktop filename and location must stay in sync with the Makefile's
// uninstall target, which removes it directly (the binary may be gone by then).
func RegisterHandler() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", herr
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	appsDir := filepath.Join(dataHome, "applications")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return "", err
	}

	// Desktop Entry spec: quote the Exec program if it contains spaces.
	execProg := exe
	if strings.ContainsAny(execProg, " \t\"") {
		execProg = `"` + strings.ReplaceAll(execProg, `"`, `\"`) + `"`
	}

	const desktopName = "matterbox-mmauth.desktop"
	desktopPath := filepath.Join(appsDir, desktopName)
	desktop := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=Matterbox Login Handler\n" +
		"Exec=" + execProg + " url-handler %u\n" +
		"Terminal=false\n" +
		"NoDisplay=true\n" +
		"MimeType=x-scheme-handler/mmauth;\n"
	if err := os.WriteFile(desktopPath, []byte(desktop), 0o644); err != nil {
		return "", err
	}

	// Best-effort: make it the default for the scheme and refresh the desktop
	// database so browsers pick up the association. Missing tools aren't fatal —
	// the MimeType line alone suffices on many setups.
	if p, err := exec.LookPath("xdg-mime"); err == nil {
		_ = exec.Command(p, "default", desktopName, "x-scheme-handler/mmauth").Run()
	}
	if p, err := exec.LookPath("update-desktop-database"); err == nil {
		_ = exec.Command(p, appsDir).Run()
	}
	return desktopPath, nil
}
