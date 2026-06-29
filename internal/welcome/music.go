//go:build demoaudio

package welcome

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"io"
	"math"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
	"github.com/gotracker/playback/format"
	"github.com/gotracker/playback/mixing"
	"github.com/gotracker/playback/mixing/sampling"
	"github.com/gotracker/playback/output"
	"github.com/gotracker/playback/player/feature"
	"github.com/gotracker/playback/player/machine"
	"github.com/gotracker/playback/player/machine/settings"
	"github.com/gotracker/playback/player/sampler"
	"github.com/gotracker/playback/song"
)

// demoModule is the FastTracker II module played behind the `--demo` intro:
// "Paradox #3" by dubmood / Razor1911. It's a tracker file, so we decode and
// synthesise it at runtime rather than shipping a multi-megabyte recording.
//
//go:embed demo.xm
var demoModule []byte

// Output format. These match gotracker's defaults so playback sounds the same as
// the standalone player: 16-bit little-endian stereo at 44.1kHz, 50% separation.
const (
	demoSampleRate    = 44100
	demoChannels      = 2
	demoStereoSep     = 0.5
	demoBytesPerFrame = demoChannels * 2 // 16-bit samples
)

// Buffering. Audio plays on a background goroutine that competes with the TUI's
// per-frame vaporwave render (and the GC churn it drives), so the producer can be
// scheduled late or pulled into GC mark-assist for tens of milliseconds at a
// time. The cure is slack, not thread priority: render well ahead of the device.
//
//   - pcmLookahead: how many rendered chunks (one tracker tick each, ~20ms) the
//     player may queue ahead of the audio device. ~128 ≈ a couple of seconds,
//     enough that a render/GC spike can't drain the queue before the producer
//     catches back up. The producer fills this once at startup, then blocks on
//     it, so it also self-paces to wall-clock time.
//   - demoDeviceBuffer: oto's own device buffer. This covers the other failure
//     mode — the whole process briefly paused (GC stop-the-world) — by giving the
//     audio backend a cushion to play from while we're stalled.
//   - playerBufferBytes: oto's per-player read-ahead. oto tops this up from our
//     PCM channel on its mux goroutine, then the device drains it. We prime it
//     with silence before Play() so oto's initial synchronous fill completes
//     instead of busy-spinning on a not-yet-producing source.
const (
	pcmLookahead      = 128
	demoDeviceBuffer  = 250 * time.Millisecond
	playerBufferBytes = demoSampleRate * demoBytesPerFrame / 4 // ~0.25s
)

// levelTap is the thread-safe bridge between playback and the audio-reactive
// mountains. It can't just stash a single RMS value per read like a naive meter
// would: oto pulls audio in big latency-sized chunks (~250ms, a few times a
// second), so an RMS over each chunk would average the beat flat and refresh far
// slower than the render loop. Instead the audio goroutine feeds served samples
// into a short ring, and the render goroutine reads a brief (~25ms) RMS window at
// a cursor that it advances by wall-clock between chunks — so the level stays
// punchy and updates every frame regardless of how coarsely oto reads.
const (
	levelSamplesPerSec = demoSampleRate * demoChannels // interleaved samples/sec
	levelWindow        = levelSamplesPerSec / 40       // ~25ms RMS window
	levelLead          = levelSamplesPerSec / 4        // ~250ms: align mountains to sound
	levelRing          = levelSamplesPerSec * 3 / 2    // ~1.5s of headroom
)

type levelTap struct {
	mu        sync.Mutex
	ring      [levelRing]int16
	total     int64     // total interleaved samples ever appended
	lastWrite time.Time // wall clock of the last append, for cursor advance
	playing   bool
}

// append copies a just-served PCM chunk (16-bit LE) into the ring and timestamps
// it. Called on oto's mux goroutine.
func (t *levelTap) append(b []byte) {
	t.mu.Lock()
	w := t.total
	n := int64(len(t.ring))
	for i := 0; i+1 < len(b); i += 2 {
		t.ring[w%n] = int16(uint16(b[i]) | uint16(b[i+1])<<8)
		w++
	}
	t.total = w
	t.lastWrite = time.Now()
	t.playing = true
	t.mu.Unlock()
}

// level returns the RMS amplitude (0..1) of the ~25ms of audio playing now,
// where "now" is the last-served position minus the downstream buffer, advanced
// by the wall-clock time elapsed since the last chunk arrived. Called on the
// render goroutine, once per frame.
func (t *levelTap) level() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.playing || t.total == 0 {
		return 0
	}
	advance := int64(time.Since(t.lastWrite).Seconds() * float64(levelSamplesPerSec))
	cursor := t.total - levelLead + advance
	if cursor > t.total {
		cursor = t.total
	}
	start := cursor - levelWindow
	if oldest := t.total - int64(len(t.ring)); start < oldest {
		start = oldest
	}
	if start < 0 {
		start = 0
	}
	if start >= cursor {
		return 0
	}
	n := int64(len(t.ring))
	var sum float64
	for a := start; a < cursor; a++ {
		s := float64(t.ring[a%n])
		sum += s * s
	}
	return math.Sqrt(sum/float64(cursor-start)) / 32768.0
}

func (t *levelTap) stop() {
	t.mu.Lock()
	t.playing = false
	t.mu.Unlock()
}

// demoLevel is the live playback level of the demo soundtrack. It's package
// state because there is only ever one wizard (so one player) at a time, and it
// lets the renderer read the level without threading a handle through New.
var demoLevel levelTap

// musicLevel returns the demo playback level (0..1 RMS amplitude) of the audio
// playing right now, or 0 when nothing is playing.
func musicLevel() float64 { return demoLevel.level() }

// oto's audio context is a process-wide singleton (creating a second one is an
// error, and it can't be closed and recreated), so we build it lazily on first
// use and reuse it. It then holds the audio device open for the rest of the
// session — fine, since the wizard is the only thing that ever plays sound.
var (
	otoOnce sync.Once
	otoCtx  *oto.Context
	otoErr  error
)

func sharedOtoContext() (*oto.Context, error) {
	otoOnce.Do(func() {
		ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
			SampleRate:   demoSampleRate,
			ChannelCount: demoChannels,
			Format:       oto.FormatSignedInt16LE,
			BufferSize:   demoDeviceBuffer,
		})
		if err != nil {
			otoErr = err
			return
		}
		<-ready // block until the device is ready to accept players
		otoCtx = ctx
	})
	return otoCtx, otoErr
}

// StartDemoMusic decodes the embedded tracker module and plays it through the
// system audio device (CoreAudio on macOS, PulseAudio/ALSA on Linux, WASAPI on
// Windows) on a background goroutine, looping until the returned stop function is
// called. It never blocks the caller and never disturbs the TUI: with no usable
// audio device (headless, CI) playback just fails silently and stop is a no-op.
func StartDemoMusic() func() {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Errors (incl. "no audio device") are intentionally swallowed: the demo
		// soundtrack is a nice-to-have and must never break the wizard.
		_ = playDemo(ctx)
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

// playDemo runs the tracker machine and feeds its audio to the device until ctx
// is cancelled. It returns when the context is done or the audio device fails.
func playDemo(ctx context.Context) error {
	defer demoLevel.stop() // settle the reactive mountains when playback stops

	// No SongLoop feature: the machine plays the module once and signals
	// ErrStopSong at the end, where the loop below rebuilds it — so the demo
	// repeats for as long as the wizard is open.
	songData, songFmt, err := format.LoadFromReader("xm", bytes.NewReader(demoModule))
	if err != nil {
		return err
	}
	var us settings.UserSettings
	us.Reset()
	if err := songFmt.ConvertFeaturesToSettings(&us, []feature.Feature{}); err != nil {
		return err
	}

	// PCM hand-off. The player flattens each rendered tick into 16-bit frames and
	// pushes them here; oto pulls at the real playback rate, which back-pressures
	// the player so it advances in wall-clock time without a manual clock. The
	// deep buffer (pcmLookahead) is what keeps it glitch-free while the TUI hogs
	// the CPU — see the const block.
	pcm := make(chan []byte, pcmLookahead)
	mix := mixing.Mixer{Channels: demoChannels}
	out := sampler.NewSampler(demoSampleRate, demoChannels, demoStereoSep, func(premix *output.PremixData) {
		data := mix.Flatten(premix.SamplesLen, premix.Data, premix.MixerVolume, sampling.Format16BitLESigned)
		select {
		case pcm <- data:
		case <-ctx.Done():
		}
	})

	otoCtx, err := sharedOtoContext()
	if err != nil {
		return err
	}

	src := &pcmReader{ctx: ctx, ch: pcm}
	src.buf.Write(make([]byte, playerBufferBytes)) // prime so Play()'s initial fill doesn't spin
	player := otoCtx.NewPlayer(src)
	player.SetBufferSize(playerBufferBytes)
	player.Play()
	defer player.Pause() // stop the device pulling; the player is dropped after

	for ctx.Err() == nil {
		m, err := machine.NewMachine(songData, us)
		if err != nil {
			return err
		}
		for ctx.Err() == nil {
			if err := m.Advance(); err != nil {
				if errors.Is(err, song.ErrStopSong) {
					break // song ended — rebuild and loop
				}
				return err
			}
			if err := m.Render(out); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

// pcmReader bridges the player's PCM channel to oto's pull-based reader. oto reads
// the source on its mux goroutine under a lock it shares with the real-time device
// callback, so Read must never block: it hands back whatever whole frames are
// ready right now (or none), and oto plays from its own read-ahead buffer until
// more arrive. Once ctx is cancelled and the buffer drains, Read reports EOF so
// the player stops cleanly.
type pcmReader struct {
	ctx context.Context
	ch  <-chan []byte
	buf bytes.Buffer
}

func (r *pcmReader) Read(p []byte) (int, error) {
	if r.ctx.Err() != nil && r.buf.Len() == 0 {
		return 0, io.EOF
	}
	for r.buf.Len() < len(p) {
		select {
		case data, ok := <-r.ch:
			if !ok {
				if r.buf.Len() == 0 {
					return 0, io.EOF
				}
				return r.served(p)
			}
			r.buf.Write(data)
		default:
			// No more PCM ready this instant — don't block oto's reader. Hand back
			// what we have; if that's nothing, (0, nil) tells oto to retry shortly
			// while it plays from its own buffer.
			if r.buf.Len() == 0 {
				return 0, nil
			}
			return r.served(p)
		}
	}
	return r.served(p)
}

// served reads the next chunk to hand oto and publishes its level. This is the
// playback tap: data read here is about to play (within the device buffer), so
// its RMS tracks what's heard far better than the producer side, which renders
// pcmLookahead chunks ahead.
func (r *pcmReader) served(p []byte) (int, error) {
	n, err := r.buf.Read(p)
	if n > 0 {
		demoLevel.append(p[:n])
	}
	return n, err
}
