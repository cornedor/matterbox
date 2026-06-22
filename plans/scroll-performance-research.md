# Trackpad scroll performance — research notes

> **Resolution (2026-06-17): Fix #1 + Fix #2 implemented.**
>
> **Fix #1 — scroll-geometry cache split** (`internal/ui/scrollcache.go`). The
> O(content) wrapped-row walk is keyed on `(ver, width)` only; the scroll percent
> is derived arithmetically (`scrollPercentFor`, bit-identical to
> `viewport.ScrollPercent`). A scroll no longer invalidates the cache.
> `BenchmarkScrollGeom`: per-scroll cost dropped from a content-sized walk
> (≈0.6 ms @240 posts, ≈3.2 ms @1200, ≈7.9 ms @3000) to a flat **~70 ns cache
> hit**. Also removed the *second* hidden walk: the old miss path called
> `vp.ScrollPercent()`, which re-runs the same walk via `calculateLine`. Guarded
> by `TestScrollGeomScrollIsCacheHit`.
>
> **Fix #2 — coalesce wheel events onto a frame tick** (`handleMouseWheel` in
> `update.go`). #1 lowered per-event cost but didn't *cap* it: a real MacBook
> trackpad still out-produced the consumer, so `MouseWheelMsg` queued in
> bubbletea and drained at a fixed rate after the gesture ended (constant-speed
> scroll, "keeps going after fingers land", faster swipe → longer scroll — the
> classic FIFO-drain signature). Now `handleMouseWheel` only accumulates the net
> delta (`wheelPending`/`wheelTarget`, O(1)) and arms one `wheelFlushMsg` tick
> (16 ms ≈ one frame, `wheelTicking` guards single-arm); `applyWheel` moves the
> viewport once per frame. Because Update is O(1) the queue never backs up, so the
> scroll tracks the gesture and stops within a frame of fingers-down, and a faster
> swipe now scrolls *faster* (bigger per-frame delta) instead of *longer*. The
> sticky free-scroll flag is set on the event itself so a mid-burst re-render
> keeps the offset; `applyPendingWheel` flushes before any key/click so input acts
> on the final position. Guards: `TestWheelCoalescesBurst`,
> `TestWheelEntersFreeScrollBeforeFlush`, `TestInputFlushesPendingWheel`.
>
> **Fix #3 (tune `MouseWheelDelta`)** left undone — pure feel knob, not needed.
>
> **Update — Fix #1 + #2 still weren't enough; the real root cause was render cost
> per message.** On a real M2 Max the scroll still drained after the gesture
> (constant speed, faster swipe → longer). Profiling `viewContent()` (see
> `BenchmarkViewContentScroll`) showed a full render is **~4 ms, and ~75% of it is
> `ansi.stringWidth`** — lipgloss re-measuring grapheme widths of *unchanged*
> content every render (viewport soft-wrap + border styling + the pane joins).
> Because bubbletea rebuilds `View()` after **every** message (`tea.go:872-888`),
> a buffered wheel flood drains at only ~250 renders/sec, so it lags behind the
> gesture. (Claude Code, same machine/terminal, scrolls fine because its renders
> are far cheaper — proof it's our render, not the terminal.)
>
> **The fix that worked: cache the whole `viewContent()` output** (`viewCache.view`
> in scrollcache.go). `update()` invalidates it on every message *except* a wheel
> event — which (thanks to Fix #2's coalescing) only accumulates `wheelPending`
> and changes nothing on screen until its flush tick. So during a flood the
> buffered events return the cached frame; only the 60fps flush ticks do a real
> render. `BenchmarkWheelFloodRender` confirms per-event render work drops from
> ~4 ms to **0** (residual ~200 µs is pure bubbletea Model-boxing/GC, no
> `stringWidth`). The invalidation is fail-safe — default is *invalidate*, wheel
> is the only opt-out — so the cache can never mask a real change. Guard:
> `TestViewCacheInvalidatedExceptWheel`. The three pieces compose: #1 makes the
> real per-tick render cheaper, #2 stabilises the offset between ticks, and the
> view cache makes the per-event flood free.

**Symptom:** Scrolling performance is terrible with a MacBook trackpad (laggy,
stuttery, keeps scrolling after the gesture ends).

**Where the code lives:** The wheel-scroll code is on the **`feat/mouse-wheel-scroll`**
branch (main checkout at `/home/corne/Development/matterbox`), with uncommitted
edits to `mouse.go` / `update.go` / `feed.go` / `search.go`. Enabling real wheel
events is what unleashes the trackpad flood. Fixes should land on that branch.

---

## Root cause (three compounding factors)

### 1. A MacBook trackpad emits a flood of wheel events
Two-finger scroll + inertial momentum produces a high-frequency stream of
`MouseWheelMsg` (easily 60–120/sec), and momentum keeps firing for a second or
more *after* the fingers lift. A notched mouse sends a handful per second; the
trackpad sends an order of magnitude more.

### 2. Bubbletea v2 calls `View()` once per message, synchronously, with no throttle
The event loop is literally `Update(msg)` then `render(model)` → `model.View()`
for *every* message, on a single goroutine:

- `charm.land/bubbletea/v2@v2.0.6/tea.go:872–880` — `model, cmd = model.Update(msg)` then `p.render(model)`
- `tea.go:886–890` — `p.render` → `p.renderer.render(model.View())`

So N wheel events = N full `View()` builds. If each takes longer than the gap
between events, the `p.msgs` channel backs up and the buffered momentum drains
*after* the gesture ends — that is the "keeps scrolling / laggy" feel.

### 3. Each scroll event currently triggers an O(content) walk (the big one — in our code)
`scrollGeomFor` caches the scrollbar geometry keyed on
`(ver, width, height, yOffset)`:

- `internal/ui/scrollcache.go:59` — `scrollGeomFor`
- `internal/ui/scrollcache.go:64` — `total := viewportVisualRows(vp.GetContent(), w)`
- `internal/ui/view.go:655` — `viewportVisualRows` → `strings.Split` + per-line width walk over the whole loaded window

Every wheel event changes `yOffset`, so the cache misses and re-runs the full
`viewportVisualRows` walk over the *entire* loaded content (up to the 400-post
render window), for the msgs pane *and* thread *and* ref pane, 2–3× per render
(see the comment at `scrollcache.go:11–25`).

But the `totalRows` walk only depends on `(content, width)` — **not on
`yOffset`.** Only the cheap scroll *percent* depends on offset. So the expensive
part is being redone on every scroll for no reason.

---

## What is already fine (don't chase it)

Terminal *writes* are already coalesced:

- `tea.go:1414` — the renderer flushes on a 60fps ticker, taking only the latest stored view
- `cursed_renderer.go:579–584` — `render()` just stores the latest view under a mutex (cheap)
- `cursed_renderer.go:287` — `flush()` has a `viewEquals` no-op short-circuit

So raw ANSI I/O is not the unbounded cost. The unbounded cost is the synchronous
per-message `Update` + `View()`, dominated by factor #3.

---

## Why "debounce" is the wrong tool

Debounce = wait until events *stop*, then act once. For scrolling that freezes
the view mid-gesture and jumps at the end — terrible feel. The right tool is
**throttle / coalesce**: apply accumulated deltas at a bounded frame rate so the
view still moves smoothly but the expensive work per second is capped. This is
the same idiom already used for the `WindowSizeMsg` resize storm (cheap work per
frame + deferred heavy work on a settle tick — see `update.go:45–74`).

---

## Recommended fixes (priority order)

### Fix #1 — split the scroll-geometry cache (keystone, low-risk, do first)
Cache `totalRows` on `(ver, width)` only; derive `percent` cheaply from
`yOffset / totalRows` every call. Removes the O(content) walk from every scroll
event — wheel *and* keyboard — turning per-scroll `View()` into ~O(visible).
This alone may make trackpad scrolling acceptable, since the renderer's 60fps
flush already absorbs the rest.

### Fix #2 — coalesce wheel deltas onto a frame tick (if #1 is not enough)
In `handleMouseWheel` (`internal/ui/update.go:2231`), accumulate the delta into a
pending counter and schedule a ~16–33ms tick instead of moving the viewport per
event; apply the summed delta once per tick. Mirrors the resize-settle pattern.
Caps the residual per-event work and the diff churn.

### Fix #3 — tune `MouseWheelDelta` (currently 3)
Pure feel knob (`charm.land/bubbles/v2@v2.1.0/viewport/viewport.go:151`), not a
cause. Worth a look only after #1/#2.

### Minor / secondary
`refreshAnimVisibility()` runs per wheel event (`update.go:2262`) and scans posts
when animated emoji are loaded (free otherwise — early return when no animated
names). Not the bottleneck, but it could ride the same coalescing tick.

---

## Suggested approach

Implement **#1 first and measure** — it is a clean, contained change that helps
all scrolling, and is very likely most of the problem. Add **#2** if the full
belt-and-suspenders is wanted after measuring.
