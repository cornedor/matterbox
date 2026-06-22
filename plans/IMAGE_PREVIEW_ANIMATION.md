# Animated GIFs

## Status

**Custom emoji — done.** Animated-GIF custom emoji cycle through their frames in
place (`internal/ui/emojiimg.go`). Gated by the `animations.custom_emoji` config
toggle (an object so more toggles can join it — see below). The frame decode and
the animation loop live entirely in `emojiimg.go`; nothing in the render path
changed.

**Image-preview modal — done.** The preview modal (`space` on a message with an
image, `internal/ui/preview.go`) animates GIFs with the same technique, gated by
`animations.image_preview`. It previews both uploaded attachments and image URLs
in the message body — the `![](…)` links a GIF picker posts (and any bare/linked
URL whose path ends in an image extension), fetched over HTTP and cached on disk
by URL hash. See "In the preview modal" below.

## How emoji animation works

The terminal will not animate a static placeholder on its own. Two routes exist:

1. **Two image ids, flipped per frame** — what `timg` does. Each frame targets a
   different id and the placement re-points at it. Routes around an old Ghostty
   bug (ghostty-org/ghostty#1037, fixed in #1043) where re-transmitting to an
   *already-known* id didn't repaint. Cheap for `timg` (absolute placements) but
   for our **Unicode virtual-placeholder** the id rides in the placeholder cells'
   foreground — flipping ids would mean rewriting every cached message line that
   shows the emoji, every frame. That fights the render-window/line cache.

2. **One id, re-transmitted per frame** — what we do. The emoji keeps a single
   image id; each frame is a prebuilt transmit APC (`frameSeqs`) targeting that
   id. The animation tick re-emits the due frame out of band (`tea.Raw`); the
   placeholder cells already on screen keep pointing at the same id, so the
   terminal repaints them with **no re-render and no cache invalidation**. This
   relies on the terminal repainting a virtual placement when its image data is
   replaced — true on current Kitty and Ghostty (post-#1043), which are the only
   terminals this feature targets.

Pieces in `emojiimg.go`:

- **`decodeImageFrames` / `compositeGIF`** — `gif.DecodeAll` yields per-frame
  *sub-images*; `compositeGIF` layers each onto a persistent RGBA canvas honoring
  the disposal methods (`DisposalNone` / `DisposalBackground` /
  `DisposalPrevious`) and snapshots every composited frame. Per-frame delays come
  from `gif.GIF.Delay` (hundredths of a second), clamped by `clampGIFDelay`
  (0/absurd → ~100ms, the browser convention).
- **Build** — `buildReadyEmoji` (on the fetch goroutine, off the render loop)
  decodes frames and prebuilds one transmit APC per frame plus the placeholder.
  A still image / single-frame GIF / animations-off all collapse to one frame.
- **State** — `emojiImgEntry` carries `frameSeqs`, `delays`, `frameIdx`,
  `frameStart`. A still emoji has one frame and never ticks.
- **Tick** — a single self-rescheduling `emojiAnimTickMsg` loop (guarded by
  `Model.emojiAnimating` so only one runs) advances every animated emoji whose
  frame is due via `advanceFrame`, emits the concatenated re-transmits, and
  reschedules from the soonest next-due frame (floored to `emojiAnimMinInterval`
  to cap the wakeup rate). The loop stops when nothing is left to animate.
- **Cache** — `cachedEmojiPath` now stores the **original** downloaded bytes
  (format sniffed on read), so a warm restart can still decode every frame.

## In the preview modal

`preview.go` reuses `decodeImageFrames`/`compositeGIF`/`clampGIFDelay` and the
same single-id re-transmit:

- **Sources** — `previewImages` enumerates a post's previewable images via
  `collectOpenables` (the same extractor `o` uses): uploaded attachments with a
  decodable MIME, then body image URLs (`isPreviewableImageURL` keys off the URL
  path extension, ignoring the query string a CDN like Giphy stuffs with cache
  ids). Each is a `previewItem` (a `*model.FileInfo` *or* a URL).
- **Fetch** — `readPreviewBytes` downloads an attachment through the Mattermost
  client (cached by file id) or an external URL over HTTP (capped at
  `previewMaxImageBytes`, cached on disk by URL hash via `cachedURLPath`).
- **Animate** — on load, frame 0 is transmitted and a multi-frame GIF arms
  `handlePreviewTick`: a per-modal loop guarded by `previewGen` so a tick from a
  cycled/closed preview is dropped. `resizePreview` re-fits the current frame;
  the loop keeps emitting subsequent frames at the new size.

Cost: a full-frame transmit per tick is fine for a modal; downscale toward the
display box only if large GIFs lag (would need `golang.org/x/image/draw`).

### Why not shell out to `timg`

`timg`/`chafa`/`viu` are standalone viewers: their value is multi-protocol
auto-detection plus ASCII/half-block fallbacks (not wanted — we're Kitty-only),
they own the screen with absolute escape sequences (can't live in a Bubble Tea
modal that repaints every frame), and the only way to run one is to suspend the
whole TUI. So we reuse the **technique**, not the tool.
