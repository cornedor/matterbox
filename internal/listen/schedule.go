package listen

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A schedule is the one trigger that isn't caused by anything happening in
// Mattermost: the rule fires because the clock said so. It is what a rule
// engine needs before "post the standup prompt at 09:00 on weekdays" or "sweep
// the ledger every hour" can be a rule rather than a separate cron job with its
// own copy of the config.
//
// Two forms, because they answer different questions:
//   - cron: "0 9 * * 1-5" — a wall-clock time ("at 09:00 on weekdays")
//   - every: "30m"        — an interval ("twice an hour, whenever that lands")
//
// Both are local time. Neither catches up: a firing missed because the daemon
// was down is skipped, not replayed at startup — a standup prompt that arrives
// four hours late is worse than one that doesn't arrive.

// ScheduleSpec is the config form of a schedule rule's timer. Exactly one of
// Cron / Every must be set.
type ScheduleSpec struct {
	// Cron is a five-field crontab expression (minute hour day-of-month month
	// day-of-week) in local time. Supports *, lists (1,15), ranges (1-5), steps
	// (*/10, 1-5/2) and names (mon, jan). As in crontab, when both day-of-month
	// and day-of-week are restricted the rule fires when *either* matches.
	Cron string
	// Every is an interval as a Go duration ("30m", "6h"). The first firing is
	// one interval after the daemon starts; the last-fire time is persisted, so
	// restarting the daemon doesn't restart the interval.
	Every string
}

// schedule is a compiled ScheduleSpec.
type schedule struct {
	cron  *cronExpr
	every time.Duration
	// text is the configured form ("cron 0 9 * * 1-5", "every 30m"), kept for
	// `matterbox rules list`.
	text string
}

// compileSchedule validates a ScheduleSpec. A schedule that is neither cron nor
// interval — or is both — is a startup error rather than a rule that never
// fires or fires twice.
func compileSchedule(s ScheduleSpec) (*schedule, error) {
	cron := strings.TrimSpace(s.Cron)
	every := strings.TrimSpace(s.Every)
	switch {
	case cron == "" && every == "":
		return nil, fmt.Errorf("schedule needs cron or every")
	case cron != "" && every != "":
		return nil, fmt.Errorf("schedule takes cron or every, not both")
	case cron != "":
		expr, err := parseCron(cron)
		if err != nil {
			return nil, fmt.Errorf("bad cron %q: %w", s.Cron, err)
		}
		return &schedule{cron: expr, text: "cron " + cron}, nil
	default:
		d, err := time.ParseDuration(every)
		if err != nil {
			return nil, fmt.Errorf("bad every %q: %w", s.Every, err)
		}
		if d < time.Minute {
			return nil, fmt.Errorf("every must be at least 1m, got %s", d)
		}
		return &schedule{every: d, text: "every " + d.String()}, nil
	}
}

// cronExpr is a parsed five-field crontab expression. Each field is a bitmask
// over its legal values; dom/dow also record whether they were restricted at
// all, because crontab's day rule depends on it (see matches).
type cronExpr struct {
	min, hour, dom, month, dow uint64
	domRestricted              bool
	dowRestricted              bool
}

// cronField describes one field's legal range and names for parsing.
type cronField struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	monthNames = map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}
	dayNames = map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}
)

// parseCron parses a five-field crontab expression into a matcher.
func parseCron(expr string) (*cronExpr, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("want 5 fields (minute hour day-of-month month day-of-week), got %d", len(fields))
	}
	specs := []cronField{
		{"minute", 0, 59, nil},
		{"hour", 0, 23, nil},
		{"day-of-month", 1, 31, nil},
		{"month", 1, 12, monthNames},
		{"day-of-week", 0, 7, dayNames},
	}
	var masks [5]uint64
	for i, f := range specs {
		m, err := parseCronField(fields[i], f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.name, err)
		}
		masks[i] = m
	}
	// Day-of-week 7 is Sunday, same as 0 — normalise so matching can index by
	// time.Weekday.
	if masks[4]&(1<<7) != 0 {
		masks[4] |= 1 << 0
		masks[4] &^= 1 << 7
	}
	return &cronExpr{
		min: masks[0], hour: masks[1], dom: masks[2], month: masks[3], dow: masks[4],
		domRestricted: strings.TrimSpace(fields[2]) != "*",
		dowRestricted: strings.TrimSpace(fields[4]) != "*",
	}, nil
}

// parseCronField turns one comma-separated field into a bitmask.
func parseCronField(text string, f cronField) (uint64, error) {
	var mask uint64
	for _, part := range strings.Split(text, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, fmt.Errorf("empty entry in %q", text)
		}
		rng, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(strings.TrimSpace(stepText))
			if err != nil || n < 1 {
				return 0, fmt.Errorf("bad step %q", stepText)
			}
			step = n
		}
		lo, hi := f.min, f.max
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if lo, err = cronValue(a, f); err != nil {
				return 0, err
			}
			hi = lo
			if isRange {
				if hi, err = cronValue(b, f); err != nil {
					return 0, err
				}
			} else if hasStep {
				hi = f.max // "5/10" means "from 5 to the end, every 10"
			}
		}
		if lo > hi {
			return 0, fmt.Errorf("range %q is backwards", rng)
		}
		for v := lo; v <= hi; v += step {
			mask |= 1 << uint(v)
		}
	}
	return mask, nil
}

// cronValue parses a single field value: a number, or a three-letter name where
// the field allows one.
func cronValue(text string, f cronField) (int, error) {
	text = strings.TrimSpace(text)
	if f.names != nil {
		if v, ok := f.names[strings.ToLower(text)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", text)
	}
	if v < f.min || v > f.max {
		return 0, fmt.Errorf("value %d out of range %d-%d", v, f.min, f.max)
	}
	return v, nil
}

// matches reports whether t (to the minute) satisfies the expression. The day
// test follows crontab: with both day-of-month and day-of-week restricted the
// expression matches when either does ("1 * * 15 * 1" is the 15th *or* any
// Monday); otherwise the restricted one alone decides.
func (c *cronExpr) matches(t time.Time) bool {
	if c.min&(1<<uint(t.Minute())) == 0 ||
		c.hour&(1<<uint(t.Hour())) == 0 ||
		c.month&(1<<uint(int(t.Month()))) == 0 {
		return false
	}
	domOK := c.dom&(1<<uint(t.Day())) != 0
	dowOK := c.dow&(1<<uint(int(t.Weekday()))) != 0
	if c.domRestricted && c.dowRestricted {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// ScheduleText renders a rule's timer for a listing ("cron 0 9 * * 1-5"), or ""
// when the rule isn't scheduled.
func (r Rule) ScheduleText() string {
	if r.schedule == nil {
		return ""
	}
	return r.schedule.text
}

// NextRun reports when a cron rule next fires after t. Interval rules depend on
// a last-firing time held by the running daemon, so they report false — the
// listing shows their interval instead. The search walks minute by minute for
// at most a year, which is both simple and fast enough for a listing (a cron
// match is a handful of bit tests) and never loops forever on an expression
// like "0 0 30 2 *" that can never come true.
func (r Rule) NextRun(t time.Time) (time.Time, bool) {
	if r.schedule == nil || r.schedule.cron == nil {
		return time.Time{}, false
	}
	next := t.Truncate(scheduleTick).Add(scheduleTick)
	for i := 0; i < 366*24*60; i++ {
		if r.schedule.cron.matches(next) {
			return next, true
		}
		next = next.Add(scheduleTick)
	}
	return time.Time{}, false
}

// scheduleMetaKey is the meta-store key holding a schedule rule's last firing,
// in the same reserved namespace as the cooldown stamps (never the user-facing
// ledger).
func scheduleMetaKey(ruleName string) string { return "schedule:" + ruleName }

// scheduleTick is how often the scheduler wakes to check every schedule rule.
// A minute is cron's own resolution, and the tick is aligned to the wall clock
// so "0 9 * * *" fires just after 09:00:00 rather than at some offset that
// depends on when the daemon happened to start.
const scheduleTick = time.Minute

// runScheduler drives every schedule rule until ctx is cancelled. It runs for
// the whole life of the daemon, independent of the WebSocket: a scheduled rule
// that posts a reminder should still fire while the connection is flapping. The
// loop is cheap enough (one wakeup a minute, re-reading the current ruleset) to
// run even when no schedule rule is configured, which is what lets a reload add
// one without a restart.
func (e *Engine) runScheduler(ctx context.Context) {
	defer e.wg.Done()
	e.seedSchedules()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(e.untilNextTick()):
		}
		e.tickSchedules(ctx, e.clock())
	}
}

// untilNextTick is the wait to the next wall-clock minute, plus a second of
// slack so a tick can't land microseconds before the minute it means to fire.
func (e *Engine) untilNextTick() time.Duration {
	now := e.clock()
	next := now.Truncate(scheduleTick).Add(scheduleTick + time.Second)
	if d := next.Sub(now); d > 0 {
		return d
	}
	return scheduleTick
}

// seedSchedules stamps every interval rule that has never fired, so `every: 6h`
// first fires six hours from now rather than the moment the daemon starts.
// Rules that have fired before keep their stamp, so a restart doesn't restart
// the interval.
func (e *Engine) seedSchedules() {
	now := e.clock()
	for _, r := range e.ruleSet() {
		if r.schedule == nil || r.schedule.every == 0 {
			continue
		}
		key := scheduleMetaKey(r.Name)
		if _, ok, err := e.store.GetMeta(key); err == nil && !ok {
			if err := e.store.SetMeta(key, strconv.FormatInt(now.UnixMilli(), 10)); err != nil {
				e.log.Printf("rule %s: seed schedule: %v", r.Name, err)
			}
		}
	}
}

// tickSchedules fires every schedule rule that is due at now.
func (e *Engine) tickSchedules(ctx context.Context, now time.Time) {
	rules := e.ruleSet()
	for i, r := range rules {
		if r.schedule == nil || !e.scheduleDue(r, now) {
			continue
		}
		if err := e.store.SetMeta(scheduleMetaKey(r.Name), strconv.FormatInt(now.UnixMilli(), 10)); err != nil {
			e.log.Printf("rule %s: record schedule: %v", r.Name, err)
		}
		e.fireSchedule(ctx, i, r, now)
	}
}

// scheduleDue reports whether a schedule rule should fire at now. A cron rule
// fires on a matching minute it hasn't already fired in (so a restart inside
// that minute doesn't fire it twice); an interval rule fires once its interval
// has elapsed since the last firing.
func (e *Engine) scheduleDue(r Rule, now time.Time) bool {
	last, ok := e.lastSchedule(r.Name)
	if r.schedule.every > 0 {
		// Compared on minute boundaries, not instant to instant: a tick lands a
		// second past the minute and the stamp it writes carries those
		// milliseconds, so the gap between two consecutive ticks is a hair under
		// a minute — and `every: 1m` would fire every *other* minute.
		return !ok || now.Truncate(scheduleTick).Sub(last.Truncate(scheduleTick)) >= r.schedule.every
	}
	if !r.schedule.cron.matches(now) {
		return false
	}
	return !ok || !last.Truncate(scheduleTick).Equal(now.Truncate(scheduleTick))
}

// lastSchedule reads a rule's persisted last-firing time.
func (e *Engine) lastSchedule(ruleName string) (time.Time, bool) {
	v, ok, err := e.store.GetMeta(scheduleMetaKey(ruleName))
	if err != nil || !ok {
		if err != nil {
			e.log.Printf("rule %s: read schedule: %v", ruleName, err)
		}
		return time.Time{}, false
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}

// fireSchedule runs one due rule. It goes through the same gate as a message
// trigger — the field conditions, the cooldown, the frequency window and the
// ledger still apply — so `state` can hold a scheduled digest back until there
// is something to report.
func (e *Engine) fireSchedule(ctx context.Context, idx int, r Rule, now time.Time) {
	t := e.scheduleTrigger(r, now)
	state := e.matchState()
	render := e.stateKeyRenderer(t)
	e.evalRule(ctx, t, idx, r, state, render)
}
