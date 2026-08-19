package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The jump-to-bottom pill: a small clickable label painted over the last row of
// the message viewport while the transcript is parked above its newest content.
// It states the key that would do the same thing (read from the live keymap, so
// a rebind shows the user's own key) and highlights under the pointer.

var (
	// jumpBottomStyle paints the pill at rest: a raised bar against the
	// transcript, quiet enough not to compete with the message under it.
	jumpBottomStyle = lipgloss.NewStyle().Foreground(jumpPillFg).Background(jumpPillBg)
	// jumpBottomHoverStyle lifts the bar a couple of steps under the pointer —
	// the same "quieter than a selection" cue a hovered channel row gets. An
	// accent colour here reads as a state change rather than a hover, and lands
	// loud on palettes that map the focus colour to a saturated hue.
	jumpBottomHoverStyle = lipgloss.NewStyle().Foreground(jumpPillFg).Background(jumpPillHoverBg)
)

// rectZone is a clickable label's screen rectangle: columns [x0,x1) on row y.
// Written at render time by whoever placed the label — the jump-to-bottom pill
// here, the Feed tab's mark-all-read button in feed.go — and read back by the
// mouse layer, so a click / hover resolves without replaying the pane layout.
// Cleared every render, re-armed only while the label shows.
type rectZone struct {
	x0, x1, y int
	active    bool
}

// contains reports whether the screen cell (x,y) falls on the label.
func (z rectZone) contains(x, y int) bool {
	return z.active && y == z.y && x >= z.x0 && x < z.x1
}

// jumpPill is the pill as renderMessagesPane resolved it for this frame: where
// it starts in viewport-content columns, what it says, and whether the pointer
// is on it. The zero value means "don't draw one".
type jumpPill struct {
	col0    int
	text    string
	active  bool
	hovered bool
}

// jumpBottomText is the pill's label, with the bottom action's configured key
// spliced in — "Jump to bottom (end/G) ↓". Generating it from the keymap (as
// the footer does) keeps it honest after a rebind.
func (m *Model) jumpBottomText() string {
	if k := m.keys.End.Help().Key; k != "" {
		return " Jump to bottom (" + k + ") ↓ "
	}
	return " Jump to bottom ↓ "
}

// msgsMoreBelow reports whether the channel's newest message is somewhere below
// what the user can see. Two ways for that to be true, and the pill needs both:
//
//   - the viewport sits above the bottom of the laid-out transcript (the same
//     test the scrollbar uses to decide it has anything to show), or
//   - the laid-out transcript itself stops short of the channel's newest message.
//     Opening a permalink, a search hit or a Feed entry loads a window *around*
//     its target, so scrolling to the bottom of that window can leave hundreds of
//     newer messages unloaded beneath it. Checking only the viewport would drop
//     the pill exactly there — the moment it's most needed.
//
// Called before any mention/emoji popup shrinks the viewport, so opening a popup
// (which pushes the bottom rows out of view without moving the scroll) doesn't
// summon the pill mid-keystroke.
func (m *Model) msgsMoreBelow() bool {
	if m.msgsTailBehind {
		return true
	}
	total, pct := m.msgsScrollGeom()
	return total > m.msgsView.Height() && pct < 1.0
}

// refreshTailBehind recomputes msgsTailBehind for the loaded window, memoized on
// the tail post so the wheel-scroll and arrow-key renders — which never change
// the tail — cost nothing. renderMessages calls it; View only reads the flag,
// keeping the store off the per-keystroke path.
func (m *Model) refreshTailBehind() {
	if m.store == nil || len(m.posts) == 0 {
		m.tailBehindChan, m.tailBehindPost, m.msgsTailBehind = "", "", false
		return
	}
	last := m.posts[len(m.posts)-1]
	if last.ChannelId == m.tailBehindChan && last.Id == m.tailBehindPost {
		return
	}
	newest, err := m.store.NewestCreateAt(last.ChannelId)
	m.tailBehindChan, m.tailBehindPost = last.ChannelId, last.Id
	// On a store error, fall back to the viewport-only test rather than pinning
	// a pill the user can't dismiss.
	m.msgsTailBehind = err == nil && newest > last.CreateAt
}

// msgsStayAtBottom reports whether the render about to run inherits a transcript
// parked on its newest message and must keep it there. True only when the
// previous render left the viewport at the bottom of the same window — same tail
// post, same selection — so whatever height change this render brings is content
// moving under a stationary reader: a reaction chip appearing on the last
// message, an edit landing, an emoji image resolving. Left to the ordinary
// keep-the-selection-visible rules, that extra row slides the newest message
// under the fold and raises the jump pill on a transcript the user never left.
//
// A different tail (new messages arrived) or a moved selection means the frame
// of reference itself changed; those paths set their own anchors — the live
// new-post path pins with anchorMsgSelBottom — and are left alone here.
//
// Reads the *previous* layout, so it must be called before renderMessages
// replaces msgRowStarts.
func (m *Model) msgsStayAtBottom() bool {
	if len(m.posts) == 0 || len(m.msgRowStarts) == 0 || m.msgsRenderTail == "" {
		return false
	}
	if m.posts[len(m.posts)-1].Id != m.msgsRenderTail {
		return false
	}
	if m.postIdx < 0 || m.postIdx >= len(m.posts) || m.posts[m.postIdx].Id != m.msgsRenderSel {
		return false
	}
	h := m.msgsView.Height()
	if h <= 0 {
		return false
	}
	// Same test the scrollbar and the pill use: the viewport is at the bottom
	// when its offset has reached the last screenful of content.
	total := m.msgRowStarts[len(m.msgRowStarts)-1]
	return total <= h || m.msgsView.YOffset() >= total-h
}

// jumpPillFor sizes and centers the pill within the message viewport, or
// returns the zero jumpPill when it's hidden or the pane is too narrow to hold
// it without crowding the text it covers.
func (m *Model) jumpPillFor(moreBelow bool) jumpPill {
	if !moreBelow {
		return jumpPill{}
	}
	w, h := m.msgsView.Width(), m.msgsView.Height()
	text := m.jumpBottomText()
	if h < 1 || w < lipgloss.Width(text)+2 {
		return jumpPill{}
	}
	return jumpPill{
		col0:    (w - lipgloss.Width(text)) / 2,
		text:    text,
		active:  true,
		hovered: m.hover.zone == hitJumpBottom,
	}
}

// armJumpZone records the pill's screen rectangle for the mouse layer, or
// disarms it when there's no pill. The viewport's content begins one column past
// the pane's left border and one row below the title, so the pill's row is the
// viewport's last — measured after any popup shrank it, which is why this has to
// happen at render time rather than from the model's resting geometry.
func (m *Model) armJumpZone(p jumpPill) {
	if m.vcache == nil {
		return
	}
	if !p.active {
		m.vcache.jumpZone = rectZone{}
		return
	}
	x0 := channelsWidth + 1 + p.col0
	m.vcache.jumpZone = rectZone{
		x0:     x0,
		x1:     x0 + lipgloss.Width(p.text),
		y:      tabsHeight + m.msgsView.Height(),
		active: true,
	}
}

// overlayJumpPill paints the pill over the last of the viewport's rendered rows,
// replacing the cells it covers rather than inserting any — the row keeps its
// display width, so the pane's right border and scrollbar stay in column. Only
// that row is rebuilt; the rows above it are handed back byte-for-byte. The
// leading reset closes whatever style the covered text left open, so the pill
// can't inherit its italics or colour, and TruncateLeft re-opens that style for
// the text resuming after it.
func overlayJumpPill(view string, p jumpPill) string {
	if !p.active || view == "" {
		return view
	}
	st := jumpBottomStyle
	if p.hovered {
		st = jumpBottomHoverStyle
	}
	head, last := "", view
	if i := strings.LastIndexByte(view, '\n'); i >= 0 {
		head, last = view[:i+1], view[i+1:]
	}
	left := ansi.Truncate(last, p.col0, "")
	// The viewport pads every row to its full width, so this only bites in the
	// degenerate cases the unit tests cover.
	if w := lipgloss.Width(left); w < p.col0 {
		left += strings.Repeat(" ", p.col0-w)
	}
	right := ansi.TruncateLeft(last, p.col0+lipgloss.Width(p.text), "")
	return head + left + ansi.ResetStyle + st.Render(p.text) + right
}

// clickJumpBottom is the pill's action: the same jump End/G performs, minus the
// focus change — a click while composing returns the transcript to the newest
// message without yanking the caret out of the composer.
func (m Model) clickJumpBottom() (tea.Model, tea.Cmd) {
	m.clearTextSel()
	m.hover = hoverState{}
	m.msgScrollFree = false
	m.selectLastMessage()
	m.renderMessages()
	return m, m.bumpMRFetch()
}
