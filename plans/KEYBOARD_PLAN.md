# Keyboard UX overhaul — plan

Goal: user-configurable keybindings on a data-driven key layer, so that
(a) every key the app advertises actually works, (b) two users with
different muscle memory can each remap what they want in config.yaml,
and (c) the next shadowing bug is caught by a test instead of a
confused user.

Status: phases 1–4 implemented (bug fixes, action registry, context
table + shadowing test + vim_nav, config `bindings:` overrides). Phase 5
(discoverability) points 1–2 done — the `> Keys` switcher cheatsheet
(internal/ui/cheatsheet.go) and the `matterbox keys` CLI verb
(internal/cli/keys.go, backed by `ui.KeybindingsList`); point 3 (first-run
hint) is still pending. Phases land in order; each leaves the tree green
and is independently revertible.

---

## Background: what the review found

The architecture is sound — a shared `keyMap` (internal/ui/keys.go)
feeds both the help bubble and the handlers, and `handleKey`
(internal/ui/update.go:1109) layers modals → global chords → typing
guards → focus panes. But the precedence lives implicitly in nesting
order, and about half the handlers match raw `msg.String()` literals
instead of keymap bindings (29 sites across 9 files). That produced
three real shadowing bugs:

1. **`ctrl+j` in the composer switches channel instead of inserting a
   newline.** The textarea's `InsertNewline` includes `ctrl+j`
   (model.go:416) but global `NavChanNext` is also `ctrl+j`
   (keys.go:125) and is checked first (update.go:1160-1169). The
   newline binding is dead.
2. **`ctrl+p` is dead as "move up" in the Search, Feed, and AI-answer
   lists.** Those handlers bind `"up", "ctrl+p"` (search.go:453,
   feed.go:319, search.go:558) but the global Switcher check
   (update.go:1150) runs first; its `popupOpen` guard only covers the
   composer's mention/emoji popups. `ctrl+n` moves down, `ctrl+p`
   opens the switcher — baffling asymmetry.
3. **Global ctrl+vim nav eats the composer's emacs editing keys, with
   no way to turn it off.** `ctrl+k` (kill to end of line) and
   `ctrl+h` (backspace alias) are stolen by channel/team nav *while
   typing*. `nav_modifier: none` only frees the arrows; ctrl+h/j/k/l
   are bound unconditionally (keys.go:124-125).

Plus smaller items folded into the phases below: vestigial `ctrl+k`
closes the switcher (switcher.go:59, stale comment at :118), the
messages-pane footer advertises 23 bindings on one line (model.go:597,
ellipsized after ~6), `NewLine` help label disagrees between keys.go
(`alt+↵`) and model.go (`shift+↵`), bare `q` quits instantly from any
reading pane, `enter` confirms the delete dialog, leader no-ops are
silent.

---

## Target design

Three layers:

### 1. Action registry (keys.go)

One table is the single source of truth for every action: config
name, default keys, help description, short-help priority. The
existing `keyMap` struct stays as the runtime representation but is
*built from* the registry plus user overrides instead of hand-written
literals.

```go
// keys.go
type actionDef struct {
    id      string   // config name, e.g. "channel_next"
    field   func(*keyMap) *key.Binding
    keys    []string // defaults; nav actions get modifier-arrow aliases appended
    desc    string   // help description ("next channel")
    primary bool     // shown in the one-line footer help
}

var actionDefs = []actionDef{
    {id: "quit", field: ..., keys: []string{"q"}, desc: "quit"},
    {id: "compose", keys: []string{"i"}, desc: "compose", primary: true},
    {id: "channel_next", keys: []string{"ctrl+j"}, desc: "next channel"},
    // ... every binding in today's keyMap
}
```

Help key-labels are **generated** from the final keys through a small
prettifier (`enter`→`↵`, `left`→`←`, `up`→`↑`, join the first two
keys with `/`) so that after a user override the footer shows the
user's keys, not stale literals. Descriptions stay hand-written.

Two new actions split the list-navigation conventions so every raw
string switch can become a binding match:

- `up` / `down` — reading panes: `↑/k`, `↓/j` (unchanged).
- `input_up` / `input_down` — lists attached to a text input
  (switcher, search results, feed, mention/emoji popups, filter):
  `↑/ctrl+p`, `↓/ctrl+n`. Typed `k`/`j` must keep going into the
  input, hence the separate action. Note: `ctrl+p` belongs to the
  switcher everywhere (decided), so in these contexts the switcher
  chord deliberately shadows `input_up`'s `ctrl+p` — a whitelisted
  shadow in the context table. The ONE exception, kept from today:
  while a mention/emoji autocomplete popup is open in the composer,
  the popup owns `ctrl+p`/`ctrl+n` for selection (the existing
  `popupOpen` guard, update.go:1149).
- `confirm_yes` / `confirm_no` — delete dialog (`y` / `n`/`esc`).

Hardwired, not in the registry: `ctrl+c` always quits; `esc` always
means cancel/close in modals. No config can brick the app.

### 2. Context table (new file: internal/ui/contexts.go)

The precedence ladder becomes declared data. Each context says when
it is active and which bindings it owns; `handleKey` keeps its handler
*functions* but iterates this table to route. The same table feeds
`ShortHelp`/`FullHelp` (model.go:584-627 stops being hand-curated) and
— the lasting payoff — a **shadowing test**.

```go
type keyContext struct {
    name     string
    active   func(*Model) bool
    bindings func(keyMap) []key.Binding // keys this layer consumes when active
    // typing: a context whose fallthrough consumes ALL remaining keys
    // (textarea/textinput). Layers above a typing context are the only
    // ones that can shadow editing keys.
    typing   bool
    // shadows: keys this layer is *allowed* to take from lower layers,
    // reviewed and intentional (e.g. global nav over the composer).
    shadows  []string
}

// Order = precedence. Mirrors today's handleKey nesting exactly.
var keyContexts = []keyContext{
    {name: "modal:delete-confirm", ...},
    {name: "modal:reaction-picker", ...},
    {name: "modal:open-picker", ...},
    {name: "modal:poll-dialog", ...},
    {name: "modal:history", ...},
    {name: "modal:summary", ...},
    {name: "modal:switcher", typing: true, ...},
    {name: "global:switcher-chord", ...},
    {name: "global:nav", ...},
    {name: "mode:filter", typing: true, ...},
    {name: "focus:input", typing: true, ...},
    {name: "focus:search", typing: true, ...},
    {name: "chord:leader", ...},
    {name: "global:reading", ...}, // F, U, i, leader, q, ? — only in content focuses
    {name: "focus:messages", ...},
    {name: "focus:thread", ...},
    {name: "focus:attachments", ...},
    {name: "focus:teams", ...},
    {name: "focus:feed", ...},
}
```

It is *not* a generic action→closure dispatch table. The per-focus
handlers carry real state logic (`canMutatePost`, thread-open checks,
store paging on ↑ at the top of the window) that would become awkward
closures for no gain. The table only owns routing and key-claims.

### 3. Config overrides (internal/config/config.go)

```yaml
keybindings:
  nav_modifier: ctrl      # existing, unchanged (incl. ctrl_arrow_nav migration)
  vim_nav: global         # NEW: global (default) | reading | off
  bindings:               # NEW: action → key or key list
    quit: []              # unbind q entirely (ctrl+c is hardwired)
    compose: ["i", "a"]
    channel_next: ctrl+j
    delete_post: shift+d
```

Rules:

- Value is a single string or a list (custom `StringOrList` YAML
  unmarshal). Empty list / `none` = unbind.
- **Unknown action name = startup error** listing the valid names —
  same loud-on-typo policy `Load()` already follows for parse errors
  (config.go:252). Key *syntax* is validated leniently (known
  modifiers `ctrl/alt/shift/super/meta/hyper` + a key token); an
  unparseable chord is also an error.
- An explicit binding for a nav action (`channel_next` etc.) fully
  replaces that action's defaults — both the vim key and the
  `nav_modifier` arrow alias. `nav_modifier` stays as the convenience
  knob when the action isn't explicitly overridden.
- The merged map is conflict-checked at startup with the same logic as
  the shadowing test; a collision between two actions in co-active
  contexts is reported with both action names.
- Don't dump all ~40 bindings into the written-back config.yaml — the
  header comment block documents the action names and syntax; `Load()`
  only rewrites when sections are missing, as today.

### Decided defaults (confirmed by Corné)

1. **`vim_nav: global`** — ctrl+h/j/k/l switch team/channel from any
   focus, *including while typing* (today's behaviour stays the
   default). `reading` (nav only outside typing contexts, which
   returns the composer's emacs keys ctrl+h/ctrl+k) and `off` are the
   opt-in alternatives. Consequence for the shadowing test: the
   `global:nav` context's vim keys must be whitelisted against the
   reserved-editing-keys check when `vim_nav: global` — that shadow is
   now a documented decision, not an accident.
2. **`ctrl+p` opens the switcher everywhere.** The dead `ctrl+p`
   list-up arms in search/feed/AI are deleted, not revived. Sole
   exception (existing, kept): the composer's mention/emoji
   autocomplete popups own `ctrl+p`/`ctrl+n` while open.
3. **`ctrl+j` is removed from the newline defaults** regardless of
   vim_nav (`alt+enter` / `shift+enter` remain). One key meaning
   "newline" or "switch channel" depending on a config value is a trap.
4. `q` stays bound to quit by default (changing that is now a one-line
   config for whoever wants it), but the delete dialog drops `enter`
   as confirm — `y` only.

---

## Phases

### Phase 1 — bug fixes & binding hygiene (no new machinery)

Small, independent, land first so the sweep in later phases starts
from honest behaviour.

1. Remove `ctrl+j` from `InsertNewline` (model.go:416-419) and from the
   `NewLine` binding (keys.go:300); unify the help label to
   `alt+↵/shift+↵`. Update the comment at model.go:413.
2. Delete the dead `ctrl+p` arms in `handleSearchKey` (search.go:453),
   `handleAIDoneKey` (search.go:558), `handleFeedKey` (feed.go:319).
   Decided: `ctrl+p` = switcher everywhere (mention/emoji popups
   excepted, as today); `ctrl+n` keeps working as list-down in those
   contexts via `input_down`.
3. Remove the vestigial `ctrl+k`-closes-switcher arm (switcher.go:59)
   and fix the stale "Ctrl+k is a deliberate jump" comment
   (switcher.go:118). Make `ctrl+p` toggle the switcher closed instead.
4. Delete dialog: `enter` no longer confirms (update.go:2185) — `y`
   confirms, `n`/`esc`/`q` cancel.
5. Esc precedence in reading panes (update.go:1252-1263): close the
   thread *before* clearing the sidebar filter (the thread is the
   visually dominant thing).
6. Leader feedback (update.go:1453): unknown second key flashes a
   status (`", x" — nothing bound; , then t/m/i/d/1-9`) instead of
   silently cancelling; `,t`/`,m` on tabs where they're no-ops say why.
7. Composer esc with a non-empty draft (update.go:1786): flash
   `draft kept — esc leaves only when the input is empty` instead of
   doing nothing silently.

Tests: update composernav_test.go (ctrl+j now navigates, never
newlines), add a regression test per fix. Existing globalnav/switcher
tests keep passing.

### Phase 2 — action registry; binding-ify every handler

No behaviour change. Biggest diff of the plan.

1. Rewrite keys.go around `actionDefs` (every current binding gets an
   id; defaults identical to today post-phase-1). `newKeyMap` builds
   the struct from the table; `navMod`/modifier-arrow aliasing and the
   help-label prettifier live here.
2. Add `input_up`/`input_down`, `confirm_yes`/`confirm_no` actions.
3. Sweep the 9 files with raw `msg.String()` switches and convert each
   arm to `key.Matches` against a named binding:
   update.go, search.go, feed.go, switcher.go, reactions.go,
   history.go, summary.go, openpicker.go, polls.go.
   Exceptions that stay literal: `ctrl+c` (hardwired), `esc` in modals
   (hardwired), digit accelerators (reaction picker 1-9, poll digits,
   leader 1-9), and the composer's structural `up`-on-first-row check
   (update.go:1750, it's positional, not a rebindable action).
4. Help labels now come from the prettifier; delete the hand-written
   `key.WithHelp` key-strings.

Tests: a registry test asserting every `keyMap` field has exactly one
`actionDef` and ids are unique; the full existing UI test suite as the
regression net (zero expected diffs).

### Phase 3 — context table, routed dispatch, shadowing tests, vim_nav

1. Add contexts.go with the table above; refactor `handleKey` to walk
   it: first active context whose bindings match wins; typing contexts
   keep their fallthrough-to-input behaviour. The per-focus handler
   functions are unchanged — only the outer routing moves.
2. Derive `ShortHelp`/`FullHelp` from the active contexts. Footer
   short help shows only `primary` actions (target ≤8 for the messages
   pane: `i`, `↵` thread, `/`, `f`, `u`, `,`, `?`); `?` full help shows
   everything, grouped by context.
3. Implement `vim_nav` (config plumbing in this phase, since the
   context table is what makes "reading-only" expressible): the
   `global:nav` context's vim keys are active per the setting; the
   modifier-arrow aliases stay global. Default `global` (today's
   behaviour); `reading`/`off` are opt-in.
4. **Shadowing test** (pure data, no UI simulation):
   - For every pair of contexts that can be co-active (compute from
     `active` predicates over a small enumeration of model states:
     each focus × thread open/closed × each modal × filter/leader),
     assert no key string appears in both a higher and a lower layer
     unless listed in the higher layer's `shadows` whitelist.
   - Assert no layer above a typing context binds an unmodified
     printable key.
   - Assert no layer above a typing context collides with a reserved
     editing-key list (`ctrl+a/e/k/u/w/h/f/b/d/t`, `alt+f/b/d`,
     `ctrl+left/right` when nav_modifier ≠ ctrl) unless whitelisted.
   All three original bugs fail this test if reintroduced.

Tests: the shadowing suite + updated globalnav_test.go for
`vim_nav: reading|global|off`.

### Phase 4 — config `bindings:` overrides

1. `KeybindingsConfig` gains `VimNav string` (if not landed in phase 3)
   and `Bindings map[string]StringOrList` with the custom unmarshal.
2. `newKeyMap(cfg)` applies overrides after defaults: replace the
   action's key list; empty = unbind (binding disabled, dropped from
   help). Unknown action / bad chord syntax → error returned from
   `config.Load()`, printed with the valid-action list.
3. Run the conflict check from phase 3 against the merged map at
   startup; report collisions with both action names. Decide
   warn-vs-fail: **fail** (consistent with parse errors; the message
   says exactly which two lines conflict).
4. Document: config.yaml header block (config.go:378) gets the action
   list + syntax + examples; README gets a Keybindings section.

Tests: override/unbind/list-or-string parsing, unknown-action error,
conflict error, help reflects overridden keys, nav-action override
suppresses the modifier-arrow alias.

### Phase 5 (optional) — discoverability

1. **Done.** `> Keys` switcher command: scrollable cheatsheet popup
   (internal/ui/cheatsheet.go) grouped by context-table layer, rows pulled
   from each context's `claims()` so it shows the user's *effective*
   bindings (overrides + nav_modifier + vim_nav all flow through). Sidebar
   nav is sourced from `navRoutes` (one row per direction, vim_nav-aware)
   since the routing bindings carry no help text. Wired as a modal in
   `inModal`/`keyContexts`/`handleKey`/`viewContent`; esc/q close, arrows
   scroll. Tests in cheatsheet_test.go.
2. **Done.** `matterbox keys` CLI verb (internal/cli/keys.go): prints every
   action, its effective keys, and a `*`-flagged note with the default it
   replaced when overridden — backed by the exported `ui.KeybindingsList`.
   Tests in internal/cli/keys_test.go.
3. First-run hint: one-time status line pointing at `?` and `> Keys`.
   *(pending)*

---

## File inventory

| File | Phases | Change |
|---|---|---|
| internal/ui/keys.go | 1,2,4 | registry rewrite, prettifier, override application |
| internal/ui/update.go | 1,2,3 | bug fixes; binding-ify; route via context table |
| internal/ui/model.go | 1,2,3 | InsertNewline; help derivation; keymap ctor call |
| internal/ui/contexts.go | 3 | NEW — context table |
| internal/ui/search.go, feed.go, switcher.go, reactions.go, history.go, summary.go, openpicker.go, polls.go | 1,2 | dead arms; binding-ify |
| internal/ui/keys_test.go, contexts_test.go | 2,3 | NEW — registry + shadowing suites |
| internal/config/config.go (+ config_test.go) | 3,4 | vim_nav, bindings map, validation, header docs |
| README.md, PLAN.md | 4 | document; status updates as phases land |

## Risks / notes

- The phase-2 sweep is wide but mechanical; the existing UI tests
  (globalnav, composernav, switcher, feed, search, markread, …) are
  the net. Do it file-by-file, one commit each.
- Bubbletea matches keys by `msg.String()` equality, so "validation"
  of user chords can only be syntactic; document that an exotic chord
  may simply never arrive on a non-kitty terminal (same caveat as
  shift+enter today, which silently *sends* on legacy terminals — note
  this in the README section).
- Keep the memory note (project_keymap_conventions) in sync once
  defaults change: capital letters still bind as bare `"F"`/`"U"`;
  nav dispatch order changes from "before typing guards" to
  "context table".
