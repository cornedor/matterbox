package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/editor"
	"matterbox/internal/viewport"
)

// infoTestModel builds a Model with the viewports + keymap the channel-info
// panel needs, an open public channel "chan123" carrying a purpose link, and
// the info panel already raised for it.
func infoTestModel() Model {
	mk := func() viewport.Model {
		vp := viewport.New()
		vp.SoftWrap = true
		vp.SetWidth(50)
		vp.SetHeight(20)
		return vp
	}
	ch := &model.Channel{
		Id:      "chan123",
		TeamId:  "t1",
		Type:    model.ChannelTypeOpen,
		Name:    "general",
		Purpose: "see [docs](https://ex.com/d) for more",
		Header:  "ping @ops",
	}
	ta := editor.New()
	ta.SetWidth(40)
	m := Model{
		keys:          newKeyMap("ctrl"),
		width:         100,
		height:        44,
		focus:         focusInfo,
		teams:         []*model.Team{{Id: "t1", Name: "eng", DisplayName: "Engineering"}},
		openChannelID: "chan123",
		channels:      map[string][]*model.Channel{"t1": {ch}},
		userNames:     map[string]string{},
		me:            &model.User{Id: "me", Username: "me"},
		msgsView:      mk(),
		threadView:    mk(),
		refView:       mk(),
		infoView:      mk(),
		input:         ta,
		infoOpen:      true,
		infoChannelID: "chan123",
		infoIdx:       -1,
		infoHoverIdx:  -1,
	}
	m.teamIdx = m.firstTeamTabIdx() // land on the channel tab, not a synthetic tab
	return m
}

// TestOsc8OpensInLine extracts each hyperlink's URL from a rendered line in
// order, skipping the empty-URL close marker and plain text.
func TestOsc8OpensInLine(t *testing.T) {
	line := osc8Link("https://a.com", "a") + " plain " + osc8Link("https://b.com", "b")
	got := osc8OpensInLine(line)
	if len(got) != 2 || got[0] != "https://a.com" || got[1] != "https://b.com" {
		t.Fatalf("osc8OpensInLine = %v, want [https://a.com https://b.com]", got)
	}
	if urls := osc8OpensInLine("no links here"); urls != nil {
		t.Fatalf("plain line returned %v, want nil", urls)
	}
}

// TestOrderedPinnedNewestFirst sorts the pinned PostList newest-first.
func TestOrderedPinnedNewestFirst(t *testing.T) {
	pl := &model.PostList{Posts: map[string]*model.Post{
		"old": p("old", 100),
		"new": p("new", 300),
		"mid": p("mid", 200),
	}}
	got := ids(orderedPinned(pl))
	if !eq(got, []string{"new", "mid", "old"}) {
		t.Fatalf("orderedPinned = %v, want [new mid old]", got)
	}
}

// TestChannelTypeLabel names each channel kind.
func TestChannelTypeLabel(t *testing.T) {
	cases := map[model.ChannelType]string{
		model.ChannelTypeOpen:    "public channel",
		model.ChannelTypePrivate: "private channel",
		model.ChannelTypeDirect:  "direct message",
		model.ChannelTypeGroup:   "group message",
	}
	for typ, want := range cases {
		if got := channelTypeLabel(&model.Channel{Type: typ}); got != want {
			t.Errorf("channelTypeLabel(%q) = %q, want %q", typ, got, want)
		}
	}
}

// TestInfoCountLabel shows a count only once loaded.
func TestInfoCountLabel(t *testing.T) {
	if got := infoCountLabel("Pinned", 3, true); got != "Pinned (3)" {
		t.Errorf("loaded label = %q, want %q", got, "Pinned (3)")
	}
	if got := infoCountLabel("Pinned", 0, false); got != "Pinned" {
		t.Errorf("loading label = %q, want %q", got, "Pinned")
	}
}

// TestRenderInfoBuildsTargetsAndContent renders the panel and checks the
// section content plus the focusable-target list: the purpose link first, then
// each pinned message in order.
func TestRenderInfoBuildsTargetsAndContent(t *testing.T) {
	m := infoTestModel()
	m.infoMembers = []*model.User{{Id: "u_alice", Username: "alice"}, {Id: "me", Username: "me"}}
	m.infoMembersLoaded = true
	m.infoPinned = []*model.Post{
		{Id: "pin1", ChannelId: "chan123", UserId: "u_alice", Message: "first pin", CreateAt: 200},
		{Id: "pin2", ChannelId: "chan123", UserId: "u_alice", Message: "second pin", CreateAt: 100},
	}
	m.userNames["u_alice"] = "alice"
	m.infoPinnedLoaded = true

	m.renderInfo()

	content := m.infoView.GetContent()
	for _, want := range []string{"Purpose", "Header", "Members (2)", "Pinned (2)", "Muted:", "chan123", "@alice", "@me (you)"} {
		if !strings.Contains(content, want) {
			t.Errorf("rendered info missing %q\n---\n%s", want, content)
		}
	}

	// Document order: purpose link, members (self first), the add-members row
	// closing the member list, pinned messages, then the media drill-down row.
	if len(m.infoTargets) != 7 {
		t.Fatalf("targets = %d, want 7 (1 link + 2 members + 1 add + 2 pins + 1 media)", len(m.infoTargets))
	}
	if m.infoTargets[0].kind != infoTargetLink || m.infoTargets[0].url != "https://ex.com/d" {
		t.Errorf("target[0] = %+v, want purpose link https://ex.com/d", m.infoTargets[0])
	}
	if m.infoTargets[1].kind != infoTargetMember || m.infoTargets[1].userID != "me" {
		t.Errorf("target[1] = %+v, want member me (self first)", m.infoTargets[1])
	}
	if m.infoTargets[2].kind != infoTargetMember || m.infoTargets[2].userID != "u_alice" {
		t.Errorf("target[2] = %+v, want member u_alice", m.infoTargets[2])
	}
	if m.infoTargets[3].kind != infoTargetAddMember {
		t.Errorf("target[3] = %+v, want the add-members row", m.infoTargets[3])
	}
	if m.infoTargets[4].kind != infoTargetPin || m.infoTargets[4].postID != "pin1" {
		t.Errorf("target[4] = %+v, want pin pin1", m.infoTargets[4])
	}
	if m.infoTargets[5].kind != infoTargetPin || m.infoTargets[5].postID != "pin2" {
		t.Errorf("target[5] = %+v, want pin pin2", m.infoTargets[5])
	}
	if m.infoTargets[6].kind != infoTargetMedia {
		t.Errorf("target[6] = %+v, want the media row", m.infoTargets[6])
	}
}

// TestInfoMemberOpensDM: activating a member target closes the panel and kicks
// off the DM-open command (resolved into a channel by groupDMResolvedMsg).
func TestInfoMemberOpensDM(t *testing.T) {
	m := infoTestModel()
	m.infoMembers = []*model.User{{Id: "u_alice", Username: "alice"}}
	m.infoMembersLoaded = true
	m.infoPinnedLoaded = true
	m.renderInfo()

	if len(m.infoTargets) != 4 || m.infoTargets[1].kind != infoTargetMember {
		t.Fatalf("targets = %+v, want a purpose link + one member + the add row + the media row", m.infoTargets)
	}
	m.infoIdx = 1 // the alice member

	out, cmd := m.activateInfoTarget()
	m = out.(Model)
	if cmd == nil {
		t.Fatal("activating a member should return a DM-open Cmd")
	}
	if m.infoOpen {
		t.Error("opening a DM should close the info panel")
	}
	if m.status != "opening DM…" {
		t.Errorf("status = %q, want %q", m.status, "opening DM…")
	}
}

// TestInfoMemberHoverHighlights: hovering a member row paints it with the hover
// background; clearing the hover drops it.
func TestInfoMemberHoverHighlights(t *testing.T) {
	m := infoTestModel()
	m.infoMembers = []*model.User{{Id: "u_alice", Username: "alice"}}
	m.infoMembersLoaded = true
	m.infoPinnedLoaded = true
	m.renderInfo()

	memberIdx := -1
	for i, tg := range m.infoTargets {
		if tg.kind == infoTargetMember {
			memberIdx = i
			break
		}
	}
	if memberIdx < 0 {
		t.Fatal("expected a member target")
	}

	m.setInfoHover(memberIdx)
	if !strings.Contains(m.infoView.GetContent(), "48;5;238") {
		t.Error("hovered member row should carry the hover background")
	}
	m.setInfoHover(-1)
	if strings.Contains(m.infoView.GetContent(), "48;5;238") {
		t.Error("clearing the hover should drop the background")
	}
}

// TestInfoHoverAtClosed: the member-hover probe is inert when the panel is shut.
func TestInfoHoverAtClosed(t *testing.T) {
	m := infoTestModel()
	m.infoOpen = false
	if got := m.infoHoverAt(10, 10); got != -1 {
		t.Errorf("infoHoverAt with panel closed = %d, want -1", got)
	}
}

// TestInfoMemberSelfIsNoOp: selecting yourself doesn't open a DM.
func TestInfoMemberSelfIsNoOp(t *testing.T) {
	m := infoTestModel()
	out, cmd := m.openDMWithMember("me")
	m = out.(Model)
	if cmd != nil {
		t.Fatal("selecting yourself should not open a DM")
	}
	if !m.infoOpen {
		t.Error("a no-op member select should leave the panel open")
	}
}

// TestInfoTargetNavActivatesLink: ↓ selects the first target (the purpose
// link) and the open key opens it without a scheme warning.
func TestInfoTargetNavActivatesLink(t *testing.T) {
	m := infoTestModel()
	m.infoMembersLoaded = true
	m.infoPinnedLoaded = true
	m.renderInfo() // builds the single purpose-link target

	out, _ := m.handleInfoKey(keyPress(tea.KeyDown))
	m = out.(Model)
	if m.infoIdx != 0 {
		t.Fatalf("after ↓ infoIdx = %d, want 0", m.infoIdx)
	}

	out, cmd := m.handleInfoKey(tea.KeyPressMsg(tea.Key{Code: 'o'}))
	m = out.(Model)
	if cmd == nil {
		t.Fatal("activating a web link should return an open Cmd")
	}
	if m.linkConfirm.active {
		t.Fatal("a web link should not raise the scheme warning")
	}
}

// TestInfoJumpToPinnedSelectsLoadedPost: activating a pinned-message target
// closes the panel and selects that post in the already-loaded messages pane.
func TestInfoJumpToPinnedSelectsLoadedPost(t *testing.T) {
	m := infoTestModel()
	m.posts = []*model.Post{
		{Id: "a", ChannelId: "chan123", CreateAt: 100},
		{Id: "pin1", ChannelId: "chan123", CreateAt: 200},
		{Id: "c", ChannelId: "chan123", CreateAt: 300},
	}
	m.postIdx = 2

	out, _ := m.jumpToInfoPin("pin1")
	m = out.(Model)

	if m.infoOpen {
		t.Error("jumping to a pinned message should close the info panel")
	}
	if m.focus != focusMessages {
		t.Errorf("focus = %v, want focusMessages", m.focus)
	}
	if m.postIdx != 1 || m.posts[m.postIdx].Id != "pin1" {
		t.Errorf("selected post = %d (%q), want index 1 (pin1)", m.postIdx, m.posts[m.postIdx].Id)
	}
}

// TestOpenChannelInfoToggles: opening the panel for the channel it already
// shows closes it (a toggle), and opening from scratch raises it focused.
func TestOpenChannelInfoToggles(t *testing.T) {
	m := infoTestModel()
	m.infoOpen = false
	m.infoChannelID = ""
	m.focus = focusMessages

	out, _ := m.openChannelInfo()
	m = out.(Model)
	if !m.infoOpen || m.infoChannelID != "chan123" || m.focus != focusInfo {
		t.Fatalf("open: infoOpen=%v id=%q focus=%v", m.infoOpen, m.infoChannelID, m.focus)
	}

	out, _ = m.openChannelInfo()
	m = out.(Model)
	if m.infoOpen {
		t.Fatal("re-opening for the same channel should toggle the panel closed")
	}
	if m.focus != focusMessages {
		t.Fatalf("after close focus = %v, want focusMessages", m.focus)
	}
}

// TestOpenChannelInfoClosesThread: the panel shares the right slot, so opening
// it dismisses an open thread sidebar.
func TestOpenChannelInfoClosesThread(t *testing.T) {
	m := infoTestModel()
	m.infoOpen = false
	m.infoChannelID = ""
	m.focus = focusMessages
	m.threadOpen = true
	m.threadRootID = "root"

	out, _ := m.openChannelInfo()
	m = out.(Model)
	if m.threadOpen {
		t.Error("opening the info panel should close the thread sidebar")
	}
	if !m.infoOpen {
		t.Error("info panel should be open")
	}
}

// mediaInfoModel is infoTestModel with a loaded media listing: two images and a
// PDF between them, newest first (the order ChannelFiles returns).
func mediaInfoModel() Model {
	m := infoTestModel()
	m.infoMembersLoaded = true
	m.infoPinnedLoaded = true
	m.infoMediaLoaded = true
	m.userNames["u_alice"] = "alice"
	m.infoMedia = []*model.FileInfo{
		{Id: "f1", PostId: "p1", CreatorId: "u_alice", Name: "shot.png", MimeType: "image/png", Size: 2048, CreateAt: 300},
		{Id: "f2", PostId: "p2", CreatorId: "u_alice", Name: "spec.pdf", MimeType: "application/pdf", Size: 1024, CreateAt: 200},
		{Id: "f3", PostId: "p3", CreatorId: "u_bob", Name: "logo.gif", MimeType: "image/gif", Size: 512, CreateAt: 100},
	}
	m.renderInfo()
	return m
}

// The main view advertises the drill-down with a live count.
func TestInfoMediaRowShowsCount(t *testing.T) {
	m := mediaInfoModel()
	if !strings.Contains(m.infoView.GetContent(), "All media (3)") {
		t.Errorf("info panel has no media row\n---\n%s", m.infoView.GetContent())
	}

	// Not loaded yet → no count rather than a wrong one.
	m2 := infoTestModel()
	m2.infoMembersLoaded, m2.infoPinnedLoaded = true, true
	m2.renderInfo()
	if c := m2.infoView.GetContent(); !strings.Contains(c, "All media") || strings.Contains(c, "All media (") {
		t.Errorf("unloaded media row should carry no count\n---\n%s", c)
	}
}

// Activating the media row drills in; esc returns to the row it came from.
func TestInfoMediaDrillDownAndBack(t *testing.T) {
	m := mediaInfoModel()
	mediaIdx := len(m.infoTargets) - 1
	if m.infoTargets[mediaIdx].kind != infoTargetMedia {
		t.Fatalf("last target = %+v, want the media row", m.infoTargets[mediaIdx])
	}
	m.infoIdx = mediaIdx

	out, _ := m.activateInfoTarget()
	m = out.(Model)
	if m.infoMode != infoModeMedia {
		t.Fatal("activating the media row should enter media mode")
	}
	content := m.infoView.GetContent()
	for _, want := range []string{"Media (3)", "shot.png", "spec.pdf", "logo.gif", "alice", "2.0KB", "esc back"} {
		if !strings.Contains(content, want) {
			t.Errorf("media view missing %q\n---\n%s", want, content)
		}
	}
	if len(m.infoTargets) != 3 {
		t.Fatalf("media targets = %d, want 3", len(m.infoTargets))
	}
	if m.infoTargets[0].kind != infoTargetMediaItem || m.infoTargets[0].mediaIdx != 0 {
		t.Errorf("target[0] = %+v, want media item 0", m.infoTargets[0])
	}
	// Each row spans its name line and its meta line.
	if m.infoTargets[0].endRow != m.infoTargets[0].startRow+1 {
		t.Errorf("media row should span 2 lines, got %+v", m.infoTargets[0])
	}
	if m.infoPaneTitle(40) != "Media · #general" {
		t.Errorf("pane title = %q", m.infoPaneTitle(40))
	}

	// Drive esc through handleKey, not handleInfoKey: a global esc handler runs
	// ahead of the focus dispatch and used to close the panel outright from the
	// media view, which calling handleInfoKey directly would never catch.
	out, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m = out.(Model)
	if m.infoMode != infoModeMain {
		t.Error("esc in media mode should return to the main view")
	}
	if !m.infoOpen {
		t.Error("esc in media mode should not close the panel")
	}
	if m.infoIdx != mediaIdx {
		t.Errorf("esc should restore the media row selection, got infoIdx %d want %d", m.infoIdx, mediaIdx)
	}

	// A second esc closes the panel, as it always did.
	out, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if out.(Model).infoOpen {
		t.Error("esc in the main view should close the panel")
	}
}

// `s` saves only the selected file, not every file on its message.
func TestInfoMediaDownloadSelected(t *testing.T) {
	m := mediaInfoModel()
	m.openInfoMedia()
	m.infoIdx = 1 // spec.pdf

	out, cmd := m.handleInfoKey(tea.KeyPressMsg(tea.Key{Code: 's'}))
	m = out.(Model)
	if cmd == nil {
		t.Fatal("s on a media row should return a download Cmd")
	}
	if !strings.Contains(m.status, "spec.pdf") {
		t.Errorf("status = %q, want it to name spec.pdf", m.status)
	}
}

// `o` opens the selected file rather than raising the multi-openable picker.
func TestInfoMediaOpenSelected(t *testing.T) {
	m := mediaInfoModel()
	m.openInfoMedia()
	m.infoIdx = 0 // shot.png

	out, cmd := m.handleInfoKey(tea.KeyPressMsg(tea.Key{Code: 'o'}))
	m = out.(Model)
	if cmd == nil {
		t.Fatal("o on a media row should return an open Cmd")
	}
	if m.openPickerActive() {
		t.Error("o on a single file should not raise the open picker")
	}
	if !strings.Contains(m.status, "shot.png") {
		t.Errorf("status = %q, want it to name shot.png", m.status)
	}
}

// space previews the selected image, and the gallery it opens spans every
// previewable file in the channel — so ←/→ walk across messages.
func TestInfoMediaPreviewGallerySpansChannel(t *testing.T) {
	m := mediaInfoModel()
	m.emojiImg = &emojiImages{mode: "on", probeDone: true, probeOK: true, profileKnown: true, truecolor: true}
	m.openInfoMedia()
	m.infoIdx = 2 // logo.gif — the second previewable file

	out, _ := m.handleInfoKey(tea.KeyPressMsg(tea.Key{Code: ' ', Text: " "}))
	m = out.(Model)
	if !m.preview.active {
		t.Fatal("space on an image row should open the preview")
	}
	// The PDF is skipped; the gif is the gallery's second entry.
	if len(m.preview.items) != 2 {
		t.Fatalf("gallery = %d items, want the 2 previewable ones", len(m.preview.items))
	}
	if m.preview.items[0].name != "shot.png" || m.preview.items[1].name != "logo.gif" {
		t.Errorf("gallery = %+v", m.preview.items)
	}
	if m.preview.idx != 1 {
		t.Errorf("preview idx = %d, want 1 (logo.gif)", m.preview.idx)
	}
}

// space on a non-previewable row explains itself instead of opening a preview.
func TestInfoMediaPreviewNonImage(t *testing.T) {
	m := mediaInfoModel()
	m.openInfoMedia()
	m.infoIdx = 1 // spec.pdf

	out, cmd := m.handleInfoKey(tea.KeyPressMsg(tea.Key{Code: ' ', Text: " "}))
	m = out.(Model)
	if m.preview.active || cmd != nil {
		t.Error("space on a PDF should not open the preview")
	}
	if !strings.Contains(m.status, "no preview for") || !strings.Contains(m.status, "spec.pdf") {
		t.Errorf("status = %q", m.status)
	}
}

// In the main view, space must still page the viewport — the media keys are
// scoped to a selected media row.
func TestInfoMainSpaceStillScrolls(t *testing.T) {
	m := mediaInfoModel()
	m.infoView.SetHeight(3)
	m.renderInfo()
	before := m.infoView.YOffset()

	out, _ := m.handleInfoKey(tea.KeyPressMsg(tea.Key{Code: ' ', Text: " "}))
	m = out.(Model)
	if m.preview.active {
		t.Fatal("space in the main info view should not open a preview")
	}
	if m.infoView.YOffset() == before {
		t.Errorf("space in the main info view should scroll, offset stayed %d", before)
	}
}

// TestMediaIcon: MIME wins, but the server leaves mime_type empty on plenty of
// real uploads (videos, archives), so the extension has to carry those.
func TestMediaIcon(t *testing.T) {
	cases := []struct {
		name, mime, ext, want string
	}{
		{"a.png", "image/png", "png", "🖼"},
		{"a.jpg", "image/jpeg; charset=binary", "jpg", "🖼"},
		{"a.pdf", "application/pdf", "pdf", "📄"},
		{"a.txt", "text/plain", "txt", "📄"},
		{"a.zip", "application/zip", "zip", "📦"},
		{"a.mp3", "audio/mpeg", "mp3", "🎵"},
		// Empty mime_type — the shape 3.7% of the real cache has.
		{"clip.mov", "", "mov", "🎬"},
		{"clip.mp4", "", "mp4", "🎬"},
		{"data.csv", "", "csv", "📄"},
		{"bundle.zip", "", "zip", "📦"},
		{"song.flac", "", "flac", "🎵"},
		// No mime and no extension field: fall back to the filename.
		{"clip.mkv", "", "", "🎬"},
		{"mystery", "", "", "📎"},
		{"thing.bin", "application/octet-stream", "bin", "📎"},
	}
	for _, tc := range cases {
		f := &model.FileInfo{Name: tc.name, MimeType: tc.mime, Extension: tc.ext}
		if got := mediaIcon(f); got != tc.want {
			t.Errorf("mediaIcon(%q, mime=%q, ext=%q) = %q, want %q", tc.name, tc.mime, tc.ext, got, tc.want)
		}
	}
}

// With nothing selected, the hint names what this view actually holds.
func TestInfoMediaNoSelectionHint(t *testing.T) {
	m := mediaInfoModel()
	m.openInfoMedia() // enters media mode with infoIdx == -1
	out, cmd := m.activateInfoTarget()
	m = out.(Model)
	if cmd != nil {
		t.Error("activating nothing should not return a Cmd")
	}
	if m.status != "nothing selected — ↑/↓ to pick an attachment" {
		t.Errorf("media hint = %q", m.status)
	}

	m2 := mediaInfoModel()
	m2.infoIdx = -1
	out2, _ := m2.activateInfoTarget()
	if got := out2.(Model).status; got != "nothing selected — ↑/↓ to pick a link or pinned message" {
		t.Errorf("main hint = %q", got)
	}
}
