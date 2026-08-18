package listen

import (
	"log"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestParseCronFields covers the crontab grammar the scheduler accepts: stars,
// lists, ranges, steps and names, plus the errors that must be loud at startup.
func TestParseCronFields(t *testing.T) {
	ok := []string{
		"* * * * *",
		"0 9 * * 1-5",
		"*/15 * * * *",
		"0 0 1 jan *",
		"30 8 * * mon,wed,fri",
		"0 9-17/2 * * *",
		"0 0 * * 7", // Sunday, the high spelling
	}
	for _, expr := range ok {
		if _, err := parseCron(expr); err != nil {
			t.Errorf("parseCron(%q) = %v, want ok", expr, err)
		}
	}
	bad := []string{
		"* * * *",        // too few fields
		"* * * * * *",    // too many
		"60 * * * *",     // minute out of range
		"0 24 * * *",     // hour out of range
		"0 9 * * funday", // unknown name
		"5-1 * * * *",    // backwards range
		"*/0 * * * *",    // zero step
	}
	for _, expr := range bad {
		if _, err := parseCron(expr); err == nil {
			t.Errorf("parseCron(%q) = nil error, want a failure", expr)
		}
	}
}

// TestCronMatches pins the matching semantics, including crontab's day rule:
// with both day-of-month and day-of-week restricted, either one firing is
// enough.
func TestCronMatches(t *testing.T) {
	// 2026-08-18 is a Tuesday.
	tue0900 := time.Date(2026, 8, 18, 9, 0, 0, 0, time.Local)
	cases := []struct {
		expr string
		at   time.Time
		want bool
	}{
		{"0 9 * * 1-5", tue0900, true},
		{"0 9 * * 1-5", tue0900.Add(time.Minute), false},
		{"0 9 * * 6,0", tue0900, false},
		{"*/15 * * * *", tue0900.Add(15 * time.Minute), true},
		{"*/15 * * * *", tue0900.Add(16 * time.Minute), false},
		{"0 9 18 * *", tue0900, true},
		{"0 9 1 * *", tue0900, false},
		// dom and dow both restricted: the 1st OR any Tuesday.
		{"0 9 1 * 2", tue0900, true},
		{"0 9 1 * 3", tue0900, false},
	}
	for _, c := range cases {
		expr, err := parseCron(c.expr)
		if err != nil {
			t.Fatalf("parseCron(%q): %v", c.expr, err)
		}
		if got := expr.matches(c.at); got != c.want {
			t.Errorf("%q at %s = %v, want %v", c.expr, c.at.Format(time.RFC3339), got, c.want)
		}
	}
}

// TestNextRun checks the listing's "next fire" walk, including a Friday→Monday
// jump that a naive "tomorrow" answer would get wrong.
func TestNextRun(t *testing.T) {
	rules := mustCompile(t, RuleSpec{
		Name:     "standup",
		On:       []string{EventSchedule},
		Schedule: &ScheduleSpec{Cron: "0 9 * * 1-5"},
		Actions:  []ActionSpec{{Type: ActionLog}},
	})
	fri := time.Date(2026, 8, 21, 10, 0, 0, 0, time.Local) // Friday, after 09:00
	next, ok := rules[0].NextRun(fri)
	if !ok {
		t.Fatal("NextRun should resolve a cron rule")
	}
	want := time.Date(2026, 8, 24, 9, 0, 0, 0, time.Local) // Monday
	if !next.Equal(want) {
		t.Errorf("NextRun = %s, want %s", next, want)
	}
}

// TestScheduleValidation pins the compile-time rules that keep a timer from
// silently never ticking, or from firing an action that has no post to act on.
func TestScheduleValidation(t *testing.T) {
	bad := []struct {
		name string
		spec RuleSpec
	}{
		{"schedule without on", RuleSpec{
			Schedule: &ScheduleSpec{Every: "1h"},
			Actions:  []ActionSpec{{Type: ActionLog}},
		}},
		{"on schedule without a timer", RuleSpec{
			On:      []string{EventSchedule},
			Actions: []ActionSpec{{Type: ActionLog}},
		}},
		{"both cron and every", RuleSpec{
			On:       []string{EventSchedule},
			Schedule: &ScheduleSpec{Cron: "* * * * *", Every: "1h"},
			Actions:  []ActionSpec{{Type: ActionLog}},
		}},
		{"schedule mixed with message", RuleSpec{
			On:       []string{EventSchedule, EventMessage},
			Schedule: &ScheduleSpec{Every: "1h"},
			Actions:  []ActionSpec{{Type: ActionLog}},
		}},
		{"react needs a post", RuleSpec{
			On:       []string{EventSchedule},
			Schedule: &ScheduleSpec{Every: "1h"},
			Actions:  []ActionSpec{{Type: ActionReact, Emoji: "tada"}},
		}},
		{"send needs a channel", RuleSpec{
			On:       []string{EventSchedule},
			Schedule: &ScheduleSpec{Every: "1h"},
			Actions:  []ActionSpec{{Type: ActionSend, Text: "hi"}},
		}},
		{"sub-minute interval", RuleSpec{
			On:       []string{EventSchedule},
			Schedule: &ScheduleSpec{Every: "10s"},
			Actions:  []ActionSpec{{Type: ActionLog}},
		}},
		{"unknown kind", RuleSpec{
			On:      []string{"whenever"},
			Actions: []ActionSpec{{Type: ActionLog}},
		}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := CompileRules([]RuleSpec{c.spec}); err == nil {
				t.Error("want a compile error")
			}
		})
	}

	good := RuleSpec{
		On:       []string{EventSchedule},
		Schedule: &ScheduleSpec{Cron: "0 9 * * 1-5"},
		Actions:  []ActionSpec{{Type: ActionSend, Text: "standup!", Channel: "eng/general"}},
	}
	if _, err := CompileRules([]RuleSpec{good}); err != nil {
		t.Errorf("valid schedule rule rejected: %v", err)
	}
}

// TestTickSchedules drives the scheduler's clock: a cron rule fires on its
// minute and not again inside it, and an interval rule waits out its interval
// (seeded at startup so it doesn't fire the moment the daemon comes up).
func TestTickSchedules(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	lg := log.New(writerFunc(func(b []byte) (int, error) {
		mu.Lock()
		lines = append(lines, string(b))
		mu.Unlock()
		return len(b), nil
	}), "", 0)

	e := newStoreEngine(t)
	e.log = lg
	now := time.Date(2026, 8, 18, 8, 59, 0, 0, time.Local)
	e.now = func() time.Time { return now }
	e.rules = mustCompile(t,
		RuleSpec{
			Name:     "standup",
			On:       []string{EventSchedule},
			Schedule: &ScheduleSpec{Cron: "0 9 * * 1-5"},
			Actions:  []ActionSpec{{Type: ActionLog, Text: "STANDUP"}},
		},
		RuleSpec{
			Name:     "sweep",
			On:       []string{EventSchedule},
			Schedule: &ScheduleSpec{Every: "30m"},
			Actions:  []ActionSpec{{Type: ActionLog, Text: "SWEEP"}},
		},
	)
	e.seedSchedules()

	count := func(want string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, l := range lines {
			if strings.Contains(l, want) {
				n++
			}
		}
		return n
	}

	e.tickSchedules(t.Context(), now) // 08:59 — neither is due
	if got := count("STANDUP") + count("SWEEP"); got != 0 {
		t.Fatalf("nothing should fire at 08:59, got %d", got)
	}

	now = now.Add(time.Minute) // 09:00
	e.tickSchedules(t.Context(), now)
	if got := count("STANDUP"); got != 1 {
		t.Errorf("cron rule should fire at 09:00, got %d", got)
	}
	if got := count("SWEEP"); got != 0 {
		t.Errorf("interval rule should still be waiting, got %d", got)
	}

	e.tickSchedules(t.Context(), now) // same minute, e.g. after a restart
	if got := count("STANDUP"); got != 1 {
		t.Errorf("cron rule must not fire twice in one minute, got %d", got)
	}

	now = now.Add(30 * time.Minute) // 09:30
	e.tickSchedules(t.Context(), now)
	if got := count("SWEEP"); got != 1 {
		t.Errorf("interval rule should fire after 30m, got %d", got)
	}
	if got := count("STANDUP"); got != 1 {
		t.Errorf("cron rule should not fire at 09:30, got %d", got)
	}
}

// TestScheduleSurvivesRestart confirms the interval is measured from the last
// firing rather than from process start, so a daemon that restarts every few
// minutes doesn't fire a "daily" rule every few minutes.
func TestScheduleSurvivesRestart(t *testing.T) {
	e := newStoreEngine(t)
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.Local)
	e.now = func() time.Time { return now }
	e.rules = mustCompile(t, RuleSpec{
		Name:     "daily",
		On:       []string{EventSchedule},
		Schedule: &ScheduleSpec{Every: "24h"},
		Actions:  []ActionSpec{{Type: ActionLog}},
	})
	e.seedSchedules()
	now = now.Add(time.Hour)
	e.seedSchedules() // a restart re-seeds; it must not reset the stamp
	if e.scheduleDue(e.rules[0], now) {
		t.Error("an interval rule must not fire an hour into a 24h interval after a restart")
	}
	now = now.Add(24 * time.Hour)
	if !e.scheduleDue(e.rules[0], now) {
		t.Error("an interval rule should fire once its interval has elapsed")
	}
}

// TestIntervalFiresEveryTick pins the arithmetic against how the scheduler
// actually ticks: a second past each minute, stamping that instant. Comparing
// instant to instant leaves the next tick a fraction short of the interval, so
// an `every: 1m` rule silently ran every other minute.
func TestIntervalFiresEveryTick(t *testing.T) {
	e := newStoreEngine(t)
	// Seeded mid-minute, as a reload at an arbitrary moment would.
	now := time.Date(2026, 8, 18, 22, 24, 1, 149_000_000, time.Local)
	e.now = func() time.Time { return now }
	e.rules = mustCompile(t, RuleSpec{
		Name:     "tick",
		On:       []string{EventSchedule},
		Schedule: &ScheduleSpec{Every: "1m"},
		Actions:  []ActionSpec{{Type: ActionLog}},
	})
	e.seedSchedules()

	minute := time.Date(2026, 8, 18, 22, 25, 1, 0, time.Local)
	for i := 0; i < 3; i++ {
		at := minute.Add(time.Duration(i) * time.Minute)
		if !e.scheduleDue(e.rules[0], at) {
			t.Fatalf("every: 1m should fire at %s", at.Format("15:04:05"))
		}
		if err := e.store.SetMeta(scheduleMetaKey("tick"), strconv.FormatInt(at.UnixMilli(), 10)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	// Twice within one tick is still once.
	if e.scheduleDue(e.rules[0], minute.Add(2*time.Minute+time.Millisecond)) {
		t.Error("a second check inside the same minute must not fire again")
	}
}
