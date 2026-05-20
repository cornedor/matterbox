package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	emoji "github.com/kyokomi/emoji/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// reactionChipBg is a subtle background tint used for the reaction
// chips. It's adaptive because the user's terminal theme might be dark
// (where we want a slightly-lighter-than-bg grey) or light (where we
// want a slightly-darker-than-bg grey). ANSI 238 / 253 sit one step
// off the typical default backgrounds on each side.
var reactionChipBg = compat.AdaptiveColor{
	Light: lipgloss.Color("253"),
	Dark:  lipgloss.Color("238"),
}

// Powerline filled half-circles. Rendered with the chip's background
// colour as their foreground and the terminal default as their own
// background, they appear as fully-filled rounded caps on either side
// of the styled chip body, giving each reaction the look of a real
// pill. Requires a terminal font with the powerline extras (a
// nerd-font variant or anything else that ships U+E0B4/U+E0B6).
const (
	reactionCapLeft  = ""
	reactionCapRight = ""
)

// reactionStyle / selfReactionStyle decorate the reaction chip rendered
// below each post's body. Both share the subtle pill background; the
// filled half-circle caps below provide all the breathing room between
// the chip text and the terminal background. The self variant tints
// the foreground bright blue and bolds it so the user can see at a
// glance which reactions they themselves contributed.
var (
	reactionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("6")). // cyan
			Background(reactionChipBg)
	selfReactionStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("12")). // bright blue
				Background(reactionChipBg).
				Bold(true)

	// reactionCapStyle paints just the cap glyph: foreground = chip
	// background colour, background = unset so the cap reads as a
	// rounded transition into the terminal's own background.
	reactionCapStyle = lipgloss.NewStyle().Foreground(reactionChipBg)
)

// renderReactions returns a single indented line summarising the post's
// reactions, or an empty string when there are none. Each reaction is
// rendered as `<glyph> N` (e.g. "👍 3"), with the user's own reactions
// highlighted so they can be toggled off by re-selecting them in the
// picker.
func (m *Model) renderReactions(p *model.Post) string {
	if p == nil || p.Metadata == nil || len(p.Metadata.Reactions) == 0 {
		return ""
	}
	type bucket struct {
		count int
		self  bool
	}
	order := []string{}
	buckets := map[string]*bucket{}
	for _, r := range p.Metadata.Reactions {
		if r == nil {
			continue
		}
		b, ok := buckets[r.EmojiName]
		if !ok {
			b = &bucket{}
			buckets[r.EmojiName] = b
			order = append(order, r.EmojiName)
		}
		b.count++
		if m.me != nil && r.UserId == m.me.Id {
			b.self = true
		}
	}
	if len(order) == 0 {
		return ""
	}
	parts := make([]string, 0, len(order))
	for _, name := range order {
		b := buckets[name]
		glyph := emoji.Sprint(":" + name + ":")
		// Shortcodes the emoji library doesn't recognise come back
		// unchanged (still wrapped in colons) — show the original name
		// instead so the user can still see what was reacted with.
		if strings.HasPrefix(glyph, ":") {
			glyph = ":" + name + ":"
		}
		body := fmt.Sprintf("%s %d", glyph, b.count)
		var styled string
		if b.self {
			styled = selfReactionStyle.Render(body)
		} else {
			styled = reactionStyle.Render(body)
		}
		parts = append(parts,
			reactionCapStyle.Render(reactionCapLeft)+
				styled+
				reactionCapStyle.Render(reactionCapRight))
	}
	return "  " + strings.Join(parts, " ")
}

// openReactionPicker enters the picker modal for the given post. The
// caller is responsible for guarding against empty Ids (optimistic
// stubs can't be reacted to until the canonical post lands).
func (m *Model) openReactionPicker(postID string) {
	if postID == "" {
		return
	}
	if len(m.reactionEmojis) == 0 {
		m.status = "no reactions configured in config.yaml"
		return
	}
	m.reactionPickerPostID = postID
	m.reactionPickerIdx = 0
}

// closeReactionPicker tears down picker state without firing anything.
func (m *Model) closeReactionPicker() {
	m.reactionPickerPostID = ""
	m.reactionPickerIdx = 0
}

// findPostByID returns the post with the given Id from either the main
// feed or the open thread (whichever holds it). Returns nil when not
// found — the caller surfaces an error in that case.
func (m *Model) findPostByID(id string) *model.Post {
	for _, p := range m.posts {
		if p.Id == id {
			return p
		}
	}
	for _, p := range m.threadPosts {
		if p.Id == id {
			return p
		}
	}
	return nil
}

// userHasReacted reports whether the current user already reacted to p
// with emojiName, so the picker can toggle add↔remove.
func (m *Model) userHasReacted(p *model.Post, emojiName string) bool {
	if p == nil || p.Metadata == nil || m.me == nil {
		return false
	}
	for _, r := range p.Metadata.Reactions {
		if r != nil && r.UserId == m.me.Id && r.EmojiName == emojiName {
			return true
		}
	}
	return false
}

// applyReactionPick toggles the configured emoji at the picker's
// current index against the picker's post and closes the modal. Returns
// the tea.Cmd that performs the server-side mutation; the WS event will
// arrive shortly after and reconcile local state via applyReactionEvent.
func (m *Model) applyReactionPick() tea.Cmd {
	if m.reactionPickerPostID == "" || m.me == nil {
		return nil
	}
	if m.reactionPickerIdx < 0 || m.reactionPickerIdx >= len(m.reactionEmojis) {
		return nil
	}
	name := m.reactionEmojis[m.reactionPickerIdx]
	postID := m.reactionPickerPostID
	p := m.findPostByID(postID)
	hasIt := m.userHasReacted(p, name)
	m.closeReactionPicker()

	userID := m.me.Id
	if hasIt {
		// Optimistic local removal — the WS event will confirm.
		m.removeLocalReaction(postID, userID, name)
		m.renderMessages()
		m.renderThread()
		return m.removeReactionCmd(userID, postID, name)
	}
	m.addLocalReaction(postID, userID, name)
	m.renderMessages()
	m.renderThread()
	return m.addReactionCmd(userID, postID, name)
}

func (m Model) addReactionCmd(userID, postID, emojiName string) tea.Cmd {
	return func() tea.Msg {
		if err := m.client.AddReaction(m.ctx, userID, postID, emojiName); err != nil {
			return reactionErrMsg{err: err}
		}
		return nil
	}
}

func (m Model) removeReactionCmd(userID, postID, emojiName string) tea.Cmd {
	return func() tea.Msg {
		if err := m.client.RemoveReaction(m.ctx, userID, postID, emojiName); err != nil {
			return reactionErrMsg{err: err}
		}
		return nil
	}
}

type reactionErrMsg struct{ err error }

// addLocalReaction mutates the in-memory post so the chip strip
// reflects the new state before the WS broadcast arrives. The WS
// applier is idempotent (it dedupes by user+emoji) so duplicating the
// work here is safe.
func (m *Model) addLocalReaction(postID, userID, emojiName string) {
	p := m.findPostByID(postID)
	if p == nil {
		return
	}
	if p.Metadata == nil {
		p.Metadata = &model.PostMetadata{}
	}
	for _, r := range p.Metadata.Reactions {
		if r != nil && r.UserId == userID && r.EmojiName == emojiName {
			return
		}
	}
	p.Metadata.Reactions = append(p.Metadata.Reactions, &model.Reaction{
		UserId:    userID,
		PostId:    postID,
		EmojiName: emojiName,
	})
}

func (m *Model) removeLocalReaction(postID, userID, emojiName string) {
	p := m.findPostByID(postID)
	if p == nil || p.Metadata == nil {
		return
	}
	out := p.Metadata.Reactions[:0]
	for _, r := range p.Metadata.Reactions {
		if r != nil && r.UserId == userID && r.EmojiName == emojiName {
			continue
		}
		out = append(out, r)
	}
	p.Metadata.Reactions = out
}

// applyReactionEvent reconciles a `reaction_added` / `reaction_removed`
// WS event with local state. The reaction payload arrives as a
// JSON-encoded string under data["reaction"].
func (m *Model) applyReactionEvent(ev *model.WebSocketEvent, added bool) tea.Cmd {
	raw, ok := ev.GetData()["reaction"].(string)
	if !ok || raw == "" {
		return nil
	}
	var r model.Reaction
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil
	}
	if added {
		m.addLocalReaction(r.PostId, r.UserId, r.EmojiName)
	} else {
		m.removeLocalReaction(r.PostId, r.UserId, r.EmojiName)
	}
	m.renderMessages()
	m.renderThread()
	// Persist so cached reopens render the same reaction set without
	// waiting for a fresh server fetch.
	if p := m.findPostByID(r.PostId); p != nil {
		return m.persistPosts(p)
	}
	return nil
}

// renderReactionPicker draws the modal popup. Layout mirrors the
// delete-confirm dialog: rounded border, centred header, then a list of
// emojis with digit accelerators. The currently-highlighted row is
// reversed; the user's own reactions are marked with a small check.
func (m *Model) renderReactionPicker() string {
	if m.reactionPickerPostID == "" {
		return ""
	}
	outerW := confirmDialogMaxWidth
	if outerW > m.width-4 {
		outerW = m.width - 4
	}
	if outerW < 32 {
		outerW = 32
	}
	inner := outerW - 8
	if inner < 1 {
		inner = 1
	}

	p := m.findPostByID(m.reactionPickerPostID)
	header := lipgloss.NewStyle().
		Width(inner).
		Align(lipgloss.Center).
		Bold(true).
		Render("React")

	hint := lipgloss.NewStyle().
		Width(inner).
		Align(lipgloss.Center).
		Foreground(dimColor).
		Italic(true).
		Render("digit/↵ toggles · ↑/↓ navigates · esc cancels")

	rowStyle := lipgloss.NewStyle()
	cursorStyle := lipgloss.NewStyle().Foreground(focusedColor).Bold(true)
	rows := make([]string, 0, len(m.reactionEmojis))
	for i, name := range m.reactionEmojis {
		glyph := emoji.Sprint(":" + name + ":")
		if strings.HasPrefix(glyph, ":") {
			glyph = ":" + name + ":"
		}
		accel := " "
		if i < 9 {
			accel = fmt.Sprintf("%d", i+1)
		}
		marker := " "
		if m.userHasReacted(p, name) {
			marker = "✓"
		}
		text := fmt.Sprintf("[%s] %s  %s  :%s:", accel, marker, glyph, name)
		if i == m.reactionPickerIdx {
			rows = append(rows, cursorStyle.Render("▸ "+text))
		} else {
			rows = append(rows, rowStyle.Render("  "+text))
		}
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		header,
		hint,
		"",
		strings.Join(rows, "\n"),
	)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(focusedColor).
		Padding(1, 3).
		Render(body)
}

// handleReactionPickerKey owns every keystroke while the picker is
// open. Digit accelerators 1-9 immediately fire; arrow keys navigate;
// enter fires the highlighted entry; esc cancels.
func (m Model) handleReactionPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.closeReactionPicker()
		return m, nil
	case "enter":
		cmd := m.applyReactionPick()
		return m, cmd
	}
	if key.Matches(msg, m.keys.Up) {
		if m.reactionPickerIdx > 0 {
			m.reactionPickerIdx--
		}
		return m, nil
	}
	if key.Matches(msg, m.keys.Down) {
		if m.reactionPickerIdx < len(m.reactionEmojis)-1 {
			m.reactionPickerIdx++
		}
		return m, nil
	}
	// Digit accelerators 1..9 → pick the matching index directly.
	if s := msg.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		idx := int(s[0] - '1')
		if idx < len(m.reactionEmojis) {
			m.reactionPickerIdx = idx
			cmd := m.applyReactionPick()
			return m, cmd
		}
	}
	return m, nil
}

