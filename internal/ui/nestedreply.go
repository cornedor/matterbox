package ui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/hidden"
	"matterbox/internal/replyto"
)

// Nested replies. A Mattermost thread is flat — every reply hangs off the same
// root — so "which of these forty messages are you answering?" is a question the
// wire format cannot ask. matterbox answers it out of band: the reply carries the
// id of the message it answers as invisible bytes (internal/replyto), and this
// file turns those ids back into structure on screen.
//
// The structure is drawn, never re-ordered. Posts stay in the order they were
// sent, exactly as every other client shows them, and a reply says what it
// answers by indenting under it and — when its parent is not the line directly
// above — quoting one dim line of it. Re-ordering into a true tree would move a
// new message away from the bottom of the pane, which is where the eye and the
// unread divider both look for it.

const (
	// nestIndentStep is how many columns one level of nesting costs. Two: the
	// same gutter a post body already carries, so an indented post lines up with
	// the text of the one it answers rather than drifting.
	nestIndentStep = 2
	// nestMaxDepth caps the indent regardless of pane width. Beyond about four
	// levels the tree stops being informative and just eats the message column —
	// deeper replies keep their quote line and share the last indent.
	nestMaxDepth = 4
)

// nestInfo is how one post sits in the reply tree of the pane rendering it.
// The zero value is a post with no parent — an ordinary flat reply — which is
// what almost every post is.
type nestInfo struct {
	// parentID is what the post's hidden payload claims to answer, "" for none.
	// It is set even when the parent isn't on screen, because "answers something
	// you can't see" is itself worth showing.
	parentID string
	// parent is the message it answers, when that message is in this pane.
	parent *model.Post
	// depth is how many ancestors are on screen above it (0 = answers the root
	// or nothing). Uncapped here; the renderer clamps it to the width it has.
	depth int
	// quote asks for the dim "↪ who · what" line above the post. Suppressed when
	// the parent is the line directly above, where the indent already says it.
	quote bool
}

// nested reports whether this post is drawn as a reply to another message.
func (n nestInfo) nested() bool { return n.parentID != "" }

// indent returns the columns this post is drawn in from, given how many levels
// the pane can spare.
func (n nestInfo) indent(maxDepth int) int {
	d := min(n.depth, maxDepth)
	if d < 0 {
		d = 0
	}
	return d * nestIndentStep
}

// nestClaim is one post's claim to be answering another, by index into the pane's
// post slice and the parent id its payload names.
type nestClaim struct {
	i  int
	id string
}

// nestInfos maps posts to their place in the reply tree the hidden parent
// references describe, or returns nil when none of them is a nested reply — the
// overwhelmingly common case, and the reason this is a per-render scan rather
// than cached state. Ruling a post out costs one substring search of its body
// (see replyto.Carries) and no allocation at all, which is what makes it safe on
// the View path; only the few posts that do carry a payload are decoded, and only
// their parents are looked up.
//
// The lookups are linear scans rather than an id index: a pane holds a few
// hundred posts and a handful of nested replies, so a few hundred pointer
// compares beat allocating a map of every post in the window on every render.
func (m *Model) nestInfos(posts []*model.Post) []nestInfo {
	var claims []nestClaim
	for i, p := range posts {
		if p == nil || !replyto.Carries(p.Message) {
			continue
		}
		id, ok := replyto.Parse(p.Message)
		if !ok || id == p.Id {
			continue
		}
		claims = append(claims, nestClaim{i, id})
	}
	if claims == nil {
		return nil
	}
	infos := make([]nestInfo, len(posts))
	for _, c := range claims {
		infos[c.i].parentID = c.id
		j := indexOfPost(posts, c.id)
		if j < 0 || j == c.i {
			// The parent isn't in this pane — scrolled out of the loaded window,
			// or in a channel this pane isn't showing. Say that much: a reply
			// left unmarked would read as an answer to whatever sits above it,
			// which is the ambiguity the feature exists to remove.
			infos[c.i].quote = true
			continue
		}
		infos[c.i].parent = posts[j]
		infos[c.i].quote = j != c.i-1
		infos[c.i].depth = nestDepthAt(j, posts, claims) + 1
	}
	return infos
}

// nestDepthAt counts how many on-screen ancestors sit above the post at index i.
// The walk is bounded by nestMaxDepth+1 so a hand-crafted payload cycle costs a
// few iterations rather than hanging the renderer — past the cap the exact depth
// no longer changes what is drawn.
func nestDepthAt(i int, posts []*model.Post, claims []nestClaim) int {
	for d := range nestMaxDepth + 1 {
		id := claimAt(claims, i)
		if id == "" {
			return d
		}
		j := indexOfPost(posts, id)
		if j < 0 || j == i {
			return d
		}
		i = j
	}
	return nestMaxDepth + 1
}

// claimAt returns the parent id the post at index i claims, or "" if it claims
// none. Linear over the claims, which number in the handful.
func claimAt(claims []nestClaim, i int) string {
	for _, c := range claims {
		if c.i == i {
			return c.id
		}
	}
	return ""
}

// indexOfPost locates a post by id in the pane's slice, or -1.
func indexOfPost(posts []*model.Post, id string) int {
	for i, p := range posts {
		if p != nil && p.Id == id {
			return i
		}
	}
	return -1
}

// maxNestDepth is how many levels of indent a pane this wide can give up
// without squeezing the messages themselves. The thread sidebar is narrow, so
// this is a real constraint rather than a formality.
func maxNestDepth(width int) int {
	d := width / 12
	if d < 1 {
		return 1
	}
	return min(d, nestMaxDepth)
}

// nestQuoteLine is the dim line drawn above a nested reply naming what it
// answers: "↪ alice · we should probably cache that". It quotes the parent
// rather than merely pointing at it because the point of the feature is to be
// readable in place, without the reader having to go hunting up the pane.
//
// The parent's text is taken raw (payload stripped, markdown left as typed) and
// flattened to one line: this is a label, not a second rendering of the message,
// and it must never grow past the row it was given.
func (m *Model) nestQuoteLine(n nestInfo, width int) string {
	if width < 8 {
		return ""
	}
	arrow := replyHintStyle.Render("↪ ")
	if n.parent == nil {
		// The parent is off-screen (scrolled out of the loaded window, or in a
		// channel this pane isn't showing). Say so rather than let the reply read
		// as an answer to whatever happens to sit above it.
		return arrow + replyHintStyle.Render("an earlier message")
	}
	label := m.postAuthorName(n.parent) + " · " + m.nestQuoteText(n.parent)
	return arrow + replyHintStyle.Render(truncate(label, width-2))
}

// nestQuoteText is the one-line gist of the quoted message: its visible text
// with whitespace collapsed, or a stand-in when it has no text of its own.
func (m *Model) nestQuoteText(p *model.Post) string {
	if p.DeleteAt != 0 {
		return "(deleted)"
	}
	if text := strings.Join(strings.Fields(hidden.Strip(p.Message)), " "); text != "" {
		return text
	}
	if len(p.FileIds) > 0 {
		return "(attachment)"
	}
	return "(no text)"
}

// nestFingerprint is the part of a post's render key that nesting owns: the
// indent it draws at and the quote line above it. The parent's UpdateAt is in
// there because the quote shows the parent's text, so editing the parent has to
// re-render every reply quoting it.
func nestFingerprint(n nestInfo, indent int) string {
	if !n.nested() {
		return ""
	}
	var b strings.Builder
	b.WriteString("|N")
	b.WriteString(strconv.Itoa(indent))
	if n.quote {
		b.WriteByte('q')
		b.WriteString(n.parentID)
		if n.parent != nil {
			b.WriteByte(':')
			b.WriteString(strconv.FormatInt(n.parent.UpdateAt, 10))
			b.WriteByte(':')
			b.WriteString(strconv.FormatInt(n.parent.DeleteAt, 10))
		}
	}
	return b.String()
}

// indentLines shifts a rendered post right by n columns. The lines are already
// wrapped to the narrower width, so this only moves them.
func indentLines(lines []string, n int) []string {
	if n <= 0 {
		return lines
	}
	pad := strings.Repeat(" ", n)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = pad + l
	}
	return out
}

// --- composer target -------------------------------------------------------
//
// Choosing what to answer is a two-step gesture, not a mode: put the cursor on
// the message, press the reply key, type. The target is shown above the composer
// and on its prompt for as long as it holds, and escape peels it off before
// escape leaves the composer — a nested reply must never be something you send
// by accident.

// setReplyParent points the composer at the message it should answer and drops
// the user into it. Value receiver like every other key handler: returning a
// *Model would skip Update's post-dispatch reconciliation (focus, selection
// bars, the status published to `matterbox listen`). Answering the thread root is the same thing as an ordinary
// reply, so it clears the target rather than encoding a parent every client
// already knows about.
func (m Model) setReplyParent(p *model.Post) (tea.Model, tea.Cmd) {
	switch {
	case p == nil:
		m.status = "no message selected"
		return m, nil
	case p.Id == "":
		m.status = "message hasn't landed yet"
		return m, nil
	case p.DeleteAt != 0:
		m.status = "message was deleted"
		return m, nil
	case p.Id == m.threadRootID:
		m.replyParentID = ""
		m.status = "replying to the thread"
	default:
		m.replyParentID = p.Id
		m.status = "replying to " + m.postAuthorName(p)
	}
	m.restoreInputPrompt()
	m.resizeMessagesViewport()
	m.resizeInput()
	return m.focusComposer()
}

// clearReplyParent drops the nested-reply target, if there is one, and reports
// whether it did. Callers that own a keystroke (escape) use the report to decide
// whether the keystroke is spent.
func (m *Model) clearReplyParent() bool {
	if m.replyParentID == "" {
		return false
	}
	m.replyParentID = ""
	m.restoreInputPrompt()
	m.resizeMessagesViewport()
	m.resizeInput()
	return true
}

// gotoReplyParent moves the thread selection to the message the selected reply
// answers — the way back up a nested conversation, and the only way to reach a
// parent that has scrolled off.
func (m Model) gotoReplyParent() (tea.Model, tea.Cmd) {
	if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
		m.status = "no message selected"
		return m, nil
	}
	id, ok := replyto.Parse(m.threadPosts[m.threadIdx].Message)
	if !ok {
		m.status = "not a nested reply"
		return m, nil
	}
	for i, p := range m.threadPosts {
		if p.Id == id {
			m.threadIdx = i
			m.renderThread()
			return m, nil
		}
	}
	m.status = "the message this answers isn't loaded"
	return m, nil
}

// attachReplyParent puts the composer's nested-reply target on an outgoing body.
// rootID guards the invariant the reader relies on: a parent reference only ever
// rides a post that is itself a thread reply, and never names the root, which
// RootId already says.
func (m *Model) attachReplyParent(body, rootID string) string {
	if rootID == "" || m.replyParentID == "" || m.replyParentID == rootID {
		return body
	}
	return replyto.Attach(body, m.replyParentID)
}

// threadPostByID finds a post in the open thread. The nested-reply target is
// always one of these, so this stays O(thread) rather than walking the whole
// loaded channel window the way findPostByID does — replyBar asks on every
// render of the pane.
func (m *Model) threadPostByID(id string) *model.Post {
	for _, p := range m.threadPosts {
		if p.Id == id {
			return p
		}
	}
	return nil
}

// editRootID is the thread root of the post being edited, or "" when that post
// isn't a reply at all. The edit path needs it because the composer's own
// thread state describes where the user is standing, not where the post being
// edited lives — they diverge as soon as someone edits a channel post with a
// thread open.
func (m *Model) editRootID(postID string) string {
	if p := m.findPostByID(postID); p != nil {
		return p.RootId
	}
	return ""
}

// replyBarHeight is the row the "↪ replying to …" strip costs above the
// composer, or 0 when nothing is targeted. Kept in step with replyBar's own
// guard — the layout reserves the row exactly when the render draws it.
func (m *Model) replyBarHeight() int {
	if m.replyParentID == "" || !m.threadOpen {
		return 0
	}
	return 1
}

// replyBar is the strip drawn directly above the composer while a nested reply
// is being written. It names the message being answered and how to back out,
// because the target survives across keystrokes and the cost of forgetting it is
// a reply nested under the wrong thing.
func (m *Model) replyBar(width int) string {
	if m.replyBarHeight() == 0 {
		return ""
	}
	p := m.threadPostByID(m.replyParentID)
	label := "a message"
	if p != nil {
		label = m.postAuthorName(p) + " · " + m.nestQuoteText(p)
	}
	head, hint := "↪ replying to ", "  esc to cancel"
	avail := width - lipgloss.Width(head)
	// On a narrow pane the escape hint is the first thing to go: what the reply
	// will hang under has to stay readable, and the strip is one row either way.
	if avail-lipgloss.Width(hint) >= 12 {
		avail -= lipgloss.Width(hint)
	} else {
		hint = ""
	}
	if avail < 1 {
		return replyTargetStyle.Render(truncate(head, width))
	}
	// Truncated before styling, so the cut can't land inside an escape sequence.
	return replyTargetStyle.Render(head) + replyHintStyle.Render(truncate(label, avail)+hint)
}
