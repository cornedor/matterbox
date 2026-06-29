//go:build linux

package welcome

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"io"
	"sync"

	"github.com/gotracker/playback/format"
	"github.com/gotracker/playback/mixing"
	"github.com/gotracker/playback/mixing/sampling"
	"github.com/gotracker/playback/output"
	"github.com/gotracker/playback/player/feature"
	"github.com/gotracker/playback/player/machine"
	"github.com/gotracker/playback/player/machine/settings"
	"github.com/gotracker/playback/player/sampler"
	"github.com/gotracker/playback/song"
	"github.com/jfreymuth/pulse"
	"github.com/jfreymuth/pulse/proto"
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
	demoSampleRate = 44100
	demoChannels   = 2
	demoStereoSep  = 0.5
)

// Buffering. Audio plays on a background goroutine that competes with the TUI's
// per-frame vaporwave render (and the GC churn it drives), so the producer can be
// scheduled late or pulled into GC mark-assist for tens of milliseconds at a
// time. The cure is slack, not thread priority: render well ahead of the device.
//
//   - pcmLookahead: how many rendered chunks (one tracker tick each, ~20ms) the
//     player may queue ahead of PulseAudio. ~128 ≈ a couple of seconds, enough
//     that a render/GC spike can't drain the queue before the producer catches
//     back up. The producer fills this once at startup, then blocks on it, so it
//     also self-paces to wall-clock time.
//   - demoLatency: PulseAudio's own buffer. This covers the other failure mode —
//     the whole process briefly paused (GC stop-the-world) — by giving the audio
//     server a cushion to play from while we're stalled.
const (
	pcmLookahead = 128
	demoLatency  = 0.25
)

// StartDemoMusic decodes the embedded tracker module and plays it through
// PulseAudio on a background goroutine, looping until the returned stop function
// is called. It never blocks the caller and never disturbs the TUI: with no
// audio server (headless, CI, no PulseAudio) playback just fails silently and
// stop is a no-op. Linux only — other platforms get the stub in music_other.go.
func StartDemoMusic() func() {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Errors (incl. "no audio server") are intentionally swallowed: the demo
		// soundtrack is a nice-to-have and must never break the wizard.
		_ = playDemo(ctx)
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

// playDemo runs the tracker machine and feeds its audio to PulseAudio until ctx
// is cancelled. It returns when the context is done or the audio device fails.
func playDemo(ctx context.Context) error {
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
	// pushes them here; the PulseAudio stream pulls at the real playback rate,
	// which back-pressures the player so it advances in wall-clock time without a
	// manual clock. The deep buffer (pcmLookahead) is what keeps it glitch-free
	// while the TUI hogs the CPU — see the const block.
	pcm := make(chan []byte, pcmLookahead)
	mix := mixing.Mixer{Channels: demoChannels}
	out := sampler.NewSampler(demoSampleRate, demoChannels, demoStereoSep, func(premix *output.PremixData) {
		data := mix.Flatten(premix.SamplesLen, premix.Data, premix.MixerVolume, sampling.Format16BitLESigned)
		select {
		case pcm <- data:
		case <-ctx.Done():
		}
	})

	// PulseAudio sink.
	client, err := pulse.NewClient(pulse.ClientApplicationName("matterbox"))
	if err != nil {
		return err
	}
	defer client.Close()

	src := &pcmReader{ctx: ctx, ch: pcm}
	stream, err := client.NewPlayback(
		pulse.NewReader(src, proto.FormatInt16LE),
		pulse.PlaybackSampleRate(demoSampleRate),
		pulse.PlaybackChannels(proto.ChannelMap{proto.ChannelLeft, proto.ChannelRight}),
		pulse.PlaybackLatency(demoLatency),
	)
	if err != nil {
		return err
	}
	defer stream.Close()

	// Prime with silence so the stream doesn't underrun before the first tick is
	// rendered, then start pulling.
	src.buf.Write(make([]byte, stream.BufferSizeBytes()))
	stream.Start()
	defer stream.Stop()

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

// pcmReader bridges the player's PCM channel to PulseAudio's pull-based reader.
// PulseAudio asks for fixed-size chunks; we accumulate whole frames and hand back
// exactly what's requested, blocking on the channel (which paces the player)
// until ctx is cancelled, after which Read drains the buffer and reports EOF so
// the stream stops cleanly.
type pcmReader struct {
	ctx context.Context
	ch  <-chan []byte
	buf bytes.Buffer
}

func (r *pcmReader) Read(p []byte) (int, error) {
	for r.buf.Len() < len(p) {
		select {
		case <-r.ctx.Done():
			if r.buf.Len() > 0 {
				return r.buf.Read(p)
			}
			return 0, io.EOF
		case data, ok := <-r.ch:
			if !ok {
				return 0, io.EOF
			}
			r.buf.Write(data)
		}
	}
	return r.buf.Read(p)
}
