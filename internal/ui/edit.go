package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/replyto"
)

// postEditedMsg lands on the bubbletea loop after a successful
// EditPost. The WS-driven applyPostEdited will also handle the update
// once the broadcast arrives; this message is mostly here to clear
// transient status and to release edit-mode state in the rare case the
// WS event is delayed or dropped.
type postEditedMsg struct {
	post *model.Post
	err  error
}

// beginEditPost puts the UI into edit mode for the given post:
// prepopulates the textarea, switches focus to the input, and updates
// the prompt so the mode is visible in both panes. Caller is
// responsible for asserting the post belongs to the current user.
func (m *Model) beginEditPost(p *model.Post) tea.Cmd {
	if p == nil || p.Id == "" {
		return nil
	}
	m.closeMention()
	m.closeSlash()
	m.closeLang()
	m.closeEffectPopup()
	m.editingPostID = p.Id
	// Adopt the post's own nested-reply target so saving the edit re-attaches it
	// rather than quietly flattening the reply — and so the strip above the
	// composer shows what this message answers while you rewrite it. Clearing it
	// (escape) is then a real, deliberate way to un-nest a reply.
	m.replyParentID, _ = replyto.Parse(p.Message)
	m.input.Reset()
	// Show the markup, not the compiled body: a post sent as \shimmer{today}
	// would otherwise re-open as the bare word trailed by an invisible payload,
	// and any edit to the text would shift the offsets out from under it.
	// Detach first: the parent reference is re-derived on save from
	// m.replyParentID (above), so the old run must not ride along invisibly
	// behind the text the user is about to rewrite.
	m.input.SetValue(decompileEffects(replyto.Detach(p.Message)))
	m.input.CursorEnd()
	// Preview the markup we just put back straight away, rather than waiting for
	// the first keystroke (scheduleGrammarCheck below is a no-op when grammar is
	// off, so it can't be relied on for this).
	m.syncComposerDecorations()
	m.input.SetPromptFunc(2, inputPromptFunc("✎ "))
	m.focus = focusInput
	focusCmd := m.input.Focus()
	m.syncInputHeight()
	// Adopting the post's reply target may have opened the strip above the
	// composer, which costs the transcript a row: re-take the geometry before
	// repainting, since syncInputHeight only reacts to the input's own height.
	m.layoutPanes()
	m.renderMessages()
	m.renderThread()
	// Check the pre-filled text right away so an edited message is underlined
	// without waiting for the first keystroke.
	return tea.Batch(focusCmd, m.scheduleGrammarCheck())
}

// cancelEdit drops the edit-mode state and resets the textarea +
// prompt. It does NOT change focus — the caller decides where to land
// (typically focusMessages or focusThread depending on context).
func (m *Model) cancelEdit() {
	if m.editingPostID == "" {
		return
	}
	m.editingPostID = ""
	m.replyParentID = ""
	m.input.Reset()
	m.syncInputHeight()
	m.restoreInputPrompt()
}

// restoreInputPrompt sets the textarea prompt back to its mode-default: "↪ "
// while the composer is answering one message inside the thread, "↳ " for a
// reply to the thread as a whole, "> " for a channel post. Called after editing
// completes or is cancelled, and whenever the nested-reply target changes.
func (m *Model) restoreInputPrompt() {
	switch {
	case m.threadOpen && m.replyParentID != "":
		m.input.SetPromptFunc(2, inputPromptFunc("↪ "))
	case m.threadOpen:
		m.input.SetPromptFunc(2, inputPromptFunc("↳ "))
	default:
		m.input.SetPromptFunc(2, inputPromptFunc("> "))
	}
}

// editPost issues the PatchPost call. The returned post (or error)
// arrives as postEditedMsg.
func (m Model) editPost(postID, message string) tea.Cmd {
	return func() tea.Msg {
		p, err := m.client.EditPost(m.ctx, postID, message)
		return postEditedMsg{post: p, err: err}
	}
}

// deletePost issues the DeletePost call. We discard the result — the
// `post_deleted` WebSocket event will land via applyPostDeleted and
// drive the local UI update.
func (m Model) deletePost(postID string) tea.Cmd {
	return func() tea.Msg {
		if err := m.client.DeletePost(m.ctx, postID); err != nil {
			return errMsg{err}
		}
		return nil
	}
}
