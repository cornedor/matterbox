package ui

import (
	"fmt"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// TestScrollGeomCacheMatchesUncached asserts the cached geometry always equals
// what the uncached viewportVisualRows + ScrollPercent would return, across the
// mutations that should invalidate it (scroll, resize, content rebuild). The
// cache must never change the rendered scrollbar — only skip recomputation.
func TestScrollGeomCacheMatchesUncached(t *testing.T) {
	m := newScrollBenchModel(300)
	m.vcache = &viewCache{}
	m.renderMessages()

	check := func(label string) {
		t.Helper()
		wantRows := viewportVisualRows(m.msgsView.GetContent(), m.msgsView.Width())
		wantPct := m.msgsView.ScrollPercent()
		// First call may miss (recompute); second is guaranteed a hit. Both
		// must equal the uncached reference.
		for _, pass := range []string{"compute", "hit"} {
			gotRows, gotPct := m.msgsScrollGeom()
			if gotRows != wantRows || gotPct != wantPct {
				t.Fatalf("%s/%s: cached (%d, %v) != uncached (%d, %v)",
					label, pass, gotRows, gotPct, wantRows, wantPct)
			}
		}
	}

	check("initial")

	m.msgsView.SetYOffset(7)
	check("after scroll")

	m.msgsView.SetHeight(18)
	check("after height resize")

	m.msgsView.SetWidth(48)
	check("after width resize")

	// Shrink the post list and rebuild — renderMessages bumps the version.
	m.posts = m.posts[:90]
	m.postIdx = 40
	m.renderMessages()
	check("after content rebuild")
}

// TestScrollGeomServesCacheUntilVersionBump proves the cache actually serves a
// stored value (rather than recomputing every call): content mutated behind the
// version counter's back is NOT observed until renderMessages bumps the version.
func TestScrollGeomServesCacheUntilVersionBump(t *testing.T) {
	m := newScrollBenchModel(300)
	m.vcache = &viewCache{}
	m.renderMessages()
	m.msgsView.SetYOffset(0)

	r1, p1 := m.msgsScrollGeom() // prime against the full 300-post content

	// Replace the viewport content directly, bypassing renderMessages so the
	// version counter doesn't move. Width/height/offset stay identical, so the
	// cache key is unchanged and the primed (stale) value must still come back.
	m.msgsView.SetContent("x")
	m.msgsView.SetYOffset(0)
	r2, p2 := m.msgsScrollGeom()
	if r2 != r1 || p2 != p1 {
		t.Fatalf("expected stale cache hit while version unchanged: primed (%d, %v), got (%d, %v)",
			r1, p1, r2, p2)
	}

	// A real rebuild bumps the version and the geometry tracks the new content.
	m.renderMessages()
	gotRows, _ := m.msgsScrollGeom()
	wantRows := viewportVisualRows(m.msgsView.GetContent(), m.msgsView.Width())
	if gotRows != wantRows {
		t.Fatalf("after version bump: got %d rows, want %d", gotRows, wantRows)
	}
}

// newSidebarFPModel returns a fresh model (fresh maps) with one open channel
// and one DM, for the fingerprint-sensitivity table. Each call is independent
// so a mutation in one case can't bleed into another via shared maps.
func newSidebarFPModel() (Model, []*model.Channel) {
	m := Model{
		me:               &model.User{Id: "me"},
		mentions:         map[string]int{},
		unread:           map[string]int{},
		statuses:         map[string]string{},
		customStatuses:   map[string]model.CustomStatus{},
		userNames:        map[string]string{"partner": "alice"},
		showCustomStatus: true,
	}
	open := &model.Channel{Id: "c1", Type: model.ChannelTypeOpen, Name: "general", DisplayName: "General"}
	dm := &model.Channel{Id: "d1", Type: model.ChannelTypeDirect, Name: "me__partner"}
	return m, []*model.Channel{open, dm}
}

// TestChannelsFingerprintSensitivity checks the sidebar fingerprint is stable
// for identical inputs and changes for every input the row render reads — so a
// cache hit can never mask a real change (unread, mention, selection, presence,
// custom status, or label).
func TestChannelsFingerprintSensitivity(t *testing.T) {
	base, vis := newSidebarFPModel()
	fp0 := base.channelsFingerprint(vis, 0, 10, 11, "Channels")

	if fp1 := base.channelsFingerprint(vis, 0, 10, 11, "Channels"); fp1 != fp0 {
		t.Fatal("fingerprint not stable for identical inputs")
	}
	// Header / window / size changes flow through too.
	if fp := base.channelsFingerprint(vis, 0, 10, 11, "DMs"); fp == fp0 {
		t.Error("header change did not alter fingerprint")
	}
	if fp := base.channelsFingerprint(vis, 1, 10, 11, "Channels"); fp == fp0 {
		t.Error("scroll offset change did not alter fingerprint")
	}
	if fp := base.channelsFingerprint(vis, 0, 9, 11, "Channels"); fp == fp0 {
		t.Error("listH change did not alter fingerprint")
	}

	cases := []struct {
		name string
		mut  func(*Model)
	}{
		{"unread", func(m *Model) { m.unread["c1"] = 3 }},
		{"mention", func(m *Model) { m.mentions["c1"] = 1 }},
		{"selection", func(m *Model) { m.channelIdx = 1 }},
		{"presence", func(m *Model) { m.statuses["partner"] = model.StatusOnline }},
		{"custom-status", func(m *Model) { m.customStatuses["partner"] = model.CustomStatus{Emoji: "rocket"} }},
		{"label", func(m *Model) { m.userNames["partner"] = "bob" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, v := newSidebarFPModel()
			tc.mut(&m)
			if fp := m.channelsFingerprint(v, 0, 10, 11, "Channels"); fp == fp0 {
				t.Errorf("%s change did not alter fingerprint", tc.name)
			}
		})
	}
}

// BenchmarkScrollGeom contrasts the per-keystroke geometry cost with the cache
// hitting (typing in the composer, content unchanged) vs forced to recompute
// (the pre-cache behaviour: a full width-measuring walk every keystroke).
func BenchmarkScrollGeom(b *testing.B) {
	for _, n := range []int{240, 1200, 3000} {
		m := newScrollBenchModel(n)
		m.vcache = &viewCache{}
		m.renderMessages()

		b.Run(fmt.Sprintf("hit/posts=%d", n), func(b *testing.B) {
			m.msgsScrollGeom() // prime
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.msgsScrollGeom()
			}
		})

		b.Run(fmt.Sprintf("miss/posts=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.msgsContentVer++ // invalidate, forcing the full O(content) walk
				m.msgsScrollGeom()
			}
		})
	}
}
