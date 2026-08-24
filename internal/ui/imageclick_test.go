package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestImageClickURLRoundTrip(t *testing.T) {
	it := previewItem{file: &model.FileInfo{Id: "file-abc"}, name: "x.png"}
	u := imageClickURL(it)
	if !strings.HasPrefix(u, imageClickURLScheme) {
		t.Fatalf("url = %q, want scheme prefix", u)
	}
	key, ok := parseImageClickURL(u)
	if !ok || key != "file-abc" {
		t.Fatalf("parse = %q, %v; want file-abc, true", key, ok)
	}

	urlIt := previewItem{url: "https://example.com/a.png?x=1&y=2", name: "a.png"}
	u2 := imageClickURL(urlIt)
	key2, ok := parseImageClickURL(u2)
	if !ok || key2 != urlIt.url {
		t.Fatalf("url parse = %q, %v; want %q", key2, ok, urlIt.url)
	}
}

func TestWrapImageClickLinkRespectsConfig(t *testing.T) {
	m := thumbModel()
	it := previewItem{file: &model.FileInfo{Id: "f1", Name: "graph.png", MimeType: "image/png"}, name: "graph.png"}

	m.imageClick = "off"
	if got := m.wrapImageClickLink(it, "cells"); got != "cells" || strings.Contains(got, imageClickURLScheme) {
		t.Fatalf("off should leave row bare: %q", got)
	}

	m.imageClick = "preview"
	got := m.wrapImageClickLink(it, "cells")
	if !strings.Contains(got, imageClickURLScheme) || !strings.Contains(got, "cells") {
		t.Fatalf("preview should wrap cells in OSC 8: %q", got)
	}
	if strings.HasPrefix(got, "  ") {
		t.Fatalf("wrap must not add the gutter: %q", got)
	}
}

func TestInlineThumbLinesCarryImageClickLink(t *testing.T) {
	m := thumbModel()
	m.imageClick = "preview"
	readyThumb(m, "f1", 10, 18, 78)
	p := thumbPost("f1")
	lines := m.inlineFileThumbLines(p, p.Metadata.Files[0], 80)
	if len(lines) == 0 {
		t.Fatal("expected thumb lines")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, imageClickURLScheme) {
		t.Fatalf("ready thumbs should carry image-click OSC 8: %q", joined)
	}
	att := m.renderAttachments(p, 80)
	parts := strings.Split(att, "\n")
	chip := parts[len(parts)-1]
	if strings.Contains(chip, imageClickURLScheme) {
		t.Fatalf("filename chip must not be an image-click target: %q", chip)
	}
}

func TestHandleImageClickPreview(t *testing.T) {
	m := thumbCollapseModel(t, 1)
	m.imageClick = "preview"
	withImageAttachment(&m, "f1")
	m.postIdx = len(m.posts) - 1
	m.focus = focusMessages

	next, _ := m.handleImageClick(focusMessages, "f1")
	out := next.(Model)
	if !out.preview.active {
		t.Fatal("preview action should open the image preview modal")
	}
	if len(out.preview.items) == 0 || thumbKey(out.preview.items[0]) != "f1" {
		t.Fatalf("preview should open on file f1, got %+v", out.preview.items)
	}
}

func TestHandleImageClickOffIsNoop(t *testing.T) {
	m := thumbCollapseModel(t, 1)
	m.imageClick = "off"
	withImageAttachment(&m, "f1")
	m.postIdx = len(m.posts) - 1
	m.focus = focusMessages

	next, cmd := m.handleImageClick(focusMessages, "f1")
	out := next.(Model)
	if out.preview.active || cmd != nil {
		t.Fatal("off must not open preview or return a cmd")
	}
}

func TestHandleImageClickDownloadURLRejected(t *testing.T) {
	m := thumbCollapseModel(t, 1)
	m.imageClick = "download"
	m.focus = focusMessages
	m.postIdx = len(m.posts) - 1
	p := m.posts[m.postIdx]
	p.Message = "https://example.com/pic.png"
	p.Metadata = nil

	next, cmd := m.handleImageClick(focusMessages, "https://example.com/pic.png")
	out := next.(Model)
	if cmd != nil {
		t.Fatal("body-image download must not start a transfer")
	}
	if !strings.Contains(out.status, "uploaded") {
		t.Fatalf("status = %q, want an attachments-only hint", out.status)
	}
}

func TestRunSetImageClick(t *testing.T) {
	m := thumbModel()
	m.imageClick = "preview"

	cmd := runSetImageClick(m, "open")
	if m.imageClick != "open" {
		t.Fatalf("imageClick = %q, want open", m.imageClick)
	}
	if cmd == nil {
		t.Fatal("expected a persist cmd")
	}

	if cmd := runSetImageClick(m, "nope"); cmd != nil || m.imageClick != "open" {
		t.Fatalf("bad arg should be rejected; click=%q cmd=%v", m.imageClick, cmd != nil)
	}
}

func TestClickOutsidePreview(t *testing.T) {
	m := Model{
		width:  80,
		height: 40,
		vcache: &viewCache{bodyH: 30},
		preview: previewState{
			active:  true,
			caption: "shot.png",
			loading: true, // keeps the box small and deterministic
		},
	}
	x0, y0, x1, y1, ok := m.previewBoxBounds()
	if !ok {
		t.Fatal("expected measurable preview box")
	}
	cx, cy := (x0+x1)/2, (y0+y1)/2
	if m.clickOutsidePreview(cx, cy) {
		t.Fatalf("center (%d,%d) of box [%d,%d)–[%d,%d) reported outside", cx, cy, x0, y0, x1, y1)
	}
	if !m.clickOutsidePreview(0, 0) {
		t.Fatal("top-left corner should be outside the centered box")
	}
	if !m.clickOutsidePreview(m.width-1, tabsHeight+m.vcache.bodyH-1) {
		t.Fatal("bottom-right of body should be outside a small loading box")
	}
}
