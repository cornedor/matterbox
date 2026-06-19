package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"
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

	// reactionChipBgStyle sets only the chip background. Used for a custom
	// emoji's image placeholder so the pill colour sits under it without a
	// foreground — the protocol carries the image id in the foreground, which
	// a chip foreground colour would overwrite.
	reactionChipBgStyle = lipgloss.NewStyle().Background(reactionChipBg)
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
		// renderEmojiGlyph yields a unicode glyph, a custom-emoji image
		// placeholder, or the literal :name: (shortcodes nothing recognises),
		// so the user always sees what was reacted with.
		glyph := m.renderEmojiGlyph(name)
		style := reactionStyle
		if b.self {
			style = selfReactionStyle
		}
		var styled string
		if emojiIsPlaceholder(glyph) {
			// Render the placeholder under the chip background only, then the
			// count as a normal styled run so it keeps its colour after the
			// placeholder's [39m foreground reset.
			styled = reactionChipBgStyle.Render(glyph) + style.Render(fmt.Sprintf(" %d", b.count))
		} else {
			styled = style.Render(fmt.Sprintf("%s %d", glyph, b.count))
		}
		parts = append(parts,
			reactionCapStyle.Render(reactionCapLeft)+
				styled+
				reactionCapStyle.Render(reactionCapRight))
	}
	return "  " + strings.Join(parts, " ")
}

// openReactionPicker enters the picker modal for the given post and focuses
// the search box (returning its blink cmd). The caller is responsible for
// guarding against empty Ids (optimistic stubs can't be reacted to until the
// canonical post lands). No configured-reactions guard: the search box can
// reach any emoji even when reactions: is empty in config.yaml.
func (m *Model) openReactionPicker(postID string) tea.Cmd {
	if postID == "" {
		return nil
	}
	m.reactionPickerPostID = postID
	m.reactionPickerIdx = 0
	m.reactionSearch.SetValue("")
	// The "placed reactions" list names each reactor; fetch any reactor we
	// can't name yet so the modal fills them in a frame or two later.
	return tea.Batch(m.reactionSearch.Focus(), m.resolveReactorNames(postID))
}

// resolveReactorNames fetches the usernames of anyone who reacted to the post
// but isn't in the name cache yet, reusing the shared usersResolvedMsg path
// (UsernamesByIDs is singleflight-deduped). Returns nil when every reactor is
// already named — including the common case of no reactions at all.
func (m Model) resolveReactorNames(postID string) tea.Cmd {
	p := m.findPostByID(postID)
	if p == nil || p.Metadata == nil {
		return nil
	}
	var ids []string
	seen := map[string]struct{}{}
	for _, r := range p.Metadata.Reactions {
		if r == nil {
			continue
		}
		// Membership (not emptiness) is the test: a "" entry negatively caches
		// a user the server didn't return, so we don't keep asking.
		if _, known := m.userNames[r.UserId]; known {
			continue
		}
		if _, dup := seen[r.UserId]; dup {
			continue
		}
		seen[r.UserId] = struct{}{}
		ids = append(ids, r.UserId)
	}
	if len(ids) == 0 {
		return nil
	}
	return func() tea.Msg {
		names, err := m.client.UsernamesByIDs(m.ctx, ids)
		if err != nil {
			return usersResolvedMsg{ids: ids, err: err}
		}
		return usersResolvedMsg{ids: ids, users: names}
	}
}

// closeReactionPicker tears down picker state without firing anything.
func (m *Model) closeReactionPicker() {
	m.reactionPickerPostID = ""
	m.reactionPickerIdx = 0
	m.reactionSearch.SetValue("")
	m.reactionSearch.Blur()
}

// reactionPickerNames returns the emoji names currently shown in the picker,
// in display order. With the search box empty that's the configured quick
// list (reactionEmojis); once the user types it's the live matches for the
// query against the full unicode + custom set, ranked by emojiMatches. Both
// the renderer and the apply path index this by reactionPickerIdx so they
// always agree on what the cursor points at.
func (m Model) reactionPickerNames() []string {
	q := strings.ToLower(strings.TrimSpace(m.reactionSearch.Value()))
	if q == "" {
		return m.reactionEmojis
	}
	items := m.emojiMatches(q)
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = it.name
	}
	return names
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

// reactorName resolves a reaction's user id to a short display name for the
// picker's "placed reactions" list. The current user shows as "you"; an id we
// can't name yet (not in the cache, or negatively cached as "") falls back to
// a truncated raw id. openReactionPicker fires resolveReactorNames so these
// self-heal to real usernames a frame or two later.
func (m *Model) reactorName(uid string) string {
	if m.me != nil && uid == m.me.Id {
		return "you"
	}
	if n := m.userNames[uid]; n != "" {
		return n
	}
	if len(uid) > 8 {
		return uid[:8]
	}
	return uid
}

// renderReactionReactors lists each reaction already on the post together with
// the people who placed it, e.g. "👍  alice, you". Reactions are grouped by
// emoji in first-seen order to match the chip strip under the message; returns
// "" when the post carries none. width bounds the wrap of the reactor list.
func (m *Model) renderReactionReactors(p *model.Post, width int) string {
	if p == nil || p.Metadata == nil || len(p.Metadata.Reactions) == 0 {
		return ""
	}
	order := []string{}
	byEmoji := map[string][]string{}
	for _, r := range p.Metadata.Reactions {
		if r == nil {
			continue
		}
		if _, ok := byEmoji[r.EmojiName]; !ok {
			order = append(order, r.EmojiName)
		}
		byEmoji[r.EmojiName] = append(byEmoji[r.EmojiName], m.reactorName(r.UserId))
	}
	if len(order) == 0 {
		return ""
	}
	label := lipgloss.NewStyle().Foreground(dimColor).Italic(true).Render("placed reactions")
	nameStyle := lipgloss.NewStyle().Foreground(dimColor)
	const gutter = "    " // continuation rows align past the "<glyph>  " column
	avail := width - len(gutter)
	if avail < 8 {
		avail = 8
	}
	lines := []string{label}
	for _, name := range order {
		// renderEmojiGlyph may return a Kitty image placeholder, whose bytes
		// must not pass through the wrapper — so wrap only the plain-text
		// reactor list and keep the glyph as a fixed prefix.
		glyph := m.renderEmojiGlyph(name)
		wrapped := strings.Split(ansi.Wrap(strings.Join(byEmoji[name], ", "), avail, ""), "\n")
		lines = append(lines, glyph+"  "+nameStyle.Render(wrapped[0]))
		for _, cont := range wrapped[1:] {
			lines = append(lines, gutter+nameStyle.Render(cont))
		}
	}
	return strings.Join(lines, "\n")
}

// applyReactionPick toggles the configured emoji at the picker's
// current index against the picker's post and closes the modal. Returns
// the tea.Cmd that performs the server-side mutation; the WS event will
// arrive shortly after and reconcile local state via applyReactionEvent.
func (m *Model) applyReactionPick() tea.Cmd {
	if m.reactionPickerPostID == "" || m.me == nil {
		return nil
	}
	names := m.reactionPickerNames()
	if m.reactionPickerIdx < 0 || m.reactionPickerIdx >= len(names) {
		return nil
	}
	name := names[m.reactionPickerIdx]
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
		Render("type to search · ↑/↓ move · ↵ react · esc cancel")

	// While the search box is empty the configured quick list is shown with
	// digit accelerators; once typing, the rows are the live matches and the
	// digits become part of the query, so the accelerator column is dropped.
	searching := strings.TrimSpace(m.reactionSearch.Value()) != ""
	names := m.reactionPickerNames()

	rowStyle := lipgloss.NewStyle()
	cursorStyle := lipgloss.NewStyle().Foreground(focusedColor).Bold(true)
	dimRow := lipgloss.NewStyle().Foreground(dimColor).Italic(true)
	rows := make([]string, 0, len(names)+1)
	if len(names) == 0 {
		empty := "no reactions configured — type to search"
		if searching {
			empty = "no matching emoji"
		}
		rows = append(rows, dimRow.Render("  "+empty))
	}
	for i, name := range names {
		glyph := m.renderEmojiGlyph(name)
		marker := " "
		if m.userHasReacted(p, name) {
			marker = "✓"
		}
		var text string
		if searching {
			text = fmt.Sprintf("%s  %s  :%s:", marker, glyph, name)
		} else {
			accel := " "
			if i < 9 {
				accel = fmt.Sprintf("%d", i+1)
			}
			text = fmt.Sprintf("[%s] %s  %s  :%s:", accel, marker, glyph, name)
		}
		if i == m.reactionPickerIdx {
			rows = append(rows, cursorStyle.Render("▸ "+text))
		} else {
			rows = append(rows, rowStyle.Render("  "+text))
		}
	}

	m.reactionSearch.SetWidth(inner - 2)
	sections := []string{header, hint, ""}
	// When the post already carries reactions, show who placed each one above
	// the pickable list so the user can see the current state at a glance.
	if reactors := m.renderReactionReactors(p, inner); reactors != "" {
		sections = append(sections, reactors, "")
	}
	sections = append(sections,
		strings.Join(rows, "\n"),
		"",
		m.reactionSearch.View(),
	)
	body := lipgloss.JoinVertical(lipgloss.Left, sections...)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(focusedColor).
		Padding(1, 3).
		Render(body)
}

// handleReactionPickerKey owns every keystroke while the picker is open.
// esc cancels, enter fires the highlighted entry, ↑/↓ (and ctrl+p/ctrl+n)
// navigate. With the search box empty, digit accelerators 1-9 immediately
// fire against the configured list; any other printable char starts a search.
// Once searching, every printable key (digits included) feeds the search box
// and the rows become live emoji matches. Navigation uses InputUp/InputDown
// rather than Up/Down so the vim j/k can be typed into the query.
func (m Model) handleReactionPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	names := m.reactionPickerNames()
	empty := strings.TrimSpace(m.reactionSearch.Value()) == ""

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closeReactionPicker()
		return m, nil
	case "enter":
		return m, m.applyReactionPick()
	}
	if key.Matches(msg, m.keys.InputUp) {
		if m.reactionPickerIdx > 0 {
			m.reactionPickerIdx--
		}
		return m, nil
	}
	if key.Matches(msg, m.keys.InputDown) {
		if m.reactionPickerIdx < len(names)-1 {
			m.reactionPickerIdx++
		}
		return m, nil
	}
	// Empty box: digit accelerators 1..9 pick the matching configured entry
	// directly. An out-of-range digit is a no-op rather than starting a search
	// (the intent was clearly to accelerate).
	if empty {
		if s := msg.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			idx := int(s[0] - '1')
			if idx < len(names) {
				m.reactionPickerIdx = idx
				return m, m.applyReactionPick()
			}
			return m, nil
		}
	}
	// Otherwise feed the keystroke to the search box and re-filter. Resetting
	// the cursor to the top on any change keeps it in bounds as matches shrink.
	prev := m.reactionSearch.Value()
	var cmd tea.Cmd
	m.reactionSearch, cmd = m.reactionSearch.Update(msg)
	if m.reactionSearch.Value() != prev {
		m.reactionPickerIdx = 0
	}
	return m, cmd
}
