//go:build !demoaudio

package welcome

// The `--demo` soundtrack pulls in oto + a tracker synth, which need cgo and
// system audio libraries (pkg-config/ALSA on Linux). That's a heavy dependency
// for a nice-to-have, so it's gated behind the `demoaudio` build tag. Without
// the tag the demo still runs — it just plays silently. Build with
// `-tags demoaudio` to compile in the soundtrack.

// StartDemoMusic is a no-op without the demoaudio build tag; see music.go.
func StartDemoMusic() func() { return func() {} }

// musicLevel reports no playback level without the demoaudio build tag, so the
// reactive mountains stay still.
func musicLevel() float64 { return 0 }
