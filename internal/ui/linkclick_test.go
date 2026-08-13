package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// linkPost builds a one-post model parked on the channel tab whose body carries
// the given message, rendered so the transcript holds the OSC 8 hyperlinks.
func linkModel(msg string) Model {
	m := mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: msg}})
	m.renderMessages()
	return m
}

// TestLinkAtDisplayCol resolves a display column to the OSC 8 URL under it,
// counting only the visible run between the open/close markers and ignoring the
// surrounding escapes — exactly the coordinate space a click is measured in.
func TestLinkAtDisplayCol(t *testing.T) {
	// "ab " + link("docs" -> URL) + " cd": visible cols a=0 b=1 sp=2 d=3 o=4
	// c=5 s=6 sp=7 c=8 d=9. The link covers cols 3..6.
	url := "https://example.com/x"
	line := "ab " + osc8Link(url, mdLinkStyle.Render("docs")) + " cd"
	for _, c := range []struct {
		col  int
		want string
	}{
		{0, ""}, {2, ""}, // plain text before the link
		{3, url}, {4, url}, {5, url}, {6, url}, // the four cells of "docs"
		{7, ""}, {8, ""}, // plain text after the close marker
		{99, ""}, // past the end
	} {
		got, ok := linkAtDisplayCol(line, c.col)
		if c.want == "" {
			if ok {
				t.Errorf("col %d: got link %q, want none", c.col, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("col %d: got (%q,%v), want (%q,true)", c.col, got, ok, c.want)
		}
	}
}

// TestLinkScanCarriesAcrossWrap: a link hard-wrapped by wrapBodyLine keeps every
// row clickable — the open marker is only on the first row, so the continuation
// rows resolve via the carried-in state replayed from the previous row.
func TestLinkScanCarriesAcrossWrap(t *testing.T) {
	url := "https://example.com/wraps"
	full := "  " + osc8Link(url, mdLinkStyle.Render("alpha beta gamma"))
	rows := wrapBodyLine(full, 12)
	if len(rows) < 2 {
		t.Fatalf("expected the link to wrap, got %d row(s): %q", len(rows), rows)
	}

	// Replay the carried-in state row by row, asserting a cell on each row that
	// holds link text resolves to the URL — including rows past the first, which
	// carry no OSC 8 open marker of their own.
	carried := ""
	for i, row := range rows {
		got, ok, next := linkScan(row, 3, carried) // col 3 = past the two-space gutter
		if !ok || got != url {
			t.Fatalf("row %d (%q): col 3 = (%q,%v), want (%q,true)", i, row, got, ok, url)
		}
		carried = next
	}
	// After the final row (which holds the close marker) the hyperlink is closed.
	if carried != "" {
		t.Fatalf("hyperlink still open after the last row: %q", carried)
	}
}

// TestLinkAtResolvesWrappedContinuation: end to end through linkAt, a click on the
// wrapped continuation row of a long link resolves to its URL — even though that
// row, scanned on its own, carries no usable open marker (it holds the close).
func TestLinkAtResolvesWrappedContinuation(t *testing.T) {
	url := "https://example.com/a/long/enough/path/to/wrap/across/several/rows/" +
		"because/the/test/viewport/is/eighty/columns/wide/and/we/need/more/than/that"
	m := linkModel("see " + url)
	lines, _ := m.ensureWrapIndex(focusMessages, m.msgsView.Width())

	// After a wrap the open marker sits on the first link row and the close on the
	// last; the rows between/after carry no usable opener. The last row holding any
	// OSC 8 sequence is the close row — a continuation of the same link.
	first, cont := -1, -1
	for i := range lines {
		if strings.Contains(lines[i], osc8Open) {
			if first < 0 {
				first = i
			}
			cont = i
		}
	}
	if first < 0 || first == cont {
		t.Fatalf("link did not wrap into multiple rows at width %d (first=%d last=%d)", m.msgsView.Width(), first, cont)
	}

	// Scanned on its own that row misses the link (the regression); linkAt resolves
	// it via the carried-in state.
	if _, isolated := linkAtDisplayCol(lines[cont], 3); isolated {
		t.Fatalf("continuation row %d resolved without carry; test no longer exercises the bug", cont)
	}
	got, ok := m.linkAt(focusMessages, cont, 3)
	if !ok || got != url {
		t.Fatalf("linkAt on continuation row %d = (%q,%v), want (%q,true)", cont, got, ok, url)
	}
}

// TestHighlightLinkStaysClickable: the hover restyle adds its background yet keeps
// the OSC 8 markers, so the highlighted link is still resolvable (clickable).
func TestHighlightLinkStaysClickable(t *testing.T) {
	url := "https://example.com"
	body := "see " + osc8Link(url, mdLinkStyle.Render("docs")) + " ok"
	out := highlightLink(body, url, mdLinkHoverStyle)
	if !strings.Contains(out, bgSGR(panelHoverBg)) {
		t.Fatalf("hover background not applied: %q", out)
	}
	if !strings.Contains(out, "\x1b]8;;"+url+"\x1b\\") {
		t.Fatalf("OSC 8 hyperlink lost: %q", out)
	}
	if got, ok := linkAtDisplayCol(out, 5); !ok || got != url { // col 5 = inside "docs"
		t.Fatalf("highlighted link no longer resolves: (%q,%v)", got, ok)
	}
}

// TestHighlightLinkOnlyMatchingURL: only the link whose target matches is
// restyled; a different link in the same body is left untouched.
func TestHighlightLinkOnlyMatchingURL(t *testing.T) {
	a := "https://a.com"
	b := "https://b.com"
	body := osc8Link(a, mdLinkStyle.Render("aaa")) + " " + osc8Link(b, mdLinkStyle.Render("bbb"))
	out := highlightLink(body, a, mdLinkHoverStyle)
	// "aaa" (3 cells) carries the background; "bbb" must not, so exactly 3.
	if n := strings.Count(out, bgSGR(panelHoverBg)); n != 3 {
		t.Fatalf("expected only the 3 cells of the matched link highlighted, got %d", n)
	}
	// Both links remain intact and resolvable.
	if !strings.Contains(out, "\x1b]8;;"+a+"\x1b\\") || !strings.Contains(out, "\x1b]8;;"+b+"\x1b\\") {
		t.Fatalf("a hyperlink was lost: %q", out)
	}
}

// TestMotionHoversAndHighlightsLink: a button-less move onto a link sets the hover
// state and paints the highlight; moving onto plain text clears both.
func TestMotionHoversAndHighlightsLink(t *testing.T) {
	url := "https://a.com"
	m := mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "x [docs](" + url + ")"}})
	// Body line "  x docs": gutter (0,1), 'x' (2), ' ' (3), "docs" (4..7). Screen
	// x = 27 + col, body row = 5.
	out, _ := m.handleMouseMotion(motion(tea.MouseNone, 27+5, 5))
	m = out.(Model)
	if m.hoverLink.url != url || m.hoverLink.postID != "p" || m.hoverLink.pane != focusMessages {
		t.Fatalf("hover not set over the link: %+v", m.hoverLink)
	}
	if !strings.Contains(m.msgsView.GetContent(), bgSGR(panelHoverBg)) {
		t.Fatal("hovered link not highlighted in the transcript")
	}
	// Move onto the 'x' (col 2) — plain text.
	out, _ = m.handleMouseMotion(motion(tea.MouseNone, 27+2, 5))
	m = out.(Model)
	if m.hoverLink.url != "" {
		t.Fatalf("hover not cleared over plain text: %+v", m.hoverLink)
	}
	if strings.Contains(m.msgsView.GetContent(), bgSGR(panelHoverBg)) {
		t.Fatal("highlight not removed after leaving the link")
	}
}

// TestHoveredLinkStillClickable: the most important regression guard — a link that
// is currently hover-highlighted still opens on click.
func TestHoveredLinkStillClickable(t *testing.T) {
	url := "https://a.com"
	m := mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "x [docs](" + url + ")"}})
	out, _ := m.handleMouseMotion(motion(tea.MouseNone, 27+5, 5)) // hover the link
	m = out.(Model)
	out, _ = m.handleMouseClick(click(tea.MouseLeft, 27+5, 5))
	m = out.(Model)
	out, cmd := m.handleMouseRelease(release(tea.MouseLeft, 27+5, 5))
	m = out.(Model)
	if cmd == nil {
		t.Fatal("clicking a hover-highlighted link produced no open command")
	}
	if m.linkConfirm.active {
		t.Fatal("an http link raised the warning modal")
	}
}

// TestHoverLinkAtIsAllocFree guards the per-motion hover path: resolving the link
// under the pointer must not allocate (and so must not copy the transcript or walk
// it top-to-bottom) even at the bottom of a long channel. A regression here
// re-introduces per-motion GC pressure — see ensureWrapIndex's cache-before-fetch
// and linkAt's nearest-marker carry.
func TestHoverLinkAtIsAllocFree(t *testing.T) {
	posts := make([]*model.Post, 300)
	for i := range posts {
		msg := fmt.Sprintf("message %d with filler words to fill out a line", i)
		if i%5 == 0 {
			msg += fmt.Sprintf(" https://example.com/%d", i)
		}
		posts[i] = &model.Post{Id: fmt.Sprintf("p%d", i), CreateAt: int64(100 + i), UserId: "u", Message: msg}
	}
	m := mouseModel(posts)
	m.postIdx = len(posts) - 1
	m.renderMessages()
	y := m.msgsView.Height() + 2
	if a := testing.AllocsPerRun(50, func() { m.hoverLinkAt(30, y) }); a > 0 {
		t.Fatalf("hoverLinkAt allocated %v times per call; the motion path must be alloc-free", a)
	}
}

// TestClickOpensWebLink: a no-drag click on an http(s) link fires the open
// command and raises no warning modal.
func TestClickOpensWebLink(t *testing.T) {
	// Body line is "  x docs" — gutter (0,1), 'x' (2), ' ' (3), "docs" (4..7).
	m := linkModel("x [docs](https://example.com)")
	const x = 27 + 5 // content col 5, inside "docs"
	out, _ := m.handleMouseClick(click(tea.MouseLeft, x, 5))
	m = out.(Model)
	out, cmd := m.handleMouseRelease(release(tea.MouseLeft, x, 5))
	m = out.(Model)
	if cmd == nil {
		t.Fatal("click on a web link produced no open command")
	}
	if m.linkConfirm.active {
		t.Fatal("a web link should open without the warning modal")
	}
}

// TestClickNonWebLinkWarns: a no-drag click on a non-http(s) link opens the
// warning modal instead of handing the target straight to the OS.
func TestClickNonWebLinkWarns(t *testing.T) {
	// Body line is "  x m" — gutter (0,1), 'x' (2), ' ' (3), "m" (4).
	m := linkModel("x [m](mailto:a@b.com)")
	const x = 27 + 4 // content col 4, the link text "m"
	out, _ := m.handleMouseClick(click(tea.MouseLeft, x, 5))
	m = out.(Model)
	out, cmd := m.handleMouseRelease(release(tea.MouseLeft, x, 5))
	m = out.(Model)
	if cmd != nil {
		t.Fatal("non-web link should wait for confirmation, not open immediately")
	}
	if !m.linkConfirm.active || m.linkConfirm.url != "mailto:a@b.com" {
		t.Fatalf("warning modal not raised for the link: %+v", m.linkConfirm)
	}

	// y confirms and opens; the modal closes.
	out, cmd = m.handleLinkConfirmKey(keyStr("y"))
	m = out.(Model)
	if cmd == nil {
		t.Fatal("confirming the warning produced no open command")
	}
	if m.linkConfirm.active {
		t.Fatal("confirming left the modal open")
	}
}

// TestClickNonWebLinkCancel: n dismisses the warning without opening anything.
func TestClickNonWebLinkCancel(t *testing.T) {
	m := linkModel("x [m](mailto:a@b.com)")
	m.linkConfirm = linkConfirmState{active: true, url: "mailto:a@b.com"}
	out, cmd := m.handleLinkConfirmKey(keyStr("n"))
	m = out.(Model)
	if cmd != nil {
		t.Fatal("cancelling the warning produced an open command")
	}
	if m.linkConfirm.active {
		t.Fatal("n didn't dismiss the warning modal")
	}
}

// TestClickPlainTextNoLink: a click on non-link text neither opens nor warns.
func TestClickPlainTextNoLink(t *testing.T) {
	m := linkModel("x [docs](https://example.com)")
	const x = 27 + 2 // content col 2, the 'x' before the link
	out, _ := m.handleMouseClick(click(tea.MouseLeft, x, 5))
	m = out.(Model)
	out, cmd := m.handleMouseRelease(release(tea.MouseLeft, x, 5))
	m = out.(Model)
	if cmd != nil || m.linkConfirm.active {
		t.Fatalf("plain-text click acted on a link: cmd=%v modal=%v", cmd != nil, m.linkConfirm.active)
	}
}

// TestOpenSingleWebURLOpensDirectly: the keyboard `o` action on a post with one
// http(s) target opens it without a prompt.
func TestOpenSingleWebURLOpensDirectly(t *testing.T) {
	m := Model{}
	out, cmd := m.openFromPost(&model.Post{Message: "see https://example.com/x"})
	m = out.(Model)
	if cmd == nil {
		t.Fatal("a single web URL should open directly")
	}
	if m.linkConfirm.active {
		t.Fatal("web URL raised the warning modal")
	}
}

// TestOpenSingleNonWebURLWarns: `o` on a post whose only target is a non-web link
// raises the same warning the mouse click does, instead of opening immediately.
func TestOpenSingleNonWebURLWarns(t *testing.T) {
	m := Model{}
	out, cmd := m.openFromPost(&model.Post{Message: "mail [me](mailto:a@b.com)"})
	m = out.(Model)
	if cmd != nil {
		t.Fatal("a non-web link should wait for confirmation")
	}
	if !m.linkConfirm.active || m.linkConfirm.url != "mailto:a@b.com" {
		t.Fatalf("warning modal not raised: %+v", m.linkConfirm)
	}
}

// TestOpenAttachmentSkipsWarning: a downloaded attachment opens straight away —
// the scheme gate only applies to URL targets, not the user's own files.
func TestOpenAttachmentSkipsWarning(t *testing.T) {
	m := Model{}
	p := &model.Post{Metadata: &model.PostMetadata{Files: []*model.FileInfo{{Id: "f1", Name: "report.pdf"}}}}
	out, cmd := m.openFromPost(p)
	m = out.(Model)
	if cmd == nil {
		t.Fatal("an attachment should open directly")
	}
	if m.linkConfirm.active {
		t.Fatal("an attachment raised the warning modal")
	}
}

// TestPickerNonWebWarns: choosing a non-web link from the multi-target picker
// closes the picker and hands off to the warning modal.
func TestPickerNonWebWarns(t *testing.T) {
	m := Model{}
	out, _ := m.openFromPost(&model.Post{Message: "[a](https://ex.com) and [b](mailto:x@y.com)"})
	m = out.(Model)
	if !m.openPickerActive() {
		t.Fatal("two targets should raise the picker")
	}
	m.openPickerIdx = 1 // the mailto entry
	cmd := m.applyOpenPick()
	if cmd != nil {
		t.Fatal("picking a non-web link should wait for confirmation")
	}
	if m.openPickerActive() {
		t.Fatal("picker should close when the warning opens")
	}
	if !m.linkConfirm.active || m.linkConfirm.url != "mailto:x@y.com" {
		t.Fatalf("warning modal not raised by the picker: %+v", m.linkConfirm)
	}
}

// TestOpenedStatusSelfClears: a successful open sets the "opened X" toast and
// schedules its own clear — a mouse-driven open never presses a key afterward,
// and only key handlers clear m.status, so without this the toast would stick.
func TestOpenedStatusSelfClears(t *testing.T) {
	m := Model{}
	out, cmd := m.update(attachmentOpenedMsg{name: "report.pdf"})
	m = out.(Model)
	if m.status != "opened report.pdf" {
		t.Fatalf("status = %q, want %q", m.status, "opened report.pdf")
	}
	if cmd == nil {
		t.Fatal("a successful open should schedule its own status clear")
	}

	// The scheduled clear empties the slot while the toast still owns it.
	out, _ = m.update(statusFlashClearMsg{text: "opened report.pdf"})
	if got := out.(Model).status; got != "" {
		t.Fatalf("status = %q, want cleared", got)
	}

	// A stale clear must not wipe a newer status that took over the slot.
	m.status = "loading messages…"
	out, _ = m.update(statusFlashClearMsg{text: "opened report.pdf"})
	if got := out.(Model).status; got != "loading messages…" {
		t.Fatalf("stale clear wiped a newer status: %q", got)
	}
}

// TestOpenErrorStatusPersists: an open *failure* stays in the footer (no
// self-clear) — it's rare and worth reading until the next interaction.
func TestOpenErrorStatusPersists(t *testing.T) {
	m := Model{}
	out, cmd := m.update(attachmentOpenedMsg{name: "report.pdf", err: errors.New("boom")})
	if cmd != nil {
		t.Fatal("an open error should not schedule a clear")
	}
	if got := out.(Model).status; got != "open report.pdf: boom" {
		t.Fatalf("status = %q, want the error to persist", got)
	}
}

// TestLinkSurvivesMouseDisabled: with mouse capture off the transcript still
// carries the OSC 8 hyperlink, so the terminal opens it on a native click — the
// app intercepts clicks only while mouseEnabled.
func TestLinkSurvivesMouseDisabled(t *testing.T) {
	m := linkModel("x [docs](https://example.com)")
	m.mouseEnabled = false
	if !m.mouseBlocked() {
		t.Fatal("mouse disabled but clicks aren't blocked; the app would steal the terminal's link click")
	}
	if !strings.Contains(m.msgsView.GetContent(), osc8Open+"https://example.com\x1b\\") {
		t.Fatalf("transcript lost the OSC 8 hyperlink: %q", m.msgsView.GetContent())
	}
}
