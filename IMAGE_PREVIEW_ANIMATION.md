# Image preview — animated GIF (out of scope, future work)

The image-preview modal (`space` on a message with an image attachment, see
`internal/ui/preview.go`) renders a **static** image via the Kitty
Unicode-placeholder protocol — the same mechanism custom emoji use
(`internal/ui/emojiimg.go`). An animated GIF currently shows its **first frame
only** (whatever `image.Decode` returns), which matches how custom emoji handle
GIFs.

Playing the animation was deliberately deferred: it can't be borrowed from an
existing tool and has to be hand-wired into our render loop. This note records
the design so it can be picked up later.

## Why not shell out to `timg`

`timg` (and `chafa`/`viu`/…) are standalone terminal image viewers. Reusing the
**binary** doesn't fit:

- Their main value is multi-protocol auto-detection plus **ASCII / half-block
  fallbacks** — explicitly not wanted here (we are Kitty-only).
- They can't live inside our modal. `timg` writes absolute-positioned escape
  sequences and runs its own animation loop owning the screen, whereas our TUI
  repaints every frame and depends on the Kitty **Unicode virtual-placeholder**
  variant so an image moves with the layout and survives repaints. Splicing
  `timg`'s output into a Bubble Tea frame fights the renderer.
- The only way to run it is to suspend the whole TUI (`tea.Exec` exists in
  bubbletea v2) and hand off full-screen — not a modal, and a runtime
  dependency the user must install.

So if we animate, we reuse the **technique**, not the tool.

## The technique (from ghostty-org/ghostty#1037)

The terminal will not animate a static placeholder on its own. `timg` animates
over Kitty by **double-buffering two image ids and flipping between them per
frame**, with the application driving frame timing. Issue #1037 / fix #1043 was
a Ghostty bug where re-transmitting to an *already-known* id didn't repaint;
the two-id flip routes around it (each frame targets a different id, so even a
terminal that ignores "update existing id" still repaints).

## Sketch of the in-process implementation

Everything below sits on top of the existing transmit + placeholder helpers, so
it's additive — no renderer changes.

1. **Decode all frames.** `gif.DecodeAll` yields per-frame *sub-images*, not
   ready-to-show full frames. Composite each onto a persistent RGBA canvas
   honoring the GIF disposal methods (`DisposalNone` / `DisposalBackground` /
   `DisposalPrevious`); snapshot each composited frame. Capture per-frame
   delays from `gif.GIF.Delay` (hundredths of a second; clamp 0 → ~100ms).

2. **State.** Extend `previewState` with `frames []image.Image`,
   `delays []time.Duration`, `frameIdx int`, a second image id, and a `cur`
   flip index. A still image keeps a single frame and never ticks.

3. **Tick loop.** On load, transmit frame 0 and (if `len(frames) > 1`) schedule
   `tea.Tick(delays[0], …)`. Each `previewTickMsg`:
   - transmits the next frame to the **other** id (`kittyTransmitImage`),
   - points the placeholder at that id (re-render),
   - schedules the next tick from `delays[next]`.
   Guard every tick with the `previewGen` counter so a tick from a closed or
   cycled preview is dropped. Scope the tick to `preview.active` so there are no
   idle wakeups.

4. **Cleanup.** Free **both** ids with `kittyDelete` on close / cycle.

5. **Cost control.** Transmitting a full frame per tick is fine for a modal, but
   downscale frames toward the display box if large GIFs lag (no scaler in the
   stdlib — would add `golang.org/x/image/draw`).

The static path already factors the hard parts (sizing/aspect via
`fitImageCells`, transmit via `kittyTransmitImage`, placeholder via
`kittyPlaceholder`, id cleanup via `kittyDelete`), so animation is mostly the
frame decode + the tick loop.
