package ui

import (
	"strconv"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/viewport"
)

// viewCache memoizes the layout-heavy parts of a render that don't change on
// most keystrokes. It lives behind a pointer on the Model (allocated in New)
// so writes from the value-receiver View path persist across renders — the
// same reason postLineCache is a map rather than a plain struct field.
//
// A pprof of typing in the composer showed View() dominating CPU, split between
// two re-render costs that are invariant while the message list and sidebar sit
// still:
//   - the message/thread scrollbar geometry: the total wrapped-row count is a
//     full O(content) width-measuring walk, recomputed 2-3× per render (the
//     scroll percent then derives from it arithmetically); and
//   - the channels sidebar, fully re-styled every render even when no channel,
//     unread count, presence dot or selection changed.
//
// Each cache turns that work into a cheap key/fingerprint comparison.
type viewCache struct {
	msgs      scrollGeom
	thread    scrollGeom
	ref       scrollGeom
	info      scrollGeom
	sidebar   sidebarCache
	msgsUpper scrollbackCache
	// view memoizes the entire rendered screen (viewContent's output). bubbletea
	// rebuilds View() after EVERY msg, and a full render is dominated by lipgloss
	// re-measuring grapheme widths of unchanged content (~75% of it is
	// ansi.stringWidth) — so a trackpad's wheel flood, one render per buffered
	// event, can't drain before the gesture ends. update() invalidates this on
	// every msg by default; a wheel event is the one exception (it only
	// accumulates wheelPending and changes nothing on screen until its flush
	// tick), so the flood returns the cached frame instead of rebuilding it. Set
	// behind the viewCache pointer so the value-receiver View path persists it.
	view      string
	viewValid bool
	// tabZones records each team tab's horizontal screen extent, written by
	// renderTeamTabs (which alone replays the tab-windowing layout). A mouse
	// click reads it back to resolve an x-coordinate to a tab index without
	// recomputing that layout. Persists across the value-receiver View path
	// because viewCache lives behind a pointer (see hitTest).
	tabZones []tabZone
	// bodyH is the body height the last render used (terminal height minus the
	// tab strip and the rendered footer). composerGeom reads it to anchor the
	// compose box from the bottom without re-rendering the footer — which would
	// allocate on the per-motion hover path (see composerGeom / hitTest).
	bodyH int
	// jumpZone is the jump-to-bottom pill's screen rect, written by
	// renderMessagesPane (the only place that knows the viewport's height after a
	// popup shrinks it) and read back by the mouse layer. Cleared each render, so
	// a tab that doesn't draw the pill can't leave a stale target behind.
	jumpZone rectZone
	// feedBtnZone is the Feed tab's mark-all-read button's screen rect, written
	// by renderFeedPane and read back by the mouse layer. Cleared each render
	// alongside jumpZone, so another tab can't inherit the target.
	feedBtnZone rectZone
	// toastZone is the overlay notice's screen rect, written by renderViewContent
	// while a toast is drawn and disarmed on every frame without one. A click on
	// it dismisses the box instead of reaching the content underneath.
	toastZone boxZone
}

// scrollGeom caches one viewport's total wrapped-row count. That total depends
// only on (content, width): ver is the content generation (bumped whenever
// renderMessages / renderThread rebuilds the viewport) and width is read off
// the viewport. When both match, the stored totalRows is returned verbatim.
//
// The scroll percent is deliberately NOT cached. It changes on every scroll,
// but is a cheap arithmetic function of the total, the height and the offset
// (see scrollPercentFor), so deriving it per call is far cheaper than letting a
// changing yOffset invalidate the cache and re-trigger the O(content) walk —
// which is exactly what made wheel/trackpad scrolling re-measure the whole
// loaded window on every event.
type scrollGeom struct {
	ver       uint64
	width     int
	totalRows int
	valid     bool
}

// scrollGeomFor returns vp's total visual rows and scroll percent. The total is
// a full width-measuring walk (viewportVisualRows), recomputed only when the
// content version or width changed — so a scroll, which only moves yOffset, no
// longer pays for it. The percent is then derived arithmetically from that
// total, mirroring viewport.Model.ScrollPercent exactly (which would otherwise
// repeat the same O(content) walk internally via calculateLine). g may be nil —
// tests build Models without a viewCache — in which case the total is computed
// fresh each call, preserving behaviour without caching.
func scrollGeomFor(g *scrollGeom, vp *viewport.Model, ver uint64) (int, float64) {
	w := vp.Width()
	var total int
	if g != nil && g.valid && g.ver == ver && g.width == w {
		total = g.totalRows
	} else {
		total = viewportVisualRows(vp.GetContent(), w)
		if g != nil {
			*g = scrollGeom{ver: ver, width: w, totalRows: total, valid: true}
		}
	}
	return total, scrollPercentFor(total, vp.Height(), vp.YOffset())
}

// primeScrollGeom records a freshly-rendered viewport's already-known total
// visual-row count in its cache slot, so the next scrollGeomFor call (from the
// View that follows the render) is a hit instead of re-walking — via GetContent
// + Split + a width measure of every line — the content we just measured. total
// must equal viewportVisualRows(content, width); renderMessages / renderThread
// accumulate exactly that as visAcc while laying the lines out. g may be nil
// (no viewCache, e.g. in tests), in which case priming is a no-op and the read
// falls back to the live recompute.
func primeScrollGeom(g *scrollGeom, ver uint64, width, total int) {
	if g == nil {
		return
	}
	*g = scrollGeom{ver: ver, width: width, totalRows: total, valid: true}
}

// scrollPercentFor reproduces viewport.Model.ScrollPercent for a known total
// row count. Keeping the arithmetic bit-identical to the viewport's (the
// height>=total short-circuit, then yOffset/(total-height) clamped to [0,1])
// means the rendered scrollbar is unchanged — only the redundant O(content)
// walk ScrollPercent performs to recompute the same total is avoided.
func scrollPercentFor(total, height, yOffset int) float64 {
	if height >= total {
		return 1.0
	}
	v := float64(yOffset) / (float64(total) - float64(height))
	return min(1.0, max(0.0, v))
}

// msgsScrollGeom / threadScrollGeom front scrollGeomFor with the matching cache
// slot + content version, tolerating a nil viewCache.
func (m *Model) msgsScrollGeom() (int, float64) {
	var g *scrollGeom
	if m.vcache != nil {
		g = &m.vcache.msgs
	}
	return scrollGeomFor(g, &m.msgsView, m.msgsContentVer)
}

func (m *Model) threadScrollGeom() (int, float64) {
	var g *scrollGeom
	if m.vcache != nil {
		g = &m.vcache.thread
	}
	return scrollGeomFor(g, &m.threadView, m.threadContentVer)
}

func (m *Model) refScrollGeom() (int, float64) {
	var g *scrollGeom
	if m.vcache != nil {
		g = &m.vcache.ref
	}
	return scrollGeomFor(g, &m.refView, m.refContentVer)
}

func (m *Model) infoScrollGeom() (int, float64) {
	var g *scrollGeom
	if m.vcache != nil {
		g = &m.vcache.info
	}
	return scrollGeomFor(g, &m.infoView, m.infoContentVer)
}

// sidebarCache memoizes renderChannelsPane's rendered string. fp is a
// fingerprint over every input the row loop reads (see channelsFingerprint);
// a hit returns the rendered string without re-styling a single row.
type sidebarCache struct {
	fp       string
	rendered string
	valid    bool
}

// scrollbackCache memoizes renderMessagesPane's upper box: the channel title
// plus the message viewport, framed with the left border only (the lower box —
// the popup/attachment/compose half — carries the bottom border). That upper
// box is invariant while you type in the composer below it, yet styling it
// re-measures the display width of every visible message row — which a pprof of
// composer typing showed dominating CPU (~half of it in ansi.stringWidth). fp
// is a fingerprint over every input those bytes depend on (see renderMsgsUpper);
// a hit returns the rendered string without re-styling — i.e. without lipgloss
// re-measuring a single row. Same shape as sidebarCache, distinct type for
// clarity at the use sites.
type scrollbackCache struct {
	fp       string
	rendered string
	valid    bool
}

// channelsFingerprint encodes everything renderChannelsPane's output depends
// on: the header line, the scroll window + inner height, the selection, and
// per visible row the channel id, badge counts, partner presence (the raw
// status string — online/away/dnd share a glyph but differ in colour),
// custom-status presence, and the resolved label. Equal fingerprints mean
// identical output, so the cached string can be reused. It walks one extra row
// past the visible window so an off-by-one only ever costs a spurious miss,
// never a stale render.
func (m *Model) channelsFingerprint(vis []*model.Channel, off, listH, innerH int, header string) string {
	var b strings.Builder
	b.Grow(96 + (listH+1)*40)
	b.WriteString(header)
	b.WriteByte('\x1f')
	b.WriteString(strconv.Itoa(off))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(listH))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(innerH))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(m.channelIdx))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(len(vis)))
	b.WriteByte('|')
	// Hover index (-1 when the pointer isn't over a channel row) so the cached
	// sidebar repaints as the hover highlight moves between rows.
	if m.hover.zone == hitChannel {
		b.WriteString(strconv.Itoa(m.hover.idx))
	} else {
		b.WriteByte('-')
	}
	b.WriteByte('\x1e')
	for i := off; i < len(vis) && i <= off+listH; i++ {
		ch := vis[i]
		b.WriteString(ch.Id)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(m.mentions[ch.Id]))
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(m.unread[ch.Id]))
		b.WriteByte(':')
		b.WriteString(m.statuses[m.dmPartnerID(ch)])
		b.WriteByte(':')
		if _, ok := m.dmCustomStatus(ch); ok {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
		b.WriteByte(':')
		b.WriteString(m.channelLabel(ch))
		b.WriteByte(':')
		// The raw display name too: channelLabel strips any effects payload, so
		// an effect-only rename (same visible name, different colours) would
		// otherwise fingerprint identically and leave a stale row.
		b.WriteString(ch.DisplayName)
		b.WriteByte('\x1e')
	}
	return b.String()
}
