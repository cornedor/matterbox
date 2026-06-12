package ui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/viewport"
	"github.com/mattermost/mattermost/server/public/model"
)

// viewCache memoizes the layout-heavy parts of a render that don't change on
// most keystrokes. It lives behind a pointer on the Model (allocated in New)
// so writes from the value-receiver View path persist across renders — the
// same reason postLineCache is a map rather than a plain struct field.
//
// A pprof of typing in the composer showed View() dominating CPU, split between
// two re-render costs that are invariant while the message list and sidebar sit
// still:
//   - the message/thread scrollbar geometry (total wrapped rows + scroll
//     percent), each a full O(content) width-measuring walk, recomputed 2-3×
//     per render; and
//   - the channels sidebar, fully re-styled every render even when no channel,
//     unread count, presence dot or selection changed.
//
// Each cache turns that work into a cheap key/fingerprint comparison.
type viewCache struct {
	msgs    scrollGeom
	thread  scrollGeom
	sidebar sidebarCache
}

// scrollGeom caches one viewport's soft-wrap geometry. ver is the content
// generation (bumped whenever the viewport's content is rebuilt by
// renderMessages / renderThread); width, height and yOffset are read straight
// off the viewport. When all four match, the stored totalRows/percent are
// returned verbatim.
type scrollGeom struct {
	ver       uint64
	width     int
	height    int
	yOffset   int
	totalRows int
	percent   float64
	valid     bool
}

// scrollGeomFor returns vp's total visual rows and scroll percent, recomputing
// (a full width-measuring walk via viewportVisualRows + ScrollPercent) only
// when the content version, width, height or scroll offset changed. g may be
// nil — tests build Models without a viewCache — in which case it always
// recomputes, preserving behaviour without caching.
func scrollGeomFor(g *scrollGeom, vp *viewport.Model, ver uint64) (int, float64) {
	w, h, y := vp.Width(), vp.Height(), vp.YOffset()
	if g != nil && g.valid && g.ver == ver && g.width == w && g.height == h && g.yOffset == y {
		return g.totalRows, g.percent
	}
	total := viewportVisualRows(vp.GetContent(), w)
	pct := vp.ScrollPercent()
	if g != nil {
		*g = scrollGeom{ver: ver, width: w, height: h, yOffset: y, totalRows: total, percent: pct, valid: true}
	}
	return total, pct
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

// sidebarCache memoizes renderChannelsPane's rendered string. fp is a
// fingerprint over every input the row loop reads (see channelsFingerprint);
// a hit returns the rendered string without re-styling a single row.
type sidebarCache struct {
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
		b.WriteByte('\x1e')
	}
	return b.String()
}
