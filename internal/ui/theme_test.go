package ui

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// bgSGR returns the SGR parameter fragment an adaptive background renders as
// ("48;2;68;68;68"), so hover assertions can look for the highlight without
// hardcoding a palette entry that moves with the terminal background.
func bgSGR(c color.Color) string {
	s := ansiOpenSeq(lipgloss.NewStyle().Background(c))
	return strings.TrimSuffix(strings.TrimPrefix(s, "\x1b["), "m")
}

// withLightBackground flips the package-level background flag for one test and
// restores it afterwards.
func withLightBackground(t *testing.T, light bool) {
	t.Helper()
	prev := lightBackground.Swap(light)
	t.Cleanup(func() { lightBackground.Store(prev) })
}

// TestAdaptiveColorFollowsBackground: the same style renders a different tint
// once the terminal reports a light background — and the dark value is what an
// unreported (silent terminal) background gets.
func TestAdaptiveColorFollowsBackground(t *testing.T) {
	for _, c := range []adaptiveColor{hoverRowBg, panelHoverBg, chipBg, jumpPillBg, jumpPillHoverBg, jumpPillFg} {
		withLightBackground(t, false)
		dark := bgSGR(c)
		withLightBackground(t, true)
		light := bgSGR(c)
		if dark == light {
			t.Errorf("adaptive colour renders identically on both backgrounds: %q", dark)
		}
	}
}

// TestBackgroundColorMsgRepaints: the terminal's OSC 11 answer flips the flag
// and drops the caches holding bytes styled under the old assumption.
func TestBackgroundColorMsgRepaints(t *testing.T) {
	withLightBackground(t, false)

	const stale = "no-such-post" // survives only if the cache map isn't dropped

	m := jumpModel(t)
	m.postMarkdownCache[stale] = postMarkdownCacheEntry{}
	m.postLineCache[stale] = postLineCacheEntry{}
	m.vcache.sidebar = sidebarCache{fp: "x", rendered: "old", valid: true}
	m.vcache.viewValid = true

	out, _ := m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	m = out.(Model)

	if !lightBackground.Load() {
		t.Fatal("a white background should have been recorded as light")
	}
	if _, ok := m.postMarkdownCache[stale]; ok {
		t.Error("markdown cache should be dropped so bodies restyle under the new palette")
	}
	if _, ok := m.postLineCache[stale]; ok {
		t.Error("line cache should be dropped so rows restyle under the new palette")
	}
	if m.vcache.sidebar.valid || m.vcache.viewValid {
		t.Error("view caches should be invalidated")
	}

	// A repeat of the same colour is a no-op: nothing to repaint.
	m.postLineCache[stale] = postLineCacheEntry{}
	out, _ = m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	m = out.(Model)
	if _, ok := m.postLineCache[stale]; !ok {
		t.Error("an unchanged background should not drop the caches")
	}
}
