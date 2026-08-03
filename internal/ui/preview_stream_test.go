package ui

import (
	"image"
	"testing"
	"time"
)

// fakeVideoStream is a videoStream that hands back canned chunks and counts its
// closes, so the streaming state machine can be driven without libav (this test
// runs in the default build).
type fakeVideoStream struct {
	remaining int // frames still to hand out
	closed    int
}

func (f *fakeVideoStream) nextChunk(max int) ([]image.Image, []time.Duration, bool, error) {
	n := max
	if n > f.remaining {
		n = f.remaining
	}
	frames := make([]image.Image, n)
	delays := make([]time.Duration, n)
	for i := range frames {
		frames[i] = image.NewRGBA(image.Rect(0, 0, 2, 2))
		delays[i] = 10 * time.Millisecond
	}
	f.remaining -= n
	return frames, delays, f.remaining == 0, nil
}

func (f *fakeVideoStream) close() { f.closed++ }

func mkStreamFrames(n int) []streamFrame {
	out := make([]streamFrame, n)
	for i := range out {
		out[i] = streamFrame{seq: "seq", delay: 10 * time.Millisecond, img: image.NewRGBA(image.Rect(0, 0, 2, 2))}
	}
	return out
}

func TestAdvanceStreamPopsAndFetches(t *testing.T) {
	m := &Model{previewGen: 1}
	fake := &fakeVideoStream{remaining: 100}
	m.preview = previewState{
		active: true, streaming: true, id: 1, rows: 1, cols: 1,
		stream: fake, streamBuf: mkStreamFrames(3),
	}
	cmd := m.advanceStream()
	if cmd == nil {
		t.Fatal("advanceStream returned no cmd")
	}
	if len(m.preview.streamBuf) != 2 {
		t.Errorf("buffer = %d, want 2 (one popped)", len(m.preview.streamBuf))
	}
	if m.preview.img == nil {
		t.Error("current frame image not set after advance")
	}
	if !m.preview.streamFetching {
		t.Error("expected a decode-ahead fetch to be kicked (buffer below high-water)")
	}
}

func TestAdvanceStreamStopsOnLastFrame(t *testing.T) {
	m := &Model{previewGen: 1}
	m.preview = previewState{active: true, streaming: true, id: 1, streamEOF: true, streamBuf: nil}
	if cmd := m.advanceStream(); cmd != nil {
		t.Error("advanceStream at eof+empty should not schedule another tick")
	}
	if !m.preview.streamDone {
		t.Error("expected streamDone at end of clip")
	}
}

func TestAdvanceStreamUnderrunWaits(t *testing.T) {
	m := &Model{previewGen: 1}
	fake := &fakeVideoStream{remaining: 100}
	// Not eof, but nothing buffered: should hold and re-check (a cmd), and ensure
	// a fetch is running.
	m.preview = previewState{active: true, streaming: true, id: 1, stream: fake, streamBuf: nil}
	cmd := m.advanceStream()
	if cmd == nil {
		t.Error("underrun should schedule a retry tick")
	}
	if m.preview.streamDone {
		t.Error("underrun must not mark playback done")
	}
	if !m.preview.streamFetching {
		t.Error("underrun should kick a decode-ahead fetch")
	}
}

func TestHandleStreamChunkAppendsAndClosesOnEOF(t *testing.T) {
	fake := &fakeVideoStream{}
	m := &Model{previewGen: 2}
	m.preview = previewState{active: true, streaming: true, id: 1, rows: 1, cols: 1, stream: fake, streamFetching: true}
	out, _ := m.handleStreamChunk(streamChunkMsg{gen: 2, stream: fake, buf: mkStreamFrames(2), eof: true, rows: 1, cols: 1})
	res := out.(Model)
	if len(res.preview.streamBuf) != 2 {
		t.Errorf("buffer = %d, want 2 appended", len(res.preview.streamBuf))
	}
	if !res.preview.streamEOF {
		t.Error("streamEOF not set")
	}
	if fake.closed != 1 {
		t.Errorf("stream closed %d times on eof, want 1", fake.closed)
	}
	if res.preview.stream != nil {
		t.Error("stream reference should be cleared after eof close")
	}
	if res.preview.streamFetching {
		t.Error("streamFetching should clear when a chunk returns")
	}
}

func TestHandleStreamChunkKeepsFetching(t *testing.T) {
	fake := &fakeVideoStream{remaining: 100}
	m := &Model{previewGen: 1}
	m.preview = previewState{active: true, streaming: true, id: 1, rows: 1, cols: 1, stream: fake, streamFetching: true}
	out, cmd := m.handleStreamChunk(streamChunkMsg{gen: 1, stream: fake, buf: mkStreamFrames(4), eof: false, rows: 1, cols: 1})
	res := out.(Model)
	if len(res.preview.streamBuf) != 4 {
		t.Errorf("buffer = %d, want 4 appended", len(res.preview.streamBuf))
	}
	if res.preview.streamEOF {
		t.Error("should not be eof mid-clip")
	}
	if fake.closed != 0 {
		t.Errorf("stream closed %d times mid-clip, want 0", fake.closed)
	}
	// Buffer (4) is below the high-water mark, so a further decode-ahead must be
	// kicked — and the returned model must reflect streamFetching=true, or the
	// next tick would launch a second concurrent decode on the same stream.
	if !res.preview.streamFetching {
		t.Error("streamFetching must stay true in the returned model (kept decoding ahead)")
	}
	if cmd == nil {
		t.Error("expected a decode-ahead cmd")
	}
}

func TestHandleStreamChunkStaleClosesStream(t *testing.T) {
	fake := &fakeVideoStream{}
	m := &Model{previewGen: 5}
	m.preview = previewState{active: true, streaming: true}
	// gen mismatch: the preview was cycled/closed while this chunk decoded, so its
	// handler is the one that must free the stream it held.
	m.handleStreamChunk(streamChunkMsg{gen: 4, stream: fake, buf: mkStreamFrames(2)})
	if fake.closed != 1 {
		t.Errorf("stale chunk closed stream %d times, want 1", fake.closed)
	}
}

func TestHandleStreamChunkDropsResizedFrames(t *testing.T) {
	m := &Model{previewGen: 1}
	m.preview = previewState{active: true, streaming: true, rows: 2, cols: 2, streamFetching: true}
	// Chunk was encoded at a placement (9x9) that no longer matches (2x2 now): its
	// frames are the wrong size and must be dropped, not shown distorted.
	out, _ := m.handleStreamChunk(streamChunkMsg{gen: 1, buf: mkStreamFrames(3), rows: 9, cols: 9})
	if n := len(out.(Model).preview.streamBuf); n != 0 {
		t.Errorf("resized-away frames buffered = %d, want 0 (dropped)", n)
	}
}

func TestHandleStreamOpenedStaleClosesStream(t *testing.T) {
	fake := &fakeVideoStream{}
	m := &Model{previewGen: 3}
	m.preview = previewState{active: true}
	// A stream opened for a preview the user already closed (gen bumped) must be freed.
	m.handleStreamOpened(streamOpenedMsg{gen: 1, stream: fake, buf: mkStreamFrames(1)})
	if fake.closed != 1 {
		t.Errorf("stale opened stream closed %d times, want 1", fake.closed)
	}
}

func TestTeardownDefersToInFlightCmd(t *testing.T) {
	fake := &fakeVideoStream{}
	m := &Model{}
	// A decode-ahead Cmd holds the stream (streamFetching): teardown must NOT free
	// it — the Cmd's stale result will. Freeing here too would double-free.
	m.preview = previewState{stream: fake, streamFetching: true}
	m.teardownPreviewStream()
	if fake.closed != 0 {
		t.Errorf("teardown freed a stream held by a Cmd (%d closes) — double-free risk", fake.closed)
	}
	// With no Cmd in flight, teardown frees it.
	m.preview = previewState{stream: fake, streamFetching: false}
	m.teardownPreviewStream()
	if fake.closed != 1 {
		t.Errorf("teardown should free an unheld stream once, got %d", fake.closed)
	}
}
