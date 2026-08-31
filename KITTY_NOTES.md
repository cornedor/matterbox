# Kitty graphics — what the STL viewer taught us, and where else it applies

Notes from PR #30. Everything below was measured, not assumed. Nothing here is
done outside `internal/stl` + the STL viewer; this is the list of places the same
fixes apply.

---

## 1. Re-transmitting an image id strobes on Ghostty

A terminal is entitled to drop an image the moment a *new* transmission under the
same id **starts**, not when it completes. Ghostty does exactly that. A frame is
tens to hundreds of KB of base64 in 4KB APC chunks, so "while the upload is in
flight" is most of a frame interval, during which the placeholder cells point at
an image that no longer exists.

`preview_stream.go` already documented this and worked around it with a 4-slot
image-id ring. That ring costs a `View()` re-render per frame (the placeholder
cells have to be repainted with the new id) and needs 4 slots rather than 2,
because bubbletea flushes the `tea.Raw` buffer and the rendered View separately.

### The better fix: double-buffer *inside* the terminal

Kitty animation frames, used as a buffer rather than an animation:

- the image carries two spare frames, created once with `a=f`;
- each new frame uploads into whichever spare is **not** displayed — invisible,
  however many chunks it takes;
- one tiny `a=a,i=…,c=<n>` switches the placement onto it.

Playback stays stopped (every image starts stopped; never send `s=`), so frames
only move when we say so. The image id and the placeholder cells never change,
so **nothing re-renders and there is no window where the id names no pixels.**

Verified against Ghostty's source (1.3.2-main), not the docs — the docs and the
web both say Ghostty has no `a=f`, which is stale:

- `graphics_exec.zig` — editing a non-displayed frame calls `markMutated` only,
  so no texture re-upload and nothing repaints.
- `a=a,c=` calls `markImageContentChanged`, which bumps `img.generation` → one
  repaint, with a complete frame.
- `graphics_image.zig:769` — the renderer draws `anim.current_index`, so the swap
  is what ends up on screen.

Implementation: `kittyCreateBlankFrame` / `kittyEditFrameRaw` / `kittyShowFrame`
in `kittyanim.go` + `kittyraw.go`; driver in `stlview.go` (`stlFrameSeq`).

### An id ring is not enough on its own

The video player rotates frames through 4 image ids so a frame is never uploaded
over the id the cells name. It still strobed every 10-30 s, because the upload
and the cell switch don't reach the terminal together:

- bubbletea hands the View to the renderer **synchronously**, the moment `Update`
  returns (`tea.go`'s event loop calls `p.render(model)` right after `Update`);
- `tea.Raw`'s bytes only reach the output buffer after a Cmd goroutine *and*
  another trip through the message loop (`BatchMsg` → goroutine → `RawMsg` →
  `p.execute`);
- the renderer flushes at 60 Hz, raw buffer first, then the cell diff.

So a flush landing in that gap emits cells naming an id whose transmission has
not started — and the next thing that happens is a transmission under exactly
that id, which the terminal is entitled to drop on start. Rare (µs gap against a
16.7 ms tick) and therefore intermittent. Fixed by ordering the two explicitly:
`tea.Sequence(tea.Raw(frame), promote)`, with a `streamPromoteMsg` that moves
`preview.id`/`img` onto the frame whose bytes are already buffered.

**The general rule: a `tea.Raw` payload and the View that depends on it are not
in the same frame.** Anything where a rendered cell references out-of-band
terminal state has to sequence the two, or stop making the cells change at all
(§1). That includes `kurve.go`/`gorillas.go` if they keep re-transmitting.

### Where else this applies

| site | shape today | strobes? |
|---|---|---|
| `kurve.go` | one `imgID`, `kittyTransmitImage` per frame at a **45 ms tick**, near-full-terminal box | **yes — identical to what the STL viewer had** |
| `gorillas.go` | same shape (`gorillas.go:616-647`) | same |
| `preview_stream.go` (video) | 4-slot id ring | **it did, rarely** — see below; fixed 2026-08-25. Still pays a View() re-render per frame and 4 resident images |
| GIF/emoji manual path (`inlineImages.advanceFrame`) | re-transmits `frameSeqs[i]` per tick under one id | small payloads, so the window is short — but a large GIF in the preview modal is the same bug |

The two games are the cleanest wins: they are structurally the code the STL
viewer was before this PR, so the fix ports almost verbatim.

For **GIFs specifically** there is a better answer that already exists:
`native_animation` uploads every frame once and lets the terminal own the loop
(`buildNativeAnimSetup`). It is a plain `bool`, default **false**, gated because
a terminal that mishandles `a=f`/`a=a` would show a frozen or blank image. See
§2 — that gate can now be a probe instead of a config flag.

---

## 2. Probe the capability, don't config-flag it

A terminal that doesn't implement `a=f` ignores the APC **without a word**, so
silence has to be read as "no". That is answerable at runtime:

- send the arming `a=f` with `q=0` — the reply is the only signal;
- `OK` → the feature is live for this session;
- an error reply, or silence, → stay on the old path forever.

Steady-state commands then use `q=1`: quiet on success (an `OK` per frame at drag
rate is a reply storm), loud on failure, so a later failure *disarms* and the
next frame rebuilds from scratch. Self-healing, and the fallback is exactly
today's behaviour.

Routing: `update.go`'s `uv.KittyGraphicsEvent` case, matched on image id.

This is what `native_animation` wanted and couldn't have. Same trick would suit
any other optional protocol corner we're currently too nervous to default on.

### Latent bug found on the way

`kittyTransmitFrame` (the native-GIF path) sends **no `q=` at all** — the
terminal answers `OK` for every frame of every GIF. Harmless today because
nothing reads those replies, but it is unwanted terminal chatter and it will
confuse anything that starts reading input more carefully. One-line fix; not
touched in PR #30.

---

## 3. PNG is the wrong encoder for synthetic frames

`image/png` runs a per-row filter heuristic (tries all five, picks the lowest
sum). On a photograph that earns its keep. On a **flat-shaded render with a
mostly-transparent background** it finds nothing to predict and costs a full pass
per row trying. Measured on a real 140k-facet STL frame:

| box | PNG | raw RGBA + zlib (`f=32,o=z`) |
|---|---|---|
| 872×656 | 16.2 ms / 53 KiB | **5.7 ms / 48 KiB** |
| 1200×900 | 25 ms / 83 KiB | **6.4 ms / 80 KiB** |

3–4× faster **and slightly smaller** — no trade to weigh.

Note this contradicts the repo's own `BenchmarkEncZlibRaw`, which measured
zlib-raw as 60% *bigger*. That bench uses photographic content. **The answer is
content-dependent, so measure per surface rather than porting the conclusion.**

Gotchas:

- `image.RGBA` is alpha-**pre**multiplied and the protocol is not, so the pixels
  must be un-premultiplied (this is what `png.Encode` does on the way out).
  `TestKittyRawMatchesThePNGItReplaces` pins the output byte-for-byte against the
  PNG path so the conversion can't drift.
- The divide only costs anything on partially covered pixels — at SSAA 1 there
  are none, so a drag frame pays nothing.
- Raw pixels carry no header: `s=`/`v=` are mandatory.
- Pool the zlib writer (~1 MB of window/tables) and the scratch row.

### Where else this applies

- **`kurve.go` / `gorillas.go`** — flat vector-ish frames, the best possible case
  for this. Almost certainly the same 3–4×. Not measured.
- **Video frames** (`preview_stream.go`) — photographic, so probably a *loss* on
  size. Measure before touching.
- **Inline screenshot thumbnails** — text and flat UI panels, i.e. somewhere in
  between. Worth measuring; unknown.

Only the STL viewer's per-frame edits switched. The transmit that *establishes*
an image stays on the PNG path deliberately: the one thing between the user and
an empty modal should not also be the new thing.

---

## 4. Blast-radius rule that worked

Root transmit → old, proven path. Steady-state frames → new path, gated behind a
probe, with the old path as the fallback. A regression can then only cost frame
rate, never a blank screen. Worth repeating for the games and the video player.

---

## 5. Not reusable

The mesh work (`Mesh.orient`, backface culling, the edge-balance and
normal-closure winding tests) is STL-specific. The only transferable part is the
*shape* of it: two cheap independent checks, each blind to what the other
catches, both conservative, so a mesh that fails either just renders the way it
always did.

---

## Open items, in rough order of value

1. Port the frame double-buffer to `kurve.go` and `gorillas.go` — same bug, same
   fix, and they are modal-sized at 22 fps.
2. Measure PNG vs zlib-raw for the games; adopt if it holds.
3. Replace the `native_animation` config flag with the runtime probe, and give
   GIFs the terminal-owned loop by default.
4. Add `q=` to `kittyTransmitFrame`.
5. Revisit the video ring: the frame swap would drop it to one image id and
   remove its per-frame `View()` re-render (and the promote hop the ordering fix
   costs). Measure the encoder separately — photographic content may well keep
   PNG.
6. STL settle frame is still 94 ms at 1200×900 (`ssaaFor`'s 6M-sample budget says
   yes at that size) — a visible hitch on mouse-up.
