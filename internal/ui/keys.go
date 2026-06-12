package ui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"

	"matterbox/internal/config"
)

// keyMap holds every user-facing keybinding. Bindings are reused by both
// the help bubble (for rendering) and the focused handlers (for matching),
// so the rendered shortcuts and the actual behaviour can't drift apart.
//
// The struct is the runtime representation; it is *built from* the
// actionDefs table below (plus the nav modifier and, later, user
// overrides) rather than hand-written — so every action has exactly one
// source of truth for its keys, help description, and footer priority.
type keyMap struct {
	// Pane navigation
	Tab      key.Binding
	ShiftTab key.Binding

	// List movement (reading panes: ↑/k, ↓/j)
	Up    key.Binding
	Down  key.Binding
	Left  key.Binding
	Right key.Binding
	Home  key.Binding
	End   key.Binding

	// List movement for lists attached to a text input (switcher, search,
	// feed, mention/emoji popups, filter): ↑/ctrl+p, ↓/ctrl+n. Typed k/j must
	// keep flowing into the input, hence the separate action.
	InputUp   key.Binding
	InputDown key.Binding

	// Global sidebar navigation (works from any reading pane). ctrl+arrows
	// and ctrl+vim keys switch team/channel and open the target immediately.
	NavTeamPrev key.Binding
	NavTeamNext key.Binding
	NavChanPrev key.Binding
	NavChanNext key.Binding

	// Direct jumps (replace the old "," leader chords). Dispatched in the
	// content-pane region, after the typing guards, so they fire only from
	// reading panes and never shadow the composer's alt-edit keys. NavTeam
	// holds alt+1…alt+9 as one action; the pressed digit selects the team.
	NavTeam key.Binding
	NavDM   key.Binding
	NavFeed key.Binding

	// Channels
	Filter      key.Binding
	ClearFilter key.Binding
	OpenChannel key.Binding

	// Search-result match cycling (messages pane)
	NextHit key.Binding
	PrevHit key.Binding

	// Message paging
	PageDown key.Binding
	PageUp   key.Binding

	// Messages / thread
	OpenThread    key.Binding
	ReplyInThread key.Binding
	OpenAttach    key.Binding
	Preview       key.Binding
	CopyMD        key.Binding
	ShowHistory   key.Binding
	EditPost      key.Binding
	DeletePost    key.Binding
	React         key.Binding
	CloseThread   key.Binding

	// Delete-confirmation modal
	ConfirmYes key.Binding
	ConfirmNo  key.Binding

	// Feed
	MarkRead key.Binding
	Refresh  key.Binding

	// Attachments (input + chip strip)
	Paste        key.Binding
	AttachRemove key.Binding

	// Teams
	SwitchTeam    key.Binding
	LoadTeam      key.Binding
	MoveTeamLeft  key.Binding
	MoveTeamRight key.Binding

	// Input / filter modes
	Compose    key.Binding
	Send       key.Binding
	NewLine    key.Binding
	LeaveInput key.Binding
	ApplyOpen  key.Binding
	CancelEdit key.Binding

	// Global
	Switcher   key.Binding
	Search     key.Binding
	SearchHere key.Binding
	Help       key.Binding
	Quit       key.Binding

	// navRoutes is a routing helper (not an action): the four sidebar-nav
	// actions split into their always-global arrow alias and their vim key,
	// so handleKey can dispatch the vim keys on a different schedule from the
	// arrows depending on vim_nav (see vimNavMode). Not a key.Binding field, so
	// the registry's "one actionDef per field" invariant ignores it.
	navRoutes []navRoute
}

// navRoute pairs a nav direction's arrow-alias binding (always global) with
// its vim-key binding (dispatched per vim_nav) and the move it performs.
type navRoute struct {
	arrow key.Binding // <navmod>+arrow; empty when arrow-nav is off
	vim   key.Binding // ctrl+h/j/k/l (or the user's override)
	team  bool        // true → team switch, false → channel switch
	dir   int         // -1 prev, +1 next
	desc  string      // help description ("next channel"), for the cheatsheet
}

// actionDef is one row of the action registry: the single source of truth
// for an action's config id, default keys, help description, and footer
// priority. newKeyMap builds the keyMap struct from this table.
type actionDef struct {
	id    string                     // config name, e.g. "channel_next"
	field func(*keyMap) *key.Binding // the keyMap field this action populates
	keys  []string                   // default keys
	// navArrow, when set, names the arrow key (left/right/up/down) that gets
	// the configured nav modifier prepended when arrow-nav is enabled, so
	// e.g. channel_next gains "ctrl+down" alongside its "ctrl+j" vim key.
	navArrow string
	desc     string // help description ("next channel")
	primary  bool   // shown in the one-line footer (short) help
	// helpKey, when set, overrides the auto-derived footer key label. Used by
	// actions whose full key list reads badly in the one-line footer — e.g.
	// goto_team binds alt+1…alt+9 but should show "alt+1…9", and search keeps
	// "/" / "F" as the footer glyph while also answering to ctrl+f / ctrl+shift+f
	// (the cheatsheet still lists every key via prettyKeysAll).
	helpKey string
}

// actionDefs is the registry. Every keyMap field appears exactly once;
// keys here are the post-phase-1 defaults. Help key-labels are generated
// from the final keys by prettyKeyLabel, so an override shows the user's
// keys rather than a stale hand-written literal — only desc is authored.
var actionDefs = []actionDef{
	{id: "focus_next", field: func(k *keyMap) *key.Binding { return &k.Tab }, keys: []string{"tab"}, desc: "focus next"},
	{id: "focus_prev", field: func(k *keyMap) *key.Binding { return &k.ShiftTab }, keys: []string{"shift+tab"}, desc: "focus prev"},

	{id: "up", field: func(k *keyMap) *key.Binding { return &k.Up }, keys: []string{"up", "k"}, desc: "up"},
	{id: "down", field: func(k *keyMap) *key.Binding { return &k.Down }, keys: []string{"down", "j"}, desc: "down"},
	{id: "left", field: func(k *keyMap) *key.Binding { return &k.Left }, keys: []string{"left", "h"}, desc: "left"},
	{id: "right", field: func(k *keyMap) *key.Binding { return &k.Right }, keys: []string{"right", "l"}, desc: "right"},
	{id: "top", field: func(k *keyMap) *key.Binding { return &k.Home }, keys: []string{"home", "g"}, desc: "top"},
	{id: "bottom", field: func(k *keyMap) *key.Binding { return &k.End }, keys: []string{"end", "G"}, desc: "bottom"},

	{id: "input_up", field: func(k *keyMap) *key.Binding { return &k.InputUp }, keys: []string{"up", "ctrl+p"}, desc: "up"},
	{id: "input_down", field: func(k *keyMap) *key.Binding { return &k.InputDown }, keys: []string{"down", "ctrl+n"}, desc: "down"},

	{id: "team_prev", field: func(k *keyMap) *key.Binding { return &k.NavTeamPrev }, keys: []string{"ctrl+h"}, navArrow: "left", desc: "prev team"},
	{id: "team_next", field: func(k *keyMap) *key.Binding { return &k.NavTeamNext }, keys: []string{"ctrl+l"}, navArrow: "right", desc: "next team"},
	{id: "channel_prev", field: func(k *keyMap) *key.Binding { return &k.NavChanPrev }, keys: []string{"ctrl+k"}, navArrow: "up", desc: "prev channel"},
	{id: "channel_next", field: func(k *keyMap) *key.Binding { return &k.NavChanNext }, keys: []string{"ctrl+j"}, navArrow: "down", desc: "next channel"},

	{id: "filter", field: func(k *keyMap) *key.Binding { return &k.Filter }, keys: []string{"f"}, desc: "filter", primary: true},
	{id: "clear_filter", field: func(k *keyMap) *key.Binding { return &k.ClearFilter }, keys: []string{"esc"}, desc: "clear filter"},
	{id: "open_channel", field: func(k *keyMap) *key.Binding { return &k.OpenChannel }, keys: []string{"enter"}, desc: "open"},

	{id: "next_match", field: func(k *keyMap) *key.Binding { return &k.NextHit }, keys: []string{"n"}, desc: "next match"},
	{id: "prev_match", field: func(k *keyMap) *key.Binding { return &k.PrevHit }, keys: []string{"N"}, desc: "prev match"},

	{id: "page_down", field: func(k *keyMap) *key.Binding { return &k.PageDown }, keys: []string{"pgdown", "ctrl+d"}, desc: "page down"},
	{id: "page_up", field: func(k *keyMap) *key.Binding { return &k.PageUp }, keys: []string{"pgup", "ctrl+u"}, desc: "page up"},

	{id: "open_thread", field: func(k *keyMap) *key.Binding { return &k.OpenThread }, keys: []string{"enter"}, desc: "open thread", primary: true},
	{id: "reply_in_thread", field: func(k *keyMap) *key.Binding { return &k.ReplyInThread }, keys: []string{"r"}, desc: "reply in thread"},
	{id: "open_attachment", field: func(k *keyMap) *key.Binding { return &k.OpenAttach }, keys: []string{"o"}, desc: "open attachment/link"},
	{id: "preview_image", field: func(k *keyMap) *key.Binding { return &k.Preview }, keys: []string{"space"}, desc: "preview image"},
	{id: "copy_markdown", field: func(k *keyMap) *key.Binding { return &k.CopyMD }, keys: []string{"y"}, desc: "copy markdown"},
	{id: "edit_history", field: func(k *keyMap) *key.Binding { return &k.ShowHistory }, keys: []string{"alt+e"}, desc: "edit history"},
	{id: "edit_post", field: func(k *keyMap) *key.Binding { return &k.EditPost }, keys: []string{"e"}, desc: "edit message"},
	{id: "delete_post", field: func(k *keyMap) *key.Binding { return &k.DeletePost }, keys: []string{"D"}, desc: "delete message"},
	{id: "react", field: func(k *keyMap) *key.Binding { return &k.React }, keys: []string{"R"}, desc: "react"},
	{id: "close_thread", field: func(k *keyMap) *key.Binding { return &k.CloseThread }, keys: []string{"esc"}, desc: "close thread"},

	{id: "confirm_yes", field: func(k *keyMap) *key.Binding { return &k.ConfirmYes }, keys: []string{"y", "Y"}, desc: "confirm delete"},
	{id: "confirm_no", field: func(k *keyMap) *key.Binding { return &k.ConfirmNo }, keys: []string{"n", "N"}, desc: "cancel"},

	{id: "mark_read", field: func(k *keyMap) *key.Binding { return &k.MarkRead }, keys: []string{"m"}, desc: "mark read"},
	{id: "refresh", field: func(k *keyMap) *key.Binding { return &k.Refresh }, keys: []string{"r"}, desc: "refresh"},

	{id: "paste", field: func(k *keyMap) *key.Binding { return &k.Paste }, keys: []string{"ctrl+v"}, desc: "paste"},
	{id: "attachment_remove", field: func(k *keyMap) *key.Binding { return &k.AttachRemove }, keys: []string{"d", "x"}, desc: "remove"},

	{id: "switch_team", field: func(k *keyMap) *key.Binding { return &k.SwitchTeam }, keys: []string{"left", "right", "h", "l"}, desc: "switch"},
	{id: "load_team", field: func(k *keyMap) *key.Binding { return &k.LoadTeam }, keys: []string{"enter"}, desc: "load"},
	{id: "move_team_left", field: func(k *keyMap) *key.Binding { return &k.MoveTeamLeft }, keys: []string{"<"}, desc: "move team left"},
	{id: "move_team_right", field: func(k *keyMap) *key.Binding { return &k.MoveTeamRight }, keys: []string{">"}, desc: "move team right"},

	{id: "compose", field: func(k *keyMap) *key.Binding { return &k.Compose }, keys: []string{"i"}, desc: "compose", primary: true},
	{id: "send", field: func(k *keyMap) *key.Binding { return &k.Send }, keys: []string{"enter"}, desc: "send"},
	{id: "newline", field: func(k *keyMap) *key.Binding { return &k.NewLine }, keys: []string{"alt+enter", "shift+enter"}, desc: "newline"},
	{id: "leave_input", field: func(k *keyMap) *key.Binding { return &k.LeaveInput }, keys: []string{"esc"}, desc: "leave"},
	{id: "apply_open", field: func(k *keyMap) *key.Binding { return &k.ApplyOpen }, keys: []string{"enter"}, desc: "apply + open"},
	{id: "cancel_edit", field: func(k *keyMap) *key.Binding { return &k.CancelEdit }, keys: []string{"esc"}, desc: "cancel"},

	{id: "switcher", field: func(k *keyMap) *key.Binding { return &k.Switcher }, keys: []string{"ctrl+p"}, desc: "switch channel"},
	{id: "search_all", field: func(k *keyMap) *key.Binding { return &k.Search }, keys: []string{"F", "ctrl+shift+f"}, desc: "search all", helpKey: "F"},
	{id: "search_here", field: func(k *keyMap) *key.Binding { return &k.SearchHere }, keys: []string{"/", "ctrl+f"}, desc: "search channel", primary: true, helpKey: "/"},
	{id: "goto_team", field: func(k *keyMap) *key.Binding { return &k.NavTeam }, keys: []string{"alt+1", "alt+2", "alt+3", "alt+4", "alt+5", "alt+6", "alt+7", "alt+8", "alt+9"}, desc: "go to team", primary: true, helpKey: "alt+1…9"},
	{id: "goto_dm", field: func(k *keyMap) *key.Binding { return &k.NavDM }, keys: []string{"alt+d"}, desc: "DMs"},
	{id: "goto_feed", field: func(k *keyMap) *key.Binding { return &k.NavFeed }, keys: []string{"alt+u"}, desc: "Feed"},
	{id: "help", field: func(k *keyMap) *key.Binding { return &k.Help }, keys: []string{"?"}, desc: "help", primary: true},
	{id: "quit", field: func(k *keyMap) *key.Binding { return &k.Quit }, keys: []string{"q", "ctrl+c"}, desc: "quit"},
}

// keyGlyphs maps the bare (modifier-stripped) key token to the symbol shown
// in help. Only the keys that read better as a glyph are listed; everything
// else (letters, "tab", "esc", function names) renders verbatim.
var keyGlyphs = map[string]string{
	"enter": "↵",
	"left":  "←",
	"right": "→",
	"up":    "↑",
	"down":  "↓",
	"space": "␣",
}

// prettyKey renders a single bubbletea key string for help, mapping the base
// key to a glyph while preserving any modifier prefix ("ctrl+down" → "ctrl+↓").
func prettyKey(k string) string {
	parts := strings.Split(k, "+")
	last := len(parts) - 1
	if g, ok := keyGlyphs[parts[last]]; ok {
		parts[last] = g
	}
	return strings.Join(parts, "+")
}

// prettyKeyLabel builds the help key-label from an action's final keys: the
// first two keys, prettified and joined with "/". Generating it (rather than
// hand-writing) keeps the footer honest after a user rebinds a key.
func prettyKeyLabel(keys []string) string {
	switch len(keys) {
	case 0:
		return ""
	case 1:
		return prettyKey(keys[0])
	default:
		return prettyKey(keys[0]) + "/" + prettyKey(keys[1])
	}
}

// prettyKeysAll prettifies every key of a binding and joins them with two
// spaces. Unlike prettyKeyLabel (footer-only, first two) the cheatsheet shows
// the full set so a user sees every key an action answers to. A run of
// consecutive modifier+digit keys (goto_team's alt+1…alt+9) is folded to one
// "alt+1…9" token so it doesn't print nine columns.
func prettyKeysAll(keys []string) string {
	keys = foldDigitRun(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, prettyKey(k))
	}
	return strings.Join(parts, "  ")
}

// foldDigitRun collapses a maximal run (length ≥ 3) of keys that share a
// modifier prefix and carry consecutive single digits — e.g. "alt+1", "alt+2",
// …, "alt+9" — into a single "alt+1…9" token. Shorter runs and any key that
// isn't <modifier>+<digit> pass through untouched.
func foldDigitRun(keys []string) []string {
	out := make([]string, 0, len(keys))
	for i := 0; i < len(keys); {
		mod, d0, ok := splitModDigit(keys[i])
		j := i + 1
		if ok {
			for j < len(keys) {
				m2, d2, ok2 := splitModDigit(keys[j])
				if !ok2 || m2 != mod || d2 != d0+(j-i) {
					break
				}
				j++
			}
		}
		if ok && j-i >= 3 {
			out = append(out, fmt.Sprintf("%s%d…%d", mod, d0, d0+(j-i)-1))
			i = j
			continue
		}
		out = append(out, keys[i])
		i++
	}
	return out
}

// splitModDigit splits "alt+5" into ("alt+", 5, true); ok is false unless the
// key is a real <modifier>+<single digit> (a bare "5" doesn't fold).
func splitModDigit(k string) (mod string, d int, ok bool) {
	if k == "" {
		return "", 0, false
	}
	last := k[len(k)-1]
	if last < '0' || last > '9' {
		return "", 0, false
	}
	mod = k[:len(k)-1]
	if !strings.HasSuffix(mod, "+") {
		return "", 0, false
	}
	return mod, int(last - '0'), true
}

// vimNavMode controls when the ctrl+vim keys (ctrl+h/j/k/l) switch team /
// channel. The arrow-alias keys always navigate globally regardless of this
// setting; only the vim keys' schedule changes. The zero value is
// vimNavGlobal so a freshly-built test Model keeps today's behaviour.
type vimNavMode int

const (
	// vimNavGlobal: vim keys navigate from any focus, including while typing.
	vimNavGlobal vimNavMode = iota
	// vimNavReading: vim keys navigate only outside text inputs, so ctrl+h /
	// ctrl+k stay available as the composer's emacs editing keys.
	vimNavReading
	// vimNavOff: vim keys never navigate.
	vimNavOff
)

// parseVimNav maps a config vim_nav value to a mode, defaulting to global for
// the empty string or anything unrecognised.
func parseVimNav(s string) vimNavMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "reading", "read":
		return vimNavReading
	case "off", "none", "false", "no", "disabled":
		return vimNavOff
	default: // "", "global", or anything unrecognised
		return vimNavGlobal
	}
}

// navMod resolves a config nav_modifier value to the bubbletea key-string
// prefix used for the arrow-key sidebar navigation (e.g. "ctrl+", "super+").
// enabled is false for "none"/"off", which disables arrow-nav entirely and
// frees ctrl+←/→ for the composer's word-jump. Friendly aliases are accepted
// so a user can write "cmd"/"command" for the macOS ⌘ key, "option" for alt,
// etc.; an unrecognised value falls back to ctrl.
func navMod(s string) (prefix string, enabled bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "off", "false", "disabled", "no":
		return "", false
	case "alt", "option", "opt":
		return "alt+", true
	case "shift":
		return "shift+", true
	case "super", "cmd", "command", "win", "windows":
		return "super+", true
	case "meta":
		return "meta+", true
	case "hyper":
		return "hyper+", true
	default: // "", "ctrl", "control", or anything unrecognised
		return "ctrl+", true
	}
}

// newKeyMap builds the keymap from actionDefs. navModifier selects the
// modifier for the arrow-key sidebar navigation (ctrl/alt/shift/super/meta/
// hyper, or "none" to disable it): the ctrl+vim keys (ctrl+h/j/k/l) always
// move teams/channels, and with arrow-nav off ctrl+arrows are left free for
// the composer's word-jump. The arrow alias is prepended (so it leads the
// help label) only when arrow-nav is enabled. See navMod for accepted values.
func newKeyMap(navModifier string) keyMap {
	prefix, navEnabled := navMod(navModifier)
	var km keyMap
	for _, def := range actionDefs {
		keys := append([]string(nil), def.keys...)
		if def.navArrow != "" && navEnabled {
			keys = append([]string{prefix + def.navArrow}, keys...)
		}
		label := prettyKeyLabel(keys)
		if def.helpKey != "" {
			label = def.helpKey
		}
		*def.field(&km) = key.NewBinding(
			key.WithKeys(keys...),
			key.WithHelp(label, def.desc),
		)
		// Build the routing split for the four nav actions: the vim key(s)
		// and (when enabled) the arrow alias, keyed off navArrow's direction.
		if def.navArrow != "" {
			team, dir := navArrowDir(def.navArrow)
			r := navRoute{vim: key.NewBinding(key.WithKeys(def.keys...)), team: team, dir: dir, desc: def.desc}
			if navEnabled {
				r.arrow = key.NewBinding(key.WithKeys(prefix + def.navArrow))
			}
			km.navRoutes = append(km.navRoutes, r)
		}
	}
	return km
}

// navArrowDir maps a nav action's navArrow direction to the (team, dir) move
// it performs.
func navArrowDir(navArrow string) (team bool, dir int) {
	switch navArrow {
	case "left":
		return true, -1
	case "right":
		return true, +1
	case "up":
		return false, -1
	case "down":
		return false, +1
	}
	return false, 0
}

// actionByID indexes the registry by config id for override lookup.
var actionByID = func() map[string]actionDef {
	m := make(map[string]actionDef, len(actionDefs))
	for _, d := range actionDefs {
		m[d.id] = d
	}
	return m
}()

// validActionIDs is the sorted list of every action id, shown when a config
// names one we don't recognise.
func validActionIDs() string {
	ids := make([]string, 0, len(actionDefs))
	for _, d := range actionDefs {
		ids = append(ids, d.id)
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}

// knownModifiers are the chord modifiers a user binding may use. Anything else
// would never arrive from bubbletea, so we reject it as a likely typo.
var knownModifiers = map[string]bool{
	"ctrl": true, "alt": true, "shift": true,
	"super": true, "meta": true, "hyper": true,
}

// validateChord checks a key string is shaped like a single chord: zero or
// more known modifiers joined by "+", then a non-empty key token. It can't
// verify the chord actually arrives (terminal-dependent), only its syntax.
func validateChord(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("empty key")
	}
	parts := strings.Split(s, "+")
	for i, p := range parts {
		if i < len(parts)-1 {
			if !knownModifiers[strings.ToLower(p)] {
				return fmt.Errorf("unknown modifier %q in %q (use ctrl/alt/shift/super/meta/hyper)", p, s)
			}
		} else if p == "" {
			return fmt.Errorf("missing key after modifier in %q", s)
		}
	}
	return nil
}

// normalizeOverride trims an override's keys; an empty list or a lone "none"
// (or "") means "unbind" — nil keys, so the action's binding never matches and
// drops out of help.
func normalizeOverride(keys config.StringOrList) []string {
	if len(keys) == 0 {
		return nil
	}
	if len(keys) == 1 {
		if t := strings.TrimSpace(keys[0]); t == "" || strings.EqualFold(t, "none") {
			return nil
		}
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, strings.TrimSpace(k))
	}
	return out
}

// applyKeyOverrides replaces the default keys of the named actions on km.
// Unknown ids and unparseable chords are errors (no partial map is applied
// past the failure). An explicit binding for a nav action replaces both its
// vim key and its modifier-arrow alias.
func applyKeyOverrides(km keyMap, overrides map[string]config.StringOrList) (keyMap, error) {
	for id, raw := range overrides {
		def, ok := actionByID[id]
		if !ok {
			return km, fmt.Errorf("unknown keybinding %q; valid actions: %s", id, validActionIDs())
		}
		keys := normalizeOverride(raw)
		for _, c := range keys {
			if err := validateChord(c); err != nil {
				return km, fmt.Errorf("keybinding %q: %w", id, err)
			}
		}
		*def.field(&km) = key.NewBinding(
			key.WithKeys(keys...),
			key.WithHelp(prettyKeyLabel(keys), def.desc),
		)
		// A nav override replaces the whole route: the user's keys take the
		// vim slot (still dispatched by vim_nav) and the arrow alias is dropped.
		if def.navArrow != "" {
			team, dir := navArrowDir(def.navArrow)
			for i := range km.navRoutes {
				if km.navRoutes[i].team == team && km.navRoutes[i].dir == dir {
					km.navRoutes[i].vim = key.NewBinding(key.WithKeys(keys...))
					km.navRoutes[i].arrow = key.Binding{}
					break
				}
			}
		}
	}
	return km, nil
}

// navModifierFromConfig resolves the configured arrow-nav modifier, honouring
// the legacy ctrl_arrow_nav toggle. Shared by New() and keyMapForConfig so the
// startup validation builds the same keymap the running app uses.
func navModifierFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return "ctrl"
	}
	switch {
	case cfg.Keybindings.NavModifier != "":
		return cfg.Keybindings.NavModifier
	case cfg.Keybindings.CtrlArrowNav != nil && !*cfg.Keybindings.CtrlArrowNav:
		return "none"
	}
	return "ctrl"
}

// keyMapForConfig builds the keymap from a config: defaults from the nav
// modifier, then any bindings overrides. Returns the build error (unknown
// action / bad chord) without applying a partially-overridden map.
func keyMapForConfig(cfg *config.Config) (keyMap, error) {
	km := newKeyMap(navModifierFromConfig(cfg))
	if cfg == nil || len(cfg.Keybindings.Bindings) == 0 {
		return km, nil
	}
	return applyKeyOverrides(km, cfg.Keybindings.Bindings)
}

// KeyBinding describes one rebindable action for the `matterbox keys` CLI
// verb: its config id, help description, built-in default keys (resolved with
// the configured nav modifier, so a nav action shows its arrow alias), the
// effective keys after the user's bindings overrides, and whether the user
// overrode it. Default/Keys are raw bubbletea key strings (e.g. "ctrl+j").
type KeyBinding struct {
	ID         string
	Desc       string
	Default    []string
	Keys       []string
	Overridden bool
}

// KeybindingsList returns every action's default and effective keys for the
// given config, in registry order — the data behind `matterbox keys`. Default
// is the keymap built from defaults + nav modifier only; Keys additionally
// applies the bindings overrides, so the two differ exactly for overridden
// actions. Returns the same build error (unknown action / bad chord) the TUI
// fails on, rather than silently reporting defaults for a broken config.
func KeybindingsList(cfg *config.Config) ([]KeyBinding, error) {
	defaults := newKeyMap(navModifierFromConfig(cfg))
	current, err := keyMapForConfig(cfg)
	if err != nil {
		return nil, err
	}
	var overrides map[string]config.StringOrList
	if cfg != nil {
		overrides = cfg.Keybindings.Bindings
	}
	out := make([]KeyBinding, 0, len(actionDefs))
	for _, d := range actionDefs {
		_, ov := overrides[d.id]
		out = append(out, KeyBinding{
			ID:         d.id,
			Desc:       d.desc,
			Default:    append([]string(nil), d.field(&defaults).Keys()...),
			Keys:       append([]string(nil), d.field(&current).Keys()...),
			Overridden: ov,
		})
	}
	return out, nil
}

// CheckKeybindings validates a config's keybinding overrides before the TUI
// launches: unknown action ids, unparseable chords, and conflicts where an
// override makes two actions collide in layers that are active together. A
// typo fails loud (consistent with config parse errors) rather than silently
// shadowing a key.
func CheckKeybindings(cfg *config.Config) error {
	km, err := keyMapForConfig(cfg)
	if err != nil {
		return err
	}
	vimNav := vimNavGlobal
	if cfg != nil {
		vimNav = parseVimNav(cfg.Keybindings.VimNav)
	}
	if c := firstKeyConflict(km, vimNav); c != nil {
		return c.err()
	}
	return nil
}
