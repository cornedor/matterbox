//go:build !linux

package welcome

// StartDemoMusic is a no-op on platforms without the pure-Go PulseAudio backend.
// The `--demo` visual animation still runs; only the soundtrack is skipped.
func StartDemoMusic() func() { return func() {} }

// musicLevel is always 0 without a soundtrack, so the mountains stay static.
func musicLevel() float64 { return 0 }
