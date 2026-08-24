package ui

import (
	"fmt"
	"math"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/stl"
)

func stlFile(id, name, mime, ext string, size int64) *model.FileInfo {
	return &model.FileInfo{Id: id, Name: name, MimeType: mime, Extension: ext, Size: size}
}

func stlPost(files ...*model.FileInfo) *model.Post {
	return &model.Post{Id: "post1", Metadata: &model.PostMetadata{Files: files}}
}

// stlViewModel is a Model with graphics available and the viewer open on one
// model, with a mesh already loaded — the state every interaction test needs.
func stlViewModel(t *testing.T) *Model {
	t.Helper()
	m := thumbModel()
	m.width, m.height = 120, 40
	next, _ := m.openSTLView([]previewItem{
		{file: stlFile("f1", "bracket.stl", "", ".stl", 4096), name: "bracket.stl"},
	}, 0)
	out, ok := next.(Model)
	if !ok {
		t.Fatalf("openSTLView returned %T, want Model", next)
	}
	if !out.stl.active {
		t.Fatal("openSTLView did not open the viewer")
	}
	// Stand in for the background load, so the frame path is live.
	out.stl.loading = false
	out.stl.mesh = testMesh(t)
	return &out
}

func testMesh(t *testing.T) *stl.Mesh {
	t.Helper()
	m, err := stl.Decode([]byte("solid t\nfacet normal 0 0 1\nouter loop\n" +
		"vertex 0 0 0\nvertex 1 0 0\nvertex 0 1 1\nendloop\nendfacet\nendsolid t\n"))
	if err != nil {
		t.Fatalf("test mesh: %v", err)
	}
	return m
}

// press builds a key event that stringifies to what the test means; handleSTLKey
// switches on msg.String(), so sendKey asserts the spelling before using it.
func stlPress(s string) tea.KeyPressMsg {
	switch s {
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "shift+left":
		return tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift}
	case "shift+up":
		return tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func sendKey(t *testing.T, m *Model, s string) *Model {
	t.Helper()
	msg := stlPress(s)
	if msg.String() != s {
		t.Fatalf("stlPress(%q) stringifies as %q — the test isn't pressing what it means", s, msg.String())
	}
	next, _ := m.handleSTLKey(msg)
	out, ok := next.(Model)
	if !ok {
		t.Fatalf("handleSTLKey(%q) returned %T, want Model", s, next)
	}
	return &out
}

// --- recognising the format ----------------------------------------------

func TestIsSTLAttachment(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    *model.FileInfo
		want bool
	}{
		{"extension", stlFile("a", "part.stl", "", ".stl", 1), true},
		{"extension without the dot", stlFile("a", "part.stl", "", "stl", 1), true},
		{"uppercase name, no extension field", stlFile("a", "PART.STL", "", "", 1), true},
		{"octet-stream upload", stlFile("a", "part.stl", "application/octet-stream", ".stl", 1), true},
		{"mime only", stlFile("a", "part", "model/stl", "", 1), true},
		{"legacy sla mime", stlFile("a", "part", "application/sla", "", 1), true},
		{"a png", stlFile("a", "shot.png", "image/png", ".png", 1), false},
		{"a name that merely mentions it", stlFile("a", "stl-notes.txt", "text/plain", ".txt", 1), false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSTLAttachment(tc.f); got != tc.want {
				t.Errorf("isSTLAttachment = %v, want %v", got, tc.want)
			}
		})
	}
}

// The image preview's gallery must never contain an STL: it would hand the
// still/GIF decoder a mesh. This separation is what the two modals rest on.
func TestPreviewImagesExcludesSTL(t *testing.T) {
	p := stlPost(stlFile("f1", "part.stl", "model/stl", ".stl", 1000))
	if items := previewImages(p, true); len(items) != 0 {
		t.Fatalf("previewImages returned %d items for an STL-only post, want 0", len(items))
	}
	if items := stlItems(p); len(items) != 1 {
		t.Fatalf("stlItems returned %d, want 1", len(items))
	}
}

// thumbItems is the union, and it is what the click lookup and the collapse
// bookkeeping walk — so a model and an image on one post must both appear.
func TestThumbItemsCoversBothKinds(t *testing.T) {
	m := thumbModel()
	p := stlPost(
		&model.FileInfo{Id: "img", Name: "a.png", MimeType: "image/png", Width: 10, Height: 10},
		stlFile("mesh", "part.stl", "model/stl", ".stl", 1000),
	)
	items := m.thumbItems(p)
	if len(items) != 2 {
		t.Fatalf("thumbItems = %d items, want 2", len(items))
	}
	keys := map[string]bool{}
	for _, it := range items {
		keys[thumbKey(it)] = true
	}
	if !keys["img"] || !keys["mesh"] {
		t.Errorf("thumbItems keys = %v, want both files", keys)
	}
	if got := m.postThumbKeys(p); len(got) != 2 {
		t.Errorf("postThumbKeys = %v, want both files", got)
	}
}

// A model too big to render unasked keeps its icon — no thumbnail, no download —
// but is still openable in the viewer.
func TestOversizedSTLDrawsNoThumbnail(t *testing.T) {
	m := thumbModel()
	big := stlFile("big", "huge.stl", "model/stl", ".stl", stlThumbMaxBytes+1)
	if m.stlThumbnailable(big) {
		t.Error("stlThumbnailable = true for a file over the thumbnail cap")
	}
	if m.drawsFileThumb(big) {
		t.Error("drawsFileThumb = true for a file over the thumbnail cap")
	}
	if len(m.thumbItems(stlPost(big))) != 0 {
		t.Error("an oversized model was enumerated for a thumbnail")
	}
	if !isSTLAttachment(big) {
		t.Error("an oversized model stopped being an STL")
	}
}

func TestSTLThumbnailNeedsGraphics(t *testing.T) {
	m := &Model{inlineImg: newInlineImages("off"), emojiImg: newEmojiImages("auto", true)}
	if m.stlThumbnailable(stlFile("f", "p.stl", "", ".stl", 100)) {
		t.Error("stlThumbnailable = true with thumbnails off")
	}
}

// --- placement ------------------------------------------------------------

// The invariant that keeps the transcript from reflowing when a model finishes
// rendering: the rows reserved before the fetch and the rows the built
// thumbnail occupies are the same figure, from the same function.
func TestSTLThumbReserveMatchesBuild(t *testing.T) {
	m := thumbModel()
	it := previewItem{file: stlFile("f1", "part.stl", "model/stl", ".stl", 1000), name: "part.stl"}
	for _, box := range []int{8, 20, 26, 40, 78} {
		resCols, resRows := m.reserveThumbCells(it, box)
		cols, rows := stlThumbCells(box, m.cellPxW, m.cellPxH)
		if resCols != cols || resRows != rows {
			t.Errorf("box %d: reserved %dx%d, build %dx%d", box, resCols, resRows, cols, rows)
		}
		if cols > box {
			t.Errorf("box %d: placement is %d cols wide, wider than the box", box, cols)
		}
		if rows != inlineThumbRows {
			t.Errorf("box %d: rows = %d, want the full thumbnail height %d — a shorter one would re-fit forever",
				box, rows, inlineThumbRows)
		}
	}
}

// The reserve for an STL must not fall through to the body-image nominal size,
// which would hold the wrong number of rows.
func TestSTLReserveUsesItsOwnBox(t *testing.T) {
	m := thumbModel()
	stlIt := previewItem{file: stlFile("f1", "part.stl", "model/stl", ".stl", 1000), name: "part.stl"}
	imgIt := previewItem{file: &model.FileInfo{Id: "i", Name: "a.png", MimeType: "image/png", Width: 1920, Height: 1080}}
	sc, _ := m.reserveThumbCells(stlIt, 78)
	ic, _ := m.reserveThumbCells(imgIt, 78)
	if sc == ic {
		t.Errorf("an STL reserved the same width (%d) as a 16:9 image — it fell through to the nominal size", sc)
	}
}

func TestThumbPixelBox(t *testing.T) {
	if w, h := thumbPixelBox(26, 10, 8, 16); w != 208 || h != 160 {
		t.Errorf("thumbPixelBox(26,10,8,16) = %dx%d, want 208x160", w, h)
	}
	// HiDPI: the terminal reports physical pixels, so the render is halved and
	// the image still fills its cells at its natural logical size.
	if w, h := thumbPixelBox(26, 10, 14, 28); w != 182 || h != 140 {
		t.Errorf("thumbPixelBox(26,10,14,28) = %dx%d, want the dpr-halved size", w, h)
	}
	// No cell metrics: fall back to an 8x16 cell rather than dividing by zero.
	if w, h := thumbPixelBox(26, 10, 0, 0); w <= 0 || h <= 0 {
		t.Errorf("thumbPixelBox with no cell metrics = %dx%d, want a usable size", w, h)
	}
}

// --- opening --------------------------------------------------------------

func TestOpenSTLViewRefusals(t *testing.T) {
	m := thumbModel()
	next, cmd := m.openSTLView(nil, 0)
	if next.(Model).stl.active {
		t.Error("opened the viewer with no items")
	}
	if cmd != nil {
		t.Error("a refusal returned a command")
	}
	if !strings.Contains(next.(Model).status, "no 3D model") {
		t.Errorf("status = %q, want an explanation", next.(Model).status)
	}

	// No terminal graphics: refuse and point at `o`, as the image modal does.
	blind := &Model{inlineImg: newInlineImages("auto"), emojiImg: newEmojiImages("off", false)}
	next, _ = blind.openSTLView([]previewItem{{file: stlFile("f", "p.stl", "", ".stl", 1), name: "p.stl"}}, 0)
	if next.(Model).stl.active {
		t.Error("opened the viewer on a terminal that can't draw")
	}
	if !strings.Contains(next.(Model).status, "press o to open") {
		t.Errorf("status = %q, want the `o` fallback", next.(Model).status)
	}
}

func TestOpenSTLViewState(t *testing.T) {
	g := stlViewModel(t).stl
	if g.imgID == 0 {
		t.Error("no image id allocated")
	}
	if g.rend == nil {
		t.Error("no renderer allocated — the frame path would no-op")
	}
	if g.rows <= 0 || g.cols <= 0 {
		t.Errorf("viewport is %dx%d cells, want a real box", g.cols, g.rows)
	}
	if g.cam != stl.DefaultCamera() {
		t.Errorf("camera = %+v, want the default three-quarter view", g.cam)
	}
}

// Space on a post that has a model and no image opens the 3D viewer, not the
// image modal. With an image present the image wins.
func TestPreviewKeyRoutesToSTL(t *testing.T) {
	m := stlViewModel(t)
	m.stl = stlState{}
	next, _ := m.openImagePreview(stlPost(stlFile("f1", "part.stl", "model/stl", ".stl", 1000)))
	out := next.(Model)
	if !out.stl.active {
		t.Fatal("space on an STL-only post did not open the 3D viewer")
	}
	if out.preview.active {
		t.Error("it also opened the image modal")
	}

	m2 := stlViewModel(t)
	m2.stl = stlState{}
	next, _ = m2.openImagePreview(stlPost(
		&model.FileInfo{Id: "img", Name: "a.png", MimeType: "image/png", Width: 8, Height: 8},
		stlFile("f1", "part.stl", "model/stl", ".stl", 1000),
	))
	out = next.(Model)
	if out.stl.active {
		t.Error("a post with an image opened the 3D viewer instead of the image modal")
	}
	if !out.preview.active {
		t.Error("the image modal did not open")
	}
}

// Clicking a model's thumbnail opens the viewer on the *models* gallery, so
// cycling there can never reach an image the viewer cannot draw.
func TestImageClickOpensSTLViewer(t *testing.T) {
	m := stlViewModel(t)
	m.stl = stlState{}
	m.imageClick = "preview"
	m.posts = []*model.Post{stlPost(
		&model.FileInfo{Id: "img", Name: "a.png", MimeType: "image/png", Width: 8, Height: 8},
		stlFile("mesh", "part.stl", "model/stl", ".stl", 1000),
	)}
	m.postIdx, m.focus = 0, focusMessages // handleImageClick resolves the post through the focused pane

	next, _ := m.handleImageClick(focusMessages, "mesh")
	out := next.(Model)
	if !out.stl.active {
		t.Fatal("clicking a model's thumbnail did not open the 3D viewer")
	}
	if len(out.stl.items) != 1 {
		t.Errorf("viewer gallery has %d items, want only the models", len(out.stl.items))
	}
	if out.preview.active {
		t.Error("the image modal opened too")
	}
}

func TestCloseSTLViewFreesTheImage(t *testing.T) {
	m := stlViewModel(t)
	gen := m.stl.gen
	cmd := m.closeSTLView()
	if m.stl.active {
		t.Error("still active after close")
	}
	if m.stl.gen != gen+1 {
		t.Errorf("gen = %d, want %d — in-flight work would not be stranded", m.stl.gen, gen+1)
	}
	if cmd == nil {
		t.Fatal("close returned no command, so the terminal keeps the image")
	}
	raw, ok := cmd().(tea.RawMsg)
	if !ok {
		t.Fatalf("close command produced %T, want a raw sequence", cmd())
	}
	if s, _ := raw.Msg.(string); !strings.Contains(s, "a=d") {
		t.Errorf("close sequence %q does not delete the image", s)
	}
}

// A load or a frame that lands after the user closed (or cycled) must be
// dropped, not written into a fresh state.
func TestSTLGenerationGuards(t *testing.T) {
	m := stlViewModel(t)
	stale := m.stl.gen - 1

	if cmd := m.applySTLLoaded(stlLoadedMsg{gen: stale, mesh: testMesh(t)}); cmd != nil {
		t.Error("a stale load produced work")
	}
	if cmd := m.applySTLFrame(stlFrameMsg{gen: stale, seq: "x"}); cmd != nil {
		t.Error("a stale frame was written to the terminal")
	}
	if cmd := m.applySTLSpin(stlSpinMsg{gen: stale}); cmd != nil {
		t.Error("a stale spin tick re-armed itself")
	}

	m.closeSTLView()
	if cmd := m.applySTLLoaded(stlLoadedMsg{gen: stale + 1, mesh: testMesh(t)}); cmd != nil {
		t.Error("a load landed after close")
	}
	if m.stl.mesh != nil {
		t.Error("a closed viewer took a mesh")
	}
}

// --- keyboard -------------------------------------------------------------

func TestSTLKeyOrbitPanZoom(t *testing.T) {
	base := stlViewModel(t)
	// The orbit keys mean "as if you had dragged that way", so their signs match
	// stlMouseMotion's — increasing Yaw swings the face you are looking at left
	// (TestYawTurnsModelLeft in internal/stl), and increasing Pitch looks down
	// on the model. TestSTLOrbitKeysMatchTheDrag holds the two together.
	for _, tc := range []struct {
		key   string
		check func(a, b stl.Camera) bool
		want  string
	}{
		{"left", func(a, b stl.Camera) bool { return b.Yaw > a.Yaw }, "the model turns left"},
		{"right", func(a, b stl.Camera) bool { return b.Yaw < a.Yaw }, "the model turns right"},
		{"up", func(a, b stl.Camera) bool { return b.Pitch < a.Pitch }, "the view rises toward the front"},
		{"down", func(a, b stl.Camera) bool { return b.Pitch > a.Pitch }, "the view drops toward the top"},
		{"h", func(a, b stl.Camera) bool { return b.Yaw > a.Yaw }, "the model turns left"},
		{"l", func(a, b stl.Camera) bool { return b.Yaw < a.Yaw }, "the model turns right"},
		{"k", func(a, b stl.Camera) bool { return b.Pitch < a.Pitch }, "the view rises toward the front"},
		{"j", func(a, b stl.Camera) bool { return b.Pitch > a.Pitch }, "the view drops toward the top"},
		{"shift+left", func(a, b stl.Camera) bool { return b.PanX < a.PanX }, "pans left"},
		{"shift+up", func(a, b stl.Camera) bool { return b.PanY > a.PanY }, "pans up"},
		{"H", func(a, b stl.Camera) bool { return b.PanX < a.PanX }, "pans left"},
		{"K", func(a, b stl.Camera) bool { return b.PanY > a.PanY }, "pans up"},
		{"+", func(a, b stl.Camera) bool { return b.Zoom > a.Zoom }, "zooms in"},
		{"=", func(a, b stl.Camera) bool { return b.Zoom > a.Zoom }, "zooms in"},
		{"-", func(a, b stl.Camera) bool { return b.Zoom < a.Zoom }, "zooms out"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			before := base.stl.cam
			out := sendKey(t, base, tc.key)
			if !tc.check(before, out.stl.cam) {
				t.Errorf("%q: %s failed (%+v → %+v)", tc.key, tc.want, before, out.stl.cam)
			}
			if !out.stl.moving {
				t.Errorf("%q did not mark the camera as moving, so the frame supersamples mid-drag", tc.key)
			}
		})
	}
}

// The hint presents "drag/↑↓←→" as one action, so the two inputs must turn the
// model the same way. This is the test that would have caught the drag being
// inverted, and the one that stops the two drifting apart again.
func TestSTLOrbitKeysMatchTheDrag(t *testing.T) {
	x0, y0, x1, y1, ok := stlMouseModel(t).stlBoxBounds()
	if !ok {
		t.Fatal("no box bounds")
	}
	cx, cy := (x0+x1)/2, (y0+y1)/2

	// dragBy returns the camera after a press at the centre and a drag of (dx, dy).
	dragBy := func(dx, dy int) stl.Camera {
		m := stlMouseModel(t)
		next, _ := m.stlMouseClick(tea.MouseClickMsg{X: cx, Y: cy, Button: tea.MouseLeft})
		out := next.(Model)
		next, _ = out.stlMouseMotion(tea.MouseMotionMsg{X: cx + dx, Y: cy + dy, Button: tea.MouseLeft})
		return next.(Model).stl.cam
	}
	start := stlViewModel(t).stl.cam

	for _, tc := range []struct {
		key    string
		dx, dy int
	}{
		{"left", -12, 0},
		{"right", 12, 0},
		{"up", 0, -12},
		{"down", 0, 12},
	} {
		t.Run(tc.key, func(t *testing.T) {
			drag := dragBy(tc.dx, tc.dy)
			key := sendKey(t, stlViewModel(t), tc.key).stl.cam
			if sign(drag.Yaw-start.Yaw) != sign(key.Yaw-start.Yaw) {
				t.Errorf("%q yaws %+v but the matching drag yaws %+v — the two inputs disagree",
					tc.key, key.Yaw-start.Yaw, drag.Yaw-start.Yaw)
			}
			if sign(drag.Pitch-start.Pitch) != sign(key.Pitch-start.Pitch) {
				t.Errorf("%q pitches %+v but the matching drag pitches %+v — the two inputs disagree",
					tc.key, key.Pitch-start.Pitch, drag.Pitch-start.Pitch)
			}
		})
	}
}

func sign(f float32) int {
	switch {
	case f > 1e-6:
		return 1
	case f < -1e-6:
		return -1
	}
	return 0
}

func TestSTLKeyReset(t *testing.T) {
	m := stlViewModel(t)
	m.stl.cam = stl.Camera{Yaw: 1, Pitch: 0.3, Zoom: 4, PanX: 0.5, PanY: -0.5}
	if got := sendKey(t, m, "r").stl.cam; got != stl.DefaultCamera() {
		t.Errorf("r left the camera at %+v, want the default", got)
	}
	// f re-fits without throwing away the angle you found.
	out := sendKey(t, m, "f")
	if out.stl.cam.Zoom != 1 || out.stl.cam.PanX != 0 || out.stl.cam.PanY != 0 {
		t.Errorf("f left zoom/pan at %+v, want them reset", out.stl.cam)
	}
	if out.stl.cam.Yaw != 1 {
		t.Errorf("f changed the yaw to %v, want it kept", out.stl.cam.Yaw)
	}
}

// Tapping an axis key twice shows the opposite side, which is how you get at the
// back of a part without orbiting all the way round it.
func TestSTLAxisViewsFlip(t *testing.T) {
	m := stlViewModel(t)
	first := sendKey(t, m, "y")
	if first.stl.cam.Yaw != 0 || first.stl.cam.Pitch != 0 {
		t.Fatalf("y gave %+v, want the front view", first.stl.cam)
	}
	second := sendKey(t, first, "y")
	if normYaw(second.stl.cam.Yaw) == normYaw(first.stl.cam.Yaw) {
		t.Error("y twice stayed on the same side")
	}

	top := sendKey(t, m, "z")
	if top.stl.cam.Pitch <= 0 {
		t.Fatalf("z gave pitch %v, want a view from above", top.stl.cam.Pitch)
	}
	bottom := sendKey(t, top, "z")
	if bottom.stl.cam.Pitch >= 0 {
		t.Errorf("z twice gave pitch %v, want the view from below", bottom.stl.cam.Pitch)
	}

	side := sendKey(t, m, "x")
	if side.stl.cam.Pitch != 0 || side.stl.cam.Yaw == 0 {
		t.Errorf("x gave %+v, want a side view", side.stl.cam)
	}
}

func TestSTLSpinToggle(t *testing.T) {
	m := stlViewModel(t)
	on := sendKey(t, m, "s")
	if !on.stl.spin {
		t.Fatal("s did not start the turntable")
	}
	if !on.stl.moving {
		t.Error("a spinning turntable should stay in the fast render path")
	}
	before := on.stl.cam.Yaw
	if cmd := on.applySTLSpin(stlSpinMsg{gen: on.stl.gen}); cmd == nil {
		t.Error("a spin tick did not re-arm itself")
	}
	if on.stl.cam.Yaw == before {
		t.Error("a spin tick did not turn the model")
	}
	off := sendKey(t, on, "s")
	if off.stl.spin {
		t.Error("s did not stop the turntable")
	}
	if off.stl.moving {
		t.Error("stopping should settle to the crisp frame")
	}
}

// The settle is what brings supersampling back after a drag, and it must ignore
// every firing but the newest — a drag arms one per motion event.
func TestSTLSettleIgnoresStaleTicks(t *testing.T) {
	m := stlViewModel(t)
	m = sendKey(t, m, "left")
	m = sendKey(t, m, "left")
	if !m.stl.moving {
		t.Fatal("not moving after two nudges")
	}
	// Let the drag's frames land, so the settle isn't merely coalesced behind an
	// in-flight render (which is TestSTLFrameCmdGuards' subject, not this one).
	m.stl.rendering, m.stl.pending = false, false
	if cmd := m.applySTLSettle(stlSettleMsg{gen: m.stl.gen, seq: m.stl.settleSeq - 1}); cmd != nil {
		t.Error("an older settle produced a render")
	}
	if !m.stl.moving {
		t.Error("an older settle cleared the moving flag")
	}
	if cmd := m.applySTLSettle(stlSettleMsg{gen: m.stl.gen, seq: m.stl.settleSeq}); cmd == nil {
		t.Error("the newest settle did not render the crisp frame")
	}
	if m.stl.moving {
		t.Error("the newest settle did not clear the moving flag")
	}
	// A spinning turntable never settles: the crisp frame would be replaced at once.
	m.stl.spin, m.stl.moving = true, true
	m.stl.rendering, m.stl.pending = false, false
	m.stl.settleSeq++
	if cmd := m.applySTLSettle(stlSettleMsg{gen: m.stl.gen, seq: m.stl.settleSeq}); cmd != nil {
		t.Error("a spinning viewer settled")
	}
}

func TestSTLKeyClose(t *testing.T) {
	for _, k := range []string{"esc", "q"} {
		if out := sendKey(t, stlViewModel(t), k); out.stl.active {
			t.Errorf("%q did not close the viewer", k)
		}
	}
	// And the key that opened it, through the binding — so a rebound preview key
	// closes the viewer too, as it does the image modal.
	m := stlViewModel(t)
	m.keys = newKeyMap("ctrl")
	next, _ := m.handleSTLKey(stlPress("space"))
	if next.(Model).stl.active {
		t.Error("the preview key did not close the viewer")
	}
}

func TestSTLCycleModels(t *testing.T) {
	m := stlViewModel(t)
	m.stl.items = []previewItem{
		{file: stlFile("a", "a.stl", "", ".stl", 1), name: "a.stl"},
		{file: stlFile("b", "b.stl", "", ".stl", 1), name: "b.stl"},
	}
	out := sendKey(t, m, "n")
	if out.stl.idx != 1 {
		t.Errorf("n moved to %d, want 1", out.stl.idx)
	}
	if out.stl.mesh != nil || !out.stl.loading {
		t.Error("cycling did not start a fresh load")
	}
	if out.stl.cam != stl.DefaultCamera() {
		t.Error("cycling carried the old camera onto a different model")
	}
	if back := sendKey(t, out, "p"); back.stl.idx != 0 {
		t.Errorf("p moved to %d, want 0", back.stl.idx)
	}
	// A single model has nothing to cycle to.
	if got := sendKey(t, stlViewModel(t), "n").stl.idx; got != 0 {
		t.Errorf("n on a single model moved to %d", got)
	}
}

// --- mouse ----------------------------------------------------------------

// stlMouseModel gives the viewer the render cache its box geometry is measured
// against, so a click can be judged inside or outside.
func stlMouseModel(t *testing.T) *Model {
	t.Helper()
	m := stlViewModel(t)
	m.vcache = &viewCache{bodyH: m.height - tabsHeight}
	return m
}

func TestSTLDragOrbits(t *testing.T) {
	m := stlMouseModel(t)
	x0, y0, x1, y1, ok := m.stlBoxBounds()
	if !ok {
		t.Fatal("no box bounds")
	}
	cx, cy := (x0+x1)/2, (y0+y1)/2

	next, _ := m.stlMouseClick(tea.MouseClickMsg{X: cx, Y: cy, Button: tea.MouseLeft})
	out := next.(Model)
	if !out.stl.drag {
		t.Fatal("a press inside the box did not arm a drag")
	}
	if out.stl.panning {
		t.Error("a plain left press armed a pan")
	}
	// A drag grabs the model, so the face you are looking at follows the pointer.
	// Increasing Yaw swings that face *left* (TestYawTurnsModelLeft in
	// internal/stl), so a rightward drag must decrease it. This is deliberately
	// the opposite sign to the arrow keys, which orbit the camera instead.
	before := out.stl.cam
	next, _ = out.stlMouseMotion(tea.MouseMotionMsg{X: cx + 10, Y: cy, Button: tea.MouseLeft})
	out = next.(Model)
	if out.stl.cam.Yaw >= before.Yaw {
		t.Errorf("dragging right turned the model the wrong way (%v → %v): the surface must follow the pointer",
			before.Yaw, out.stl.cam.Yaw)
	}
	next, _ = out.stlMouseMotion(tea.MouseMotionMsg{X: cx, Y: cy, Button: tea.MouseLeft})
	if back := next.(Model).stl.cam.Yaw; math.Abs(float64(back-before.Yaw)) > 1e-5 {
		t.Errorf("dragging back to where it started left the model at %v, want %v", back, before.Yaw)
	}
	if out.stl.cam.PanX != before.PanX {
		t.Error("an orbit drag also panned")
	}
	// Dragging down looks down on the model — the top rotates toward you.
	next, _ = out.stlMouseMotion(tea.MouseMotionMsg{X: cx + 10, Y: cy + 6, Button: tea.MouseLeft})
	if p := next.(Model).stl.cam.Pitch; p <= out.stl.cam.Pitch {
		t.Errorf("dragging down gave pitch %v, want a higher one (looking down on it)", p)
	}
	if next, _ = out.stlMouseRelease(); next.(Model).stl.drag {
		t.Error("release did not end the drag")
	}
}

func TestSTLShiftDragPans(t *testing.T) {
	m := stlMouseModel(t)
	x0, y0, x1, y1, _ := m.stlBoxBounds()
	cx, cy := (x0+x1)/2, (y0+y1)/2
	next, _ := m.stlMouseClick(tea.MouseClickMsg{X: cx, Y: cy, Button: tea.MouseLeft, Mod: tea.ModShift})
	out := next.(Model)
	if !out.stl.panning {
		t.Fatal("shift+press did not arm a pan")
	}
	before := out.stl.cam
	next, _ = out.stlMouseMotion(tea.MouseMotionMsg{X: cx + 8, Y: cy - 4, Button: tea.MouseLeft})
	out = next.(Model)
	if out.stl.cam.PanX <= before.PanX {
		t.Error("dragging right did not pan right")
	}
	if out.stl.cam.PanY <= before.PanY {
		t.Error("dragging up did not pan up")
	}
	if out.stl.cam.Yaw != before.Yaw {
		t.Error("a pan drag also orbited")
	}

	// The right button pans without a modifier, as a 3D application does.
	m2 := stlMouseModel(t)
	next, _ = m2.stlMouseClick(tea.MouseClickMsg{X: cx, Y: cy, Button: tea.MouseRight})
	if !next.(Model).stl.panning {
		t.Error("a right press did not arm a pan")
	}
}

// A motion event with no drag armed must not move the camera — the pointer
// crossing the box is not an interaction.
func TestSTLMotionWithoutDragDoesNothing(t *testing.T) {
	m := stlMouseModel(t)
	before := m.stl.cam
	next, cmd := m.stlMouseMotion(tea.MouseMotionMsg{X: 10, Y: 10})
	if next.(Model).stl.cam != before {
		t.Error("a bare motion moved the camera")
	}
	if cmd != nil {
		t.Error("a bare motion produced a render")
	}
}

func TestSTLClickOutsideCloses(t *testing.T) {
	m := stlMouseModel(t)
	if !m.clickOutsideSTL(0, 0) {
		t.Fatal("the top-left corner is inside the centred box?")
	}
	next, cmd := m.stlMouseClick(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	if next.(Model).stl.active {
		t.Error("a click outside the box did not close the viewer")
	}
	if cmd == nil {
		t.Error("closing on an outside click did not free the image")
	}
}

func TestSTLWheelZooms(t *testing.T) {
	m := stlMouseModel(t)
	in, _ := m.stlMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if in.(Model).stl.cam.Zoom <= m.stl.cam.Zoom {
		t.Error("wheel up did not zoom in")
	}
	out, _ := m.stlMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if out.(Model).stl.cam.Zoom >= m.stl.cam.Zoom {
		t.Error("wheel down did not zoom out")
	}
	// A horizontal wheel is not a zoom.
	side, cmd := m.stlMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelLeft})
	if side.(Model).stl.cam.Zoom != m.stl.cam.Zoom || cmd != nil {
		t.Error("a horizontal wheel zoomed")
	}
}

// --- rendering the modal --------------------------------------------------

func TestRenderSTLView(t *testing.T) {
	m := stlViewModel(t)
	out := m.renderSTLView()
	if out == "" {
		t.Fatal("rendered nothing")
	}
	if !strings.Contains(out, "bracket.stl") {
		t.Error("the caption doesn't name the file")
	}
	if !strings.Contains(out, "facets") {
		t.Error("the caption doesn't state the facet count")
	}
	if !strings.Contains(out, "orbit") {
		t.Error("the hint doesn't mention how to orbit")
	}
	if !strings.Contains(out, kittyPlaceholder(m.stl.imgID, 1, 1)[:8]) {
		t.Error("no placeholder cells — the pixels would have nowhere to land")
	}

	// Loading and error states say so rather than drawing an empty box.
	m.stl.mesh, m.stl.loading = nil, true
	if !strings.Contains(m.renderSTLView(), "loading") {
		t.Error("the loading state isn't shown")
	}
	m.stl.loading = false
	m.stl.err = stl.ErrNotSTL
	if !strings.Contains(m.renderSTLView(), "not an STL") {
		t.Error("the error isn't shown")
	}

	if closed := (&Model{}).renderSTLView(); closed != "" {
		t.Errorf("a closed viewer rendered %q", closed)
	}
}

// The frame command is the only thing that renders; it must refuse to run when
// there is nothing to draw with, and coalesce rather than queue.
func TestSTLFrameCmdGuards(t *testing.T) {
	m := stlViewModel(t)
	if cmd := m.stlFrameCmd(); cmd == nil {
		t.Fatal("a loaded viewer produced no frame")
	}
	if !m.stl.rendering {
		t.Error("the in-flight flag wasn't set")
	}
	// A second request while one is in flight queues exactly one more.
	if cmd := m.stlFrameCmd(); cmd != nil {
		t.Error("a second frame ran concurrently with the first")
	}
	if !m.stl.pending {
		t.Error("the queued frame was dropped instead of pending")
	}
	if cmd := m.stlFrameCmd(); cmd != nil {
		t.Error("a third frame ran")
	}
	// The frame that lands launches the queued one and nothing more.
	if cmd := m.applySTLFrame(stlFrameMsg{gen: m.stl.gen, seq: "\x1b_Gx\x1b\\"}); cmd == nil {
		t.Error("a finished frame neither drew nor launched the queued one")
	}
	if m.stl.pending {
		t.Error("pending survived the frame that consumed it")
	}

	for _, tc := range []struct {
		name     string
		sabotage func(*Model)
	}{
		{"no mesh", func(m *Model) { m.stl.mesh = nil }},
		{"no renderer", func(m *Model) { m.stl.rend = nil }},
		{"no box", func(m *Model) { m.stl.rows, m.stl.cols = 0, 0 }},
		{"closed", func(m *Model) { m.stl.active = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := stlViewModel(t)
			tc.sabotage(m)
			if cmd := m.stlFrameCmd(); cmd != nil {
				t.Error("produced a frame anyway")
			}
			if m.stl.rendering {
				t.Error("left the in-flight flag set, wedging every later frame")
			}
		})
	}
}

func TestResizeSTLViewRefits(t *testing.T) {
	m := stlViewModel(t)
	wide := m.stl.cols
	m.width, m.height = 60, 24
	if cmd := m.resizeSTLView(); cmd == nil {
		t.Error("a resize produced no re-render")
	}
	if m.stl.cols >= wide {
		t.Errorf("cols = %d after shrinking the terminal, was %d", m.stl.cols, wide)
	}
	// A tiny terminal still yields a drawable box rather than a negative one.
	m.width, m.height = 8, 4
	m.sizeSTLView()
	if m.stl.cols <= 0 || m.stl.rows <= 0 {
		t.Errorf("viewport is %dx%d in a tiny terminal", m.stl.cols, m.stl.rows)
	}
	if closed := (&Model{}); closed.resizeSTLView() != nil {
		t.Error("a closed viewer re-rendered on resize")
	}
}

func TestWithThousands(t *testing.T) {
	for in, want := range map[int]string{
		0: "0", 12: "12", 999: "999", 1000: "1,000",
		12345: "12,345", 199712: "199,712", 1000000: "1,000,000",
	} {
		if got := withThousands(in); got != want {
			t.Errorf("withThousands(%d) = %q, want %q", in, got, want)
		}
	}
}

// The attachment line marks a model with its own icon and, when a thumbnail is
// drawn, the same disclosure chevron an image gets.
func TestAttachmentLineForSTL(t *testing.T) {
	m := thumbModel()
	m.width = 100
	p := stlPost(stlFile("f1", "bracket.stl", "model/stl", ".stl", 4096))
	line := m.renderAttachments(p, 80)
	if !strings.Contains(line, "🧊") {
		t.Errorf("attachment line %q has no 3D icon", line)
	}
	if !strings.Contains(line, thumbOpenChevron) {
		t.Errorf("attachment line %q has no disclosure chevron", line)
	}
	// Collapsed: the chevron flips and no thumbnail rows are drawn.
	m.thumbsCollapsed = map[string]bool{"post1": true}
	if line := m.renderAttachments(p, 80); !strings.Contains(line, thumbShutChevron) {
		t.Errorf("collapsed line %q does not show the shut chevron", line)
	}
	if rows := m.inlineFileThumbLines(p, p.Metadata.Files[0], 80); rows != nil {
		t.Errorf("a collapsed post drew %d thumbnail rows", len(rows))
	}
}

// A sighted-but-unbuilt model reserves its rows, so the transcript doesn't
// reflow when the render lands.
func TestSTLThumbReservesRows(t *testing.T) {
	m := thumbModel()
	f := stlFile("f1", "bracket.stl", "model/stl", ".stl", 4096)
	lines := fileThumbLines(m, f, 80)
	if !blankRows(lines) {
		t.Fatalf("an unbuilt model drew %d non-blank rows", len(lines))
	}
	if len(lines) != inlineThumbRows {
		t.Errorf("reserved %d rows, want %d", len(lines), inlineThumbRows)
	}
	// And it is queued for the background build.
	if got := m.inlineImg.pendingKeys(); len(got) != 1 || got[0] != "f1" {
		t.Errorf("pending = %v, want the model queued for a render", got)
	}
}

// --- frame double buffering ------------------------------------------------

// runSTLFrame drives one frame Cmd to completion and applies it, the way the
// event loop would.
func runSTLFrame(t *testing.T, m *Model) stlFrameMsg {
	t.Helper()
	return runSTLFrameCmd(t, m, m.stlFrameCmd())
}

// runSTLFrameCmd is runSTLFrame for the paths that produce the Cmd themselves
// (a resize, a settle), which have already marked a render in flight.
func runSTLFrameCmd(t *testing.T, m *Model, cmd tea.Cmd) stlFrameMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no frame produced")
	}
	msg, ok := cmd().(stlFrameMsg)
	if !ok {
		t.Fatalf("frame Cmd returned %T, want stlFrameMsg", cmd())
	}
	if msg.err != nil {
		t.Fatalf("frame: %v", msg.err)
	}
	m.applySTLFrame(msg)
	return msg
}

// The first frame has to establish the image itself, and with it the two spare
// animation frames a swap needs — one of them asking for a reply, since a
// terminal that can't do frame edits says nothing at all.
func TestSTLFirstFrameArmsFrameSwap(t *testing.T) {
	m := stlViewModel(t)
	msg := runSTLFrame(t, m)

	if !strings.Contains(msg.seq, "a=T") {
		t.Error("the first frame didn't transmit the image")
	}
	if n := strings.Count(msg.seq, "a=f"); n != 2 {
		t.Errorf("created %d spare frames, want 2", n)
	}
	if m.stl.curFrame != 1 {
		t.Errorf("curFrame = %d after a transmit, want the root (1)", m.stl.curFrame)
	}
	if msg.w != m.stl.cols*m.cellPxOr(8) || msg.h != m.stl.rows*m.cellPxHOr(16) {
		t.Errorf("frame box %dx%d doesn't match the viewport", msg.w, msg.h)
	}
	if !m.stl.swapAsked {
		t.Error("the arming frame edit went out without being recorded")
	}
	// Exactly one of the two creates wants an answer; the other is quiet.
	first, rest, _ := strings.Cut(msg.seq[strings.Index(msg.seq, "a=f"):], "a=f")
	if strings.Contains(first, "q=") {
		t.Error("the arming frame edit suppressed the reply that arms swap mode")
	}
	if !strings.Contains(rest, "q=2") {
		t.Error("the second spare frame asked for a reply it doesn't need")
	}
	// And nothing swaps until the terminal has actually said OK.
	if next := runSTLFrame(t, m); !strings.Contains(next.seq, "a=T") {
		t.Error("swapped frames before the terminal confirmed it could")
	}
	if m.stl.swapAsked && strings.Count(runSTLFrame(t, m).seq, "q=") == 0 {
		t.Error("asked for a second reply")
	}
}

// Once armed, a frame goes into the spare that isn't on screen and the placement
// is switched onto it — never over the image the cells are pointing at.
func TestSTLSwapAlternatesSpareFrames(t *testing.T) {
	m := stlViewModel(t)
	runSTLFrame(t, m) // establishes the image and the spares
	m.applySTLFrameReply("OK")
	if !m.stl.swap {
		t.Fatal("an OK reply didn't arm frame swapping")
	}

	for _, want := range []int{2, 3, 2} {
		msg := runSTLFrame(t, m)
		if strings.Contains(msg.seq, "a=T") {
			t.Fatalf("frame %d re-transmitted the image instead of swapping", want)
		}
		if !strings.Contains(msg.seq, fmt.Sprintf("r=%d", want)) {
			t.Errorf("frame went into a spare other than %d: %q", want, kittyOpts(msg.seq))
		}
		if !strings.Contains(msg.seq, fmt.Sprintf("c=%d", want)) {
			t.Errorf("the placement wasn't switched onto frame %d", want)
		}
		if m.stl.curFrame != want {
			t.Errorf("curFrame = %d, want %d", m.stl.curFrame, want)
		}
		// The upload must come before the switch, or the switch lands on a frame
		// that is still half-written.
		if strings.Index(msg.seq, "a=f") > strings.Index(msg.seq, "a=a") {
			t.Error("switched the displayed frame before uploading it")
		}
		// Quiet about success, loud about failure: an OK per frame would be a
		// reply storm at drag rate, but a refusal is what disarms the swap.
		if !strings.Contains(msg.seq, "q=1") || strings.Contains(msg.seq, "q=2") {
			t.Errorf("wrong quiet level on a swap frame: %q", kittyOpts(msg.seq))
		}
	}
}

// A frame edit that fails mid-session — the spares evicted from the terminal's
// image storage, say — has to put the viewer back on re-transmits rather than
// leave it talking to frames that aren't there.
func TestSTLFrameErrorDisarmsSwap(t *testing.T) {
	m := stlViewModel(t)
	runSTLFrame(t, m)
	m.applySTLFrameReply("OK")
	runSTLFrame(t, m)

	m.applySTLFrameReply("ENOENT: image not found")
	if m.stl.swap {
		t.Fatal("a failed frame edit left swapping armed")
	}
	msg := runSTLFrame(t, m)
	if !strings.Contains(msg.seq, "a=T") {
		t.Error("kept swapping into frames the terminal says are gone")
	}
	if n := strings.Count(msg.seq, "a=f"); n != 2 {
		t.Errorf("rebuilt %d spares, want 2", n)
	}
	// And it stays there: one question per viewer, so a terminal that keeps
	// failing isn't asked thirty times a second what it already answered.
	for range 3 {
		next := runSTLFrame(t, m)
		if !strings.Contains(next.seq, "a=T") {
			t.Fatal("started swapping again on its own")
		}
		for _, opts := range strings.Split(kittyOpts(next.seq), " | ") {
			if strings.Contains(opts, "a=f") && !strings.Contains(opts, "q=") {
				t.Errorf("asked to be armed again: %q", opts)
			}
		}
	}
}

// A spare frame is a canvas the size of the image, so a resize invalidates both
// of them and the next frame has to rebuild the lot.
func TestSTLResizeRebuildsSpareFrames(t *testing.T) {
	m := stlViewModel(t)
	runSTLFrame(t, m)
	m.applySTLFrameReply("OK")
	runSTLFrame(t, m)
	if m.stl.curFrame == 1 {
		t.Fatal("setup: never got onto a spare frame")
	}

	m.width, m.height = 70, 24
	msg := runSTLFrameCmd(t, m, m.resizeSTLView())
	if !strings.Contains(msg.seq, "a=T") {
		t.Error("kept swapping into spares built for the old size")
	}
	if m.stl.curFrame != 1 {
		t.Errorf("curFrame = %d after a rebuild, want 1", m.stl.curFrame)
	}
	if msg.w == 0 || msg.w != m.stl.cols*m.cellPxOr(8) {
		t.Errorf("rebuilt at %d px wide, viewport is %d cells", msg.w, m.stl.cols)
	}
	// And it swaps again from there, without a second arming reply.
	if next := runSTLFrame(t, m); strings.Contains(next.seq, "a=T") {
		t.Error("didn't resume swapping after the rebuild")
	}
}

// Silence and errors both mean "this terminal can't do it" — the viewer stays on
// plain re-transmits rather than talking to a frame that was never created.
func TestSTLFrameReplyRefusals(t *testing.T) {
	for _, payload := range []string{"ENOTSUP: unsupported", "EINVAL: bad frame", ""} {
		m := stlViewModel(t)
		runSTLFrame(t, m)
		m.applySTLFrameReply(payload)
		if m.stl.swap {
			t.Errorf("reply %q armed frame swapping", payload)
		}
		if msg := runSTLFrame(t, m); !strings.Contains(msg.seq, "a=T") {
			t.Errorf("reply %q: stopped re-transmitting anyway", payload)
		}
	}
	// A reply that outlives the viewer it belongs to touches nothing.
	closed := &Model{}
	closed.applySTLFrameReply("OK")
	if closed.stl.swap {
		t.Error("a reply armed a closed viewer")
	}
}

// kittyOpts pulls the option keys out of a graphics sequence, so a failure
// message shows what was actually sent rather than a screenful of base64.
func kittyOpts(seq string) string {
	var out []string
	for _, part := range strings.Split(seq, "\x1b_G") {
		if opts, _, ok := strings.Cut(part, ";"); ok {
			out = append(out, opts)
		}
	}
	return strings.Join(out, " | ")
}
