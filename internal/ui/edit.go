package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
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
	m.editingPostID = p.Id
	m.input.Reset()
	m.input.SetValue(p.Message)
	m.input.CursorEnd()
	m.input.SetPromptFunc(2, inputPromptFunc("✎ "))
	m.focus = focusInput
	focusCmd := m.input.Focus()
	m.syncInputHeight()
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
	m.input.Reset()
	m.syncInputHeight()
	m.restoreInputPrompt()
}

// restoreInputPrompt sets the textarea prompt back to its mode-default
// (either thread-reply or normal channel). Called after editing
// completes or is cancelled.
func (m *Model) restoreInputPrompt() {
	if m.threadOpen {
		m.input.SetPromptFunc(2, inputPromptFunc("↳ "))
	} else {
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
