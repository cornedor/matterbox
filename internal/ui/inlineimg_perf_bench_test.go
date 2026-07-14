package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"strconv"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// --- fixtures --------------------------------------------------------------

// benchAnimGIF encodes a wPx×hPx animated GIF with n frames — stand-in for the
// Giphy GIFs the picker posts, which are the common animated thumbnail.
func benchAnimGIF(wPx, hPx, n int) []byte {
	g := &gif.GIF{Config: image.Config{Width: wPx, Height: hPx}}
	pal := color.Palette{}
	for i := 0; i < 256; i++ {
		pal = append(pal, color.RGBA{uint8(i), uint8(255 - i), uint8(i * 7), 255})
	}
	for f := 0; f < n; f++ {
		fr := image.NewPaletted(image.Rect(0, 0, wPx, hPx), pal)
		// Non-trivial, frame-varying content so PNG compression is realistic
		// (a flat fill would compress to nothing and understate the byte volume).
		for y := 0; y < hPx; y++ {
			for x := 0; x < wPx; x++ {
				fr.SetColorIndex(x, y, uint8((x*3+y*5+f*11)%256))
			}
		}
		g.Image = append(g.Image, fr)
		g.Delay = append(g.Delay, 8) // 80ms — a typical GIF frame
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// benchThumbFromGIF runs raw GIF bytes through exactly the path buildInlineThumb
// uses after readThumbBytes — decode every frame, fit it to the cell box, and
// prebuild one Kitty transmit APC per frame. Returns the ready thumbnail so a
// bench can install it without a server or a terminal.
func benchThumbFromGIF(b *testing.B, raw []byte, id uint32, box, cellPxW, cellPxH int) readyInlineImg {
	b.Helper()
	frames, delays, err := decodeImageFrames(raw, true)
	if err != nil {
		b.Fatalf("decodeImageFrames: %v", err)
	}
	bnd := frames[0].Bounds()
	cols, rows := inlineThumbCells(bnd.Dx(), bnd.Dy(), box, cellPxW, cellPxH)
	seqs := make([]string, len(frames))
	for i, fr := range frames {
		seq, err := kittyTransmitImage(id, fitFrameToCells(fr, cols, rows, cellPxW, cellPxH), rows, cols)
		if err != nil {
			b.Fatalf("kittyTransmitImage: %v", err)
		}
		seqs[i] = seq
	}
	return readyInlineImg{
		id: id, rows: rows, cols: cols, box: box,
		placeholder: kittyPlaceholder(id, rows, cols),
		frameSeqs:   seqs, delays: delays,
	}
}

// benchThumbViewModel is benchViewModel with thumbnails switched on, the terminal
// probe satisfied, and k of the on-screen posts carrying a ready *animated* GIF
// thumbnail — the steady state of a channel where someone has been posting GIFs.
func benchThumbViewModel(b *testing.B, nPosts, kThumbs int) Model {
	b.Helper()
	m := benchViewModel(nPosts)
	m.cellPxW, m.cellPxH = 10, 20 // a typical non-HiDPI Kitty cell
	m.animateInline = true
	m.inlineImg = newInlineImages("auto")
	m.emojiImg.setProbeOK()
	m.emojiImg.setColorProfile(true)

	box := inlineThumbBox(m.msgsView.Width())
	raw := benchAnimGIF(480, 270, 30) // a middling Giphy: 480×270, 30 frames

	// Hang the GIFs off the newest posts, which is what's on screen.
	for i := 0; i < kThumbs; i++ {
		p := m.posts[len(m.posts)-1-i]
		fid := "file" + strconv.Itoa(i)
		p.Metadata = &model.PostMetadata{Files: []*model.FileInfo{{
			Id: fid, Name: "party.gif", MimeType: "image/gif",
			Width: 480, Height: 270, Size: int64(len(raw)),
		}}}
		m.inlineImg.markReady(fid, benchThumbFromGIF(b, raw, uint32(0x100000+i), box, m.cellPxW, m.cellPxH))
	}
	m.postLineCache = nil // the new attachments changed the posts
	m.renderMessages()
	m.refreshAnimVisibility()
	m.imgAnimating = true
	return m
}

// --- what one animation tick actually costs --------------------------------

// BenchmarkInlineAnimTick measures a single GIF animation tick exactly as
// bubbletea drives it: update(imgAnimTickMsg) then View(). The tick fires every
// imgAnimMinInterval (50ms → 20Hz) for as long as any animated thumbnail is on
// screen, and — like every non-wheel message — it invalidates the memoized frame
// (update.go:90), so each one rebuilds the whole screen.
//
// thumbs=0 is the control: the cost of the 20Hz View() rebuild alone, with no
// thumbnail work in it. The delta to thumbs>0 is what the thumbnails themselves
// add. Compare ns/op against the 50ms tick budget: that ratio is the fraction of
// a core the idle-but-for-a-GIF UI burns.
func BenchmarkInlineAnimTick(b *testing.B) {
	for _, k := range []int{0, 1, 3, 8} {
		b.Run("thumbs="+strconv.Itoa(k), func(b *testing.B) {
			var tm tea.Model = benchThumbViewModel(b, 400, k)
			_ = tm.(Model).View() // prime
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tm, _ = tm.(Model).update(imgAnimTickMsg{})
				_ = tm.(Model).View()
			}
		})
	}
}

// BenchmarkInlineAnimBytes measures the bytes advanceImageAnim pushes down the
// tty per tick — the terminal's share of the cost, which no Go profile shows.
// Every due frame re-transmits its *whole* PNG under the thumbnail's id (that's
// how the placeholder repaints in place without a re-render). The scheme is
// borrowed from custom emoji, where a frame is 1–2 cells; a thumbnail is
// inlineThumbRows(10) rows tall, so the same trick moves orders of magnitude
// more bytes.
//
// b/tick × 20Hz = the sustained throughput matterbox asks the terminal emulator
// to decode, forever, while a GIF is on screen.
func BenchmarkInlineAnimBytes(b *testing.B) {
	for _, k := range []int{1, 3, 8} {
		b.Run("thumbs="+strconv.Itoa(k), func(b *testing.B) {
			m := benchThumbViewModel(b, 400, k)
			var total, ticks int64
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Advance far enough that every GIF's frame is due, i.e. the
				// steady state at 20Hz with 80ms frames: most ticks move some.
				seq, _, _ := m.inlineImg.advanceFrame(benchNow(i))
				total += int64(len(seq))
				ticks++
			}
			b.StopTimer()
			b.ReportMetric(float64(total)/float64(ticks), "B/tick")
			b.ReportMetric(float64(total)/float64(ticks)*20/1024, "KB/s@20Hz")
		})
	}
}

// BenchmarkInlineThumbBuild measures the *first-paint* cost of one GIF thumbnail:
// decode every frame, downscale each to the cell box, PNG-encode each. This runs
// off the render loop (loadInlineImages, on a Cmd goroutine), so it doesn't
// stall the UI — but loadInlineImages loops over a batch sequentially, so a
// screenful of GIFs pays this serially before any of them appear.
func BenchmarkInlineThumbBuild(b *testing.B) {
	for _, f := range []int{1, 30, 90} {
		b.Run("frames="+strconv.Itoa(f), func(b *testing.B) {
			raw := benchAnimGIF(480, 270, f)
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = benchThumbFromGIF(b, raw, 0x200000, 88, 10, 20)
			}
		})
	}
}

// benchNow returns a monotonically advancing time 50ms per step — the tick
// cadence — so advanceFrame sees frames actually falling due.
func benchNow(i int) time.Time { return benchEpoch.Add(time.Duration(i) * imgAnimMinInterval) }

var benchEpoch = time.Unix(1700000000, 0)
