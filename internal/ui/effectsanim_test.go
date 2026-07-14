package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/effects"
)

// effectPost builds a post whose body carries an effects payload, exactly as the
// composer would put it on the wire.
func effectPost(id, src string) *model.Post {
	return &model.Post{Id: id, Message: compileEffects(src)}
}

func TestHasEffectPayload(t *testing.T) {
	if !hasEffectPayload(compileEffects("gg \\shimmer{well played}")) {
		t.Error("a post with an effect was not detected")
	}
	// Plain posts, and posts that merely mention the syntax without a known
	// effect name, carry no payload at all.
	for _, s := range []string{"just a message", "\\wobble{not an effect}", "C:\\path\\to\\thing"} {
		if hasEffectPayload(compileEffects(s)) {
			t.Errorf("false positive on %q", s)
		}
	}
}

// The viewport gate: the frame loop must only run while a post carrying effects
// is actually on screen, so a channel without them (or one scrolled away from
// them) costs nothing.
func TestEffectsVisibleIn(t *testing.T) {
	posts := []*model.Post{
		{Id: "a", Message: "plain"},
		effectPost("b", "\\shimmer{hi}"),
	}
	starts := []int{0, 10, 20} // post a on rows 0-9, post b on rows 10-19

	tests := []struct {
		name        string
		top, height int
		want        bool
	}{
		{"effect post in view", 10, 5, true},
		{"effect post partially in view", 8, 4, true},
		{"scrolled above the effect post", 0, 10, false},
		{"scrolled below the effect post", 20, 5, false},
		{"zero height", 10, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectsVisibleIn(posts, starts, tt.top, tt.height); got != tt.want {
				t.Errorf("effectsVisibleIn(top=%d, h=%d) = %v; want %v", tt.top, tt.height, got, tt.want)
			}
		})
	}

	// A mismatched row index must not panic or guess.
	if effectsVisibleIn(posts, []int{0}, 0, 10) {
		t.Error("a stale row index should report nothing visible")
	}
}

// The loop stops itself the moment nothing with effects is on screen — the
// property that keeps an idle client at zero cost.
func TestEffectsTickSelfStops(t *testing.T) {
	m := &Model{}
	m.effectsAnim.active = true
	m.posts = []*model.Post{{Id: "a", Message: "plain"}}
	m.msgRowStarts = []int{0, 5}
	m.msgsView.SetHeight(10)

	if cmd := m.applyEffectsTick(); cmd != nil {
		t.Error("the tick rescheduled with no effect on screen")
	}
	if m.effectsAnim.active {
		t.Error("the loop did not stop itself")
	}
}

// ...and re-arms (advancing the phase) once one is.
func TestEffectsTickAdvancesPhaseWhenVisible(t *testing.T) {
	m := &Model{}
	m.posts = []*model.Post{effectPost("a", "\\shimmer{hi}")}
	m.msgRowStarts = []int{0, 5}
	m.msgsView.SetHeight(10)

	cmd := m.maybeStartEffectsAnim()
	if cmd == nil || !m.effectsAnim.active {
		t.Fatal("the loop did not arm with an effect on screen")
	}
	// Arming twice must not stack a second loop.
	if again := m.maybeStartEffectsAnim(); again != nil {
		t.Error("a second loop was armed")
	}

	before := m.effectsAnim.phase
	if next := m.applyEffectsTick(); next == nil {
		t.Fatal("the tick did not reschedule while an effect is on screen")
	}
	if m.effectsAnim.phase == before {
		t.Error("the phase did not advance")
	}
}

// The phase wraps into [0,1) rather than growing without bound.
func TestEffectsPhaseWraps(t *testing.T) {
	m := &Model{}
	m.posts = []*model.Post{effectPost("a", "\\shimmer{hi}")}
	m.msgRowStarts = []int{0, 5}
	m.msgsView.SetHeight(10)
	m.effectsAnim.active = true
	m.effectsAnim.phase = 1 - effectsAnimStep/2

	m.applyEffectsTick()
	if p := m.effectsAnim.phase; p < 0 || p >= 1 {
		t.Errorf("phase = %v; want it wrapped into [0,1)", p)
	}
}

// The hot path: with nothing on screen carrying effects, painting is a single
// bool test that hands the cached render straight back.
func TestPaintEffectsNoOpWhenNothingOnScreen(t *testing.T) {
	m := &Model{}
	in := "│ a cached, already-rendered box\n│ with no effects in view"
	if got := m.paintEffects(in, msgsBoxChrome); got != in {
		t.Error("paintEffects touched the render with no effect on screen")
	}
}

// With one on screen, the sentinels the cached render carries are resolved away
// and the span is coloured — and the cached bytes themselves are left alone, so
// the box cache survives across frames.
func TestPaintEffectsResolvesWhenOnScreen(t *testing.T) {
	m := &Model{}
	m.effectsAnim.onScreen = true
	m.effectsAnim.phase = effectStaticPhase

	cached := "│ " + string(effStart(effects.Shimmer)) + "shiny" + string(effSentinelEnd) + " text"
	got := m.paintEffects(cached, msgsBoxChrome)

	if hasEffectSentinel(got) {
		t.Error("sentinels reached the screen")
	}
	if !strings.Contains(got, "\x1b[38;2;") {
		t.Error("the span was not coloured")
	}
	if !hasEffectSentinel(cached) {
		t.Error("paintEffects mutated the cached render — the box cache would go stale")
	}
}
