//go:build !video

package ui

import (
	"image"
	"time"
)

// Without the `video` build tag there is no video decoding: playing a clip
// needs cgo + libav (see video_libav.go), a heavy dependency for a
// nice-to-have, so it is opt-in exactly like the demoaudio soundtrack. videoBuild
// is a compile-time false here, so decodeImageFrames' video branch and
// videoPlayable both fold away and libav is never linked. Build with
// `-tags video` (and CGO_ENABLED=1 + libav dev libs) to compile in the real
// decoder in video_libav.go.
const videoBuild = false

// decodeVideoFrames is a no-op stub in this build: it is only ever reached
// behind `if videoBuild`, which is false here, so it never actually runs — it
// exists solely so the shared code compiles. See video_libav.go for the real one.
func decodeVideoFrames(raw []byte, animate bool, prof videoProfile) ([]image.Image, []time.Duration, error) {
	return nil, nil, errVideoUnsupported
}

// openVideoStream is likewise a stub: streamsPreviewVideo (which gates every
// call) is false without videoBuild, so this never runs. See video_libav.go.
func openVideoStream(raw []byte, prof videoProfile) (videoStream, error) {
	return nil, errVideoUnsupported
}
