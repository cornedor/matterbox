# Performance notes — `internal/ui`

Optimization opportunities surfaced while shrinking `ui.Model` (the per-event
copy). Ordered roughly by leverage. Each is independent; none are blocking.

Context: `internal/ui` uses **value-receiver bubbletea dispatch**, so the whole
`Model` struct is `memcpy`'d on every keystroke (Update) and every render
(View), plus heap-boxed into `tea.Model` at the boundary each event. `Model`
was 123KB; it's now ~102KB after boxing the cold modal overlays (see
`TestModelSizeCeiling`). The items below are the remaining levers.

---

## 1. The post-dispatch `Model` copies in `Update` — ✅ DONE
**Result:** flipping `resolveUnknownSenders`/`fetchPendingEmoji`/`fetchPendingMRStatus`
to pointer receivers (`maybeStartEmojiAnim` already was) removed **−99.98% time
and −100% allocs** on `BenchmarkUpdatePostDispatch` (112µs → 19ns, **208 KiB → 0,
2 → 0 allocs** per event). `resolveUnknownSenders`'s inner `want` closure had been
forcing the whole value-receiver `Model` to escape to the heap on every event.
Safe because value-based dispatch means each event's `nm` is a frozen snapshot,
and the methods only mutate shared-pointer state, not value fields. Original note
kept below for context.
`update.go` `Update()` runs four value-receiver methods on `nm` after every
event:
```go
nm.resolveUnknownSenders(); nm.fetchPendingEmoji();
nm.fetchPendingMRStatus(); nm.maybeStartEmojiAnim()
```
Each call copies the whole ~102KB `Model` (value receiver on an addressable
local). That's ~4×102KB memcpy **per event**, on top of the dispatch copies.
- **Fix:** convert these four to pointer receivers. Not a uniform flip — check
  each closure first:
  - `fetchPendingMRStatus` already snapshots (`client, ctx := m.glClient, m.ctx`)
    → safe to flip as-is.
  - `fetchPendingEmoji` (`return func() tea.Msg { return m.loadEmojiImages(names) }`)
    and `resolveUnknownSenders` capture `m` directly in their `tea.Cmd` closure,
    so flipping the receiver would make the async goroutine read live (mutated)
    state. Snapshot what the closure needs into locals first (the
    `fetchPosts`/`feed.go:219` convention), then flip.
- **Win:** removes up to ~300KB of per-event copying (4×~75KB after #2/#5 shrink
  the struct further). **Risk:** low once snapshots are in place. **Effort:** small.

## 2. `layoutPanes` sizes every pane's viewport on every layout (CPU + blocks boxing)
`view.go` `layoutPanes` unconditionally calls `sizeSearchView`, `sizeFeedView`,
`sizeSQLView` (≈232–234) and sizes `refView`/`infoView` (≈168–221) even when
those tabs/panes aren't visible. This both wastes work on hidden panes and is
the reason `search`/`sql`/`feed` couldn't be boxed (they'd nil-panic literal
test Models).
- **Fix:** size only the visible pane(s) — gate each `sizeXView` on its
  tab/focus being active. Then those big sub-states (`search` 10.6KB, `sql`
  11.9KB, `aiSearch` 8.7KB, `feed` 2.7KB) become safe to box behind pointers
  for another ~34KB off `Model` (→ ~2.4× total shrink).
- **Win:** less layout CPU on hidden panes **and** unlocks more struct-shrink.
  **Risk:** medium (must confirm a pane is sized before it's first shown).

## 3. `renderAllPanes` repaints hidden panes on resize
`view.go` `renderAllPanes` (≈241–) repaints **every** pane — search results,
feed, SQL — on each settle, even the non-active tab. `renderSearchResults` walks
hit bubbles; the feed/SQL repaints walk their content. Only the visible pane
needs repainting.
- **Fix:** repaint only the active pane (or mark hidden panes dirty and repaint
  lazily on tab switch). **Win:** big on resize for users on the search/feed/SQL
  tabs with lots of content. **Risk:** medium.

## 4. Root cause of the bloat: `lipgloss.Style` embedded ~15× by value
The 102KB is dominated by `lipgloss.Style` values (each carries many fields)
embedded inside every `viewport.Model` (×6), `textinput.Model` (×4), and
`editor.Model` (×2) — see the field dump in `TestModelSizeCeiling`'s failure
output. The struct-shrink boxing only relocates these; it doesn't reduce the
underlying per-style cost.
- **Fix (larger):** share a single style set via pointer/registry instead of
  embedding copies, or hold the heavy components behind one lazily-built
  sub-struct. **Win:** large. **Risk:** high (touches component construction).

## 5. Box the Jira modal widgets (`jiraCommentInput`, `jiraPointsInput`)
Both are cold, strictly modal-gated (`jiraCommentActive` / `jiraPointsActive`),
and not touched in the unconditional layout/render passes — so they're safe to
box for another ~17KB (→ ~1.4× total). The only wrinkle is they're built on
open / reset on close, so boxing means allocate-pointer-on-open + nil-on-close
(`jira_comment.go:59/77/107`, `jira_edit.go:249/283`).
- **Win:** ~17KB off `Model`. **Risk:** low–medium (nil-when-closed must stay
  behind the mode flags). **Effort:** small.

## 6. Full pointer-receiver conversion of the dispatch tree (the complete fix)
The struct-shrink only makes the per-event copy *smaller*. The complete fix for
the 102KB copy + per-event heap-box is converting `Update`/`View` + the 102
`(tea.Model, tea.Cmd)` handlers to pointer receivers that mutate in place. This
is the route deferred in the memory note: ~600 source + ~400 test edits, plus an
async-closure audit. Eliminates the copy/box entirely rather than shrinking it.
- **Win:** largest. **Risk:** medium-high. **Effort:** large but mechanical
  (compiler-guided; tests are the safety net).

---

## 7. The GIF animation tick re-rendered the whole screen — ✅ DONE
**Result:** adding `imgAnimTickMsg` to `preservesFrame` (`update.go`, alongside
`tea.MouseWheelMsg`) took `BenchmarkInlineAnimTick/thumbs=3` from **1.57ms →
76µs/tick (−95%), 1137 → 10 allocs**.

`advanceImageAnim` is built so it needs *no* re-render: it re-transmits the next
GIF frame under the **same** Kitty image id via `tea.Raw`, so the placeholder
cells on screen are unchanged and the terminal repaints the image itself. But the
tick still went through `update()`, which invalidated the memoized frame
(`viewCache.view`) for every message except a wheel event — so each one rebuilt
the entire screen for nothing. Any visible GIF (custom emoji *or*, since
`image_thumbnails`, a Giphy thumbnail — the common case) meant a full ~1.5ms
`View()` at 12–20Hz on an otherwise idle UI, which is what everything else then
queued behind.

Note this is the *same* storm the viewport-gating fix chased: gating cut how
often the tick is armed, but every armed tick still paid a full re-render. The
frame memo is what actually removes the cost.

- **Lesson:** a `tea.Raw` side-channel write does not exempt a message from the
  render loop. If a message leaves the frame byte-identical, it must say so in
  `preservesFrame` or it costs a full `View()`.

## 8. Inline thumbnails rebuilt forever in image-heavy channels — ✅ DONE
**Found by:** live pprof of channel-switching (`matterbox --pprof`, see
`scripts/matterbox-pprof`). **70% of all CPU in `buildInlineThumb`, with
`readThumbBytes` at 0.01s** — nothing was being downloaded. It was re-decoding,
re-downscaling and re-PNG-encoding images already in the disk cache, forever.

Two numbers disagreed. `renderMessages` renders **every post in the render
window** (up to `maxLoadedPosts`, 400) — not just the viewport — so `sight()`
fired for every image in the window. `maxInlineImages` capped *terminal* memory
at 64. Past 64 images the cycle never closed: render sights all N → fetch builds
them → eviction **deletes the entries** to stay under the cap → those posts'
lines are invalidated → re-render sights them as brand new → rebuild. And because
eviction discarded the *built* frames, switching between two image-heavy channels
rebuilt every image from scratch each way.

**Fixes, both needed:**
1. **Terminal residency ≠ built frames.** `inlineImgEntry.resident` now tracks
   the terminal copy; eviction `kittyDelete`s it and *keeps* the decoded/encoded
   frames. A later sighting re-transmits a string we already have
   (`sight` → `needTransmit` → `flushInlineTransmits`). Our own memory is bounded
   separately by `maxInlineBuiltBytes` (bytes, not count — a still is ~tens of KB,
   a 30-frame GIF is a few MB).
2. **Fetch only near the viewport** (`inlineFetchMarginScreens`). The window holds
   ~20× more images than the screen shows. Sighted-but-far images park in
   `pending` and are picked up if you scroll toward them.

**Row reservation is what makes (2) safe.** A post whose image loads later would
*grow*, and a wheel scroll anchors on an absolute row offset (`m.msgFreeOffset`),
so content growing above the viewport jumps the page under the cursor. `sight()`
therefore reserves the thumbnail's rows on first render (`reserveThumbCells`) and
draws blanks until it arrives — the post is its final height from the start. Only
`rows` has to be right, and any image tall enough to hit the height cap lands on
exactly `inlineThumbRows`, which is essentially all of them. Attachments predict
exactly from `FileInfo`; a body URL (Giphy) has no dimensions, so it assumes
`nominalBodyImage{W,H}`. **Corollary:** every fetch outcome — ready, failed *and*
retry — must invalidate the owning post's lines, or a failed image's reservation
stays as a permanent blank hole (`TestFailedThumbReleasesItsReservedRows`).

**Results:** `TestThumbFetchConverges` — an 80-image window went **96 builds → 8**
(96 = 80 + 16 immediately-evicted rebuilds; 8 = only what's within the margin).
`BenchmarkChannelOpenThumbs`: **~0 builds per channel-switch pair** in steady
state, vs. a full rebuild of both channels before.

- **Lesson:** a cache that bounds one resource (terminal memory) must not throw
  away a *different*, more expensive one (the decode + PNG encode). And a
  per-render sighting hook fires for the whole render window, not the screen —
  `renderMessages` is O(loaded posts) by design (see `project_render_window`).

## Not a problem (measured, don't re-chase)
- **Inline-thumbnail animation byte volume.** Re-transmitting a whole PNG per GIF
  frame *looks* alarming at a 10-row placement, but realistic cartoon/video GIF
  content is **~1.9KB/frame (~23KB/s per GIF)** at `inlineThumbRows`. Only
  pathologically noisy/photographic content approaches ~95KB/frame. Not worth
  moving to Kitty native animation frames (`a=f`/`a=a`) on perf grounds alone.
- **Per-event overhead of `image_thumbnails`.** `fetchPendingInlineImages` +
  `maybeStartImageAnim` on the post-dispatch path are in the noise against the
  keystroke's own `View()` (`BenchmarkThumbKeystroke`, thumbs=0 vs 8).

---

## Other things noticed (not measured)
- **cmd-builders copy the whole `Model`** (value receivers) on each call, but
  only fire on actions (send/fetch), not per keystroke — low priority.
- **`Model{...}` literal test convention (109 sites)** is the hard constraint on
  any pointer-boxing: a zero-value `Model{}` must stay usable, so only fields
  never derefed outside a strict mode-gate can be boxed without nil-guards. A
  shared `func testModel(...) Model` constructor would lift this constraint and
  unlock boxing the layout-touched fields — but it's a 109-site test refactor.
