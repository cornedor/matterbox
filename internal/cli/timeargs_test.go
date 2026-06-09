package cli

import (
	"testing"
	"time"
)

func TestParseTimeArg(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 9, 14, 30, 0, 0, loc) // Tue 2026-06-09 14:30 UTC
	startToday := time.Date(2026, 6, 9, 0, 0, 0, 0, loc)

	cases := []struct {
		in   string
		want int64
	}{
		{"now", now.UnixMilli()},
		{"today", startToday.UnixMilli()},
		{"TODAY", startToday.UnixMilli()}, // case-insensitive keyword
		{"yesterday", startToday.AddDate(0, 0, -1).UnixMilli()},
		{"7d", now.Add(-7 * 24 * time.Hour).UnixMilli()},
		{"2w", now.Add(-14 * 24 * time.Hour).UnixMilli()},
		{"2h", now.Add(-2 * time.Hour).UnixMilli()},
		{"90m", now.Add(-90 * time.Minute).UnixMilli()},
		{"1h30m", now.Add(-90 * time.Minute).UnixMilli()},
		{"2026-06-08", time.Date(2026, 6, 8, 0, 0, 0, 0, loc).UnixMilli()},
		{"2026-06-08 09:30", time.Date(2026, 6, 8, 9, 30, 0, 0, loc).UnixMilli()},
		{"2026-06-08T09:30:00Z", time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC).UnixMilli()},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseTimeArg(c.in, now, loc)
			if err != nil {
				t.Fatalf("parseTimeArg(%q) error: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("parseTimeArg(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestParseTimeArgErrors(t *testing.T) {
	now := time.Date(2026, 6, 9, 14, 30, 0, 0, time.UTC)
	for _, in := range []string{"", "   ", "garbage", "5", "2026-13-40", "tomorrow"} {
		if _, err := parseTimeArg(in, now, time.UTC); err == nil {
			t.Errorf("parseTimeArg(%q) = nil error, want error", in)
		}
	}
}

func TestParseSinceUntil(t *testing.T) {
	now := time.Date(2026, 6, 9, 14, 30, 0, 0, time.UTC)
	loc := time.UTC

	t.Run("both empty leaves both unset", func(t *testing.T) {
		s, u, err := parseSinceUntil("", "", now, loc)
		if err != nil || s != 0 || u != 0 {
			t.Fatalf("got (%d,%d,%v), want (0,0,nil)", s, u, err)
		}
	})

	t.Run("valid range", func(t *testing.T) {
		s, u, err := parseSinceUntil("2026-06-08", "2026-06-09", now, loc)
		if err != nil {
			t.Fatal(err)
		}
		if s != time.Date(2026, 6, 8, 0, 0, 0, 0, loc).UnixMilli() {
			t.Errorf("since = %d", s)
		}
		if u != time.Date(2026, 6, 9, 0, 0, 0, 0, loc).UnixMilli() {
			t.Errorf("until = %d", u)
		}
	})

	t.Run("inverted range is an error", func(t *testing.T) {
		if _, _, err := parseSinceUntil("2026-06-09", "2026-06-08", now, loc); err == nil {
			t.Error("want error for since >= until")
		}
	})

	t.Run("equal bounds is an error", func(t *testing.T) {
		if _, _, err := parseSinceUntil("2026-06-08", "2026-06-08", now, loc); err == nil {
			t.Error("want error for since == until")
		}
	})

	t.Run("bad since surfaces", func(t *testing.T) {
		if _, _, err := parseSinceUntil("nonsense", "", now, loc); err == nil {
			t.Error("want error for bad --since")
		}
	})
}
