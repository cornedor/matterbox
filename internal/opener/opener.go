// Package opener launches the platform's default handler for a URL or
// local file path — the desktop equivalent of double-clicking it.
package opener

import (
	"os/exec"
	"runtime"
)

// Open hands target (a URL or a local file path) to the OS default
// handler. The launcher forks and returns immediately; we don't wait for
// the viewer/browser process to exit. On every platform the chosen
// command accepts both URLs and filesystem paths.
func Open(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}
