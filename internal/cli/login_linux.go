//go:build linux

package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// loginSocketPath is the Unix socket the hidden url-handler dials to hand
// the captured mmauth:// URL back to the waiting login process. A fixed
// path under the runtime dir is fine because only one interactive login
// runs at a time.
func loginSocketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "matterbox-login.sock")
}

// startURLHandlerCapture registers matterbox as the mmauth:// scheme
// handler and listens on a local socket. When the browser opens
// mmauth://callback?… after SSO, the OS launches `matterbox url-handler`,
// which connects to this socket; the URL it sends is forwarded to tokenCh.
// Registration/socket failures are non-fatal — the caller still accepts a
// pasted link — so they return enabled=false with a hint on stderr.
func startURLHandlerCapture(ctx context.Context, tokenCh chan<- string) (cleanup func(), enabled bool) {
	if _, err := registerMmauthHandler(); err != nil {
		fmt.Fprintf(os.Stderr, "matterbox: couldn't register the mmauth:// handler (%v); falling back to copy-paste\n", err)
		return func() {}, false
	}

	path := loginSocketPath()
	_ = os.Remove(path) // clear a stale socket left by an aborted run
	ln, err := net.Listen("unix", path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "matterbox: couldn't open login socket (%v); falling back to copy-paste\n", err)
		return func() {}, false
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed by cleanup
			}
			data, _ := io.ReadAll(io.LimitReader(conn, 8192))
			conn.Close()
			if s := strings.TrimSpace(string(data)); s != "" {
				select {
				case tokenCh <- s:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	return func() {
		ln.Close()
		os.Remove(path)
	}, true
}

// runURLHandler is the body of the hidden `url-handler` command. It dials
// the login socket and writes the URL it was launched with. If no login is
// in progress (socket absent), it exits quietly — there is nothing to do.
func runURLHandler(args []string) error {
	if len(args) == 0 || args[0] == "" {
		return nil
	}
	conn, err := net.Dial("unix", loginSocketPath())
	if err != nil {
		return nil // no login waiting; nothing to deliver to
	}
	defer conn.Close()
	_, err = io.WriteString(conn, args[0])
	return err
}

// registerSchemeHandler registers the mmauth:// handler and reports where
// the .desktop entry landed. Used by `matterbox register-handler` (which
// `make install` runs); login registers lazily via registerMmauthHandler.
func registerSchemeHandler(out io.Writer) error {
	p, err := registerMmauthHandler()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "registered mmauth:// login handler → %s\n", p)
	return nil
}

// registerMmauthHandler writes a .desktop entry mapping the mmauth://
// scheme to `matterbox url-handler %u` and makes it the default handler. It
// returns the path of the written entry. The entry is rewritten every run
// so its Exec path tracks the current binary location (so moving or
// reinstalling matterbox self-heals).
//
// The .desktop filename and location must stay in sync with the Makefile's
// uninstall target, which removes it directly (the binary may be gone by
// then).
func registerMmauthHandler() (string, error) {
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

	// Best-effort: make it the default for the scheme and refresh the
	// desktop database so browsers pick up the association. Missing tools
	// aren't fatal — the MimeType line alone suffices on many setups.
	if p, err := exec.LookPath("xdg-mime"); err == nil {
		_ = exec.Command(p, "default", desktopName, "x-scheme-handler/mmauth").Run()
	}
	if p, err := exec.LookPath("update-desktop-database"); err == nil {
		_ = exec.Command(p, appsDir).Run()
	}
	return desktopPath, nil
}
