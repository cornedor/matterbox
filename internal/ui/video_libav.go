//go:build video

package ui

import (
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"time"

	"github.com/asticode/go-astiav"
)

// videoBuild is true in this build: decodeImageFrames' video branch and
// videoPlayable are live, and libav is linked in. Needs CGO_ENABLED=1 and the
// ffmpeg dev libraries (pkg-config: libavformat/libavcodec/libavutil/libswscale)
// — see the Makefile's `video` notes.
const videoBuild = true

func init() {
	// libav logs to stderr by default, which would corrupt the TUI's own hold on
	// the terminal. Silence it — a decode failure surfaces through the returned
	// error, which is all a thumbnail needs.
	astiav.SetLogLevel(astiav.LogLevelQuiet)
}

// jxlOK is whether the linked ffmpeg can decode JPEG XL, resolved once. Both
// codec ids are required, not either: a still .jxl arrives as AV_CODEC_ID_JPEGXL
// and an animated one as AV_CODEC_ID_JPEGXL_ANIM, and claiming the format while
// only half of it decodes would reserve thumbnail rows for images that never come.
//
// FindDecoder is a lookup over libavcodec's static decoder list — no context, no
// allocation — so asking at init costs nothing and saves every later caller from
// asking again.
var jxlOK = astiav.FindDecoder(astiav.CodecIDJpegxl) != nil &&
	astiav.FindDecoder(astiav.CodecIDJpegxlAnim) != nil

// jxlDecodable reports whether this binary can decode JPEG XL. True only when
// the system ffmpeg was built --enable-libjxl, which many are not — see
// routesToLibav for why this one format has to be asked at runtime while every
// other libav format is settled by the build tag.
func jxlDecodable() bool { return jxlOK }

// av1OK is whether this ffmpeg can decode AV1 — and so AVIF — in software.
//
// FindDecoder(CodecIDAv1) is the wrong question, and answers yes on builds that
// cannot decode a single .avif: ffmpeg's own "av1" decoder is a hardware
// wrapper, so with no accelerator behind it every packet comes back ENOSYS.
// Software AV1 is always an external library, and either of the two will do.
var av1OK = astiav.FindDecoderByName("libdav1d") != nil ||
	astiav.FindDecoderByName("libaom-av1") != nil

// avifDecodable reports whether this binary can decode AVIF. Same runtime
// question as JPEG XL, for the same reason: the decoder is an optional library
// the build tag says nothing about.
func avifDecodable() bool { return av1OK }

// decodeVideoFrames decodes a short clip into the same []image.Image +
// []time.Duration a GIF produces, so the Kitty native-animation pipeline plays
// it unchanged. It caps the clip hard (see video.go's constants): decimate to
// ~videoTargetFPS, stop at videoMaxFrames / videoMaxDuration, and downscale to
// videoMaxSide. With animate false it returns only the first (poster) frame and
// a nil delays slice — the still-thumbnail case.
//
// libav reads from a path, so the bytes are staged to a temp file (short clips,
// decoded rarely). The container is probed by content, so no extension is needed.
func decodeVideoFrames(raw []byte, animate bool, prof videoProfile) (frames []image.Image, delays []time.Duration, err error) {
	vi, err := openVideoInput(raw)
	if err != nil {
		return nil, nil, err
	}
	defer vi.free()

	step, delay := decimation(vi.vs, prof.targetFPS)
	dst := &videoDecoder{animate: animate, step: step, delay: delay, prof: prof, scaler: videoScaler{maxSide: prof.maxSide}}
	defer dst.close()

	pkt := astiav.AllocPacket()
	defer pkt.Free()
	frame := astiav.AllocFrame()
	defer frame.Free()

	for {
		if err := vi.fc.ReadFrame(pkt); err != nil {
			if errors.Is(err, astiav.ErrEof) {
				break
			}
			return nil, nil, fmt.Errorf("video: read frame: %w", err)
		}
		if pkt.StreamIndex() != vi.vs.Index() {
			pkt.Unref()
			continue
		}
		done, err := dst.feed(vi.cc, pkt, frame)
		pkt.Unref()
		if err != nil {
			return nil, nil, err
		}
		if done {
			return dst.frames, dst.delays, nil
		}
	}
	// Flush the decoder's buffered frames.
	if _, err := dst.feed(vi.cc, nil, frame); err != nil {
		return nil, nil, err
	}
	if len(dst.frames) == 0 {
		return nil, nil, errVideoUnsupported
	}
	return dst.frames, dst.delays, nil
}

// videoInput bundles an opened container + its video stream's decoder, staged
// from a temp file (libav reads a path). free() releases all of it, including
// the temp file. Shared by the one-shot decode and the streaming decoder.
type videoInput struct {
	path string
	fc   *astiav.FormatContext
	cc   *astiav.CodecContext
	vs   *astiav.Stream
}

func openVideoInput(raw []byte) (vi *videoInput, err error) {
	path, _, err := writeTempVideo(raw)
	if err != nil {
		return nil, err
	}
	// Anything that fails past here must still drop the temp file + any libav
	// handle already allocated, so build up a videoInput and free it on error
	// (vi.free removes vi.path — the cleanup closure is not needed here).
	vi = &videoInput{path: path}
	defer func() {
		if err != nil {
			vi.free()
			vi = nil
		}
	}()

	vi.fc = astiav.AllocFormatContext()
	if vi.fc == nil {
		return vi, errors.New("video: alloc format context failed")
	}
	if err = vi.fc.OpenInput(path, nil, nil); err != nil {
		return vi, fmt.Errorf("video: open input: %w", err)
	}
	if err = vi.fc.FindStreamInfo(nil); err != nil {
		return vi, fmt.Errorf("video: find stream info: %w", err)
	}
	vi.vs = pickVideoStream(vi.fc.Streams())
	if vi.vs == nil {
		return vi, errVideoUnsupported
	}
	dec := astiav.FindDecoder(vi.vs.CodecParameters().CodecID())
	if dec == nil {
		return vi, errVideoUnsupported
	}
	if vi.cc = astiav.AllocCodecContext(dec); vi.cc == nil {
		return vi, errors.New("video: alloc codec context failed")
	}
	if err = vi.vs.CodecParameters().ToCodecContext(vi.cc); err != nil {
		return vi, fmt.Errorf("video: init codec context: %w", err)
	}
	if err = vi.cc.Open(dec, nil); err != nil {
		return vi, fmt.Errorf("video: open codec: %w", err)
	}
	return vi, nil
}

// pickVideoStream chooses which video stream to decode: the first one that has
// more than one frame, falling back to the first video stream of any kind.
//
// Not simply "the first video stream", because an animated AVIF is written as a
// one-frame still item *followed by* the animated sequence — so taking the first
// would show a single frame of a file that has motion in it. The same shape turns
// up in an mp4 carrying cover art ahead of the real track.
//
// The fallback is what keeps this safe. nb_frames is unknown (0) in plenty of
// containers — a webm, a fragmented mp4 — and a genuinely still image has exactly
// one frame, so in both of those cases the loop finds nothing and we take the
// first video stream, which is what the code did before and what those files want.
// The preference can therefore only ever move us off a stream that was provably a
// single frame.
func pickVideoStream(streams []*astiav.Stream) *astiav.Stream {
	var first *astiav.Stream
	for _, s := range streams {
		if s.CodecParameters().MediaType() != astiav.MediaTypeVideo {
			continue
		}
		if first == nil {
			first = s
		}
		if s.NbFrames() > 1 {
			return s
		}
	}
	return first
}

func (vi *videoInput) free() {
	if vi == nil {
		return
	}
	if vi.cc != nil {
		vi.cc.Free()
		vi.cc = nil
	}
	if vi.fc != nil {
		vi.fc.CloseInput()
		vi.fc.Free()
		vi.fc = nil
	}
	if vi.path != "" {
		os.Remove(vi.path)
		vi.path = ""
	}
}

// decimation returns the frame step (keep every step-th source frame to
// approximate targetFPS) and the display delay each kept frame stands in for.
//
// Thinning is skipped entirely when the container does not report a real average
// frame rate. That is not a corner case: an animated-image demuxer (jpegxl_anim,
// and the same shape elsewhere) leaves avg_frame_rate at 0/0 and exposes only its
// timebase as r_frame_rate — 100Hz for JPEG XL — which is a unit, not a cadence.
// Decimating from it computed step=7 on a five-frame animation and kept exactly
// one frame, turning every animated JXL into a still. When the rate is unknown
// there is nothing to thin *from*, and an image container holds a handful of
// frames anyway, so keeping all of them is both correct and cheap; the profile's
// frame ceiling is still the backstop.
func decimation(vs *astiav.Stream, targetFPS float64) (step int, delay time.Duration) {
	srcFPS, known := sourceFrameRate(vs)
	if !known {
		return 1, time.Second / time.Duration(targetFPS)
	}
	step = int(math.Round(srcFPS / targetFPS))
	if step < 1 {
		step = 1
	}
	delay = time.Duration(float64(step) / srcFPS * float64(time.Second))
	if delay <= 0 {
		delay = time.Second / time.Duration(targetFPS)
	}
	return step, delay
}

// videoDecoder accumulates the kept, scaled frames of one clip. The swscale
// context is built lazily from the first decoded frame's real size and pixel
// format, then reused for every frame (they share those attributes).
type videoDecoder struct {
	animate bool
	step    int
	delay   time.Duration
	prof    videoProfile
	scaler  videoScaler

	seen   int // source video frames seen (for decimation)
	frames []image.Image
	delays []time.Duration
}

// feed sends one packet (or nil to flush) and drains every frame it yields. It
// returns done=true once the caps are hit so the caller can stop reading early.
func (d *videoDecoder) feed(cc *astiav.CodecContext, pkt *astiav.Packet, frame *astiav.Frame) (done bool, err error) {
	if err := cc.SendPacket(pkt); err != nil {
		// EAGAIN here just means "drain first"; anything else is fatal.
		if !errors.Is(err, astiav.ErrEagain) {
			return false, fmt.Errorf("video: send packet: %w", err)
		}
	}
	for {
		if err := cc.ReceiveFrame(frame); err != nil {
			if errors.Is(err, astiav.ErrEof) || errors.Is(err, astiav.ErrEagain) {
				return false, nil
			}
			return false, fmt.Errorf("video: receive frame: %w", err)
		}
		keep := d.seen%d.step == 0
		d.seen++
		if !keep {
			frame.Unref()
			continue
		}
		img, err := d.scale(frame)
		frame.Unref()
		if err != nil {
			return false, err
		}
		d.frames = append(d.frames, img)
		if !d.animate {
			return true, nil // poster frame only
		}
		d.delays = append(d.delays, d.delay)
		if len(d.frames) >= d.prof.maxFrames ||
			time.Duration(len(d.frames))*d.delay >= d.prof.maxDuration {
			return true, nil
		}
	}
}

func (d *videoDecoder) scale(frame *astiav.Frame) (image.Image, error) {
	return d.scaler.scale(frame)
}

func (d *videoDecoder) close() { d.scaler.free() }

// videoScaler lazily builds one swscale context (from the first frame's real
// size + pixel format, capped to maxSide) and reuses it to turn every later
// frame — which shares those attributes — into an RGBA image.Image. Shared by
// the one-shot decoder and the streaming one.
type videoScaler struct {
	maxSide    int
	sws        *astiav.SoftwareScaleContext
	dstW, dstH int
}

func (s *videoScaler) scale(frame *astiav.Frame) (image.Image, error) {
	if s.sws == nil {
		s.dstW, s.dstH = fitVideoSize(frame.Width(), frame.Height(), s.maxSide)
		sws, err := astiav.CreateSoftwareScaleContext(
			frame.Width(), frame.Height(), frame.PixelFormat(),
			s.dstW, s.dstH, astiav.PixelFormatRgba,
			astiav.NewSoftwareScaleContextFlags(astiav.SoftwareScaleContextFlagBilinear),
		)
		if err != nil {
			return nil, fmt.Errorf("video: scale context: %w", err)
		}
		s.sws = sws
	}
	out := astiav.AllocFrame()
	defer out.Free()
	out.SetWidth(s.dstW)
	out.SetHeight(s.dstH)
	out.SetPixelFormat(astiav.PixelFormatRgba)
	if err := out.AllocBuffer(1); err != nil {
		return nil, fmt.Errorf("video: alloc scaled buffer: %w", err)
	}
	if err := s.sws.ScaleFrame(frame, out); err != nil {
		return nil, fmt.Errorf("video: scale frame: %w", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, s.dstW, s.dstH))
	if err := out.Data().ToImage(img); err != nil {
		return nil, fmt.Errorf("video: frame to image: %w", err)
	}
	return img, nil
}

func (s *videoScaler) free() {
	if s.sws != nil {
		s.sws.Free()
		s.sws = nil
	}
}

// sourceFrameRate reports the stream's average frame rate and whether it is a
// real cadence at all.
//
// Only avg_frame_rate counts as known. r_frame_rate is the smallest unit that can
// express every timestamp in the stream, which for a real video happens to equal
// the frame rate but for an animated-image container is just its timebase — see
// decimation for what trusting it did to a five-frame JPEG XL.
func sourceFrameRate(s *astiav.Stream) (float64, bool) {
	if r := s.AvgFrameRate().Float64(); r > 0 {
		return r, true
	}
	return 0, false
}

// fitVideoSize scales w×h down so the longest edge is at most maxSide, never up,
// rounding each edge to an even number (some pixel formats need it) and never
// below 2.
func fitVideoSize(w, h, maxSide int) (int, int) {
	if w <= 0 || h <= 0 {
		return maxSide, maxSide
	}
	longest := w
	if h > longest {
		longest = h
	}
	if longest > maxSide {
		scale := float64(maxSide) / float64(longest)
		w = int(math.Round(float64(w) * scale))
		h = int(math.Round(float64(h) * scale))
	}
	return evenAtLeast2(w), evenAtLeast2(h)
}

func evenAtLeast2(v int) int {
	if v < 2 {
		return 2
	}
	return v &^ 1
}

// writeTempVideo stages the clip bytes to a temp file for libav's path-based
// input. The returned cleanup removes it.
func writeTempVideo(raw []byte) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "matterbox-vid-*")
	if err != nil {
		return "", nil, fmt.Errorf("video: temp file: %w", err)
	}
	name := f.Name()
	cleanup = func() { os.Remove(name) }
	if _, err := f.Write(raw); err != nil {
		f.Close()
		cleanup()
		return "", nil, fmt.Errorf("video: write temp: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("video: close temp: %w", err)
	}
	return name, cleanup, nil
}

// libavVideoStream is the streaming decoder behind the space-to-preview modal:
// it keeps one container + decoder open and yields the next batch of frames on
// each nextChunk, resuming exactly where the last call stopped (the fc/cc hold
// their position; seen carries the decimation phase across calls). This is what
// lets the preview start playing a long clip after ~one chunk instead of
// decoding the whole thing, and hold only a bounded buffer of frames — see
// preview.go's streaming player. It implements the videoStream interface.
type libavVideoStream struct {
	vi     *videoInput
	scaler videoScaler
	step   int
	delay  time.Duration
	pkt    *astiav.Packet
	frame  *astiav.Frame
	seen   int
	eof    bool
}

// openVideoStream opens raw for streaming playback at the profile's fps/maxSide.
// The whole clip is available (no total frame/duration cap — nextChunk pulls it
// a piece at a time), so only targetFPS and maxSide from prof are used.
func openVideoStream(raw []byte, prof videoProfile) (videoStream, error) {
	vi, err := openVideoInput(raw)
	if err != nil {
		return nil, err
	}
	step, delay := decimation(vi.vs, prof.targetFPS)
	return &libavVideoStream{
		vi:     vi,
		scaler: videoScaler{maxSide: prof.maxSide},
		step:   step,
		delay:  delay,
		pkt:    astiav.AllocPacket(),
		frame:  astiav.AllocFrame(),
	}, nil
}

// nextChunk decodes forward until it has max kept frames or the clip ends,
// returning the frames, their (uniform) delays, and whether the stream is now
// exhausted. A subsequent call continues from here.
func (s *libavVideoStream) nextChunk(max int) (frames []image.Image, delays []time.Duration, eof bool, err error) {
	if s.eof {
		return nil, nil, true, nil
	}
	if max < 1 {
		max = 1
	}
	take := func(f *astiav.Frame) error {
		keep := s.seen%s.step == 0
		s.seen++
		if !keep {
			return nil
		}
		img, err := s.scaler.scale(f)
		if err != nil {
			return err
		}
		frames = append(frames, img)
		delays = append(delays, s.delay)
		return nil
	}
	for len(frames) < max {
		rerr := s.vi.fc.ReadFrame(s.pkt)
		if rerr != nil {
			if !errors.Is(rerr, astiav.ErrEof) {
				return frames, delays, false, fmt.Errorf("video: read frame: %w", rerr)
			}
			// End of file: flush the decoder's buffered frames, then done.
			if derr := s.drain(nil, take); derr != nil {
				return frames, delays, false, derr
			}
			s.eof = true
			return frames, delays, true, nil
		}
		if s.pkt.StreamIndex() != s.vi.vs.Index() {
			s.pkt.Unref()
			continue
		}
		derr := s.drain(s.pkt, take)
		s.pkt.Unref()
		if derr != nil {
			return frames, delays, false, derr
		}
	}
	return frames, delays, false, nil
}

// drain sends one packet (nil to flush) and hands every frame it yields to take.
func (s *libavVideoStream) drain(pkt *astiav.Packet, take func(*astiav.Frame) error) error {
	if err := s.vi.cc.SendPacket(pkt); err != nil && !errors.Is(err, astiav.ErrEagain) {
		return fmt.Errorf("video: send packet: %w", err)
	}
	for {
		if err := s.vi.cc.ReceiveFrame(s.frame); err != nil {
			if errors.Is(err, astiav.ErrEof) || errors.Is(err, astiav.ErrEagain) {
				return nil
			}
			return fmt.Errorf("video: receive frame: %w", err)
		}
		err := take(s.frame)
		s.frame.Unref()
		if err != nil {
			return err
		}
	}
}

func (s *libavVideoStream) close() {
	if s.pkt != nil {
		s.pkt.Free()
		s.pkt = nil
	}
	if s.frame != nil {
		s.frame.Free()
		s.frame = nil
	}
	s.scaler.free()
	s.vi.free()
}
