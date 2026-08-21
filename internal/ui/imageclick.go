package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/config"
)

// Clicking a rendered inline thumbnail: the Kitty placeholder cells are wrapped
// in an OSC 8 hyperlink whose "URL" is our own image-click scheme (see
// imageClickURL). A bare click lands in activateLink the same way an ordinary
// link does; we intercept the scheme here and dispatch per config.image_click
// (preview / open / download / off). The filename chip and the rest of the
// message stay ordinary selection targets — only the thumbnail cells carry the
// link.

const imageClickURLScheme = "matterbox-image:"

// imageClickURL is the OSC 8 target for one thumbnail. The payload is the
// thumbKey (file id or body-image URL), base64 so a URL with punctuation can't
// break the escape sequence.
func imageClickURL(it previewItem) string {
	key := thumbKey(it)
	if key == "" {
		return ""
	}
	return imageClickURLScheme + encodeCopyPayload(key)
}

// parseImageClickURL extracts the thumbKey from an image-click OSC 8 URL.
func parseImageClickURL(url string) (key string, ok bool) {
	enc, ok := strings.CutPrefix(url, imageClickURLScheme)
	if !ok {
		return "", false
	}
	return decodeCopyPayload(enc)
}

// wrapImageClickLink wraps a ready thumbnail row in its OSC 8 target when
// image_click is not off. The gutter ("  ") stays outside the link so only the
// image cells themselves are the hit target.
func (m *Model) wrapImageClickLink(it previewItem, row string) string {
	if m.imageClick == "off" {
		return row
	}
	u := imageClickURL(it)
	if u == "" {
		return row
	}
	return osc8Link(u, row)
}

// handleImageClick runs the configured action for a clicked thumbnail.
// pane is the transcript the click landed in (mousedown already selected the
// post there — see selectPostAt / selectThreadPostAt / clickSQLRow).
func (m Model) handleImageClick(pane focus, key string) (tea.Model, tea.Cmd) {
	if m.imageClick == "off" || key == "" {
		return m, nil
	}
	var p *model.Post
	if pane == focusSQLResults {
		p = m.sqlSelectedPost()
	} else {
		p = m.selectedPost()
	}
	if p == nil {
		return m, nil
	}
	items := previewImages(p, m.videoPlayable())
	idx := -1
	var it previewItem
	for i, cand := range items {
		if thumbKey(cand) == key {
			idx, it = i, cand
			break
		}
	}
	if idx < 0 {
		m.status = "image not found on this message"
		return m, nil
	}
	switch m.imageClick {
	case "open":
		o := openable{name: it.name, file: it.file, url: it.url}
		if o.file != nil {
			o.name = o.file.Name
		} else if o.name == "" {
			o.name = o.url
		}
		return m, m.openTarget(o)
	case "download":
		// Same scope as `s`: uploaded attachments only, not body-image URLs.
		if it.file == nil {
			m.status = "download only works for uploaded attachments"
			return m, nil
		}
		m.status = fmt.Sprintf("downloading %s…", downloadName(it.file))
		return m, m.downloadFiles([]*model.FileInfo{it.file})
	default: // preview (and any unrecognised value treated as preview)
		return m.openPreviewItems(items, idx)
	}
}

// runSetImageClick is the ">" palette entry: set (and persist) what a mouse
// click on an inline thumbnail does. Accepts preview / open / download / off.
func runSetImageClick(m *Model, arg string) tea.Cmd {
	v := strings.ToLower(strings.TrimSpace(arg))
	switch v {
	case "preview", "open", "download", "off":
	default:
		m.status = "image click: use preview, open, download, or off"
		return nil
	}
	if m.imageClick == v {
		m.status = "image click already " + v
		return nil
	}
	m.imageClick = v
	// Thumb OSC 8 wrap depends on this setting; drop the line cache so the next
	// paint rebuilds with (or without) the click targets.
	m.postLineCache = nil
	m.renderMessages()
	if m.threadOpen {
		m.renderThread()
	}
	m.status = "image click → " + v + " (saved)"
	return func() tea.Msg {
		_ = config.SaveImageClick(v)
		return nil
	}
}
