package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestLooksLikeVideo(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
		want bool
	}{
		{"mp4 ftyp", append([]byte{0, 0, 0, 0x18}, []byte("ftypmp42____")...), true},
		{"mov ftyp", append([]byte{0, 0, 0, 0x14}, []byte("ftypqt  ____")...), true},
		{"webm ebml", []byte{0x1A, 0x45, 0xDF, 0xA3, 1, 2, 3, 4, 5, 6, 7, 8}, true},
		{"avi riff", append([]byte("RIFF"), append([]byte{0, 0, 0, 0}, []byte("AVI ")...)...), true},
		{"webp riff", append([]byte("RIFF"), append([]byte{0, 0, 0, 0}, []byte("WEBP")...)...), true},
		{"flv", []byte("FLV\x01\x05\x00\x00\x00\x09\x00\x00\x00"), true},
		{"gif is not video", []byte("GIF89a__________"), false},
		{"png is not video", []byte("\x89PNG\r\n\x1a\n____"), false},
		{"jpeg is not video", []byte("\xff\xd8\xff\xe0____JFIF"), false},
		{"wav riff is not video", append([]byte("RIFF"), append([]byte{0, 0, 0, 0}, []byte("WAVE")...)...), false},
		{"too short", []byte("ftyp"), false},
	}
	for _, c := range cases {
		if got := looksLikeVideo(c.b); got != c.want {
			t.Errorf("looksLikeVideo(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsVideoAttachment(t *testing.T) {
	yes := []*model.FileInfo{
		{MimeType: "video/mp4"},
		{MimeType: "video/webm; codecs=vp9"},
		{MimeType: "image/webp"},
		{MimeType: "", Extension: "mov"},
		{MimeType: "", Extension: ".MKV"},
		{Extension: "webp"},
	}
	no := []*model.FileInfo{
		{MimeType: "image/png"},
		{MimeType: "image/gif"},
		{MimeType: "application/pdf", Extension: "pdf"},
		nil,
	}
	for _, f := range yes {
		if !isVideoAttachment(f) {
			t.Errorf("isVideoAttachment(%+v) = false, want true", f)
		}
	}
	for _, f := range no {
		if isVideoAttachment(f) {
			t.Errorf("isVideoAttachment(%+v) = true, want false", f)
		}
	}
}

// In a non-video build videoBuild is false, so no config makes a video file
// previewable and videoPlayable is always false — the feature is fully compiled
// out. (The video build has its own decode test in video_libav_test.go.)
func TestVideoGatingDefaultBuild(t *testing.T) {
	m := &Model{nativeAnim: true}
	if videoBuild {
		t.Skip("video build: gating covered by video_libav_test.go")
	}
	if m.videoPlayable() {
		t.Error("videoPlayable() = true in a non-video build, want false")
	}
	if m.filePreviewable(&model.FileInfo{MimeType: "video/mp4"}) {
		t.Error("a video attachment is previewable in a non-video build, want not")
	}
	// A plain image stays previewable regardless of build.
	if !m.filePreviewable(&model.FileInfo{MimeType: "image/png"}) {
		t.Error("png attachment should be previewable")
	}
}

func TestFilePreviewableImageAlways(t *testing.T) {
	m := &Model{}
	for _, mime := range []string{"image/png", "image/jpeg", "image/gif"} {
		if !m.filePreviewable(&model.FileInfo{MimeType: mime}) {
			t.Errorf("filePreviewable(%s) = false, want true", mime)
		}
	}
	if m.filePreviewable(&model.FileInfo{MimeType: "application/pdf"}) {
		t.Error("pdf should not be previewable")
	}
	if m.filePreviewable(nil) {
		t.Error("nil file should not be previewable")
	}
}
