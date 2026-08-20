//go:build !video

package libav

// Linked reports no libav in this build: without the `video` tag no ffmpeg
// library is linked, so there is no third-party license to constrain handing
// the binary out. The zero Info classifies as ClassNone.
func Linked() Info { return Info{} }
