# Custom Emoji as Inline Images (Kitty Graphics Protocol)

Plan status: researched + designed, not yet implemented. Decisions made: custom (server)
emoji only — unicode emoji keep rendering as kyokomi font glyphs; animated GIF emoji
render as a **static first frame** (Ghostty has no protocol animation support; kitty-only
animation could be a later phase, no rework needed).

## Context

Mattermost custom emoji (`:party_parrot:` etc.) currently render as literal `:name:` text —
kyokomi (`emoji.Sprint`) only knows unicode emoji. Goal: render them as real images in
Kitty **and Ghostty**, in all surfaces: chat bodies, reaction pills, the `:` emoji picker,
the reaction picker, and custom-status display. Must be efficient (disk cache, transmit
once per session, no O(history) work) and degrade gracefully to today's behavior.

## Approach: Unicode placeholders (the only TUI-safe variant)

The kitty graphics protocol's *Unicode placeholder* variant is supported by Kitty and
Ghostty (the only two terminals with full support). It is the only variant that survives
full-screen TUI repaints/scrolling, because the image is anchored to ordinary text cells:

1. **Transmit once** (out-of-band): APC `ESC_G a=T, U=1, f=100 (PNG), t=d, i=<id>, r=1, c=2, q=2 ; <base64, 4KB chunks m=1…m=0> ESC\` — transmit + virtual placement in one go.
2. **Render as text**: each cell is `U+10EEEE` + combining diacritics encoding (row, col),
   with the **24-bit image ID carried in the truecolor foreground** (`ESC[38;2;R;G;Bm`).
   One emoji = 1 row × 2 cols = 2 placeholder clusters ≈ square at typical cell aspect.

### Validated technical facts (don't re-research)

- **Deps already present, nothing new needed**: `github.com/charmbracelet/x/ansi` v0.11.7
  has `ansi.KittyGraphics()`, and `x/ansi/kitty`: `kitty.Placeholder` (U+10EEEE),
  `kitty.Diacritic(i)`, `kitty.Options` (note: quiet field is misspelled `Quite byte`),
  `kitty.EncodeGraphics(w, image.Image, *Options)` (PNG-encode + base64 + 4KB chunking),
  `kitty.MaxChunkSize`.
- **bubbletea v2 raw output**: the v2 cell renderer would strip APC from `View()` output;
  use `tea.Raw(seq)` (raw.go) to send transmit sequences directly to the terminal.
- **Probe responses arrive in Update**: `tea.Msg = uv.Event`; ultraviolet's decoder yields
  `uv.KittyGraphicsEvent{Options, Payload}` for graphics query replies.
- **Width math checks out**: uniseg treats U+10EEEE as EAW-Ambiguous → width 1, diacritics
  → width 0. Placeholder clusters flow through `ansi.Wrap`/`lipgloss.Width` as 1 col each.
  A wrap between the two clusters is harmless (each cell renders independently).
- **Render timing**: post bodies are rendered during *Update* (renderMessages/renderThread),
  not View. Only the emoji popup, reaction picker, and custom-status header render at View
  time — so "saw an unknown emoji → enqueue fetch" can be drained in an Update tail
  (same pattern as `resolveUnknownSenders`, update.go:20-30).
- **Color profile gate**: the cell renderer re-emits SGR per detected profile; a non-truecolor
  profile would quantize the fg-encoded image ID. Gate on `tea.ColorProfileMsg` == TrueColor
  in addition to the graphics probe.
- **Client4 API**: `GetEmojisByNames(ctx, names)` (bulk name→emoji), `GetEmojiImage(ctx, id)`,
  `GetEmojiList` (page 200) all exist in mattermost/server/public.

## Implementation steps

### 1. Config (`internal/config/config.go`)
`EmojiImages string \`yaml:"emoji_images"\`` — `"auto"` (default, probe at startup) | `"off"`.
fillDefaults + writeConfig doc comment; snapshot into Model in `New()` (NavModifier pattern,
model.go:~456).

### 2. Client wrapper (`internal/mm/client.go`)
- `CustomEmojisByNames(ctx, names) ([]*model.Emoji, error)` — bulk resolve; absent names = not custom emoji. (Old servers may 404 → optional per-name `GetEmojiByName` fallback, deferrable.)
- `CustomEmojiImage(ctx, id) ([]byte, error)` — **singleflight-deduped** per id (use `x/sync/singleflight`, per project convention).
- `AllCustomEmoji(ctx) ([]string, error)` — page GetEmojiList until exhausted (picker index).

### 3. New file `internal/ui/emojiimg.go` — manager + pure encoding
State machine per emoji name: `unknown → pending → fetching → ready(imageID) | failed` (failed = literal forever).

```go
type emojiImages struct {        // held as *pointer* on Model (Model is value-copied)
    mu sync.Mutex                // inline() called from Update AND View renders
    mode string                  // "auto"|"off"
    probed, supported bool
    entries map[string]*emojiImgEntry   // state, id, prebuilt placeholder string
    pending map[string]struct{}
    nextID uint32                // random 24-bit seed (avoid stale-ID collisions after restart), then sequential
}
func (e *emojiImages) inline(name string) (string, bool) // ready→placeholder; unknown→record pending, return false
func (e *emojiImages) setProbeResult(supported bool)
func (e *emojiImages) takePending() []string             // nil unless probed && supported
func (e *emojiImages) markFetching/markReady/markFailed
```

Pure helpers (unit-tested, no Model):
- `kittyPlaceholder(id uint32, rows, cols int) string` — hand-rolled `\x1b[38;2;R;G;Bm` (NOT lipgloss — keep profile machinery away from the ID) + clusters `U+10EEEE + Diacritic(row) + Diacritic(col)` + `\x1b[39m`. Cell(0,0)=`U+10EEEE U+0305 U+0305`, cell(0,1)=`U+10EEEE U+0305 U+030D`.
- `kittyTransmit(id uint32, png []byte) (string, error)` — `kitty.EncodeGraphics` with `{Action: TransmitAndPut, VirtualPlacement: true, ID, Rows:1, Columns:2, Format: PNG, Transmission: Direct, Quite: 2, Chunk: true}`.
- `normalizeEmojiPNG(raw []byte) ([]byte, error)` — PNG passthrough; GIF first frame (image/gif) and JPEG re-encoded to PNG (stdlib only).
- `cachedEmojiPath(name string)` → `~/.cache/matterbox/emoji/<name>.png` (mirror `cachedFilePath`, model.go:832).
- `kittyProbe() string` + `const kittyProbeID = 0xB0C5` — `ansi.KittyGraphics(b64(4 bytes), "i=…","s=1","v=1","a=q","t=d","f=32","q=2")`.

### 4. Model wiring (`internal/ui/model.go`)
Fields `emojiImg *emojiImages`, `customEmojiNames []string`. In `Init()` (model.go:629), when
mode=="auto" **and** `$TMUX` is empty: batch `tea.Raw(kittyProbe())` + 1s `tea.Tick` → `emojiProbeTimeoutMsg`.

### 5. Msgs + handlers (`internal/ui/update.go`)
- `case uv.KittyGraphicsEvent:` — if `Options.ID == kittyProbeID` && payload prefix "OK" → `setProbeResult(profileIsTrueColor)`. Track truecolor from `tea.ColorProfileMsg`.
- `case emojiProbeTimeoutMsg:` — unresolved → `setProbeResult(false)`.
- `case customEmojiListMsg:` — store sorted picker index. Kick `AllCustomEmoji` fetch once from `channelsLoadedMsg` (names only; images stay lazy).
- `case emojiImagesFetchedMsg{ready map[string][]byte, failed []string}:`
  1. per ready name: alloc ID, build transmit seq, `markReady`;
  2. `markFailed(failed...)`;
  3. **invalidation**: walk `m.posts`/`m.threadPosts` (bounded by 400-post window), `strings.Contains(p.Message, ":"+name+":")` or reaction match → `invalidatePostLines(p.Id)` (postcache.go:137) — no `postLineFingerprint` change needed since every state transition explicitly invalidates;
  4. `renderMessages(); renderThread()`;
  5. `return m, tea.Raw(allTransmitSeqs)`.

### 6. Update tail: drain pending (mirror resolveUnknownSenders)
`fetchPendingEmoji() tea.Cmd`: `takePending()` → `markFetching` → cmd that: disk-cache check
per name → bulk `CustomEmojisByNames` for misses → `CustomEmojiImage` per hit →
`normalizeEmojiPNG` → best-effort write to disk cache → `emojiImagesFetchedMsg`.
View-time sightings (popup/status) land in pending and drain on the next Update message.

### 7. Render call sites
- **markdown.go:104**: replace `emoji.Sprint(s)` with one regex pass `:([a-zA-Z0-9_+\-]+):` —
  kyokomi CodeMap hit → glyph (as today); else `ei.inline(name)` → placeholder if ready;
  else literal (and pending recorded). Signature: `renderInline(s string, ei *emojiImages)`
  (callers: view.go:576, view.go:626; tests pass nil). Placeholder has no markdown
  metacharacters → later bold/italic/link passes can't corrupt it.
- **reactions.go:94 (pills)**: render glyph and count as separate styled runs so the pill bg
  sits under the placeholder (bg is irrelevant to the protocol; ID rides on fg only) and the
  count keeps its color after the placeholder's `[39m` reset.
- Extract `(m *Model) renderEmojiGlyph(name) string` (kyokomi → custom image → literal) and
  use in reaction picker (reactions.go:~323), custom status (view.go:1006), commands.go:152.
- **emoji.go picker**: `emojiMatches` becomes a Model method merging kyokomi index +
  `m.customEmojiNames`; `renderEmojiPopup` resolves via `renderEmojiGlyph` at render time
  (just-readied images appear without recomputing matches). Placeholder is width 2 like most
  emoji glyphs; use lipgloss.Width-based padding if rows misalign.

### 8. Tests (`internal/ui/emojiimg_test.go`)
1. Placeholder golden: `kittyPlaceholder(0x123456,1,2)` == `"\x1b[38;2;18;52;86m\U0010EEEE̅̅\U0010EEEE̅̍\x1b[39m"`; assert `lipgloss.Width()==2`.
2. Transmit: starts `\x1b_G`, contains U=1/r=1/c=2/f=100/i/q=2; >4KB PNG → m=1 chunks ending m=0 (round-trip via `kitty.Options.UnmarshalText`).
3. State machine: pending dedup, drain-once, post-probe gating, failed = permanent literal.
4. normalizeEmojiPNG: GIF frame-0 / JPEG / PNG → PNG magic.
5. Invalidation: cached literal post + fetched msg → cache dropped, re-render contains `kitty.Placeholder`.
6. Regression: markdown tests with nil manager byte-identical.

### Suggested order
1. Step 3 helpers + tests 1–4, then a throwaway debug command to **visually smoke-test a
   placeholder in Ghostty early** (if the cursed renderer mangled clusters, the whole
   approach changes — de-risk first).
2. Steps 1, 2, 4 (config, client, wiring).
3. Steps 5–6 (probe, msgs, drain).
4. Step 7 call sites: body text → pills → pickers/status.
5. Tests 5–6, manual pass.

## Manual verification
- Ghostty + Kitty: custom emoji in body, pill (bg continuity), `:`-picker (server-only names
  listed), reaction picker, custom status. Scroll/resize/thread/channel-switch repaints.
- Efficiency: second sighting → zero APC + zero HTTP; restart → disk cache, one transmit.
- Degradation: `emoji_images: off`; foot/xterm (probe timeout → literal); tmux (skipped);
  ssh to kitty (probe passes through — should work).
- Animated GIF emoji → static first frame.

## Risks / limitations
- **Terminal image-quota eviction** (~320MB): long sessions could evict early images →
  blank 2-cell gaps, no client-side detection. Acceptable at emoji size; future: re-transmit
  ready images on channel switch.
- **tmux**: needs allow-passthrough, probe reply unreliable → disabled under `$TMUX`.
- **Non-truecolor profile** (e.g. missing COLORTERM over ssh): feature silently off — document.
- **Disk cache keyed by name**: re-uploaded emoji under same name shows stale image until
  `~/.cache/matterbox/emoji/<name>.png` deleted. Acceptable; note in README.
- **Old servers**: `GetEmojisByNames` may 404 → per-name fallback (deferrable).
