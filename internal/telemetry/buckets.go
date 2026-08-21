package telemetry

import (
	"strconv"
	"time"
)

// Bucketing exists because a raw number can identify a person and a bucket
// can't. "sent a 4,193-character message at 14:02" is close to a fingerprint;
// "sent a 2000+ message" is a fact about the product. Every numeric property
// that describes user *content* or *behaviour* therefore ships as one of the
// labels below rather than as an integer.
//
// The labels are also closed sets, which is what lets the catalogue whitelist
// them (see PropSpec.Values): a bucket function can only ever return a value
// the catalogue already declares, so a bucketed property cannot leak a
// surprise. Counts of internal things that are not user content — how many
// keybindings were overridden, how many rules a daemon loaded — stay exact,
// because there is nothing personal in them.
//
// Boundaries are deliberately coarse at the top end: the difference between
// 200 and 5,000 loaded posts matters for a perf question, the difference
// between 4,193 and 4,194 characters never matters for anything.

// CountBuckets is the label set returned by Count: generic "how many of these
// were there" for user-facing collections (posts, results, mentions, replies).
var CountBuckets = []string{"0", "1", "2-5", "6-20", "21-100", "101-1000", "1000+"}

// Count buckets a non-negative count. A negative input is treated as 0 rather
// than rejected: the caller is reporting a count and a negative one is a bug
// upstream, not something worth dropping an event over.
func Count(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n == 1:
		return "1"
	case n <= 5:
		return "2-5"
	case n <= 20:
		return "6-20"
	case n <= 100:
		return "21-100"
	case n <= 1000:
		return "101-1000"
	default:
		return "1000+"
	}
}

// LengthBuckets is the label set returned by Length.
var LengthBuckets = []string{"0", "1-40", "41-160", "161-500", "501-2000", "2000+"}

// Length buckets a text length in characters. Used for composed messages,
// search queries and edits — never alongside the text itself, which never
// leaves the machine. The boundaries track how a message reads rather than
// anything technical: a line, a paragraph, a screenful, an essay.
func Length(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n <= 40:
		return "1-40"
	case n <= 160:
		return "41-160"
	case n <= 500:
		return "161-500"
	case n <= 2000:
		return "501-2000"
	default:
		return "2000+"
	}
}

// MillisBuckets is the label set returned by Millis and Duration.
var MillisBuckets = []string{"<1ms", "1-10ms", "10-50ms", "50-200ms", "200ms-1s", "1-5s", "5-30s", "30s+"}

// Millis buckets a latency in milliseconds. The low boundaries are tight
// because they are where the interesting answers live: a render is fine at
// 10ms, visibly laggy at 50ms and broken at 200ms, so those need to be
// distinguishable. Anything past a few seconds is uniformly "too slow" and
// gets lumped together.
func Millis(ms int64) string {
	switch {
	case ms < 1:
		return "<1ms"
	case ms < 10:
		return "1-10ms"
	case ms < 50:
		return "10-50ms"
	case ms < 200:
		return "50-200ms"
	case ms < 1000:
		return "200ms-1s"
	case ms < 5000:
		return "1-5s"
	case ms < 30000:
		return "5-30s"
	default:
		return "30s+"
	}
}

// Duration buckets a time.Duration through Millis. Convenience for the many
// call sites holding a duration rather than a millisecond count.
func Duration(d time.Duration) string { return Millis(d.Milliseconds()) }

// SecondsBuckets is the label set returned by Seconds.
var SecondsBuckets = []string{"<5s", "5-30s", "30s-2m", "2-10m", "10-60m", "1-4h", "4h+"}

// Seconds buckets a span in seconds — how long a session lasted, how long a
// picker stayed open, how old a message was when it got edited. Coarser than
// Millis because these are human-scale spans where nobody needs 10ms
// resolution.
func Seconds(sec int64) string {
	switch {
	case sec < 5:
		return "<5s"
	case sec < 30:
		return "5-30s"
	case sec < 120:
		return "30s-2m"
	case sec < 600:
		return "2-10m"
	case sec < 3600:
		return "10-60m"
	case sec < 4*3600:
		return "1-4h"
	default:
		return "4h+"
	}
}

// SinceSeconds buckets the span between t and now. Zero times bucket as "<5s"
// rather than an absurd span: an unset timestamp means the caller had nothing
// to measure, and inventing "4h+" from it would poison the data.
func SinceSeconds(t time.Time) string {
	if t.IsZero() {
		return "<5s"
	}
	return Seconds(int64(time.Since(t).Seconds()))
}

// ColsBuckets is the label set returned by Cols.
var ColsBuckets = []string{"<80", "80-119", "120-159", "160-199", "200-279", "280+"}

// Cols buckets a terminal width. This is the single most useful environment
// fact for a TUI: it decides whether the three-pane layout fits, whether
// markdown tables have room, and whether a feature was tested at the size
// people actually run. The boundaries are the layout's own breakpoints, not
// round numbers.
func Cols(n int) string {
	switch {
	case n < 80:
		return "<80"
	case n < 120:
		return "80-119"
	case n < 160:
		return "120-159"
	case n < 200:
		return "160-199"
	case n < 280:
		return "200-279"
	default:
		return "280+"
	}
}

// RowsBuckets is the label set returned by Rows.
var RowsBuckets = []string{"<24", "24-39", "40-59", "60-99", "100+"}

// Rows buckets a terminal height, for the same reason as Cols: how many
// messages fit on screen changes how the app is used, and a 24-row terminal is
// a different product from a 60-row one.
func Rows(n int) string {
	switch {
	case n < 24:
		return "<24"
	case n < 40:
		return "24-39"
	case n < 60:
		return "40-59"
	case n < 100:
		return "60-99"
	default:
		return "100+"
	}
}

// BytesBuckets is the label set returned by Bytes.
var BytesBuckets = []string{"<64KB", "64KB-1MB", "1-10MB", "10-100MB", "100MB+"}

// Bytes buckets a file size, for attachments and downloads: enough to tell a
// pasted screenshot from a video upload, which is what decides whether the
// attachment path needs a progress indicator.
func Bytes(n int64) string {
	switch {
	case n < 64<<10:
		return "<64KB"
	case n < 1<<20:
		return "64KB-1MB"
	case n < 10<<20:
		return "1-10MB"
	case n < 100<<20:
		return "10-100MB"
	default:
		return "100MB+"
	}
}

// RankBuckets is the label set returned by Rank.
var RankBuckets = []string{"1", "2-3", "4-10", "11-50", "50+"}

// Rank buckets a 1-based position in a result list — which search hit got
// opened, which switcher row got picked. The point is to answer "is the top
// result the right one?", so position 1 is its own bucket and the tail is not.
func Rank(n int) string {
	switch {
	case n <= 1:
		return "1"
	case n <= 3:
		return "2-3"
	case n <= 10:
		return "4-10"
	case n <= 50:
		return "11-50"
	default:
		return "50+"
	}
}

// Exact renders a count that is safe to send precisely, for the catalogue's
// KindCount properties. It exists so call sites read the same whether a number
// is bucketed or not — Exact(n) next to Count(n) makes the choice visible at
// the call site instead of hiding it in the property's type.
func Exact(n int) string { return strconv.Itoa(n) }
