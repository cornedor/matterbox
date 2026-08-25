//go:build video

package ui

import (
	_ "embed"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// stillAVIF is a 48x32 AVIF still (testsrc pattern) — an AV1 keyframe in an
// ISO-BMFF container, i.e. exactly the shape a phone's HEIC has with a different
// codec inside. 614 bytes.
//
//go:embed testdata/still.avif
var stillAVIF []byte

// TestDecodeAVIFStill is the empirical half of the libav still tier: the claim is
// that a modern camera/web image needs no new decoder because it is already a
// video codec in a video container, and looksLikeVideo already matches any ftyp
// box. This proves the whole route — sniff, demux, decode, scale — rather than
// trusting it.
func TestDecodeAVIFStill(t *testing.T) {
	if !looksLikeVideo(stillAVIF) {
		t.Fatal("looksLikeVideo(AVIF) = false: the bytes would never reach libav")
	}
	frames, delays, err := decodeImageFrames(stillAVIF, true)
	if err != nil {
		t.Fatalf("decodeImageFrames(AVIF): %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1 (a still)", len(frames))
	}
	if b := frames[0].Bounds(); b.Dx() != 48 || b.Dy() != 32 {
		t.Errorf("frame bounds %v, want 48x32", b)
	}
	// One frame means nothing to animate, so the native-animation path stays off
	// no matter what the config says — which is why the still tier can ignore it.
	if len(delays) > 1 {
		t.Errorf("got %d delays for a still, want 0 or 1", len(delays))
	}
}

// TestAVIFAttachmentPreviewableWithAnimationOff pins the gate split on a real
// file: animation settings must not decide whether a still image renders.
func TestAVIFAttachmentPreviewableWithAnimationOff(t *testing.T) {
	m := &Model{} // nativeAnim false => videoPlayable() false
	f := &model.FileInfo{Id: "f1", Name: "IMG_0042.avif", Size: int64(len(stillAVIF))}
	if !m.filePreviewable(f) {
		t.Error("filePreviewable(.avif) = false with animation off, want true")
	}
	if m.videoPlayable() {
		t.Fatal("videoPlayable() = true on a zero Model, test premise is wrong")
	}
}

// animAVIF is a 10-frame animated AVIF (48x32 testsrc at 10fps). ffmpeg writes an
// animated AVIF as a one-frame still item *followed by* the animated sequence, so
// this file is also the regression fixture for pickVideoStream — taking the first
// video stream shows one frame of a file that plainly has motion in it.
//
//go:embed testdata/anim.avif
var animAVIF []byte

// TestDecodeAnimatedAVIF: "still image format" is the common case for AVIF, not a
// guarantee — it can carry an animated AV1 sequence exactly as a WebP can carry an
// animated VP8 one. Animating must yield the whole sequence, and not animating
// must yield just the poster frame.
func TestDecodeAnimatedAVIF(t *testing.T) {
	frames, delays, err := decodeImageFrames(animAVIF, true)
	if err != nil {
		t.Fatalf("decodeImageFrames(animated AVIF): %v", err)
	}
	if len(frames) < 2 {
		t.Fatalf("got %d frames, want the whole sequence — pickVideoStream took the still item", len(frames))
	}
	if len(delays) != len(frames) {
		t.Errorf("%d delays for %d frames", len(delays), len(frames))
	}
	still, _, err := decodeImageFrames(animAVIF, false)
	if err != nil {
		t.Fatalf("decodeImageFrames(animated AVIF, still): %v", err)
	}
	if len(still) != 1 {
		t.Errorf("the poster decode returned %d frames, want 1", len(still))
	}
}

// TestAnimatedAVIFIsAVideoAttachment: the animation only ever plays if the file is
// claimed by the video path, which is why .avif sits in that list next to .webp.
func TestAnimatedAVIFIsAVideoAttachment(t *testing.T) {
	f := &model.FileInfo{Id: "f1", Name: "loop.avif", Size: int64(len(animAVIF))}
	if !isVideoAttachment(f) {
		t.Error("isVideoAttachment(.avif) = false, so an animated one could never play")
	}
	// And it is still a still image, so it renders with animation off.
	if !isStillImageAttachment(f) {
		t.Error("isStillImageAttachment(.avif) = false")
	}
}

// TestPickVideoStreamFallsBackToFirst: nb_frames is unknown in plenty of
// containers, so the preference must never leave us with no stream at all.
func TestPickVideoStreamFallsBackToFirst(t *testing.T) {
	if pickVideoStream(nil) != nil {
		t.Error("pickVideoStream(nil) returned a stream")
	}
	// A plain single-stream clip must still resolve, and still decode.
	frames, _, err := decodeVideoFrames(clipMP4, false, thumbVideoProfile)
	if err != nil || len(frames) != 1 {
		t.Errorf("plain mp4 poster decode: %d frames, err %v", len(frames), err)
	}
}
