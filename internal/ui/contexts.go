package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
)

// keyContext is one rung of handleKey's precedence ladder, declared as data
// so the routing it implements can be audited by the shadowing test and the
// help view rather than living only in the nesting order of handleKey.
//
// The table below mirrors handleKey exactly. handleKey still calls the real
// per-focus handler functions (they carry state logic a generic dispatch
// table couldn't) — this is the declared spec of which layer owns which key,
// kept honest by contexts_test.go.
type keyContext struct {
	// name is a stable label for the layer (used in test failures + help).
	name string
	// active reports whether this layer is live for the given model state.
	active func(*Model) bool
	// claims returns the bindings this layer consumes when active. For a
	// terminal layer (a modal or a text input) these are the keys it handles
	// *before* swallowing the rest; for a pass-through global it's the full
	// set it owns.
	claims func(*Model) []key.Binding
	// typing marks a layer backed by a free-text input (composer, search box,
	// filter, switcher, poll dialog). Layers reachable above a typing layer
	// are the only ones that may shadow the input's emacs editing keys, and
	// then only via the shadows whitelist.
	typing bool
	// terminal marks a layer that consumes every remaining key when active
	// (modals + typing inputs). Lower layers are unreachable while a terminal
	// layer is active — so the walk stops at the first active terminal layer.
	terminal bool
	// shadows lists key strings this layer is *allowed* to take from a lower
	// (or editing) layer — reviewed, intentional shadows. Anything not listed
	// here that collides is a bug the shadowing test fails on.
	shadows []string
}

// hardwired declares keys a handler matches literally rather than through the
// registry — a modal's esc/q dismiss, a form's tab/enter, a picker's digit
// accelerators. Nothing rebinds them, but a layer still *consumes* them, so
// they belong in claims(): that is what keeps the shadow audit and the
// cheatsheet honest about which keys a layer swallows.
func hardwired(desc string, keys ...string) key.Binding {
	// The label folds a digit run ("1…9") so an accelerator row reads as one
	// token in the footer instead of "1/2".
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(prettyKeyLabel(foldDigitRun(keys)), desc))
}

// viewportScrollKeys are the scroll keys a popup's viewport handles once its
// own keys have had their say (bubbles' viewport default keymap).
var viewportScrollKeys = hardwired("scroll", "up", "k", "down", "j", "pgup", "pgdown", "b", "f", "u", "d")

// digitKeys is the "1".."9" accelerator row the pickers offer.
var digitKeys = []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}

// contentFocus reports whether the model is in one of the reading/content
// focuses (where the global:reading layer and the per-pane handlers run).
func (m *Model) contentFocus() bool {
	switch m.focus {
	case focusMessages, focusThread, focusRef, focusInfo, focusAttachments, focusTeams, focusFeed, focusSQLResults:
		return true
	}
	return false
}

// inModal reports whether any of the fully-modal overlays is up. Used by the
// pass-through globals' active predicates so the table's reachability matches
// handleKey (which returns at the modal before ever reaching them).
func (m *Model) inModal() bool {
	return m.deleteConfirmPostID != "" || m.reactionPickerPostID != "" ||
		m.openPickerActive() || m.codePickerActive() || m.pollDialog.open || m.historyMode ||
		m.summary.active() || m.switcherMode || m.keysSheetMode || m.textPopup.active ||
		m.templatePicker.active || m.savedPosts.active || m.kaomojiPicker.active || m.preview.active ||
		m.createChan != nil || m.chanEdit != nil || m.chanConfirm != nil || m.joinChan != nil ||
		m.gorillas.active || m.kurve.active || m.stl.active || m.keyDebugMode ||
		m.jiraPicker.active || m.jiraPointsActive || m.jiraCommentActive ||
		m.refConfirm.active || m.linkConfirm.active
}

// yesNoConfirm reports whether one of the three y/n confirmations is up: the
// forge approve/merge check, the non-web link warning, and the channel
// archive/leave/privacy check. They share a handler shape, so they share a
// context row.
func (m *Model) yesNoConfirm() bool {
	return m.refConfirm.active || m.linkConfirm.active || m.chanConfirm != nil
}

// channelForm reports whether one of the channel modals raised from the "> "
// palette is up: the create form, the edit form, or the join catalogue. All
// three are tab-through forms over a text input.
func (m *Model) channelForm() bool {
	return m.createChan != nil || m.chanEdit != nil || m.joinChan != nil
}

// popupOpenInComposer mirrors handleKey's popupOpen guard: the @-mention /
// :emoji autocomplete owns ctrl+p/ctrl+n while open, so the global switcher
// chord stands down.
func (m *Model) popupOpenInComposer() bool {
	return m.focus == focusInput && (m.mention.active || m.emoji.active || m.slash.active || m.lang.active || m.effectPopup.active)
}

// keyContexts is the precedence ladder, highest first. Order mirrors
// handleKey; contexts_test.go asserts no accidental shadows between any two
// layers that can be reachable for the same keypress.
var keyContexts = []keyContext{
	{
		// Key inspector ("> Debug: key inspector"): echoes every decoded
		// keystroke instead of acting on it, so it consumes the lot.
		name:     "modal:key-debug",
		active:   func(m *Model) bool { return m.keyDebugMode },
		terminal: true,
		claims:   func(m *Model) []key.Binding { return []key.Binding{hardwired("close", "esc")} },
	},
	{
		// An open game owns every key — it is a game. Its own controls are
		// drawn on its board rather than listed here.
		name:     "modal:game",
		active:   func(m *Model) bool { return m.gorillas.active || m.kurve.active },
		terminal: true,
		claims:   func(m *Model) []key.Binding { return []key.Binding{hardwired("quit the game", "esc", "q")} },
	},
	{
		// The 3D viewer for an .stl attachment. Like a game it owns every key —
		// the arrows, hjkl, the axis snaps and the zoom keys are the interface,
		// and they are drawn on the viewer rather than listed here.
		name:     "modal:stl-view",
		active:   func(m *Model) bool { return m.stl.active },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				hardwired("orbit", "up", "down", "left", "right", "h", "j", "k", "l"),
				hardwired("pan", "shift+up", "shift+down", "shift+left", "shift+right", "H", "J", "K", "L"),
				hardwired("zoom", "+", "=", "-", "_", "pgup", "pgdown"),
				hardwired("standard views", "x", "y", "z"),
				hardwired("reset the view", "r", "0"),
				hardwired("fit to the frame", "f"),
				hardwired("spin the turntable", "s"),
				hardwired("next / previous model", "n", "p", "tab", "shift+tab"),
				m.keys.Preview, // the key that opened it closes it
				hardwired("close", "esc", "q"),
			}
		},
	},
	{
		name:     "modal:delete-confirm",
		active:   func(m *Model) bool { return m.deleteConfirmPostID != "" },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{m.keys.ConfirmYes, m.keys.ConfirmNo, hardwired("cancel", "esc", "q")}
		},
	},
	{
		name:     "modal:reaction-picker",
		active:   func(m *Model) bool { return m.reactionPickerPostID != "" },
		terminal: true,
		typing:   true, // has a free-text search box at its foot
		claims: func(m *Model) []key.Binding {
			// The digit accelerators only fire while the search box is empty;
			// once it holds a query every printable key feeds the search.
			return []key.Binding{
				m.keys.InputUp, m.keys.InputDown,
				hardwired("react with the highlighted emoji", "enter"),
				hardwired("pick from the configured list", digitKeys...),
				hardwired("close", "esc"),
			}
		},
	},
	{
		// Jira field pickers (status / priority / assignee). The assignee list
		// filters as you type, so it navigates with ↑/↓ + ctrl+p/ctrl+n; the
		// short fixed lists also take j/k and the digit accelerators.
		name:     "modal:jira-picker",
		active:   func(m *Model) bool { return m.jiraPicker.active },
		terminal: true,
		typing:   true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.InputUp, m.keys.InputDown,
				hardwired("apply", "enter"),
				hardwired("pick from the list", digitKeys...),
				hardwired("cancel", "esc"),
			}
		},
	},
	{
		name:     "modal:jira-points",
		active:   func(m *Model) bool { return m.jiraPointsActive },
		terminal: true,
		typing:   true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{hardwired("save the points", "enter"), hardwired("cancel", "esc")}
		},
	},
	{
		name:     "modal:jira-comment",
		active:   func(m *Model) bool { return m.jiraCommentActive },
		terminal: true,
		typing:   true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{m.keys.NewLine, hardwired("post the comment", "enter"), hardwired("cancel", "esc")}
		},
	},
	{
		// The three y/n confirmations: a forge approve/merge, the non-web link
		// warning, and the channel archive/leave/privacy check.
		name:     "modal:confirm",
		active:   func(m *Model) bool { return m.yesNoConfirm() },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{hardwired("confirm", "y", "Y", "enter"), hardwired("cancel", "n", "N", "esc")}
		},
	},
	{
		name:     "modal:open-picker",
		active:   func(m *Model) bool { return m.openPickerActive() },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				m.keys.Up, m.keys.Down,
				hardwired("open the highlighted target", "enter"),
				hardwired("open by number", digitKeys...),
				hardwired("close", "esc", "q"),
			}
		},
	},
	{
		name:     "modal:code-picker",
		active:   func(m *Model) bool { return m.codePickerActive() },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				m.keys.Up, m.keys.Down,
				hardwired("copy the highlighted block", "enter"),
				hardwired("copy by number", digitKeys...),
				hardwired("close", "esc", "q"),
			}
		},
	},
	{
		name:     "modal:poll-dialog",
		active:   func(m *Model) bool { return m.pollDialog.open },
		terminal: true,
		typing:   true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{m.keys.Tab, m.keys.ShiftTab, hardwired("submit", "enter"), hardwired("cancel", "esc")}
		},
	},
	{
		// Channel modals raised from the "> " palette: the create form, the
		// edit form (rename / purpose / header) and the join catalogue.
		name:     "modal:channel-form",
		active:   func(m *Model) bool { return m.channelForm() },
		terminal: true,
		typing:   true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				hardwired("next field / row", "tab", "down", "ctrl+n"),
				hardwired("previous field / row", "shift+tab", "up", "ctrl+p"),
				hardwired("submit", "enter"),
				hardwired("cancel", "esc"),
			}
		},
	},
	// The list sheets (saved messages, templates, kaomoji) each own every
	// keystroke while open: ↑/↓ move, enter picks, esc/q close (hardwired).
	{
		name:     "modal:saved-posts",
		active:   func(m *Model) bool { return m.savedPosts.active },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.InputUp, m.keys.InputDown, m.keys.OpenChannel,
				m.keys.SheetRemove, hardwired("close", "esc", "q"),
			}
		},
	},
	{
		name:     "modal:template-picker",
		active:   func(m *Model) bool { return m.templatePicker.active },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.InputUp, m.keys.InputDown, m.keys.OpenChannel,
				m.keys.SheetRemove, hardwired("close", "esc", "q"),
			}
		},
	},
	{
		name:     "modal:kaomoji-picker",
		active:   func(m *Model) bool { return m.kaomojiPicker.active },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.InputUp, m.keys.InputDown, m.keys.OpenChannel,
				hardwired("close", "esc", "q"),
			}
		},
	},
	{
		name:     "modal:history",
		active:   func(m *Model) bool { return m.historyMode },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{m.keys.ShowHistory, viewportScrollKeys, hardwired("close", "esc", "q")}
		},
	},
	{
		// Channel summary: a duration picker, then the running / result view.
		name:     "modal:summary",
		active:   func(m *Model) bool { return m.summary.active() },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			// The picker matches its keys literally (it is a numeric field, not
			// a list), so they're declared as they are typed.
			return []key.Binding{
				hardwired("previous / next field", "left", "h", "shift+tab", "right", "l", "tab"),
				hardwired("adjust the field", "up", "k", "+", "=", "down", "j", "-", "_"),
				hardwired("type a value", append(append([]string{"0"}, digitKeys...), "backspace")...),
				hardwired("summarize", "enter"),
				hardwired("fold / unfold the thinking section", "t"),
				hardwired("close", "esc", "q"),
			}
		},
	},
	{
		// Keyboard cheatsheet (switcher "> Keys"): esc/q close, arrows scroll.
		// All keys are hardwired in handleKeysSheetKey, so it claims nothing.
		name:     "modal:keys-sheet",
		active:   func(m *Model) bool { return m.keysSheetMode },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{viewportScrollKeys, hardwired("close", "esc", "q")}
		},
	},
	{
		name:     "modal:text-popup",
		active:   func(m *Model) bool { return m.textPopup.active },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{viewportScrollKeys, hardwired("close", "esc", "q")}
		},
	},
	{
		// Image-preview modal (space on a message image). The preview key
		// toggles it shut and the list-left/right keys cycle images (esc/q are
		// the hardwired dismiss, handled before claims in handlePreviewKey).
		name:     "modal:image-preview",
		active:   func(m *Model) bool { return m.preview.active },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{m.keys.Preview, m.keys.Left, m.keys.Right, hardwired("close", "esc", "q")}
		},
	},
	{
		name:     "modal:switcher",
		active:   func(m *Model) bool { return m.switcherMode },
		terminal: true,
		typing:   true,
		claims: func(m *Model) []key.Binding {
			// q types into the query box here, so esc alone dismisses.
			return []key.Binding{
				m.keys.Switcher, m.keys.InputUp, m.keys.InputDown, m.keys.OpenChannel,
				hardwired("cancel", "esc"),
			}
		},
	},
	{
		name:   "global:switcher-chord",
		active: func(m *Model) bool { return !m.inModal() && !m.popupOpenInComposer() },
		claims: func(m *Model) []key.Binding { return []key.Binding{m.keys.Switcher} },
		// The switcher owns ctrl+p everywhere; the input_up arms below it bind
		// ctrl+p too, a deliberate (whitelisted) shadow.
		shadows: []string{"ctrl+p"},
	},
	{
		// f1 opens the "> " command palette from anywhere — a function key can't
		// collide with composing, so it needs no typing guard.
		name:   "global:command-picker",
		active: func(m *Model) bool { return !m.inModal() },
		claims: func(m *Model) []key.Binding { return []key.Binding{m.keys.CommandPicker} },
	},
	{
		// alt+1…9 jumps to a team from ANY focus, the composer included: no
		// alt+digit is an editing key, so handleKey dispatches it above the
		// typing guards (unlike alt+d / alt+u, which live in global:reading).
		name:   "global:team-jump",
		active: func(m *Model) bool { return !m.inModal() },
		claims: func(m *Model) []key.Binding { return []key.Binding{m.keys.NavTeam} },
	},
	{
		name:   "global:nav",
		active: func(m *Model) bool { return !m.inModal() },
		claims: func(m *Model) []key.Binding {
			var bs []key.Binding
			for _, r := range m.keys.navRoutes {
				if len(r.arrow.Keys()) > 0 {
					bs = append(bs, r.arrow)
				}
				// The vim keys are claimed at this global layer only when
				// vim_nav is "global"; in "reading" mode they live in the
				// global:reading layer (below the typing inputs) instead.
				if m.vimNav == vimNavGlobal {
					bs = append(bs, r.vim)
				}
			}
			return bs
		},
		// With vim_nav=global the vim keys ride above the composer; ctrl+h /
		// ctrl+k are emacs editing keys, so this shadow is the documented
		// decision (the user can pick vim_nav=reading to get them back).
		shadows: []string{"ctrl+h", "ctrl+j", "ctrl+k", "ctrl+l"},
	},
	{
		name:     "mode:filter",
		active:   func(m *Model) bool { return m.filterMode },
		terminal: true,
		typing:   true,
		claims: func(m *Model) []key.Binding {
			// esc here is cancel_edit (what handleFilterKey matches), not the
			// reading layer's clear_filter — same key, different owner.
			return []key.Binding{m.keys.CancelEdit, m.keys.ApplyOpen, m.keys.InputUp, m.keys.InputDown}
		},
	},
	{
		name:     "focus:input",
		active:   func(m *Model) bool { return m.focus == focusInput },
		terminal: true,
		typing:   true,
		claims: func(m *Model) []key.Binding {
			// InputUp/InputDown are claimed by the mention/emoji popups; the
			// rest are the composer's own keys.
			bs := []key.Binding{
				m.keys.Send, m.keys.NewLine, m.keys.Paste, m.keys.LeaveInput,
				m.keys.ClearInput, m.keys.Undo, m.keys.Redo, m.keys.Tab, m.keys.ShiftTab,
				m.keys.InputUp, m.keys.InputDown,
			}
			// The grammar popup's key only exists when the checker is on.
			if m.grammarEnabled() {
				bs = append(bs, hardwired("grammar suggestions", "alt+g"))
			}
			return bs
		},
	},
	{
		name:     "focus:search",
		active:   func(m *Model) bool { return m.focus == focusSearch },
		terminal: true,
		typing:   true,
		claims: func(m *Model) []key.Binding {
			// pgup/pgdn scroll the results, but PageUp's ctrl+u alias is an
			// editing key the query box needs, so they stay hardwired here.
			return []key.Binding{
				m.keys.InputUp, m.keys.InputDown, m.keys.ApplyOpen,
				m.keys.Tab, m.keys.ShiftTab, m.keys.Paste,
				hardwired("scroll results", "pgup", "pgdown"),
				hardwired("clear the query / leave", "esc"),
			}
		},
	},
	{
		name:     "focus:sql",
		active:   func(m *Model) bool { return m.focus == focusSQL },
		terminal: true,
		typing:   true,
		claims: func(m *Model) []key.Binding {
			// The multi-line editor owns enter (run) and the newline keys; Tab/
			// ShiftTab cycle focus, Paste pulls the clipboard. Everything else is
			// raw typing into the textarea.
			return []key.Binding{
				m.keys.Send, m.keys.NewLine, m.keys.Tab, m.keys.ShiftTab, m.keys.Paste,
				hardwired("scroll results", "pgup", "pgdown"),
				hardwired("clear results / leave", "esc"),
			}
		},
	},
	{
		name:   "global:reading",
		active: func(m *Model) bool { return m.contentFocus() && !m.inModal() },
		claims: func(m *Model) []key.Binding {
			bs := []key.Binding{
				m.keys.NavDM, m.keys.NavFeed,
				m.keys.Search, m.keys.Compose,
				m.keys.Tab, m.keys.ShiftTab, m.keys.Help, m.keys.Quit,
				m.keys.SearchHere, m.keys.Filter, m.keys.MoveTeamLeft, m.keys.MoveTeamRight,
				m.keys.ClearFilter, // esc: close thread / clear filter, before the focus panes
			}
			// In vim_nav=reading the vim keys navigate from this layer (out of
			// the text inputs above), so list them here for the shadow audit.
			if m.vimNav == vimNavReading {
				for _, r := range m.keys.navRoutes {
					bs = append(bs, r.vim)
				}
			}
			return bs
		},
		// esc and < / > are handled here before the per-pane handlers (which
		// also bind them); same effect, an intentional shadow.
		shadows: []string{"esc", "<", ">"},
	},
	{
		name:     "focus:messages",
		active:   func(m *Model) bool { return m.focus == focusMessages },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.Home, m.keys.End, m.keys.PageUp, m.keys.PageDown,
				m.keys.NextHit, m.keys.PrevHit, m.keys.PrevOwnMsg, m.keys.OpenThread, m.keys.ReplyInThread,
				m.keys.EditPost, m.keys.DeletePost, m.keys.OpenAttach, m.keys.Download, m.keys.OpenRef, m.keys.Preview, m.keys.CopyMD,
				m.keys.CopyCode, m.keys.ShowHistory, m.keys.React, m.keys.Collapse, m.keys.ChannelInfo,
			}
		},
	},
	{
		name:     "focus:thread",
		active:   func(m *Model) bool { return m.focus == focusThread },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.Home, m.keys.End, m.keys.OpenAttach, m.keys.Download, m.keys.OpenRef, m.keys.Preview,
				m.keys.CopyMD, m.keys.CopyCode, m.keys.ShowHistory, m.keys.EditPost, m.keys.DeletePost, m.keys.React, m.keys.Collapse,
				m.keys.CloseThread, m.keys.ReplyInThread, m.keys.GotoParent,
			}
		},
	},
	{
		// Reference side panel (open-reference key on a message naming a Jira
		// issue or forge change request). The open-reference key toggles it shut, ←/→ cycle
		// references, r refetches, o opens it in a browser; esc (hardwired) also
		// closes. Provider keys (Jira s/p/P/a, forge A/M) and scrolling fall
		// through to the focused handler / viewport.
		name:     "focus:ref",
		active:   func(m *Model) bool { return m.focus == focusRef },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			bs := []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.Left, m.keys.Right,
				m.keys.OpenRef, m.keys.Refresh, m.keys.OpenAttach,
				hardwired("close", "esc"),
			}
			// Provider keys act only once the panel has loaded the issue / change;
			// before that they fall through to scrolling it.
			bs = append(bs,
				m.keys.JiraStatus, m.keys.JiraPriority, m.keys.JiraPoints, m.keys.JiraAssignee,
				m.keys.JiraComment, m.keys.JiraReply,
				m.keys.RefApprove, m.keys.RefMerge, m.keys.RefJobs,
			)
			return bs
		},
	},
	{
		// Channel-info side panel (channel-info key on a channel/DM tab). The
		// channel-info key toggles it shut, ↑/↓ move between focusable targets
		// (links + pinned messages), ↵/o activate the selected one; esc
		// (hardwired) also closes. Anything else scrolls the viewport.
		name:     "focus:info-media",
		active:   func(m *Model) bool { return m.focus == focusInfo && m.infoMode == infoModeMedia },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.ChannelInfo, m.keys.OpenAttach, m.keys.OpenChannel,
				m.keys.Preview, m.keys.Download, hardwired("back to the info panel", "esc"),
			}
		},
	},
	{
		name:     "focus:info",
		active:   func(m *Model) bool { return m.focus == focusInfo },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.ChannelInfo, m.keys.OpenAttach, m.keys.OpenChannel,
				hardwired("close", "esc"),
			}
		},
	},
	{
		// The result list sits below global:reading like the other reading panes
		// (handleKey reaches it through the focus switch at the foot).
		name:     "focus:sqlresults",
		active:   func(m *Model) bool { return m.focus == focusSQLResults },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			// Selection nav + the read-only message actions, reused from the
			// messages pane. Tab/ShiftTab are owned by global:reading above, so
			// they're not claimed here.
			return []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.Home, m.keys.End, m.keys.PageUp, m.keys.PageDown,
				m.keys.OpenAttach, m.keys.Download, m.keys.Preview, m.keys.CopyMD, m.keys.CopyCode,
				hardwired("back to the editor", "esc"),
			}
		},
	},
	{
		name:     "focus:attachments",
		active:   func(m *Model) bool { return m.focus == focusAttachments },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{m.keys.Left, m.keys.Right, m.keys.Home, m.keys.End, m.keys.OpenAttach, m.keys.AttachRemove}
		},
	},
	{
		name:     "focus:teams",
		active:   func(m *Model) bool { return m.focus == focusTeams },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			// MoveTeamLeft/Right (< >) are owned by global:reading above; the
			// pane's own arms for them are shadowed, so they're not claimed here.
			// Bare ←/→ no longer switch teams (ctrl+←/→ does), so only ↑/↓ (drop
			// into the body) and enter (LoadTeam) are claimed.
			return []key.Binding{m.keys.Up, m.keys.Down, m.keys.LoadTeam}
		},
	},
	{
		name:     "focus:feed",
		active:   func(m *Model) bool { return m.focus == focusFeed },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			// Tab/ShiftTab are owned by global:reading above; not claimed here.
			// Bare ←/→ no longer switch teams here (ctrl+←/→ does).
			return []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.InputUp, m.keys.InputDown,
				m.keys.Home, m.keys.End, m.keys.PageUp, m.keys.PageDown,
				m.keys.OpenChannel, m.keys.MarkRead, m.keys.MarkAllRead,
				m.keys.Refresh, m.keys.FeedMuted, m.keys.FeedReply,
				hardwired("back to the tab strip", "esc"),
			}
		},
	},
}

// shadowProbeStates enumerates a representative model state per context so
// every layer's reachability is exercised — both by the shadowing test and by
// the startup conflict check that validates user overrides.
var shadowProbeStates = []struct {
	name  string
	apply func(*Model)
}{
	{"input", func(m *Model) { m.focus = focusInput }},
	{"input+popup", func(m *Model) { m.focus = focusInput; m.mention.active = true }},
	{"search", func(m *Model) { m.focus = focusSearch }},
	{"sql", func(m *Model) { m.focus = focusSQL }},
	{"sqlresults", func(m *Model) { m.focus = focusSQLResults }},
	{"messages", func(m *Model) { m.focus = focusMessages }},
	{"thread", func(m *Model) { m.focus = focusThread; m.threadOpen = true }},
	{"ref", func(m *Model) { m.focus = focusRef; m.refOpen = true }},
	{"info", func(m *Model) { m.focus = focusInfo; m.infoOpen = true }},
	{"attachments", func(m *Model) { m.focus = focusAttachments }},
	{"teams", func(m *Model) { m.focus = focusTeams }},
	{"feed", func(m *Model) { m.focus = focusFeed }},
	{"filter", func(m *Model) { m.focus = focusMessages; m.filterMode = true }},
	{"delete-confirm", func(m *Model) { m.deleteConfirmPostID = "x" }},
	{"key-debug", func(m *Model) { m.keyDebugMode = true }},
	{"game", func(m *Model) { m.gorillas.active = true }},
	{"jira-picker", func(m *Model) { m.jiraPicker.active = true }},
	{"jira-points", func(m *Model) { m.jiraPointsActive = true }},
	{"jira-comment", func(m *Model) { m.jiraCommentActive = true }},
	{"confirm", func(m *Model) { m.linkConfirm.active = true }},
	{"channel-form", func(m *Model) { m.createChan = &createChannelState{} }},
	{"reaction-picker", func(m *Model) { m.reactionPickerPostID = "x" }},
	{"open-picker", func(m *Model) { m.openPickerItems = make([]openable, 1) }},
	{"code-picker", func(m *Model) { m.codePickerBlocks = make([]codeBlock, 1) }},
	{"poll-dialog", func(m *Model) { m.pollDialog.open = true }},
	{"history", func(m *Model) { m.historyMode = true }},
	{"saved-posts", func(m *Model) { m.savedPosts.active = true }},
	{"template-picker", func(m *Model) { m.templatePicker.active = true }},
	{"kaomoji-picker", func(m *Model) { m.kaomojiPicker.active = true }},
	{"summary", func(m *Model) { m.summary.phase = summaryPicking }},
	{"switcher", func(m *Model) { m.switcherMode = true }},
	{"keys-sheet", func(m *Model) { m.keysSheetMode = true }},
	{"text-popup", func(m *Model) { m.textPopup.active = true }},
	{"image-preview", func(m *Model) { m.preview.active = true }},
	{"stl-view", func(m *Model) { m.stl.active = true }},
}

// keySet flattens a binding slice to the set of key strings it matches.
func keySet(bs []key.Binding) map[string]bool {
	out := map[string]bool{}
	for _, b := range bs {
		for _, k := range b.Keys() {
			out[k] = true
		}
	}
	return out
}

func inList(list []string, k string) bool {
	for _, s := range list {
		if s == k {
			return true
		}
	}
	return false
}

// actionsForKey returns the ids of every action whose binding includes key k,
// used to name the actions involved in a conflict.
func actionsForKey(km keyMap, k string) []string {
	var out []string
	for _, def := range actionDefs {
		for _, kk := range def.field(&km).Keys() {
			if kk == k {
				out = append(out, def.id)
				break
			}
		}
	}
	return out
}

// keyConflict is a non-whitelisted collision: a key claimed by two layers
// reachable for the same keypress, bound to two different actions.
type keyConflict struct {
	key     string
	actions []string
	state   string
}

func (c keyConflict) err() error {
	if len(c.actions) >= 2 {
		return fmt.Errorf("keybinding %q is bound to conflicting actions (%s) that are active together (in the %q context)",
			c.key, strings.Join(c.actions, " and "), c.state)
	}
	return fmt.Errorf("keybinding %q collides in the %q context", c.key, c.state)
}

// firstKeyConflict runs the shadowing audit over the merged keymap and returns
// the first non-whitelisted cross-layer collision, or nil. Used at startup to
// reject a user override that shadows another action. The default keymap is
// conflict-free (TestNoAccidentalShadows), so this only fires on overrides.
func firstKeyConflict(km keyMap, vimNav vimNavMode) *keyConflict {
	for _, st := range shadowProbeStates {
		m := Model{keys: km, vimNav: vimNav}
		st.apply(&m)
		reach := reachableContexts(&m)
		sets := make([]map[string]bool, len(reach))
		for i, c := range reach {
			sets[i] = keySet(c.claims(&m))
		}
		for hi := 0; hi < len(reach); hi++ {
			for lo := hi + 1; lo < len(reach); lo++ {
				for k := range sets[lo] {
					if sets[hi][k] && !inList(reach[hi].shadows, k) {
						return &keyConflict{key: k, actions: actionsForKey(km, k), state: st.name}
					}
				}
			}
		}
	}
	return nil
}

// reachableContexts returns the contexts that can fire for a keypress in the
// model's current state, highest precedence first: every active context down
// to and including the first active terminal layer (which swallows the rest).
// Pass-through globals (switcher-chord, nav, reading) don't terminate, so they
// stack above the terminal focus/modal that follows.
func reachableContexts(m *Model) []keyContext {
	var out []keyContext
	for _, c := range keyContexts {
		if !c.active(m) {
			continue
		}
		out = append(out, c)
		if c.terminal {
			break
		}
	}
	return out
}
