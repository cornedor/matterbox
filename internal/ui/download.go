package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// attachmentsDownloadedMsg reports the result of saving a post's uploaded
// files to the download directory. saved holds the base names actually
// written (so a partial run before an error still names what landed); err
// is the first failure, if any.
type attachmentsDownloadedMsg struct {
	// started dates the transfer, for feature_used's latency: a download is
	// slow enough for the missing progress indicator to be a real complaint.
	started time.Time
	dir     string
	saved   []string
	err     error
}

// expandUserPath expands a leading "~" (or "~/") in p to the user's home
// directory. Any other path — absolute or relative — is returned unchanged.
// A bare "~" with no home resolvable falls back to the literal input.
func expandUserPath(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// postFiles returns the uploaded file attachments on a post, in metadata
// order. Inline image/link URLs (which `o` can open) are intentionally
// excluded — downloading saves the files Mattermost stores, not arbitrary
// web targets.
func postFiles(p *model.Post) []*model.FileInfo {
	if p == nil || p.Metadata == nil {
		return nil
	}
	return p.Metadata.Files
}

// downloadFromPost saves every uploaded file on the selected post to the
// configured download directory, mirroring openFromPost's entry shape. The
// actual transfer runs in the returned command; completion arrives as
// attachmentsDownloadedMsg.
func (m Model) downloadFromPost(p *model.Post) (tea.Model, tea.Cmd) {
	files := postFiles(p)
	if len(files) == 0 {
		m.status = "no files to download on this message"
		return m, nil
	}
	m.status = fmt.Sprintf("downloading %s…", countFiles(len(files)))
	return m, m.downloadFiles(files)
}

// downloadFiles fetches each file's bytes and writes them to m.downloadDir,
// avoiding collisions with uniqueDownloadPath. The directory is created on
// demand. A failure stops at the first error but still reports whatever was
// saved before it.
func (m Model) downloadFiles(files []*model.FileInfo) tea.Cmd {
	dir := m.downloadDir
	client := m.client
	ctx := m.ctx
	started := featureStart()
	return func() tea.Msg {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return attachmentsDownloadedMsg{dir: dir, err: err, started: started}
		}
		var saved []string
		for _, f := range files {
			data, err := client.DownloadFile(ctx, f.Id)
			if err != nil {
				return attachmentsDownloadedMsg{dir: dir, saved: saved, started: started, err: fmt.Errorf("%s: %w", downloadName(f), err)}
			}
			dest := uniqueDownloadPath(dir, downloadName(f))
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				return attachmentsDownloadedMsg{dir: dir, saved: saved, started: started, err: err}
			}
			saved = append(saved, filepath.Base(dest))
		}
		return attachmentsDownloadedMsg{dir: dir, saved: saved, started: started}
	}
}

// downloadName picks the on-disk name for a file, falling back to its id (or
// a generic "file") when the server reports no name, and stripping any path
// separators a hostile name might carry so the write stays inside dir.
func downloadName(f *model.FileInfo) string {
	name := filepath.Base(f.Name)
	if name == "" || name == "." || name == string(filepath.Separator) {
		if f.Id != "" {
			return f.Id
		}
		return "file"
	}
	return name
}

// uniqueDownloadPath returns a path inside dir for name that doesn't clash
// with an existing file: "report.pdf", then "report (1).pdf", "report (2).pdf",
// … inserting the counter before the extension so the suffix stays meaningful.
func uniqueDownloadPath(dir, name string) string {
	dest := filepath.Join(dir, name)
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		return dest
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// applyDownloadResult turns a completed download into a status update: a
// self-clearing toast on success, or a sticky error naming what (if anything)
// was saved before the failure so a partial multi-file download isn't lost.
func (m *Model) applyDownloadResult(msg attachmentsDownloadedMsg) tea.Cmd {
	m.recordFeature("download", "key", msg.started, len(msg.saved), msg.err)
	if msg.err != nil {
		if len(msg.saved) > 0 {
			m.status = fmt.Sprintf("download failed after %s: %v", countFiles(len(msg.saved)), msg.err)
		} else {
			m.status = "download: " + msg.err.Error()
		}
		return nil
	}
	if len(msg.saved) == 1 {
		return m.flashStatus(fmt.Sprintf("saved %s to %s", msg.saved[0], msg.dir))
	}
	return m.flashStatus(fmt.Sprintf("saved %s to %s", countFiles(len(msg.saved)), msg.dir))
}

// countFiles renders a file count for status lines ("1 file" / "3 files").
func countFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}
