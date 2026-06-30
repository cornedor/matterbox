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
		m.summary.active() || m.switcherMode || m.keysSheetMode || m.preview.active
}

// popupOpenInComposer mirrors handleKey's popupOpen guard: the @-mention /
// :emoji autocomplete owns ctrl+p/ctrl+n while open, so the global switcher
// chord stands down.
func (m *Model) popupOpenInComposer() bool {
	return m.focus == focusInput && (m.mention.active || m.emoji.active || m.slash.active || m.lang.active)
}

// keyContexts is the precedence ladder, highest first. Order mirrors
// handleKey; contexts_test.go asserts no accidental shadows between any two
// layers that can be reachable for the same keypress.
var keyContexts = []keyContext{
	{
		name:     "modal:delete-confirm",
		active:   func(m *Model) bool { return m.deleteConfirmPostID != "" },
		terminal: true,
		claims:   func(m *Model) []key.Binding { return []key.Binding{m.keys.ConfirmYes, m.keys.ConfirmNo} },
	},
	{
		name:     "modal:reaction-picker",
		active:   func(m *Model) bool { return m.reactionPickerPostID != "" },
		terminal: true,
		typing:   true, // has a free-text search box at its foot
		claims:   func(m *Model) []key.Binding { return []key.Binding{m.keys.InputUp, m.keys.InputDown} },
	},
	{
		name:     "modal:open-picker",
		active:   func(m *Model) bool { return m.openPickerActive() },
		terminal: true,
		claims:   func(m *Model) []key.Binding { return []key.Binding{m.keys.Up, m.keys.Down} },
	},
	{
		name:     "modal:code-picker",
		active:   func(m *Model) bool { return m.codePickerActive() },
		terminal: true,
		claims:   func(m *Model) []key.Binding { return []key.Binding{m.keys.Up, m.keys.Down} },
	},
	{
		name:     "modal:poll-dialog",
		active:   func(m *Model) bool { return m.pollDialog.open },
		terminal: true,
		typing:   true,
		claims:   func(m *Model) []key.Binding { return []key.Binding{m.keys.Tab, m.keys.ShiftTab} },
	},
	{
		name:     "modal:history",
		active:   func(m *Model) bool { return m.historyMode },
		terminal: true,
		claims:   func(m *Model) []key.Binding { return []key.Binding{m.keys.ShowHistory} },
	},
	{
		name:     "modal:summary",
		active:   func(m *Model) bool { return m.summary.active() },
		terminal: true,
		claims:   func(m *Model) []key.Binding { return nil },
	},
	{
		// Keyboard cheatsheet (switcher "> Keys"): esc/q close, arrows scroll.
		// All keys are hardwired in handleKeysSheetKey, so it claims nothing.
		name:     "modal:keys-sheet",
		active:   func(m *Model) bool { return m.keysSheetMode },
		terminal: true,
		claims:   func(m *Model) []key.Binding { return nil },
	},
	{
		// Image-preview modal (space on a message image). The preview key
		// toggles it shut and the list-left/right keys cycle images (esc/q are
		// the hardwired dismiss, handled before claims in handlePreviewKey).
		name:     "modal:image-preview",
		active:   func(m *Model) bool { return m.preview.active },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{m.keys.Preview, m.keys.Left, m.keys.Right}
		},
	},
	{
		name:     "modal:switcher",
		active:   func(m *Model) bool { return m.switcherMode },
		terminal: true,
		typing:   true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{m.keys.Switcher, m.keys.InputUp, m.keys.InputDown, m.keys.OpenChannel}
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
			return []key.Binding{m.keys.ClearFilter, m.keys.ApplyOpen, m.keys.InputUp, m.keys.InputDown}
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
			return []key.Binding{
				m.keys.Send, m.keys.NewLine, m.keys.Paste, m.keys.LeaveInput,
				m.keys.ClearInput, m.keys.Tab, m.keys.ShiftTab,
				m.keys.InputUp, m.keys.InputDown,
			}
		},
	},
	{
		name:     "focus:search",
		active:   func(m *Model) bool { return m.focus == focusSearch },
		terminal: true,
		typing:   true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{m.keys.InputUp, m.keys.InputDown, m.keys.Tab, m.keys.ShiftTab, m.keys.Paste}
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
			return []key.Binding{m.keys.Send, m.keys.NewLine, m.keys.Tab, m.keys.ShiftTab, m.keys.Paste}
		},
	},
	{
		name:     "focus:sqlresults",
		active:   func(m *Model) bool { return m.focus == focusSQLResults },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			// The result list: selection nav + the read-only message actions,
			// reused from the messages pane. Tab/ShiftTab are owned by global:
			// reading above, so they're not claimed here.
			return []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.Home, m.keys.End, m.keys.PageUp, m.keys.PageDown,
				m.keys.OpenAttach, m.keys.Download, m.keys.Preview, m.keys.CopyMD, m.keys.CopyCode,
			}
		},
	},
	{
		name:   "global:reading",
		active: func(m *Model) bool { return m.contentFocus() && !m.inModal() },
		claims: func(m *Model) []key.Binding {
			bs := []key.Binding{
				m.keys.NavTeam, m.keys.NavDM, m.keys.NavFeed,
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
				m.keys.NextHit, m.keys.PrevHit, m.keys.OpenThread, m.keys.ReplyInThread,
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
			}
		},
	},
	{
		// Reference side panel (open-reference key on a message naming a Jira
		// issue or GitLab MR). The open-reference key toggles it shut, ←/→ cycle
		// references, r refetches, o opens it in a browser; esc (hardwired) also
		// closes. Provider keys (Jira s/p/P/a, GitLab A/M) and scrolling fall
		// through to the focused handler / viewport.
		name:     "focus:ref",
		active:   func(m *Model) bool { return m.focus == focusRef },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.Left, m.keys.Right,
				m.keys.OpenRef, m.keys.Refresh, m.keys.OpenAttach,
			}
		},
	},
	{
		// Channel-info side panel (channel-info key on a channel/DM tab). The
		// channel-info key toggles it shut, ↑/↓ move between focusable targets
		// (links + pinned messages), ↵/o activate the selected one; esc
		// (hardwired) also closes. Anything else scrolls the viewport.
		name:     "focus:info",
		active:   func(m *Model) bool { return m.focus == focusInfo },
		terminal: true,
		claims: func(m *Model) []key.Binding {
			return []key.Binding{
				m.keys.Up, m.keys.Down, m.keys.ChannelInfo, m.keys.OpenAttach, m.keys.OpenChannel,
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
				m.keys.Home, m.keys.End, m.keys.OpenChannel, m.keys.MarkRead, m.keys.Refresh,
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
	{"reaction-picker", func(m *Model) { m.reactionPickerPostID = "x" }},
	{"open-picker", func(m *Model) { m.openPickerItems = make([]openable, 1) }},
	{"code-picker", func(m *Model) { m.codePickerBlocks = make([]codeBlock, 1) }},
	{"poll-dialog", func(m *Model) { m.pollDialog.open = true }},
	{"history", func(m *Model) { m.historyMode = true }},
	{"summary", func(m *Model) { m.summary.phase = summaryPicking }},
	{"switcher", func(m *Model) { m.switcherMode = true }},
	{"keys-sheet", func(m *Model) { m.keysSheetMode = true }},
	{"image-preview", func(m *Model) { m.preview.active = true }},
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
