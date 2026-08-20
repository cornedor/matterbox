//go:build video

package libav

/*
#cgo pkg-config: libavutil
#include <libavutil/avutil.h>
*/
import "C"

// Linked reports what this build's libav says about itself. avutil_license and
// av_version_info are plain accessors over string constants compiled into
// libavutil — no context to allocate, no initialisation needed, safe to call at
// any time. Only libavutil is needed, so this file pulls in the smallest
// possible slice of ffmpeg even though the decoder itself uses far more.
func Linked() Info {
	return Info{
		Linked:  true,
		License: C.GoString(C.avutil_license()),
		Version: C.GoString(C.av_version_info()),
	}
}
