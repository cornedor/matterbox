package ui

import (
	"errors"
	"image"
	"time"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/telemetry"
)

// Streaming video playback for the space-to-preview modal. Where an image or GIF
// is decoded whole and then displayed/looped, a video is played incrementally:
// a background Cmd decodes a chunk ahead while the tick plays the buffered
// frames and evicts them, so a long clip starts after ~one chunk and never holds
// more than streamBufferFrames at once. It plays once and stops on the last
// frame (streamDone). Inline thumbnails do NOT stream — they stay on the one-shot
// decodeVideoFrames with the tight thumbVideoProfile. See video.go for the
// videoStream interface + profiles, and preview.go for the shared modal.
//
// Ownership / lifecycle of the live decoder is the delicate part. The stream is
// only ever touched by one goroutine at a time: the decode-ahead Cmd while
// streamFetching is set, the main loop otherwise. Closing it therefore follows
// one rule — if a Cmd holds it (streamFetching), the main loop must NOT free it;
// the Cmd's result (stale by then) frees it instead (see handleStreamChunk's
// stale branch and teardownPreviewStream). On normal EOF, the current-gen
// handler frees it once no chunk is outstanding.

// previewStreamMinInterval floors the streaming tick. Lower than the GIF path's
// so a 24fps clip isn't slowed to 20fps; it still bounds a pathological delay.
const previewStreamMinInterval = 8 * time.Millisecond

// previewStreamUnderrunPoll is how often we re-check an empty buffer while the
// decoder catches up. Deliberately much coarser than the frame floor above:
// every tick is a full View() re-render, and polling an underrun at the frame
// rate would spin the render loop at 125Hz for nothing — the wait is for a
// decode Cmd, not for a frame that's due.
const previewStreamUnderrunPoll = 25 * time.Millisecond

// previewStreamTickMsg drives streaming playback (distinct from the GIF
// previewTickMsg so the two paths never cross). gen drops a tick from a preview
// the user has since cycled or closed.
type previewStreamTickMsg struct{ gen int }

// streamOpenedMsg carries the opened decoder plus its first decoded+encoded
// chunk. A stale one (the user closed/cycled during the open) still owns the
// stream, so its handler frees it.
type streamOpenedMsg struct {
	gen        int
	stream     videoStream
	buf        []streamFrame
	eof        bool
	cols, rows int
	caption    string
	err        error
	// started dates the open + first chunk, for media_rendered. Video is the
	// most fragile graphics path and the one most likely to be missing
	// altogether (the `video` build tag).
	started time.Time
}

// streamChunkMsg carries one decode-ahead chunk. rows/cols record the placement
// it was encoded for, so a resize since it was kicked is detected and the (now
// wrong-size) frames dropped. stream is the decoder the producing Cmd held, so a
// stale result can free it.
type streamChunkMsg struct {
	gen        int
	stream     videoStream
	buf        []streamFrame
	eof        bool
	cols, rows int
	err        error
}

// streamReencodeMsg carries the current frame re-fitted to a new placement after
// a resize (see resizePreviewStream).
type streamReencodeMsg struct {
	gen int
	seq string
	err error
}

// streamPromoteMsg moves the modal onto the frame whose upload has just been
// written out. Sequenced after that frame's tea.Raw so the cells can never name
// an image the terminal hasn't received yet — see advanceStream. up is the
// parked frame it was sequenced for (see uploadedFrame).
type streamPromoteMsg struct {
	gen int
	up  int
}

// loadPreviewItem picks how to load a preview item: a streaming video, or the
// shared still/GIF decode. The single branch point for openPreviewItems and
// cyclePreview.
func (m Model) loadPreviewItem(gen int, id uint32, it previewItem) tea.Cmd {
	if m.streamsPreviewVideo(it) {
		return m.loadPreviewStream(gen, id, it)
	}
	return m.loadPreviewImage(gen, id, it)
}

// loadPreviewStream opens the video and decodes+encodes its first chunk entirely
// in the background, returning a streamOpenedMsg. Everything heavy (download,
// libav open, decode, PNG encode) is off the UI goroutine; the handler just
// installs the result and starts the tick.
func (m Model) loadPreviewStream(gen int, id uint32, it previewItem) tea.Cmd {
	cellPxW, cellPxH := m.cellPxW, m.cellPxH
	ring := m.preview.streamRing
	mm := m
	started := featureStart()
	return func() tea.Msg {
		data, err := mm.readPreviewBytes(it)
		if err != nil {
			return streamOpenedMsg{gen: gen, err: err, started: started}
		}
		stream, err := openVideoStream(data, streamVideoProfile)
		if err != nil {
			return streamOpenedMsg{gen: gen, err: err, started: started}
		}
		frames, delays, eof, err := stream.nextChunk(streamChunkFrames)
		if err != nil {
			stream.close()
			return streamOpenedMsg{gen: gen, err: err, started: started}
		}
		if len(frames) == 0 {
			stream.close()
			return streamOpenedMsg{gen: gen, err: errors.New("video produced no frames"), started: started}
		}
		cols, rows := mm.computePreviewCells(frames[0].Bounds())
		buf, err := encodeStreamFrames(frames, delays, cols, rows, ring, 0, cellPxW, cellPxH)
		if err != nil {
			stream.close()
			return streamOpenedMsg{gen: gen, err: err, started: started}
		}
		w, h, size := streamCaptionDims(it, frames[0].Bounds(), len(data))
		return streamOpenedMsg{
			gen: gen, stream: stream, buf: buf, eof: eof, cols: cols, rows: rows,
			caption: previewCaption(it.name, w, h, size), started: started,
		}
	}
}

// streamCaptionDims reports the width/height/size for the caption, preferring the
// attachment's real metadata (the decoded frame is downscaled) and falling back
// to the frame bounds / byte length.
func streamCaptionDims(it previewItem, b image.Rectangle, nbytes int) (w, h int, size int64) {
	w, h, size = b.Dx(), b.Dy(), int64(nbytes)
	if it.file != nil {
		if it.file.Width > 0 && it.file.Height > 0 {
			w, h = it.file.Width, it.file.Height
		}
		if it.file.Size > 0 {
			size = it.file.Size
		}
	}
	return w, h, size
}

// handleStreamOpened installs the opened stream + first chunk and starts
// playback. A stale result (preview already gone) frees the stream it carries.
func (m Model) handleStreamOpened(msg streamOpenedMsg) (tea.Model, tea.Cmd) {
	if !m.preview.active || msg.gen != m.previewGen {
		if msg.stream != nil {
			msg.stream.close()
		}
		return m, nil
	}
	m.preview.loading = false
	if msg.err != nil {
		m.preview.err = msg.err
		m.recordMedia("video", "error", telemetry.ClassifyError(msg.err), decodeMillis(msg.started))
		return m, nil
	}
	m.recordMedia("video", "ok", "", decodeMillis(msg.started))
	telemetry.Feature("video_preview")
	m.preview.streaming = true
	m.preview.stream = msg.stream
	m.preview.streamBuf = msg.buf
	m.preview.streamEOF = msg.eof
	m.preview.streamFetching = false
	m.preview.streamDone = false
	m.preview.cols, m.preview.rows = msg.cols, msg.rows
	m.preview.caption = msg.caption
	// The opener encoded this chunk starting the rotation at 0, so the next
	// chunk continues from here.
	m.preview.streamSeq = len(msg.buf)
	return m, m.advanceStream()
}

// loadPreviewStreamChunk pulls the next chunk from the (main-loop-lent) stream
// and encodes it at rows×cols, off the UI goroutine. startSeq continues the
// rotation through the ring from wherever the previous chunk left off.
func (m Model) loadPreviewStreamChunk(gen int, stream videoStream, rows, cols int, ring [streamRingSlots]uint32, startSeq int, cellPxW, cellPxH int) tea.Cmd {
	return func() tea.Msg {
		frames, delays, eof, err := stream.nextChunk(streamChunkFrames)
		if err != nil {
			return streamChunkMsg{gen: gen, stream: stream, err: err, eof: true, rows: rows, cols: cols}
		}
		buf, eerr := encodeStreamFrames(frames, delays, cols, rows, ring, startSeq, cellPxW, cellPxH)
		if eerr != nil {
			return streamChunkMsg{gen: gen, stream: stream, err: eerr, eof: true, rows: rows, cols: cols}
		}
		return streamChunkMsg{gen: gen, stream: stream, buf: buf, eof: eof, rows: rows, cols: cols}
	}
}

// handleStreamChunk appends a decode-ahead chunk (or, if the preview was resized
// since it was kicked, drops the now-wrong-size frames), frees the stream on EOF,
// and keeps decoding ahead if still hungry. A stale result frees the stream it
// held — see the ownership note at the top.
func (m Model) handleStreamChunk(msg streamChunkMsg) (tea.Model, tea.Cmd) {
	if !m.preview.active || msg.gen != m.previewGen || !m.preview.streaming {
		if msg.stream != nil {
			msg.stream.close()
		}
		return m, nil
	}
	m.preview.streamFetching = false
	if msg.err != nil {
		m.preview.streamEOF = true
		m.closePreviewStream()
		return m, nil
	}
	m.preview.streamEOF = msg.eof
	if msg.eof {
		m.closePreviewStream() // no more chunks needed; free decoder + temp now
	}
	// Advance the rotation counter by everything this chunk encoded, whether
	// or not we install it: the ids it consumed are spent either way, and the
	// next chunk has to continue past them.
	m.preview.streamSeq += len(msg.buf)
	// Only install frames encoded at the current placement; a resize since this
	// chunk was kicked makes them the wrong size, so drop them (a few-frame skip)
	// and let the next chunk decode at the new size.
	if msg.rows == m.preview.rows && msg.cols == m.preview.cols {
		m.preview.streamBuf = append(m.preview.streamBuf, msg.buf...)
	}
	return m, m.maybeFetchStream()
}

// handleStreamTick advances streaming playback one frame.
func (m Model) handleStreamTick(msg previewStreamTickMsg) (tea.Model, tea.Cmd) {
	if !m.preview.active || msg.gen != m.previewGen || !m.preview.streaming {
		return m, nil
	}
	return m, m.advanceStream()
}

// promoteStreamFrame points the modal's placeholder cells at the frame whose
// upload has already gone out (see advanceStream). Nothing is decoded or encoded
// here; it is the second half of one frame's ordering.
func (m *Model) promoteStreamFrame(msg streamPromoteMsg) {
	p := &m.preview
	if !p.active || msg.gen != m.previewGen || !p.streaming {
		return
	}
	// Only the frame this promote was sequenced for: a resize has since dropped
	// the parked frame (wrong placement — resizePreviewStream already put the
	// current one back up), or another frame was parked while this promote was in
	// flight and owns the cells instead.
	if p.streamUp.img == nil || p.streamUp.n != msg.up {
		return
	}
	if p.streamUp.id != 0 {
		p.id = p.streamUp.id
	}
	p.img = p.streamUp.img
}

// handleStreamReencode displays the current frame re-fitted after a resize.
func (m Model) handleStreamReencode(msg streamReencodeMsg) (tea.Model, tea.Cmd) {
	if !m.preview.active || msg.gen != m.previewGen || !m.preview.streaming || msg.err != nil {
		return m, nil
	}
	return m, tea.Raw(msg.seq)
}

// advanceStream shows the next buffered frame and schedules the following tick,
// or handles the two stall cases: end-of-clip (stop, holding the last frame) and
// underrun (decoder fell behind — hold and re-check shortly, ensuring a
// decode-ahead is running). Also tops up the decode-ahead buffer.
//
// Frames rotate through preview.streamRing rather than all being re-transmitted
// under one id. Re-transmitting an id replaces the image it names, and a
// terminal is entitled to drop the old one the moment the new transmission
// starts rather than when it completes — Ghostty does exactly that. A frame is
// several hundred KB of base64 in ~4KB chunks, so "while the upload is in
// flight" is most of a frame interval: uploading over the id the placeholder
// cells point at leaves them pointing at nothing, and the image strobes.
//
// The rotation alone is not enough, because the upload and the cell switch that
// moves the modal onto it do not reach the terminal together. bubbletea hands
// the View to the renderer the moment this Update returns (tea.go's event loop
// renders right after Update), while tea.Raw's bytes only reach the output
// buffer after a Cmd goroutine and another trip through the message loop. The
// 60Hz flush can land in that gap, emitting cells that name an id whose
// transmission has not started — and since a terminal may drop an image when a
// new transmission under its id *begins*, those cells then name nothing for most
// of an upload. That is the strobe the ring exists to prevent, leaking through
// as an occasional flicker rather than a constant one.
//
// So the upload is written first and a streamPromoteMsg moves the cells after
// it, the two ordered by tea.Sequence through the one message loop: the cells
// only ever name an id whose bytes are already buffered, and the buffer is
// flushed before the cell diff (see startRenderer). Do not fold the switch back
// into this update — that is the bug, not an optimisation.
func (m *Model) advanceStream() tea.Cmd {
	p := &m.preview
	if p.streamDone {
		return nil
	}
	if len(p.streamBuf) == 0 {
		if p.streamEOF {
			p.streamDone = true // played everything: stop on the last frame
			return nil
		}
		// Underrun: nothing buffered yet. Keep the current frame up, make sure a
		// chunk is decoding, and poll again shortly.
		return tea.Batch(m.maybeFetchStream(), previewStreamTickCmd(m.previewGen, previewStreamUnderrunPoll))
	}

	// Take the next frame, skipping any that would upload onto the slot the modal
	// is displaying or the one it is about to (streamUp: uploaded, not promoted
	// yet). Only reachable when dropped frames (a resize) shift the rotation;
	// costs a frame, like the resize itself.
	onScreen, pending := p.id, p.streamUp.id
	blank := p.img == nil && pending == 0 // nothing up yet: any slot is free
	var fr streamFrame
	for len(p.streamBuf) > 0 {
		fr, p.streamBuf = p.streamBuf[0], p.streamBuf[1:]
		if fr.id == 0 || blank || (fr.id != onScreen && fr.id != pending) {
			break
		}
		fr = streamFrame{}
	}
	if fr.seq == "" {
		return tea.Batch(m.maybeFetchStream(), previewStreamTickCmd(m.previewGen, previewStreamUnderrunPoll))
	}

	// Park the frame, write its upload, and let the promote that follows move the
	// cells onto it (see the ordering note above).
	p.streamUp = uploadedFrame{n: p.streamUp.n + 1, id: fr.id, img: fr.img}
	gen, up := m.previewGen, p.streamUp.n
	promote := func() tea.Msg { return streamPromoteMsg{gen: gen, up: up} }
	cmds := []tea.Cmd{
		tea.Sequence(tea.Raw(fr.seq), promote),
		previewStreamTickCmd(gen, fr.delay),
	}
	if c := m.maybeFetchStream(); c != nil {
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

// maybeFetchStream launches a decode-ahead Cmd when the buffer is below the
// high-water mark and none is already running (and the stream isn't exhausted).
// Setting streamFetching hands the stream to that Cmd until its result returns.
func (m *Model) maybeFetchStream() tea.Cmd {
	p := &m.preview
	if p.stream == nil || p.streamFetching || p.streamEOF {
		return nil
	}
	if len(p.streamBuf) >= streamBufferFrames {
		return nil
	}
	p.streamFetching = true
	return m.loadPreviewStreamChunk(
		m.previewGen, p.stream, p.rows, p.cols,
		p.streamRing, p.streamSeq, m.cellPxW, m.cellPxH,
	)
}

// resizePreviewStream re-fits the current frame to the new placement and drops
// the buffered frames (encoded at the old size); the decoder keeps going and
// refills at the new size. A resize therefore costs a few skipped frames, which
// is imperceptible next to re-decoding.
func (m *Model) resizePreviewStream() tea.Cmd {
	if m.preview.img == nil {
		return nil
	}
	m.sizePreview()
	m.preview.streamBuf = nil
	// Any frame uploaded but not yet promoted was encoded for the old placement:
	// drop it, and move the token on so a promote still in flight can neither put
	// it back on screen behind the re-fit below nor land on a later frame.
	m.preview.streamUp = uploadedFrame{n: m.preview.streamUp.n + 1}
	// The re-fit below deliberately re-transmits under the id already on screen,
	// which does blank it for the length of that one upload. A resize already
	// costs dropped frames, and doing it here instead would need a promotion
	// tick to switch cells afterwards for no visible gain.
	cur := m.preview.img
	rows, cols, id := m.preview.rows, m.preview.cols, m.preview.id
	cw, ch := m.cellPxW, m.cellPxH
	gen := m.previewGen
	reencode := func() tea.Msg {
		fitted := fitFrameToCells(cur, cols, rows, cw, ch)
		seq, err := kittyTransmitImage(id, fitted, rows, cols)
		if err != nil {
			return streamReencodeMsg{gen: gen, err: err}
		}
		return streamReencodeMsg{gen: gen, seq: seq}
	}
	return tea.Batch(reencode, m.maybeFetchStream())
}

// teardownPreviewStream frees the streaming decoder on close/cycle — but only
// when no decode-ahead Cmd is holding it. If one is (streamFetching), that Cmd's
// result is now stale and its handler frees the stream instead (see
// handleStreamChunk), so freeing here too would double-free.
func (m *Model) teardownPreviewStream() {
	if m.preview.streamFetching {
		return
	}
	m.closePreviewStream()
}

// closePreviewStream frees the decoder if present. Only safe on the main loop
// when no Cmd holds the stream — its callers guarantee that.
func (m *Model) closePreviewStream() {
	if m.preview.stream != nil {
		m.preview.stream.close()
		m.preview.stream = nil
	}
}

// encodeStreamFrames right-sizes each decoded frame to the rows×cols placement
// and pre-builds its Kitty transmit sequence, keeping the decoded image alongside
// so a resize can re-fit it. Run off the UI goroutine.
//
// ring is the preview's pool of image ids and startSeq is how many frames have
// already been encoded for this stream, so frames keep cycling through the ring
// even across a chunk boundary. Every frame's id is baked into its pre-built
// sequence, so the tick never recomputes the rotation — it reads
// streamFrame.id (see advanceStream).
func encodeStreamFrames(frames []image.Image, delays []time.Duration, cols, rows int, ring [streamRingSlots]uint32, startSeq int, cellPxW, cellPxH int) ([]streamFrame, error) {
	out := make([]streamFrame, len(frames))
	for i, f := range frames {
		id := ring[(startSeq+i)%streamRingSlots]
		fitted := fitFrameToCells(f, cols, rows, cellPxW, cellPxH)
		seq, err := kittyTransmitImage(id, fitted, rows, cols)
		if err != nil {
			return nil, err
		}
		out[i] = streamFrame{seq: seq, delay: delays[i], img: f, id: id}
	}
	return out, nil
}

// previewStreamTickCmd schedules the next streaming tick after d (floored to
// previewStreamMinInterval), tagged with gen so a stale tick is dropped.
func previewStreamTickCmd(gen int, d time.Duration) tea.Cmd {
	if d < previewStreamMinInterval {
		d = previewStreamMinInterval
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return previewStreamTickMsg{gen: gen} })
}
