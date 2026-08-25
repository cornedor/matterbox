package ui

import (
	"errors"
	"image"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/svgimg"
)

// Short-clip video rendering. A `video`-tagged build (cgo + libav; see
// video_libav.go) decodes an uploaded mp4/webm/mov/… into the same
// []image.Image + []time.Duration a GIF produces (compositeGIF), so the whole
// Kitty native-animation pipeline (kittyanim.go) plays it with no new transmit
// code — a video is just a GIF whose frames came from libav. Without the tag
// (the default build) decodeVideoFrames is a stub, videoBuild is false, and
// every video keeps its 🎬 file icon.
//
// The native path uploads every frame to the terminal up front, which is right
// for a short loop but would blow up on a real movie, so decodeVideoFrames caps
// the clip hard (see the constants below): decimate to videoTargetFPS, stop at
// videoMaxFrames / videoMaxDuration, and downscale to videoMaxSide. Anything
// longer than that just isn't decoded — the file keeps its icon. This matches
// what actually gets posted in chat: reaction clips that are GIFs in an mp4
// coat (Mattermost/Slack/Discord transcode GIF uploads to h264).
// videoProfile bounds one decode: how many frames it may contribute, the
// wall-clock span (a second ceiling for a low-fps source), the fps the source
// is decimated down to, and the longest pixel edge each frame is scaled to. The
// two presets below are the whole difference between an inline thumbnail and the
// space-to-preview modal — the decode/scale code is identical.
type videoProfile struct {
	maxFrames   int
	maxDuration time.Duration
	targetFPS   float64
	maxSide     int
}

// thumbVideoProfile keeps inline thumbnails cheap: a short, low-fps, small clip.
// A channel can hold many at once, so every knob is tight — ~15fps reads as
// smooth at thumbnail size and keeps the per-clip upload a fraction of the
// source. The placement fit downsizes 480px further, so it never needs more.
var thumbVideoProfile = videoProfile{
	maxFrames:   150,
	maxDuration: 6 * time.Second,
	targetFPS:   15,
	maxSide:     480,
}

// previewVideoProfile plays the whole clip at display resolution when the user
// deliberately opens one video with space: generous frame/duration ceilings (a
// safety net against an hour-long file, not a normal limit) and a higher fps,
// with maxSide raised per terminal in previewProfile(). The still-image
// preview's ≤1MP downscale is skipped for video for the same reason a GIF's is
// (usePreviewRendition) — the point is to see it properly. maxSide here is only
// the floor; the real cap tracks the terminal's own pixel width.
var previewVideoProfile = videoProfile{
	maxFrames:   900,
	maxDuration: 30 * time.Second,
	targetFPS:   24,
	maxSide:     1280,
}

// previewProfile is previewVideoProfile with maxSide raised to the terminal's
// pixel width when that is larger, so the clip decodes as crisply as the screen
// can actually show it (never upscaled past source — fitVideoSize only shrinks).
func (m *Model) previewProfile() videoProfile {
	p := previewVideoProfile
	if px := m.width * m.cellPxW; px > p.maxSide {
		p.maxSide = px
	}
	return p
}

// Streaming playback for the space-to-preview modal. Rather than decode a video
// whole (fine for a thumbnail, unbounded for a long clip), the preview opens a
// videoStream and pulls it a chunk at a time on background Cmds while the tick
// plays the buffered frames — so playback starts after ~one chunk and memory
// stays flat regardless of length. See preview.go's streaming player.
type videoStream interface {
	// nextChunk decodes up to max more kept frames (fewer at the end), returning
	// them, their per-frame delays, and whether the stream is now exhausted. The
	// next call resumes where this one stopped.
	nextChunk(max int) (frames []image.Image, delays []time.Duration, eof bool, err error)
	// close frees the decoder and its temp file.
	close()
}

// openVideoStream is provided per build (libav in video_libav.go, an error stub
// otherwise); it is only ever called behind streamsPreviewVideo, which is false
// unless videoBuild.

const (
	// streamChunkFrames is how many frames one decode-ahead Cmd pulls — small
	// enough that the first frame shows almost immediately, big enough to amortise
	// the Cmd round-trip.
	streamChunkFrames = 8
	// streamBufferFrames is the decode-ahead target: keep pulling chunks until at
	// least this many frames are buffered, so playback stays ~2/3s ahead of the
	// decoder and memory stays bounded (the whole point of streaming a long clip).
	// Each buffered frame keeps its decoded image (for resize re-fitting), so at
	// previewStreamMaxSide the buffer is a few tens of MB — flat regardless of
	// clip length, and freed when the modal closes.
	streamBufferFrames = 16
	// previewStreamMaxSide caps the streaming decode resolution. Fixed (not
	// terminal-tied like previewProfile) precisely because streaming holds several
	// frames at once — the buffer's memory is bounded by this. 960px is crisp for
	// a modal that occupies most of the screen; fitFrameToCells fits it to the box.
	previewStreamMaxSide = 960
)

// streamVideoProfile is the decode profile for streaming playback: the whole
// clip (nextChunk ignores the frame/duration ceilings, pulling until EOF), at a
// smooth fps and the bounded streaming resolution.
var streamVideoProfile = videoProfile{
	maxFrames:   1 << 30,
	maxDuration: 1 << 62,
	targetFPS:   24,
	maxSide:     previewStreamMaxSide,
}

// streamFrame is one buffered frame awaiting display: its pre-built Kitty
// transmit sequence (encoded off the main thread) and how long to hold it, plus
// the decoded image so a resize can re-fit it to the new placement box.
type streamFrame struct {
	seq   string
	delay time.Duration
	img   image.Image
	// id is the Kitty image id seq transmits under. Playback alternates it
	// between the preview's two ids in encode order (see encodeStreamFrames),
	// which is what keeps a frame from ever being uploaded over the image the
	// terminal is currently showing.
	id uint32
}

// streamsPreviewVideo reports whether opening it in the preview modal should
// stream it as video rather than decode a still/animation: a playable video
// attachment, with animation on (image_preview off means the user wants stills,
// so the poster path handles it instead).
func (m *Model) streamsPreviewVideo(it previewItem) bool {
	return videoBuild && m.videoPlayable() && m.animatePreview && it.file != nil && isVideoAttachment(it.file)
}

// errVideoUnsupported is what the non-video build's decodeVideoFrames returns,
// and what the libav build returns when a file carries no decodable video
// stream. Either way the caller surfaces it as a decode failure and the file
// falls back to its icon.
var errVideoUnsupported = errors.New("video decoding not available (build with -tags video)")

// looksLikeVideo sniffs the container magic of the formats libav decodes for us,
// deliberately excluding the still/animated image formats the stdlib path
// already owns (GIF/PNG/JPEG) so those never route through libav. It gates the
// decodeImageFrames video branch, so a mislabelled MIME can't send a PNG to the
// video decoder nor a real clip to the GIF path.
func looksLikeVideo(b []byte) bool {
	if len(b) < 12 {
		return false
	}
	switch {
	case string(b[4:8]) == "ftyp": // ISO base media: mp4, mov, m4v, …
		return true
	case b[0] == 0x1A && b[1] == 0x45 && b[2] == 0xDF && b[3] == 0xA3: // EBML: mkv, webm
		return true
	case string(b[0:4]) == "RIFF" && (string(b[8:12]) == "AVI " || string(b[8:12]) == "WEBP"):
		return true
	case string(b[0:3]) == "FLV":
		return true
	case string(b[0:4]) == "OggS": // Ogg (Theora/VP8)
		return true
	}
	return false
}

// isVideoAttachment reports whether an uploaded file is one decodeVideoFrames
// can play — by MIME (video/*, plus animated webp) or, since Mattermost leaves
// mime_type empty for a fair slice of uploads, by extension. Mirrors the icon
// list in fileIcon (info.go). webp rides along because libav plays its
// animation, which the stdlib image path cannot.
func isVideoAttachment(f *model.FileInfo) bool {
	if f == nil {
		return false
	}
	mime, _, _ := strings.Cut(f.MimeType, ";")
	if strings.HasPrefix(mime, "video/") || mime == "image/webp" {
		return true
	}
	switch strings.ToLower(strings.TrimPrefix(f.Extension, ".")) {
	case "mp4", "mov", "mkv", "webm", "avi", "m4v", "wmv", "flv", "mpg", "mpeg", "webp":
		return true
	}
	return false
}

// videoPlayable reports whether short-clip video should render this session: the
// binary must be built with the `video` tag (libav present) and the experimental
// animations.native_animation must be on, since video only ever animates through
// the Kitty native path — the manual per-frame re-transmit tick is GIF-only and
// re-sending 150 frames on a timer is exactly what we don't want.
func (m *Model) videoPlayable() bool { return videoBuild && m.nativeAnim }

// filePreviewable reports whether an attachment can be shown inline / in the
// preview modal: a stdlib-decodable image always, or a short-clip video when
// this session can play one. The Model-aware superset of previewableMIME.
func (m *Model) filePreviewable(f *model.FileInfo) bool {
	if f == nil {
		return false
	}
	return previewableMIME(f.MimeType) || isSVGAttachment(f) || (m.videoPlayable() && isVideoAttachment(f))
}

// decodePreviewFrames decodes bytes for the space-to-preview modal: a video at
// the generous preview profile (whole clip, display resolution — see
// previewProfile), and anything else through the shared still/GIF path. The
// inline-thumbnail and emoji paths stay on decodeImageFrames, which uses the
// tight thumbVideoProfile — the two differ only in how much of the clip, and at
// what resolution, they pull.
func (m *Model) decodePreviewFrames(raw []byte, animate bool) ([]image.Image, []time.Duration, error) {
	if svgimg.Looks(raw) {
		return decodeSVGFrames(raw, m.svgPreviewSide())
	}
	if videoBuild && looksLikeVideo(raw) {
		return decodeVideoFrames(raw, animate, m.previewProfile())
	}
	return decodeImageFrames(raw, animate)
}
