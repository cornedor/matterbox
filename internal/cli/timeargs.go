package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseTimeArg interprets a CLI time argument as an absolute instant and
// returns it as unix-ms. It is the single parser shared by the date flags on
// `read` and `digest`, so they all accept the same vocabulary. now anchors
// every relative form and loc is the calendar used for day keywords and bare
// dates — both are passed in so callers (and tests) control the clock and
// zone. Accepted forms (case-insensitive for keywords):
//
//	now                  the current instant
//	today / yesterday    midnight (00:00) at the start of that day
//	<N>d / <N>w          that many days / weeks before now (e.g. 7d, 2w)
//	<go duration>        any time.ParseDuration value before now (30m, 2h, 1h30m)
//	2006-01-02           midnight at the start of that calendar day
//	2006-01-02 15:04     that local date and time
//	RFC3339              that exact instant (e.g. 2026-06-08T09:30:00Z)
func parseTimeArg(s string, now time.Time, loc *time.Location) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty time")
	}
	switch strings.ToLower(s) {
	case "now":
		return now.UnixMilli(), nil
	case "today":
		return startOfDay(now, loc).UnixMilli(), nil
	case "yesterday":
		return startOfDay(now, loc).AddDate(0, 0, -1).UnixMilli(), nil
	}
	// Day/week offsets first: Go's duration grammar rejects 'd' and 'w'.
	if d, ok := parseDaysWeeks(s); ok {
		return now.Add(-d).UnixMilli(), nil
	}
	// Generic Go duration (m/h/s and combinations), interpreted as "ago".
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d).UnixMilli(), nil
	}
	// Absolute calendar dates / datetimes, anchored in loc.
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.UnixMilli(), nil
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli(), nil
	}
	return 0, fmt.Errorf("unrecognized time %q (try: now, today, yesterday, 7d, 2h, or 2006-01-02)", s)
}

// parseDaysWeeks reads a "<N>d" / "<N>w" offset into a duration (days/weeks
// being absent from Go's duration grammar). ok is false for any other shape,
// so the caller can fall through to time.ParseDuration.
func parseDaysWeeks(s string) (time.Duration, bool) {
	if len(s) < 2 {
		return 0, false
	}
	unit := s[len(s)-1]
	scale := time.Duration(0)
	switch unit {
	case 'd', 'D':
		scale = 24 * time.Hour
	case 'w', 'W':
		scale = 7 * 24 * time.Hour
	default:
		return 0, false
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 0 {
		return 0, false
	}
	return time.Duration(n) * scale, true
}

// startOfDay returns midnight at the start of t's calendar day in loc.
func startOfDay(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// parseSinceUntil resolves the --since / --until flag pair into a unix-ms
// window [since, until). An empty string leaves that bound at 0 (no bound).
// It rejects a window whose lower bound is not strictly before its upper one
// so an empty or inverted range surfaces as an error rather than silently
// printing nothing. Shared by `read` and `digest`.
func parseSinceUntil(since, until string, now time.Time, loc *time.Location) (sinceMs, untilMs int64, err error) {
	if since != "" {
		if sinceMs, err = parseTimeArg(since, now, loc); err != nil {
			return 0, 0, fmt.Errorf("--since: %w", err)
		}
	}
	if until != "" {
		if untilMs, err = parseTimeArg(until, now, loc); err != nil {
			return 0, 0, fmt.Errorf("--until: %w", err)
		}
	}
	if sinceMs > 0 && untilMs > 0 && sinceMs >= untilMs {
		return 0, 0, fmt.Errorf("--since must be before --until")
	}
	return sinceMs, untilMs, nil
}
