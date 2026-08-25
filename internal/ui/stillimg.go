package ui

import (
	"net/url"
	"path"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

// Which still-image formats we can turn into pixels, and where the decoder for
// each comes from. Three tiers, and the tier is what decides whether a format is
// available at all:
//
//	stdlib          png, jpeg, gif          always
//	golang.org/x/image  webp, bmp, tiff     always (the module is already linked
//	                                        for x/image/draw's scalers, so these
//	                                        cost three blank imports and no new
//	                                        dependency — see preview.go)
//	libav               heic, heif, avif    only with -tags video
//	libav + libjxl      jxl                 only with -tags video AND an ffmpeg
//	                                        built --enable-libjxl (asked at
//	                                        runtime — see jxlDecodable)
//
// The last tier is the interesting one. HEIC is what every iPhone photo is, and
// AVIF is spreading fast on the web, and neither has a pure-Go decoder worth
// linking: both are an image in an ISO-BMFF container coded with a video codec
// (HEVC and AV1 respectively), so a decoder means a video decoder. We already have
// one behind the `video` tag, and looksLikeVideo already matches any ftyp box, so
// the bytes route themselves — decodeImageFrames hands them to libav and gets its
// frames back. All this file has to do is admit them.
//
// "Still" is the common case, not a guarantee: an AVIF can carry an animated AV1
// sequence, exactly as a WebP can carry an animated VP8 one. Nothing here needs to
// know which — the decoder returns however many frames the file holds, and a
// one-frame result folds the animation machinery away by itself (the native path
// is gated on len(frames) > 1). isVideoAttachment claims .avif for the same reason
// it claims .webp, so an animated one plays where the session can play animation
// and shows its first frame where it cannot. See the note there about why HEIC is
// treated as a still even when it holds several images.
//
// Note what that does NOT depend on: animations.native_animation. Playing a clip
// needs the Kitty native-animation path (see videoPlayable), but decoding one
// still frame needs nothing of the sort, so a HEIC renders on a video build
// whatever the animation settings say. Keeping those two questions apart is why
// isStillImageAttachment exists next to isVideoAttachment rather than inside it.
//
// Recognition is by extension first and MIME second, for the reason
// isSVGAttachment and isSTLAttachment do it that way: Mattermost leaves
// mime_type empty for a fair slice of uploads, so the filename is the more
// reliable field. The bytes are sniffed again at decode time, so a mislabelled
// file fails cleanly rather than drawing nonsense.

// stillImageExt reports whether an extension (no dot, any case) names a still
// image this build can always decode. The set deliberately matches the one the
// Mattermost server itself decodes — stdlib plus x/image's bmp/tiff/webp — so a
// file the server was willing to make a thumbnail of is a file we can draw.
func stillImageExt(ext string) bool {
	switch strings.ToLower(ext) {
	case "png", "jpg", "jpeg", "jpe", "gif", "webp", "bmp", "tif", "tiff":
		return true
	}
	return false
}

// stillImageMIME reports the same by MIME type. The x-prefixed spellings are the
// legacy ones still emitted by some clients (and by Windows for BMP).
func stillImageMIME(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/jpg", "image/gif",
		"image/webp", "image/bmp", "image/x-ms-bmp", "image/x-bmp",
		"image/tiff", "image/x-tiff":
		return true
	}
	return false
}

// jxlExt reports whether an extension names a JPEG XL image. Separate from
// libavStillExt because it is gated differently — by a runtime probe rather than
// by the build tag. See jxlDecodable.
func jxlExt(ext string) bool { return strings.EqualFold(ext, "jxl") }

// jxlMIME reports the same by MIME type. image/jxl is the registered type;
// image/jpegxl and image/x-jxl turn up from older tooling.
func jxlMIME(mime string) bool {
	switch mime {
	case "image/jxl", "image/jpegxl", "image/x-jxl":
		return true
	}
	return false
}

// decodableStillExt folds all three tiers into the one question the callers below
// actually have: can this build turn a file with that extension into pixels?
func decodableStillExt(ext string) bool {
	return stillImageExt(ext) ||
		(videoBuild && libavStillExt(ext)) ||
		(jxlDecodable() && jxlExt(ext))
}

// decodableStillMIME is decodableStillExt by MIME type.
func decodableStillMIME(mime string) bool {
	return stillImageMIME(mime) ||
		(videoBuild && libavStillMIME(mime)) ||
		(jxlDecodable() && jxlMIME(mime))
}

// libavStillExt reports whether an extension names an image only libav can decode.
// Gated by videoBuild at every call site, never here, so the list itself stays
// readable as "what FFmpeg adds". "Still" names the common case: an .avif may be
// animated, and is handled as such elsewhere (see the package comment above).
func libavStillExt(ext string) bool {
	switch strings.ToLower(ext) {
	case "heic", "heif", "hif", "avif":
		return true
	}
	return false
}

// libavStillMIME reports the same by MIME type. image/heic-sequence and
// image/heif-sequence are multi-image HEIF files; libav decodes the first frame,
// which is exactly what we want to show.
func libavStillMIME(mime string) bool {
	switch mime {
	case "image/heic", "image/heif", "image/heic-sequence", "image/heif-sequence", "image/avif":
		return true
	}
	return false
}

// attachmentMIME is an upload's MIME type, lowercased and stripped of any
// parameter ("image/jpeg; charset=binary" → "image/jpeg").
func attachmentMIME(f *model.FileInfo) string {
	mime, _, _ := strings.Cut(f.MimeType, ";")
	return strings.ToLower(strings.TrimSpace(mime))
}

// isStillImageAttachment reports whether an uploaded file is a still image this
// build can decode — the whole question, across both tiers and both fields.
func isStillImageAttachment(f *model.FileInfo) bool {
	if f == nil {
		return false
	}
	// Extension first: it is a short string compare, where the MIME test has to
	// split a parameter off first. This is asked of every attachment on every
	// uncached render (see drawsFileThumb), so the order is worth the thought.
	if decodableStillExt(attachmentExt(f)) {
		return true
	}
	return decodableStillMIME(attachmentMIME(f))
}

// JPEGXLDecodable reports whether this binary can decode JPEG XL. Exported for
// `matterbox --version`, which is the only place a user can find out: the answer
// depends on how the system ffmpeg was configured, so two binaries from the same
// commit and the same build tag can disagree, and the only symptom otherwise is a
// .jxl quietly keeping its paperclip.
func JPEGXLDecodable() bool { return jxlDecodable() }

// AVIFDecodable reports whether this binary can decode AVIF, for the same
// reason and the same caller.
func AVIFDecodable() bool { return avifDecodable() }

// isStillImageURL reports whether a URL's path ends in a still-image extension
// this build can decode. The query string — which a CDN like Giphy stuffs with
// cache keys — is ignored.
func isStillImageURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	ext := strings.TrimPrefix(path.Ext(u.Path), ".")
	if ext == "" {
		return false
	}
	return decodableStillExt(ext)
}
