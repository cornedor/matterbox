package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// TestStillImageAttachmentByExtension: the filename decides, because Mattermost
// leaves mime_type empty for a fair slice of uploads — which is exactly the case
// that used to leave a perfectly decodable image showing a paperclip.
func TestStillImageAttachmentByExtension(t *testing.T) {
	for _, name := range []string{"a.png", "b.JPG", "c.jpeg", "d.gif", "e.webp", "f.bmp", "g.tif", "h.tiff"} {
		if !isStillImageAttachment(&model.FileInfo{Name: name}) {
			t.Errorf("isStillImageAttachment(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"a.pdf", "b.zip", "c.mp4", "d.stl", "e", "f.svg"} {
		if isStillImageAttachment(&model.FileInfo{Name: name}) {
			t.Errorf("isStillImageAttachment(%q) = true, want false", name)
		}
	}
	if isStillImageAttachment(nil) {
		t.Error("isStillImageAttachment(nil) = true")
	}
}

// TestStillImageAttachmentByMIME: no usable filename, so the MIME type carries it.
func TestStillImageAttachmentByMIME(t *testing.T) {
	for _, mime := range []string{"image/png", "image/webp", "IMAGE/WEBP", "image/tiff", "image/x-ms-bmp", "image/jpeg; charset=binary"} {
		if !isStillImageAttachment(&model.FileInfo{Name: "upload", MimeType: mime}) {
			t.Errorf("isStillImageAttachment(mime %q) = false, want true", mime)
		}
	}
	for _, mime := range []string{"application/pdf", "video/mp4", "image/svg+xml", ""} {
		if isStillImageAttachment(&model.FileInfo{Name: "upload", MimeType: mime}) {
			t.Errorf("isStillImageAttachment(mime %q) = true, want false", mime)
		}
	}
}

// TestLibavStillTierFollowsBuildTag: HEIC and AVIF are decodable only where libav
// is linked, so whether we claim them must track videoBuild exactly — claiming one
// without a decoder would reserve thumbnail rows for an image that never arrives.
func TestLibavStillTierFollowsBuildTag(t *testing.T) {
	for _, f := range []*model.FileInfo{
		{Name: "IMG_0042.HEIC"},
		{Name: "photo.avif"},
		{Name: "upload", MimeType: "image/heic"},
		{Name: "upload", MimeType: "image/avif"},
		{Name: "upload", MimeType: "image/heif-sequence"},
	} {
		if got := isStillImageAttachment(f); got != videoBuild {
			t.Errorf("isStillImageAttachment(%q/%q) = %v, want %v (videoBuild)", f.Name, f.MimeType, got, videoBuild)
		}
	}
}

// TestStillImageURLQueryIgnored: a CDN stuffs the query string with cache keys, so
// the extension has to be read off the path (mirrors the Giphy case).
func TestStillImageURLQueryIgnored(t *testing.T) {
	if !isStillImageURL("https://cdn.example.com/a/b.webp?v=3&sig=abc") {
		t.Error("isStillImageURL with a query = false, want true")
	}
	if isStillImageURL("https://cdn.example.com/a/b.webp.zip") {
		t.Error("isStillImageURL(.webp.zip) = true, want false")
	}
	if isStillImageURL("") {
		t.Error("isStillImageURL(\"\") = true")
	}
}

// TestStillImageDecodeGateIsNotTheAnimationGate: a still image must be previewable
// on a build that can decode it, whatever the animation settings say. This is the
// regression the split fixed — filePreviewable used to route webp through
// videoPlayable(), so native_animation being off (the default) hid it.
func TestStillImageDecodeGateIsNotTheAnimationGate(t *testing.T) {
	m := &Model{} // nativeAnim false, so videoPlayable() is false
	if m.videoPlayable() {
		t.Fatal("videoPlayable() = true on a zero Model, test premise is wrong")
	}
	for _, name := range []string{"a.webp", "b.bmp", "c.tiff"} {
		if !m.filePreviewable(&model.FileInfo{Name: name}) {
			t.Errorf("filePreviewable(%q) = false with animation off, want true", name)
		}
	}
}
