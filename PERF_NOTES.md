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

## 9. Every transmitted image allocated a fresh zlib writer — ✅ DONE
Every image matterbox shows — custom emoji, inline thumbnails, the preview modal
— is PNG-encoded and base64'd into a Kitty APC by `kittyTransmitImage`, and a
pprof of scrolling an image-heavy channel put ~70% of all CPU there. The kitty
library calls `png.Encode`, which **cannot be given a `BufferPool`** — so it
allocates a fresh zlib writer and scratch buffers on *every call*: ~860KB of
garbage per frame, costing more than the compression itself.

`kittyTransmitImage` is now `kitty.EncodeGraphics` reimplemented around a shared
pooled `png.Encoder` (`kittyPNG`). The framing — option keys, base64, 4KB
chunking, `m=1`/`m=0` — is byte-for-byte what the library produces, pinned by
`TestKittyTransmitMatchesLibraryFraming` against the library itself.

    EncPNGDefault          15.2 ms   140 KB   856 KB/op, 28 allocs   ← stdlib png.Encode
    EncPNGDefaultPooled     7.7 ms   140 KB   1.4 KB/op,  0 allocs   ← the pool is the win
    EncPNGBestSpeedPooled   5.6 ms   140 KB   1.0 KB/op,  0 allocs   ← chosen
    EncZlibRaw              3.6 ms   224 KB                          ← no filter search

    kittyTransmitImage:  19.1 ms → 10.5 ms/frame,  2.9 MB → 1.3 MB/op

**Compression level barely matters** for a thumbnail-sized frame (BestSpeed and
Default are within 0.5% on photographic content), so BestSpeed is free speed.
After pooling, the remaining bottleneck *moved*: `png.filter` is now ~75% of the
encode, because stdlib PNG tries all five filter types per row and picks the
smallest via `abs8` sums. `EncZlibRaw` bounds what removing that could buy —
1.5×, at **60% more bytes**, for a hand-rolled PNG encoder or a raw-RGBA wire
format. Not taken: the bytes go down a tty that re-sends GIF frames live.

**Beware:** `TestKittyTransmitMatchesLibraryFraming` compares against the real
library but can only reach payload sizes real PNGs happen to produce — **none of
which land on a chunk boundary**. A payload that is an exact multiple of
`MaxChunkSize` must end with an *empty* `m=0` chunk, and a `for len(p) > K` loop
silently drops it (the terminal then waits forever for a continuation).
`TestKittyChunkMatchesReadFullLoop` is what actually covers that, against a
transcription of the library's `io.ReadFull` loop.

## 10. GIF thumbnails encoded every frame at fetch time — ✅ DONE

A GIF only animates while it is *on screen*, and the fetch margin
(`inlineFetchMarginScreens`) deliberately builds several screens' worth of images
to display one screenful — so nearly every GIF whose frames we encoded was one
nobody ever watched. At ~10ms per frame (§9), that was the dominant cost left in
an image-heavy channel.

`buildInlineThumb` now builds a GIF as a **still — its first frame and nothing
else**. The rest are encoded only if the thumbnail actually reaches the visible
rows: `inlineImages.deferred` → `buildVisibleThumbFrames` (an Update kicker, right
after `flushInlineTransmits` refreshes what's on screen) → `markFramesBuilt`.

    frames    eager (was)   fetch (now)   on-screen completion
    1            13 ms        13 ms         —
    30          350 ms        13 ms        350 ms
    90          740 ms        13 ms        730 ms

**The fetch cost is flat in the frame count** — `gif.Decode` returns at the first
image descriptor, so the other 89 frames' LZW streams are never touched. A GIF you
scroll past costs what a still costs (**27× / 57×** less at 30 / 90 frames); a GIF
you actually watch pays ~0.5% more than before (frame 0 gets encoded twice), which
is the trade.

Everything rests on the still being **frame 0 of the full decode, bit for bit**.
`decodeFirstGIFFrame` therefore paints the decoded frame onto the logical-screen
canvas exactly as `compositeGIF` does — a raw `gif.Decode` hands back the first
*sub-rectangle*, which for an offset first frame has different bounds, and the
placement is sized from those bounds. Get that wrong and the post silently changes
height when the frames land. `TestFirstGIFFrameMatchesComposite` pins the identity;
`TestGIFThumbBuildsStillFirst` pins that the still's transmit APC is byte-identical
to the full build's frame 0 at the same cell box.

Because the frames carry the still's own id and cell box, installing them changes
*nothing that is drawn*: no invalidation, no re-render, and
`inlineThumbFramesMsg` joins `imgAnimTickMsg` in `preservesFrame`. The next
animation tick simply has somewhere to go.

## 11. Collapsing a post (`z`) takes its thumbnails off the budget — ✅ DONE

§7–§10 made a GIF cheap to *scroll past*. What was still unavoidable was a GIF you
are looking at: on screen, it animates, and animating is the one thing here that
costs anything per frame. `z` is the manual lever for that — collapse the message
and its images go quiet.

The trick is that collapsing had to unhook the post from the machinery, not just
skip drawing it. All three viewport questions — *what is worth fetching*, *what is
on screen* (so must not be evicted), *what animates* — are answered by
`thumbKeysInRows` walking the posts in view, and a collapsed post is as in view as
ever. One `continue` there (`m.thumbsHidden(p)`) answers all three at once:

- **Not animated.** `refreshAnimVisibility` drops it, `advanceImageAnim` stops the
  loop when it was the last GIF on screen (`TestCollapsedGIFStopsAnimating`).
- **Not fetched.** A thumbnail collapsed before it arrives is never built — no
  download, no decode, no ~10ms/frame encode (`TestCollapsedThumbNotFetched`).
- **Not resident.** `releaseThumbs` → `queueRelease` hands the image's terminal
  memory straight back (`kittyDelete`) rather than waiting for the LRU, but keeps
  the built frames, so expanding is a re-transmit of a string we already hold, not
  a rebuild. The queue is drained in `takeTransmits`, *after* the render has
  settled what is on screen — the same image may be drawn by another, uncollapsed
  post (`TestReleaseSparesImageShownElsewhere`).

## 12. A GIF was decoded whole before being made small — ✅ DONE

§7–§11 are all about *time*: how often a GIF is rebuilt, encoded, animated. This
one is about *memory*, and it was the larger number. `compositeGIF` painted one
fully-resolved RGBA frame **per GIF frame at the source's own resolution**, and
both consumers took the whole slice before downscaling any of it. A GIF has no
size or frame-count ceiling anywhere in the pipeline (SVG has `svgThumbMaxBytes`,
STL has `stlThumbMaxBytes`, video has its `videoProfile`), so the cost was
frames × width × height × 4, with the compressed file size no guide at all.

Measured against the real attachment cache:

- **Inline thumbnail** (`loadInlineThumbFrames`, a GIF scrolling on screen):
  `creepy-hamster.gif`, 3.0 MB on disk, peaked at **178 MB** to keep 4 MB of
  encoded output. A 0.8 MB GIF peaked at 110 MB.
- **Preview modal** (space on a GIF): held **128 MB** for as long as the modal was
  open — for a **0.4 MB** file. 91.5 MB of that was `previewState.frames`, kept
  only so a resize could re-fit. Median GIF in a real cache: ~10 MB.
- End to end, RSS went 40 MB → 249 MB opening one preview, and stayed at 240 MB
  after closing it (Go's scavenger returns the arena slowly; there are no GC
  knobs set anywhere in the tree).

**Fix, in three parts:**

- `eachCompositeGIFFrame` hands each composited frame to a callback off a single
  shared canvas instead of collecting clones (`frameFunc`'s `shared` flag is the
  contract: consume it before returning, copy it if you keep it). `compositeGIF`
  is now that function with every frame cloned, which is all the emoji path
  wants. `eachFrame` picks the streaming path only for animated GIFs and falls
  back to the batch decoder for everything already bounded at its source.
- Both big consumers fit-and-encode inside the callback — `buildThumbFrames`
  (replacing the decode-then-`encodeInlineThumbNative` pair) and
  `buildPreviewFrames`. `frameFitter` reuses one downscale destination across a
  run of same-sized frames, which for a 90-frame GIF at a full-screen placement
  is ~300 MB of churn removed.
- `previewState` no longer keeps the frames at all — only `frameCount`, the
  delays, and frame 0 (for the placement aspect and the caption). `reencodePreview`
  re-reads the bytes from the disk cache the first load populated and re-decodes,
  because a resize is debounced and an open GIF is not.

**Result:** thumbnail build peak **178 → 63 MB**; preview retained **128 → 35 MB**
(decoded frames held: 91.5 → 1.0 MB); RSS through one open/close **249 → 168 MB**.
What is left in both is proportional to the *compressed* GIF — `gif.DecodeAll`
still materialises every paletted sub-frame, and stdlib offers no streaming
decoder — plus the pre-encoded per-frame sequences the animation actually needs.

## 13. Two allocation sites a live heap profile named — ✅ DONE

§12 fixed what was *held*. Profiling the running TUI while scrolling an
image-heavy DM (`matterbox --pprof localhost:6060`) showed what was *churned*:
`TotalAlloc` 3.57 GB in six minutes, against a live heap of only **82 MB**. RSS
read 327 MB, but `HeapIdle` was 325 MB with `HeapReleased` 230 MB — Go had
already handed those pages back and Linux simply had not reclaimed them. Not a
leak; an arena sized for the allocation peaks between GCs.

Two sites were worth fixing on their own merits:

- **`x/image/draw` rebuilt its scaler on every frame — 355 MB, the largest single
  site.** `Kernel.Scale` calls `newScaler(..., usePool=false)`: fresh weight
  tables and a fresh `dw×sh [4]float64` scratch buffer per call, megabytes at
  real sizes. `NewScaler` is the same object kept, pooling the scratch. Caching
  one on `frameFitter` (and using a fitter in `encodeInlineThumb` and
  `encodeStreamFrames`, the other per-frame scale loops) took a 89-frame fit run
  from **523.6 MB / 714 allocs to 3.6 MB / 13 allocs — 145×**, and 4.1× faster.
- **`strings.Builder` cost 5× the finished string — 216 MB.** Go's `append` grows
  a large slice by ~1.25×, so the copies compound; a Builder fed N frame
  sequences allocates roughly five times the result. Collecting into a `[]string`
  and `strings.Join`-ing once sums the parts and allocates exactly once:
  **23.3 MB → 4.4 MB, 5.8× faster** for an 89-frame setup. Applied in
  `buildThumbFrames` and `buildPreviewFrames`; `handleInlineThumbFrames` already
  has every piece in hand, so it just sizes its Builder up front.

`TestFrameFitterReusesItsScaler` pins the first with `AllocsPerRun`.

**Still on the table, in order of RSS won per unit of work:**

1. **No GC knobs are set anywhere in the tree.** With 82 MB live and the default
   GOGC=100, `NextGC` sits at 165 MB and the arena sizes for the peaks.
   `debug.SetMemoryLimit` in `runTUI` would cap it directly — the single biggest
   RSS lever left, and one line.
2. **`maxInlineBuiltBytes` is 64 MiB.** 44 MB of one DM's GIF native-animation
   setups was over half the live heap. 16 MiB still holds hundreds of stills.
3. **The `Model` dispatch chain is 1.25 GB of the 3.5 GB** — `Update` → `update`
   → `dispatchNav`/`handleKey`/`View` each heap-box another ~102 KB copy, ~330 KB
   per event, and scrolling floods events. This is §6, and scrolling is the
   workload that maximises it.
4. A 20 MB `bytes.Buffer` sits retained inside bubbletea's `cursedRenderer.flush`,
   grown to fit one multi-megabyte `tea.Raw` write and never shrunk. Chunking the
   native setup across events would keep it small.

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
