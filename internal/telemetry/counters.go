package telemetry

import (
	"sync"
	"time"
)

// The counted tier. A keystroke is far too frequent to be an event of its own —
// a busy day is thousands of them, most of which are `down` — so the
// high-frequency signals are tallied in memory here and shipped as one
// usage_snapshot every few minutes instead.
//
// What that buys is complete coverage of the "what is dead code" question at
// negligible volume: every action, every click target, every command and every
// friction signal gets a counter, so an id that never appears in any snapshot
// from any user is genuinely unused rather than merely unobserved.
//
// What it costs is ordering. A snapshot says an action fired fourteen times in
// five minutes, not what happened between the presses — so anything where the
// sequence *is* the finding (searched then gave up; hit a dead key then opened
// help) is detected here, while the sequence is still in hand, and reported as
// its own discrete event. That is why the friction signals are computed in
// this package rather than derived later in PostHog: the raw material for them
// does not survive aggregation.

// snapshotInterval is how often the counters flush. Short enough that a
// SIGKILLed session loses little, long enough that a full day of heavy use is
// under a hundred events.
const snapshotInterval = 5 * time.Minute

// mashWindow and mashThreshold define the "action_repeated" signal: the same
// action firing this many times inside this window is someone mashing a key,
// which almost always means the first press produced no visible feedback.
const (
	mashWindow    = 1500 * time.Millisecond
	mashThreshold = 4
)

// mashExempt are the actions whose repetition is the point. Moving a cursor or
// a viewport by one step fires four times in a second and a half during
// ordinary scrolling, which is not someone waiting for feedback — and counting
// it as mashing made it 61% of every friction event the first fortnight of
// telemetry produced, burying the signals that do mean something.
//
// The run is still counted for these, so a later run of a non-exempt action is
// measured from the right place; only the friction signal is withheld.
var mashExempt = map[string]bool{
	"up": true, "down": true, "left": true, "right": true,
	"input_up": true, "input_down": true,
	"page_up": true, "page_down": true, "top": true, "bottom": true,
	"select_up": true, "select_down": true, "select_left": true, "select_right": true,
	"next_match": true, "prev_match": true,
}

// escCascade is how many escapes in a row count as trying to get out of
// something. Two is ordinary (leave the composer, close the thread); three
// means the way out wasn't obvious.
const escCascade = 3

// counters holds the tallies for the current snapshot window plus the
// session-long totals app_stopped reports. Guarded by its own mutex rather
// than the client mutex in telemetry.go: the UI bumps these on every keystroke
// and must never contend with a network flush.
type counters struct {
	mu sync.Mutex

	windowStart time.Time

	actions  map[string]int
	mouse    map[string]int
	palette  map[string]int
	slash    map[string]int
	features map[string]int
	friction map[string]int
	surfaces map[string]int

	// Session-long totals, not reset by a flush.
	sessionStart   time.Time
	totalActions   int
	messagesSent   int
	channelsOpened int

	// Repeat detection for the action_repeated signal.
	lastAction   string
	lastActionAt time.Time
	repeatRun    int

	// Consecutive escapes, for the esc_cascade signal.
	escRun int
}

var tally = &counters{}

// resetWindow clears the per-window maps. Caller holds mu.
func (c *counters) resetWindow(now time.Time) {
	c.windowStart = now
	c.actions = nil
	c.mouse = nil
	c.palette = nil
	c.slash = nil
	c.features = nil
	c.friction = nil
	c.surfaces = nil
}

// bump increments one key in one of the maps, allocating on first use so an
// opted-out session (which never gets here) and a quiet window both cost
// nothing. Caller holds mu.
func bump(m *map[string]int, key string) {
	if key == "" {
		return
	}
	if *m == nil {
		*m = make(map[string]int, 8)
	}
	(*m)[key]++
}

// Action records that a keyboard action fired in a context, and returns
// whether that press completed a mash run worth reporting. The caller emits
// the friction event, because only it knows the surface — keeping the network
// call out of the locked section.
//
// This is the hot one: it runs on every keystroke that resolves to a binding,
// so it does a handful of map writes and nothing else.
func Action(id, context string) (mashed bool, runLength int) {
	if !active.Load() {
		return false, 0
	}
	now := time.Now()
	tally.mu.Lock()
	defer tally.mu.Unlock()
	bump(&tally.actions, id)
	bump(&tally.surfaces, context)
	tally.totalActions++

	// Mash detection: consecutive presses of the same action inside the
	// window. Report once, at the threshold, rather than on every press past
	// it — a stuck key would otherwise produce hundreds of events.
	if id == tally.lastAction && now.Sub(tally.lastActionAt) <= mashWindow {
		tally.repeatRun++
	} else {
		tally.repeatRun = 1
	}
	tally.lastAction, tally.lastActionAt = id, now
	if tally.repeatRun == mashThreshold && !mashExempt[id] {
		bump(&tally.friction, "action_repeated")
		return true, tally.repeatRun
	}
	return false, 0
}

// Escape records an escape keypress and reports whether it completed a
// cascade. Any other key resets the run, so callers must call ResetEscape for
// non-escape presses — cheap, and it keeps the state machine in one place.
func Escape() (cascaded bool, runLength int) {
	if !active.Load() {
		return false, 0
	}
	tally.mu.Lock()
	defer tally.mu.Unlock()
	tally.escRun++
	if tally.escRun == escCascade {
		bump(&tally.friction, "esc_cascade")
		return true, tally.escRun
	}
	return false, 0
}

// ResetEscape clears the escape run. Called for any keypress that isn't esc.
func ResetEscape() {
	if !active.Load() {
		return
	}
	tally.mu.Lock()
	tally.escRun = 0
	tally.mu.Unlock()
}

// Mouse records a click on a target region.
func Mouse(target string) { countIn(&tally.mouse, target) }

// Palette records a ">" command-palette entry running.
func Palette(id string) { countIn(&tally.palette, id) }

// Slash records a "/" command running.
func Slash(id string) { countIn(&tally.slash, id) }

// Feature records a feature being used. The counted half of feature adoption;
// FeatureUsed in emit.go is the discrete half, for features whose outcome
// matters as well as their frequency.
func Feature(id string) { countIn(&tally.features, id) }

// Friction records a friction signal. Most callers also emit the discrete
// `friction` event — the counter is what makes rates comparable between
// sessions, the event is what gives one an address.
func Friction(id string) { countIn(&tally.friction, id) }

// countIn is the shared no-op-when-off path for the simple counters.
func countIn(m *map[string]int, key string) {
	if !active.Load() {
		return
	}
	tally.mu.Lock()
	bump(m, key)
	tally.mu.Unlock()
}

// countMessageSent and countChannelOpened bump the session totals app_stopped
// reports. They are separate from the feature counters because they describe
// the size of a session rather than the use of a feature, and unexported
// because the emitters in emit.go are the only callers.
func countMessageSent() {
	if !active.Load() {
		return
	}
	tally.mu.Lock()
	tally.messagesSent++
	tally.mu.Unlock()
}

func countChannelOpened() {
	if !active.Load() {
		return
	}
	tally.mu.Lock()
	tally.channelsOpened++
	tally.mu.Unlock()
}

// snapshot builds the usage_snapshot properties and clears the window. Returns
// nil when the window is empty, so an idle session — matterbox left open in a
// tmux pane all afternoon — sends nothing at all rather than a stream of empty
// events.
func (c *counters) snapshot(final bool) map[string]any {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	// surfaces counts too: a window spent typing bumps nothing but that map, and
	// "the time went into the composer" is exactly the kind of thing worth
	// knowing. Leaving it out here made a whole session of composing look idle.
	if len(c.actions) == 0 && len(c.mouse) == 0 && len(c.palette) == 0 &&
		len(c.slash) == 0 && len(c.features) == 0 && len(c.friction) == 0 &&
		len(c.surfaces) == 0 {
		// Still move the window forward: the next snapshot should describe its
		// own span, not the whole idle stretch before it.
		c.resetWindow(now)
		return nil
	}

	window := now.Sub(c.windowStart)
	if c.windowStart.IsZero() {
		window = 0
	}
	props := map[string]any{
		"window": Seconds(int64(window.Seconds())),
		"final":  final,
	}
	// The maps are handed over rather than copied — resetWindow drops our
	// reference immediately afterwards, so nothing else can observe them.
	putMap(props, "actions", c.actions)
	putMap(props, "mouse", c.mouse)
	putMap(props, "palette", c.palette)
	putMap(props, "slash", c.slash)
	putMap(props, "features", c.features)
	putMap(props, "friction", c.friction)
	putMap(props, "surfaces", c.surfaces)
	if len(c.actions) > 0 {
		used := make([]string, 0, len(c.actions))
		for id := range c.actions {
			used = append(used, id)
		}
		props["actions_used"] = used
	}
	c.resetWindow(now)
	return props
}

// putMap adds a counter map under name when it has anything in it. An absent
// property reads better than an empty object in PostHog's property list, and
// validate would drop the empty one anyway.
func putMap(props map[string]any, name string, m map[string]int) {
	if len(m) > 0 {
		props[name] = m
	}
}

// sessionTotals returns the figures app_stopped reports.
func (c *counters) sessionTotals() (actions, messages, channels int, since time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalActions, c.messagesSent, c.channelsOpened, c.sessionStart
}

// Flush sends the pending snapshot immediately. Called on the interval timer,
// and once more at shutdown with final set.
func Flush(final bool) {
	if !active.Load() {
		return
	}
	if props := tally.snapshot(final); props != nil {
		Capture("usage_snapshot", props)
	}
}

// startSnapshots runs the flush loop until stop is closed. Started by Start and
// torn down by Close, so an opted-out process has no extra goroutine.
func startSnapshots(stop <-chan struct{}) {
	t := time.NewTicker(snapshotInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			Flush(false)
		case <-stop:
			return
		}
	}
}

// KeyHandled records a keystroke that some layer consumed without it resolving
// to a registry action — a modal's esc, a form's tab, a character typed into
// the composer. It bumps only the surface counter, so "where does the time go"
// stays accurate without inventing an action id for a key that has none.
func KeyHandled(context string) { countIn(&tally.surfaces, context) }
