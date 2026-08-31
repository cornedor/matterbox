package ui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"matterbox/internal/telemetry"
)

// Keyboard telemetry, from one hook at the top of handleKey.
//
// The obvious way to count actions would be to wrap the ~190 key.Matches calls
// scattered through the pane handlers. That was rejected: over half of them are
// navigation and dismiss keys whose counts answer no product question, a match
// inside a guarded branch isn't the same as the action running, and it measures
// keys rather than features — missing the mouse and palette routes to the same
// things entirely.
//
// keyContexts (contexts.go) is a better instrument and was already here. It is
// the declared precedence ladder handleKey implements: which layer is live for
// a given model state, and which bindings each layer claims. Walking it for a
// keypress gives the owning layer, the action that layer will run, and — when
// nothing claims the key — the fact that the press did nothing at all. That
// last one is the most valuable signal in the whole catalogue: it is someone's
// mental model of the keymap disagreeing with the keymap.
//
// The honest limit: the ladder says which binding *owns* a key, and a handler
// may then decline to act on state the ladder doesn't model — the reference
// panel's provider keys fall through to scrolling until the issue has loaded.
// So an action count is "the key that would run this fired", which for the
// adoption questions (does anyone change a Jira status from here?) is the right
// granularity. Where performed-versus-attempted matters, the feature's own
// event records the performance, and the gap between the two is itself a
// finding: a key that is pressed and does nothing is a UX bug.

// recordKey reports one keypress: which layer owned it, which action it
// resolved to, and whether it resolved to nothing. Called at the top of
// handleKey, before any state changes, so the ladder is evaluated against the
// state the user actually pressed the key in.
//
// Returns immediately when telemetry is off, which is the common case and costs
// one atomic load — nothing below it allocates or walks anything.
func (m *Model) recordKey(msg tea.KeyPressMsg) {
	if !telemetry.Enabled() {
		return
	}
	// msg.String() is what bindings are matched against (see key.Matches), so
	// it is what has to be compared here too: a shifted letter arrives as "R",
	// which is the string the keymap binds, while Keystroke() would say
	// "shift+r" and match nothing.
	keystroke := msg.String()

	// Escape runs are tracked across keypresses: three in a row is someone
	// trying to get out of something and not finding the door. Every other key
	// breaks the run.
	if keystroke == "esc" {
		if cascaded, n := telemetry.Escape(); cascaded {
			telemetry.FrictionEvent(telemetry.Stuck{
				Signal:  "esc_cascade",
				Context: m.currentContext(),
				Count:   n,
			})
		}
	} else {
		telemetry.ResetEscape()
	}

	reach := reachableContexts(m)
	if len(reach) == 0 {
		// No layer is live — a keypress during startup, before the first
		// layout. Nothing to attribute it to, and nothing went wrong.
		return
	}

	// Where the user *is* is the terminal layer at the bottom of the reachable
	// set — the focused pane or the open modal. The pass-through globals above
	// it (the switcher chord, sidebar nav, the reading layer) are active in
	// every reading state and would attribute everything to themselves.
	place := reach[len(reach)-1].name

	// Highest precedence first, so the first layer claiming the key is the one
	// that gets it — the same order handleKey dispatches in.
	for _, c := range reach {
		for _, b := range c.claims(m) {
			if !bindingHasKey(keystroke, b) {
				continue
			}
			if id := m.keys.actionID(b); id != "" {
				if mashed, n := telemetry.Action(id, place); mashed {
					telemetry.FrictionEvent(telemetry.Stuck{
						Signal:  "action_repeated",
						Context: place,
						Action:  id,
						Count:   n,
					})
				}
			} else {
				// A hardwired binding: a modal's esc, a form's tab. Consumed,
				// but not a rebindable action, so there is no id to count —
				// only the fact that time was spent here.
				telemetry.KeyHandled(place)
			}
			return
		}
	}

	// Nothing claimed it. If a text input is live the key is prose or an
	// editing chord the editor handles itself, and reporting it would mean
	// reporting what someone typed — so it is counted as time spent in that
	// layer and nothing more.
	for _, c := range reach {
		if c.typing {
			telemetry.KeyHandled(c.name)
			return
		}
	}

	// A dead key in a reading pane. ReportableKey drops it to "other" unless it
	// is one of the non-text keystrokes the catalogue allows, so this can never
	// spell out anything typed.
	telemetry.UnhandledKey(
		telemetry.ReportableKey(keystroke),
		place,
		m.keys.boundSomewhere(keystroke),
	)
	// Remembered so a help lookup in the next few seconds can be attributed to
	// it — someone who got stuck and went looking.
	m.noteUnhandledAt()
}

// bindingHasKey mirrors key.Matches for an already-derived keystroke: the same
// string comparison against the binding's keys, and the same Enabled() check so
// an action the user unbound (empty key list) never matches. Taking the
// keystroke as a string rather than the message avoids re-deriving it for every
// binding on the ladder, which is a few hundred comparisons per press.
func bindingHasKey(keystroke string, b key.Binding) bool {
	if !b.Enabled() {
		return false
	}
	for _, k := range b.Keys() {
		if k == keystroke {
			return true
		}
	}
	return false
}

// currentContext names the layer the user is in — the terminal one at the
// bottom of the reachable set, i.e. the focused pane or the open modal — for
// the events that need a surface but aren't triggered by a key resolving
// through the ladder. Returns "unknown" when nothing is live yet.
func (m *Model) currentContext() string {
	if reach := reachableContexts(m); len(reach) > 0 {
		return reach[len(reach)-1].name
	}
	return "unknown"
}

// --- mouse ---------------------------------------------------------------

// telemetryTarget names a click zone for the mouse counter. Explicit rather
// than derived from the constant's name so a rename in mouse.go can't silently
// re-label a metric, and so the whitelist in the catalogue has something stable
// to validate against. TestMouseTargetsCoverHitZones asserts every zone is
// mapped and every name is catalogued.
func (z hitZone) telemetryTarget() string {
	switch z {
	case hitTab:
		return "tab"
	case hitChannel:
		return "channel"
	case hitMessage:
		return "message"
	case hitThread:
		return "thread"
	case hitFeed:
		return "feed"
	case hitSearch:
		return "search"
	case hitRef:
		return "reference"
	case hitInfo:
		return "info"
	case hitSQL:
		return "sql"
	case hitComposer:
		return "composer"
	case hitJumpBottom:
		return "jump_bottom"
	case hitFeedMarkAll:
		return "feed_mark_all"
	case hitFeedBlobs:
		return "feed_blobs"
	case hitToast:
		return "toast"
	}
	return "nothing"
}

// recordClick counts a click on a region. The comparison with the keyboard
// counters is the point: a pane people only ever reach by clicking has a
// discoverability problem in its binding, and a region nobody clicks may not
// need to be clickable. "nothing" is counted too — a lot of clicks landing on
// dead space says the UI looks more interactive than it is.
func recordClick(z hitZone) {
	telemetry.Mouse(z.telemetryTarget())
}
