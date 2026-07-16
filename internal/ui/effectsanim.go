package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/effects"
	"matterbox/internal/hidden"
)

// Animating the message pane's text effects has one hard constraint: View() is a
// per-keystroke hot path, and re-rendering the scrollback to advance a gradient
// would defeat the caches that make it cheap — the same animated-render storm
// this codebase already had to dig itself out of once (see scrollbackCache,
// refreshAnimVisibility).
//
// So a frame does not re-render anything. The effect sentinels are width-0 and
// phase-independent, so they sit *inside* every cached artefact — the per-post
// line cache, the viewport's content, even the memoized bordered box — without
// changing a single measurement. Advancing the animation is then just a recolour
// of the rows already on screen (resolveEffects), applied to the cached box on
// its way out. A frame costs one pass over the visible lines, and nothing else.
//
// The loop is gated the way the GIF loop is: it only runs while a post carrying
// effects is actually inside the viewport, and stops itself the moment one isn't
// — so a channel with no effects, or one scrolled away from them, pays nothing
// but a bool.

// effectsAnimState drives the gradient. active gates the frame loop so repeated
// kicks can't stack ticks; phase, in [0,1), slides the band; onScreen is the
// viewport gate, refreshed every event and every frame.
type effectsAnimState struct {
	active   bool
	phase    float64
	onScreen bool
}

// The effects sweep at the composer's cadence, so a shimmering `/command` in the
// input and a \shimmer{} span in a post move at the same speed — they are meant
// to read as the same thing.
const (
	effectsAnimInterval = cmdShimmerInterval
	effectsAnimStep     = cmdShimmerStep
)

// effectsAnimTickMsg advances the effect gradients by one frame.
type effectsAnimTickMsg struct{}

// effectsMagicPrefix is the encoded MBF1 magic — the invisible rune run that
// opens every effects payload. Testing a post body for it is a plain substring
// search, which is what makes the per-event visibility scan free.
var effectsMagicPrefix = hidden.Encode(effects.MagicEffects, nil)

// hasEffectPayload reports whether a raw post body carries an effects payload.
func hasEffectPayload(msg string) bool {
	return strings.Contains(msg, effectsMagicPrefix)
}

// refreshEffectsVisibility recomputes what is on screen and returns whether an
// *animated* effect is among it — the frame ticker's gate. As a side effect it
// sets m.effectsAnim.onScreen, which paintEffects reads: painting (which also
// strips the sentinels) has to happen for every visible effect, animated or not,
// while the ticker only needs to run for the ones that move. A channel showing
// nothing but static effects paints once per event and never ticks.
//
// Cheap enough for the per-event kicker: it looks only at the posts the row index
// says are on screen, tests each with a substring search, and decodes a payload
// only for the few posts that carry one.
func (m *Model) refreshEffectsVisibility() bool {
	// The Search / Feed / SQL tabs never render the message, thread or info
	// panes (see viewContent), so nothing carrying effects is on screen there —
	// the posts viewport and the open channel's header only *look* visible from
	// their state. Without this the ticker would keep painting a hidden pane.
	// Switching back re-arms on that very keypress (maybeStartEffectsAnim runs
	// per event).
	if m.onSearchTab() || m.onFeedTab() || m.onSQLTab() {
		m.effectsAnim.onScreen = false
		return false
	}
	any, animated := effectsVisibleIn(m.posts, m.msgRowStarts, m.msgsView.YOffset(), m.msgsView.Height())
	if m.threadOpen && (!any || !animated) {
		a2, an2 := effectsVisibleIn(m.threadPosts, m.threadRowStarts, m.threadView.YOffset(), m.threadView.Height())
		any, animated = any || a2, animated || an2
	}
	// The open channel's header rides the always-visible title line (and the
	// info panel when raised), so it counts as on screen for as long as the
	// channel is open — no row arithmetic to do.
	if !any || !animated {
		if ch := m.findChannel(m.openChannelID); ch != nil && hasEffectPayload(ch.Header) {
			any = true
			animated = animated || spansHaveAnimated(ch.Header)
		}
	}
	if m.infoOpen && m.infoChannelID != m.openChannelID && (!any || !animated) {
		if ch := m.findChannel(m.infoChannelID); ch != nil && hasEffectPayload(ch.Header) {
			any = true
			animated = animated || spansHaveAnimated(ch.Header)
		}
	}
	m.effectsAnim.onScreen = any
	return animated
}

// effectsVisibleIn reports, for the posts whose rows intersect the window
// [top, top+height), whether any carries effects at all (anyEffect, the paint
// gate) and whether any carries an *animated* one (animated, the ticker gate).
// Mirrors the emoji scan's row arithmetic (see viewportVisibleAnimatedEmoji).
func effectsVisibleIn(posts []*model.Post, starts []int, top, height int) (anyEffect, animated bool) {
	if height <= 0 || len(starts) != len(posts)+1 {
		return false, false
	}
	bot := top + height
	for i, p := range posts {
		if starts[i] >= bot {
			break // this post and all later ones start below the viewport
		}
		if starts[i+1] <= top {
			continue // entirely scrolled above the viewport
		}
		if p == nil || !hasEffectPayload(p.Message) {
			continue
		}
		anyEffect = true
		if spansHaveAnimated(p.Message) {
			return true, true // both gates satisfied; nothing more to learn
		}
	}
	return anyEffect, animated
}

// spansHaveAnimated reports whether a post's effect payload carries any span
// whose effect moves. Only called for posts hasEffectPayload already passed, so
// the decode is paid by the handful of effect posts on screen, not every post.
func spansHaveAnimated(msg string) bool {
	spans, ok := decodeEffectSpans(msg)
	if !ok {
		return false
	}
	for _, s := range spans {
		if effectAnimated(s.ID) {
			return true
		}
	}
	return false
}

// maybeStartEffectsAnim refreshes the viewport gate and arms the frame loop when
// an *animated* effect has come on screen and the loop isn't already running.
// Batched from Update after every event, mirroring maybeStartImageAnim. The
// refresh runs unconditionally (not just when the loop is stopped) so that
// scrolling an effect into view repaints it on that very event — including a
// static effect, which sets onScreen without ever arming the loop — rather than
// staying unpainted until the next tick.
func (m *Model) maybeStartEffectsAnim() tea.Cmd {
	if !m.refreshEffectsVisibility() || m.effectsAnim.active {
		return nil
	}
	m.effectsAnim.active = true
	return effectsAnimTickCmd()
}

// applyEffectsTick advances the gradient one frame and reschedules, stopping the
// loop as soon as nothing with effects is on screen (channel switched, scrolled
// away, post deleted). The tick Msg itself drives the re-render that paints the
// new frame — and the one that leaves the text clean when the loop stops.
func (m *Model) applyEffectsTick() tea.Cmd {
	if !m.effectsAnim.active {
		return nil
	}
	if !m.refreshEffectsVisibility() {
		m.effectsAnim.active = false
		return nil
	}
	m.effectsAnim.phase += effectsAnimStep
	if m.effectsAnim.phase >= 1 {
		m.effectsAnim.phase--
	}
	return effectsAnimTickCmd()
}

// effectsAnimTickCmd schedules the next gradient frame.
func effectsAnimTickCmd() tea.Cmd {
	return tea.Tick(effectsAnimInterval, func(time.Time) tea.Msg {
		return effectsAnimTickMsg{}
	})
}

// paintEffects resolves the effect sentinels in an already-rendered pane
// fragment, at the current animation phase — the last thing that happens to
// those bytes before they reach the screen. chrome is the number of leading
// runes per line that belong to the pane frame rather than the message (1 for a
// bordered box, 0 for a bare viewport); see resolveEffects.
//
// This is the whole hot-path story: when nothing with effects is on screen it is
// a single bool test, and the cached, sentinel-carrying render is handed back
// untouched.
func (m *Model) paintEffects(s string, chrome int) string {
	if !m.effectsAnim.onScreen {
		return s
	}
	lines := strings.Split(s, "\n")
	lines = resolveEffects(lines, m.effectsAnim.phase, chrome)  // recolour; hands geometric spans on
	lines = resolveGeometry(lines, m.effectsAnim.phase, chrome) // move: the scroll marquee
	return strings.Join(lines, "\n")
}
