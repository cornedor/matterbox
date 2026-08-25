package ui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

// Kitty's native animation frames (behind animations.native_animation,
// default off) — an alternative to the manual scheme every other file in this
// package uses (encode every frame once, then re-transmit whichever one is due
// on a client-side timer; see emojiimg.go's advanceFrame and its counterparts
// in inlineimg.go/preview.go). Instead, every frame is uploaded to the terminal
// exactly once and the terminal itself owns the timing and the loop:
//
//   - kittyTransmitImage (existing, emojiimg.go) creates the *root* frame
//     (frame 1) and the on-screen placement, exactly as for a still image.
//   - kittyTransmitFrame appends each remaining frame (a=f), carrying its own
//     display duration as the frame *gap*.
//   - kittySetRootGap sets the root frame's gap — the one delay a=T can't
//     carry, since a=T's z key means z-index, not gap.
//   - kittyAnimateStart (a=a) tells the terminal to run the loop.
//
// buildNativeAnimSetup below composes the last three into the one string a
// call site sends once the root already exists (see its own comment for why
// the root is always transmitted separately, and first). Once both have gone
// out, nothing further is required of the client: no tick, no re-transmit,
// until the image is freed with kittyDelete (which drops every frame and the
// animation state with it).
//
// This is intentionally gated behind a flag rather than replacing the manual
// path outright: the manual path only needs a terminal that can display a
// still Kitty image, which is exactly what the startup graphics probe
// (emojiImages.graphicsReady) checks. Animation frames are a newer, less
// widely exercised corner of the protocol, and a terminal that mishandles an
// a=f/a=a it doesn't fully support could show a frozen or blank image instead
// of gracefully falling back — a risk worth taking only for someone who opts
// in. See https://sw.kovidgoyal.net/kitty/graphics-protocol/#animation.

// kittyChunkRaw is kittyChunk generalized to an explicit first-chunk option
// list, for APCs the kitty.Options struct can't express: a=f's r/c/X/Y/z keys
// mean entirely different things there (frame number, base frame, composition
// mode, background colour, gap) than the struct's same-lettered Rows/Columns/
// OffsetX/OffsetY/Z fields do for image display, so building them through
// Options would mean repurposing fields for unrelated semantics. Continuation
// chunks carry only q=<quiet> and the m=1/m=0 boundary markers, exactly like
// kittyChunkOpts — see its comment for why the boundary case (an exact
// multiple of the chunk size) needs the trailing empty m=0 chunk.
func kittyChunkRaw(payload []byte, firstOpts []string, quiet int) string {
	var sb strings.Builder
	sb.Grow(len(payload) + (len(payload)/kitty.MaxChunkSize+1)*48)
	opts := func(first, last bool) []string {
		var o []string
		if first {
			o = firstOpts
		} else if quiet > 0 {
			o = append(o, fmt.Sprintf("q=%d", quiet))
		}
		if !first || !last {
			if last {
				o = append(o, "m=0")
			} else {
				o = append(o, "m=1")
			}
		}
		return o
	}
	first := true
	for len(payload) >= kitty.MaxChunkSize {
		sb.WriteString(ansi.KittyGraphics(payload[:kitty.MaxChunkSize], opts(first, false)...))
		payload = payload[kitty.MaxChunkSize:]
		first = false
	}
	sb.WriteString(ansi.KittyGraphics(payload, opts(first, true)...))
	return sb.String()
}

// kittyTransmitFrame builds the out-of-band APC that appends one more
// animation frame to an already-transmitted image (a=f): img is PNG-encoded
// exactly like a still transmit, but with X=1 (simple replacement) rather than
// the protocol's default alpha blend. Every frame we hand it — from
// compositeGIF, or a full decode's later frames — is already a fully resolved
// RGBA canvas, not a delta, so replacement is what makes the terminal treat it
// as such instead of blending our already-final pixels over whatever the
// previous frame left behind.
//
// gap is this frame's own display duration — the time the terminal waits,
// once this frame is current, before advancing to the next one — sent as the
// z key. The protocol treats z=0 as "unspecified" (falling back to a 40ms
// default), so a non-positive gap is clamped to 1ms rather than silently
// landing on that default.
func kittyTransmitFrame(enc *png.Encoder, id uint32, img image.Image, gap time.Duration) (string, error) {
	var raw bytes.Buffer
	if err := enc.Encode(&raw, img); err != nil {
		return "", fmt.Errorf("encode kitty animation frame: %w", err)
	}
	payload := make([]byte, base64.StdEncoding.EncodedLen(raw.Len()))
	base64.StdEncoding.Encode(payload, raw.Bytes())
	ms := gap.Milliseconds()
	if ms <= 0 {
		ms = 1
	}
	// f=<PNG> matters: the default format is raw RGBA (f=32), which the terminal
	// can only make sense of with explicit s=/v= pixel dimensions — omitting the
	// format here (as an earlier version of this code did) makes the terminal try
	// to parse our PNG bytes as headerless raw pixels and reject the frame with
	// "EINVAL: dimensions required".
	opts := []string{"a=f", fmt.Sprintf("i=%d", id), fmt.Sprintf("f=%d", kitty.PNG), "X=1", fmt.Sprintf("z=%d", ms)}
	return kittyChunkRaw(payload, opts, 2), nil
}

// kittySetRootGap sets the root frame's (frame 1's) own display duration. The
// root frame is created by a plain a=T transmit, whose z key means something
// else entirely (z-index), so this is the only way to give it a gap — see the
// protocol's Animation section: "the first frame or root frame ... has no
// gap, so its gap must be set using this control code." q=2 suppresses the
// OK/error replies, matching every other APC in this package.
func kittySetRootGap(id uint32, gap time.Duration) string {
	ms := gap.Milliseconds()
	if ms <= 0 {
		ms = 1
	}
	return ansi.KittyGraphics(nil, "a=a", fmt.Sprintf("i=%d", id), "r=1", fmt.Sprintf("z=%d", ms), "q=2")
}

// kittyAnimateStart tells the terminal to run id's animation itself: s=3 loops
// normally (wrapping back to frame 1 after the last), v=1 loops forever. Real
// GIFs carry their own loop count (image/gif's GIF.LoopCount), but the manual
// path already ignores it and always wraps forever — see emojiImages/
// inlineImages.advanceFrame — so matching that rather than plumbing the real
// count through is what keeps the two paths behaving alike.
//
// There is deliberately no matching "stop when scrolled off screen": the whole
// point of the native path is that the terminal, not this app, owns the
// animation loop from here on, and the manual path's off-screen bookkeeping
// exists only to spare a client-side tick this path doesn't have. An image
// stops precisely when it is freed — kittyDelete removes every frame and the
// animation state with it — via the same eviction/collapse caps the manual
// path already enforces (see inlineImages.evictResidentLocked and friends).
func kittyAnimateStart(id uint32) string {
	return ansi.KittyGraphics(nil, "a=a", fmt.Sprintf("i=%d", id), "s=3", "v=1", "q=2")
}

// buildNativeAnimSetup builds everything an already-transmitted root frame
// needs to become a running native animation: append every later frame
// (frames[1:], a=f) with its own gap (delays[1:]), set the root frame's gap
// (delays[0] — see kittySetRootGap), and start the loop.
//
// Deliberately returned separately from whatever transmitted the root (a plain
// kittyTransmitImage/kittyTransmitWith call), rather than one combined
// root+setup string: every call site transmits the root alone first and sends
// this follow-up as its own message at least one event later (see
// emojiimg.go's deliverEmojiNativeSetup and preview.go's
// deliverPreviewNativeSetup). Bundling the two together — this code's first
// cut — made Kitty warn "missing image for virtual placement, ignoring
// image_id=…": for a many-frame GIF the combined blob is large enough that the
// terminal's own repaint can fire before it has finished parsing it,
// including the small root transmit at the very front. Sending the root alone
// first, small and fast exactly like an ordinary still, then the (possibly
// large) rest of the frames afterward, gives the root a chance to actually
// resolve before anything else competes for the terminal's attention. Inline
// thumbnails get this gap for free, since their still and their later frames
// are already built in genuinely separate events (see buildInlineThumb's
// deferred-frames laziness) — this is that same shape, applied everywhere
// else too.
//
// frames must already be fitted to the placement's exact pixel box (whatever
// that means for the caller — the emoji path transmits at native decoded
// size, inline thumbnails and the preview modal fit via fitFrameToCells):
// every a=f frame paints over the same canvas the root established, so a
// mismatched size would only cover part of it, or leave the frame past its
// own bounds undefined.
func buildNativeAnimSetup(enc *png.Encoder, id uint32, frames []image.Image, delays []time.Duration) (string, error) {
	if len(frames) < 2 || len(frames) != len(delays) {
		return "", fmt.Errorf("native animation needs >=2 frames with matching delays, got %d frames and %d delays", len(frames), len(delays))
	}
	var sb strings.Builder
	for i := 1; i < len(frames); i++ {
		seq, err := kittyTransmitFrame(enc, id, frames[i], delays[i])
		if err != nil {
			return "", err
		}
		sb.WriteString(seq)
	}
	sb.WriteString(kittySetRootGap(id, delays[0]))
	sb.WriteString(kittyAnimateStart(id))
	return sb.String(), nil
}

// --- frames as a double buffer --------------------------------------------
//
// The three below are the animation protocol used for something it was not
// primarily meant for: not a terminal-driven loop, but a place to put pixels
// where they cannot be seen until they are complete. Playback stays stopped
// (every image starts that way, and nothing here ever sends s=), so the frames
// only ever change when a=a,c= says so. The STL viewer drives it — see
// stlState's frame-double-buffering note for why re-transmitting an id strobes
// and this doesn't.

// kittyCreateBlankFrame appends one frame to an already-transmitted image
// (a=f). No c= key, so the new frame starts as background — transparent — rather
// than a copy of another, and the payload is a single transparent pixel: the
// frame is stored at the image's full size regardless, and every pixel of it is
// overwritten by kittyEditFrame before it is ever displayed.
//
// quiet 0 asks for the reply. That reply is the only way to find out whether the
// terminal implements frame edits at all: one that doesn't ignores the APC
// without a word, so silence has to be read as "no" (see applySTLFrameReply).
// Ghostty and kitty both answer OK.
func kittyCreateBlankFrame(id uint32, quiet int) string {
	opts := []string{
		"a=f", fmt.Sprintf("i=%d", id),
		fmt.Sprintf("f=%d", kitty.RGBA), "s=1", "v=1", "X=1",
	}
	if quiet > 0 {
		opts = append(opts, fmt.Sprintf("q=%d", quiet))
	}
	return ansi.KittyGraphics([]byte("AAAAAA=="), opts...) // one RGBA pixel, all zero
}

// kittyShowFrame switches which frame an image displays (a=a with c=<frame>).
// This is the client-driven animation primitive: it moves the current frame and
// nothing else, so the terminal repaints that image once, with a whole frame,
// and then waits to be told again.
func kittyShowFrame(id uint32, frame int) string {
	return ansi.KittyGraphics(nil, "a=a", fmt.Sprintf("i=%d", id), fmt.Sprintf("c=%d", frame), "q=1")
}
