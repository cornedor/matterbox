package ui

import (
	"image/color"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"
)

// typingIndicator tracks who is currently typing in the open channel and
// drives the little three-dot animation that rides on the composer's
// separator line. It is distinct from typingAnim (the "> Typing
// animation" gimmick that fake-types one of *our own* messages): this is
// the standard "someone is typing…" presence cue fed by Mattermost
// `typing` WebSocket events.
//
// Only the open channel is tracked — typing events for other channels are
// dropped — so the indicator only ever describes the composer on screen.
type typingIndicator struct {
	// users maps a typing user's ID to the moment their cue expires.
	// Mattermost has no "stopped typing" event; the webapp re-emits a
	// typing event every few seconds while a key is pressed, so each
	// event just pushes the expiry out and a tick prunes the stale ones.
	users      map[string]time.Time
	channelID  string // channel the entries above belong to
	animActive bool   // whether the frame-tick loop is running
	phase      int    // animation frame counter
}

// typingIndicatorTTL is how long a single typing event keeps the cue
// alive. It must exceed the server's re-emit cadence
// (TimeBetweenUserTypingUpdatesMilliseconds, 5s by default) so a user who
// keeps typing doesn't flicker off between events.
const typingIndicatorTTL = 6 * time.Second

// typingIndicatorInterval is the gap between animation frames — a calm
// pulse rather than a frantic blink.
const typingIndicatorInterval = 400 * time.Millisecond

// typingDotCount is how many dots the indicator draws.
const typingDotCount = 3

// typingDotOffset is how many rule dashes precede the leading space and
// dots. One dash plus the space keeps the dots two columns in — just past
// the "> " prompt — while leaving them balanced by spaces: ─ ••• ───────.
const typingDotOffset = 1

// typingIndicatorTickMsg advances the dot animation by one frame.
type typingIndicatorTickMsg struct{}

var (
	// Dim dots read as part of the separator rule; the lit one pulses in
	// the same bright blue used for focus, so the wave is easy to spot
	// without shouting.
	typingDotDimStyle = lipgloss.NewStyle().Foreground(dimColor)
	typingDotLitStyle = lipgloss.NewStyle().Foreground(focusedColor)
	// Typer names ride the rule too, so keep them quiet (grey).
	typingNameStyle = lipgloss.NewStyle().Foreground(dimColor)
)

// applyTypingEvent records a `typing` WebSocket event. Events for any
// channel other than the open one are ignored, as is our own typing, so
// the cue only ever reflects the composer currently on screen. It returns
// a Cmd that (re)arms the animation loop when needed.
func (m *Model) applyTypingEvent(ev *model.WebSocketEvent) tea.Cmd {
	b := ev.GetBroadcast()
	if b == nil || !m.isCurrentChannel(b.ChannelId) {
		return nil
	}
	uid, _ := ev.GetData()["user_id"].(string)
	if uid == "" {
		uid = b.UserId
	}
	if uid == "" || (m.me != nil && uid == m.me.Id) {
		return nil
	}
	m.noteTyping(b.ChannelId, uid, time.Now())

	cmds := []tea.Cmd{m.maybeStartTypingAnim()}
	// In channels and group chats the cue names the typer, so resolve any
	// username we don't have cached yet (the result lands in m.userNames
	// via usersResolvedMsg and the next frame renders it).
	if m.typingNamesShown() {
		if _, known := m.userNames[uid]; !known {
			cmds = append(cmds, m.resolveTypingName(uid))
		}
	}
	return tea.Batch(cmds...)
}

// typingNamesShown reports whether the open channel should name its
// typers. Channels (public/private) and group chats do; a 1:1 DM doesn't,
// since the only other party is already obvious.
func (m *Model) typingNamesShown() bool {
	c := m.findChannel(m.openChannelID)
	return c != nil && c.Type != model.ChannelTypeDirect
}

// typingLabel is the "@john, @jane" list of currently-typing users for the
// composer cue, resolved from the username cache and sorted for a stable
// order. It is empty in 1:1 DMs and omits anyone not yet resolved (the
// fetch armed in applyTypingEvent fills them in shortly).
func (m *Model) typingLabel() string {
	if !m.typingNamesShown() {
		return ""
	}
	names := make([]string, 0, len(m.typingIndicator.users))
	for uid := range m.typingIndicator.users {
		if n := m.userNames[uid]; n != "" {
			names = append(names, "@"+n)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// resolveTypingName fetches a single typer's username in the background,
// reusing the shared usersResolvedMsg path (UsernamesByIDs is
// singleflight-deduped, so repeated calls for the same id coalesce).
func (m Model) resolveTypingName(uid string) tea.Cmd {
	ids := []string{uid}
	return func() tea.Msg {
		names, err := m.client.UsernamesByIDs(m.ctx, ids)
		if err != nil {
			return usersResolvedMsg{ids: ids, err: err}
		}
		return usersResolvedMsg{ids: ids, users: names}
	}
}

// noteTyping records that user uid is typing in channelID as of now,
// resetting the tracked set when the channel changed since last time.
func (m *Model) noteTyping(channelID, uid string, now time.Time) {
	if m.typingIndicator.channelID != channelID || m.typingIndicator.users == nil {
		m.typingIndicator.channelID = channelID
		m.typingIndicator.users = map[string]time.Time{}
	}
	m.typingIndicator.users[uid] = now.Add(typingIndicatorTTL)
}

// pruneTyping drops everyone whose cue has expired as of now.
func (m *Model) pruneTyping(now time.Time) {
	for uid, exp := range m.typingIndicator.users {
		if !now.Before(exp) {
			delete(m.typingIndicator.users, uid)
		}
	}
}

// maybeStartTypingAnim arms the frame loop if it isn't already running.
// Idempotent so each new typing event doesn't stack ticks.
func (m *Model) maybeStartTypingAnim() tea.Cmd {
	if m.typingIndicator.animActive {
		return nil
	}
	m.typingIndicator.animActive = true
	return typingIndicatorTickCmd()
}

// applyTypingIndicatorTick advances one frame and reschedules, stopping
// the loop once the tracked channel is no longer open or everyone has
// stopped typing. The tick Msg itself triggers the re-render that hides
// the cue when it stops.
func (m *Model) applyTypingIndicatorTick() tea.Cmd {
	if m.typingIndicator.channelID != m.openChannelID {
		m.typingIndicator.users = nil
		m.typingIndicator.animActive = false
		return nil
	}
	m.pruneTyping(time.Now())
	if len(m.typingIndicator.users) == 0 {
		m.typingIndicator.animActive = false
		return nil
	}
	m.typingIndicator.phase++
	return typingIndicatorTickCmd()
}

// typingIndicatorTickCmd schedules the next animation frame.
func typingIndicatorTickCmd() tea.Cmd {
	return tea.Tick(typingIndicatorInterval, func(time.Time) tea.Msg {
		return typingIndicatorTickMsg{}
	})
}

// typingIndicatorVisible reports whether the dot cue should be drawn on
// the composer right now: someone is typing in the channel that's open.
// Deliberately cheap (no clock read) so the per-keystroke View path stays
// fast — expiry is handled by the tick loop.
func (m *Model) typingIndicatorVisible() bool {
	return len(m.typingIndicator.users) > 0 && m.typingIndicator.channelID == m.openChannelID
}

// renderTypingDots draws the three-dot wave for the given frame, using the
// same bullet glyph as the sidebar presence dots (statusDot). One dot
// lights up at a time, sweeping left to right with a brief all-dim rest
// frame, so it reads as an unhurried "typing…" pulse. The result is
// exactly typingDotCount columns wide.
func renderTypingDots(phase int) string {
	// The +1 adds a rest frame where no dot is lit (lit == typingDotCount,
	// which never matches a real index).
	lit := phase % (typingDotCount + 1)
	var b strings.Builder
	for i := 0; i < typingDotCount; i++ {
		if i == lit {
			b.WriteString(typingDotLitStyle.Render(statusDot))
		} else {
			b.WriteString(typingDotDimStyle.Render(statusDot))
		}
	}
	return b.String()
}

// typingDotLeadWidth is the fixed run before the typer names: the offset
// dashes, a space, the dots, and a trailing space — "─ ••• ".
const typingDotLeadWidth = typingDotOffset + 1 + typingDotCount + 1

// overlayTypingDots returns the composer's separator rule with the dots
// (and, when set, the typer names) laid over it — "─ ••• @john ───────" —
// so the cue costs no extra vertical space. The dots sit two columns in,
// spaced for balance; label is the already-resolved "@name" list (empty
// for a 1:1 DM). ruleColor matches the surrounding border so the dashes on
// either side are indistinguishable from a plain rule, and width is the
// full rule width.
func overlayTypingDots(phase, width int, ruleColor color.Color, label string) string {
	ruleStyle := lipgloss.NewStyle().Foreground(ruleColor)
	rule := func(n int) string {
		if n < 0 {
			n = 0
		}
		return ruleStyle.Render(strings.Repeat("─", n))
	}
	if width < typingDotLeadWidth+1 {
		// Too narrow for the spaced dots plus a trailing dash; fall back to
		// a plain rule rather than overflow it.
		return rule(max(width, 0))
	}
	lead := rule(typingDotOffset) + " " + renderTypingDots(phase) + " "

	// Fit the typer names when there's room, truncating to keep a couple of
	// trailing dashes so the cue still reads as riding the rule.
	if label != "" {
		const minRight = 2
		avail := width - typingDotLeadWidth - 1 - minRight // -1 for the space after the names
		if avail >= 1 {
			shown := label
			if lipgloss.Width(shown) > avail {
				shown = ansi.Truncate(shown, avail, "…")
			}
			if w := lipgloss.Width(shown); w > 0 {
				return lead + typingNameStyle.Render(shown) + " " + rule(width-typingDotLeadWidth-w-1)
			}
		}
	}
	return lead + rule(width-typingDotLeadWidth)
}
