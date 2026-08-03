//go:build video

package ui

import (
	_ "embed"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// clipMP4 is a 1-second, 30fps, 48x32 h264 test clip (testsrc pattern). At
// videoTargetFPS=15 the decoder decimates 30fps by a step of 2, so a full decode
// yields ~15 frames.
//
//go:embed testdata/clip.mp4
var clipMP4 []byte

func TestDecodeVideoFramesAnimated(t *testing.T) {
	frames, delays, err := decodeVideoFrames(clipMP4, true, thumbVideoProfile)
	if err != nil {
		t.Fatalf("decodeVideoFrames: %v", err)
	}
	// 30fps decimated to ~15fps => ~15 frames; allow a little slack for the
	// decoder dropping/rounding at the boundaries.
	if len(frames) < 10 || len(frames) > 16 {
		t.Fatalf("got %d frames, want ~15 (30fps decimated to 15)", len(frames))
	}
	if len(delays) != len(frames) {
		t.Fatalf("delays len %d != frames len %d", len(delays), len(frames))
	}
	// Downscaled within the profile's maxSide, aspect preserved (48x32 is already
	// small, so it should come back unchanged and even-dimensioned).
	b := frames[0].Bounds()
	if b.Dx() > thumbVideoProfile.maxSide || b.Dy() > thumbVideoProfile.maxSide {
		t.Errorf("frame %dx%d exceeds maxSide %d", b.Dx(), b.Dy(), thumbVideoProfile.maxSide)
	}
	if b.Dx() <= 0 || b.Dy() <= 0 {
		t.Errorf("frame has empty bounds %v", b)
	}
	for i, d := range delays {
		if d <= 0 {
			t.Errorf("delay[%d] = %v, want > 0", i, d)
		}
	}
}

func TestDecodeVideoFramesPoster(t *testing.T) {
	frames, delays, err := decodeVideoFrames(clipMP4, false, thumbVideoProfile)
	if err != nil {
		t.Fatalf("decodeVideoFrames poster: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("poster decode returned %d frames, want 1", len(frames))
	}
	if delays != nil {
		t.Errorf("poster decode delays = %v, want nil", delays)
	}
}

func TestDecodeVideoFramesRejectsNonVideo(t *testing.T) {
	// A PNG's bytes should never reach the video decoder in practice (looksLikeVideo
	// gates it), but if forced, libav finds no video stream and it errors cleanly.
	if _, _, err := decodeVideoFrames([]byte("\x89PNG\r\n\x1a\nnot a video"), true, thumbVideoProfile); err == nil {
		t.Error("decodeVideoFrames on non-video bytes = nil error, want failure")
	}
}

func TestVideoStreamChunks(t *testing.T) {
	s, err := openVideoStream(clipMP4, streamVideoProfile)
	if err != nil {
		t.Fatalf("openVideoStream: %v", err)
	}
	defer s.close()

	var total int
	var eof bool
	rounds := 0
	for !eof {
		rounds++
		if rounds > 100 {
			t.Fatal("stream never reached eof")
		}
		frames, delays, e, err := s.nextChunk(4)
		if err != nil {
			t.Fatalf("nextChunk: %v", err)
		}
		if len(frames) != len(delays) {
			t.Fatalf("frames %d != delays %d", len(frames), len(delays))
		}
		for i, f := range frames {
			if f.Bounds().Dx() <= 0 || f.Bounds().Dy() <= 0 {
				t.Errorf("chunk frame %d empty bounds", i)
			}
			if delays[i] <= 0 {
				t.Errorf("chunk frame %d delay %v", i, delays[i])
			}
		}
		total += len(frames)
		eof = e
	}
	// 30 source frames decimated 30→24fps: step=round(30/24)=1, so ~all 30 kept.
	// Just assert it streamed a plausible number across more than one chunk.
	if total < 10 {
		t.Fatalf("streamed only %d frames, want the whole ~30-frame clip", total)
	}
	if rounds < 2 {
		t.Fatalf("clip came in %d round(s); expected chunking across several", rounds)
	}
	// After eof, further pulls are empty + eof.
	f, _, e, err := s.nextChunk(4)
	if err != nil || !e || len(f) != 0 {
		t.Errorf("post-eof nextChunk = (%d frames, eof=%v, err=%v), want (0, true, nil)", len(f), e, err)
	}
}

// In the video build, a video attachment is previewable exactly when
// native_animation is on.
func TestVideoGatingVideoBuild(t *testing.T) {
	on := &Model{nativeAnim: true}
	off := &Model{nativeAnim: false}
	vid := &model.FileInfo{MimeType: "video/mp4"}
	if !on.videoPlayable() || !on.filePreviewable(vid) {
		t.Error("with native_animation on, video should be playable/previewable")
	}
	if off.videoPlayable() || off.filePreviewable(vid) {
		t.Error("with native_animation off, video should not be playable/previewable")
	}
	// Images never depend on native_animation.
	if !off.filePreviewable(&model.FileInfo{MimeType: "image/png"}) {
		t.Error("png should stay previewable regardless of native_animation")
	}
}
