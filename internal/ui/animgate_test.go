package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/mattermost/mattermost/server/public/model"
)

// onTeamTab puts m on a real team tab, so the transcript panes are what the
// frame shows. Without it a bare test Model sits on the synthetic Feed tab,
// where the panes aren't drawn at all and nothing in them counts as on screen
// (see transcriptHidden).
func onTeamTab(m *Model) {
	m.teams = []*model.Team{{Id: "t1", Name: "team", DisplayName: "Team"}}
	m.channels = map[string][]*model.Channel{
		"t1": {{Id: "c1", TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: "general"}},
	}
	m.teamIdx = m.firstTeamTabIdx()
	m.openChannelID = "c1"
}

// animThumbModel is a renderable model showing one post whose image attachment
// is a ready, resident, two-frame GIF thumbnail — i.e. the animation loop has
// something to drive.
func animThumbModel(t *testing.T) *Model {
	t.Helper()
	m := New(nil, nil)
	m.width, m.height = 120, 40
	m.resizeMessagesViewport()
	m.resizeInput()
	m.cellPxW, m.cellPxH = 10, 20
	m.emojiImg.setProbeOK()
	m.emojiImg.setColorProfile(true)
	m.inlineImg.mode = "auto"
	m.animateInline = true
	onTeamTab(&m)

	p := thumbPost("file1")
	p.UserId = "u1"
	p.Message = "look at this"
	p.CreateAt = 1
	m.posts = []*model.Post{p}

	const id = 0x123456
	m.inlineImg.markReady("file1", readyInlineImg{
		id:          id,
		rows:        inlineThumbRows,
		cols:        12,
		box:         1000,
		placeholder: kittyPlaceholder(id, inlineThumbRows, 12),
		frameSeqs:   []string{"<frame0>", "<frame1>"},
		delays:      []time.Duration{40 * time.Millisecond, 40 * time.Millisecond},
	})
	m.inlineImg.entries["file1"].resident = true
	m.renderMessages()
	return &m
}

// framePlaceholders counts the Kitty placeholder cells in a rendered frame —
// how many cells of it the terminal will paint an image over.
func framePlaceholders(m *Model) int {
	m.vcache.viewValid = false
	return strings.Count(m.viewContent(), string(kitty.Placeholder))
}

// hidingStates is one representative state per full-body overlay, plus the tabs
// that own the body instead of the panes. TestHiddenTranscriptStopsAnimation
// checks the count against bodyOverlays, so a new overlay has to be listed here.
var hidingStates = []struct {
	name  string
	apply func(*Model)
}{
	{"switcher", func(m *Model) { m.switcherMode = true }},
	{"gorillas", func(m *Model) { m.gorillas.active = true }},
	{"kurve", func(m *Model) { m.kurve.active = true }},
	{"history", func(m *Model) { m.historyMode = true }},
	{"keys-sheet", func(m *Model) { m.keysSheetMode = true }},
	{"key-debug", func(m *Model) { m.keyDebugMode = true }},
	{"delete-confirm", func(m *Model) { m.deleteConfirmPostID = "post1" }},
	{"reaction-picker", func(m *Model) { m.reactionPickerPostID = "post1" }},
	{"jira-picker", func(m *Model) { m.jiraPicker.active = true }},
	{"jira-points", func(m *Model) { m.jiraPointsActive = true }},
	{"jira-comment", func(m *Model) { m.jiraCommentActive = true }},
	{"gitlab-confirm", func(m *Model) { m.glConfirm.active = true }},
	{"link-confirm", func(m *Model) { m.linkConfirm.active = true }},
	{"open-picker", func(m *Model) { m.openPickerItems = make([]openable, 1) }},
	{"code-picker", func(m *Model) { m.codePickerBlocks = make([]codeBlock, 1) }},
	{"poll-dialog", func(m *Model) { m.pollDialog.open = true }},
	{"create-channel", func(m *Model) { m.openCreateChannel() }},
	{"edit-channel", func(m *Model) { m.openEditChannel("c1", 0) }},
	{"join-channel", func(m *Model) { m.openJoinChannel() }},
	{"channel-confirm", func(m *Model) { m.openChannelConfirm(chanConfirmLeave, "c1") }},
	{"summary", func(m *Model) { m.summary.phase = summaryPicking }},
	{"image-preview", func(m *Model) { m.preview.active = true }},
	{"feed-tab", func(m *Model) { gotoTab(m, tabFeed) }},
	{"search-tab", func(m *Model) { gotoTab(m, tabSearch) }},
	{"sql-tab", func(m *Model) { m.showSQL = true; gotoTab(m, tabSQL) }},
}

// gotoTab moves the active tab to the first one of the given kind.
func gotoTab(m *Model, want tabKind) {
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if kind, _, _ := m.tabAt(i); kind == want {
			m.teamIdx = i
			return
		}
	}
	panic("no tab of that kind")
}

// TestHiddenTranscriptStopsAnimation: while a popup (or another tab) owns the
// body, the transcript's image placeholders are not in the frame, so the
// animation loop must not keep re-transmitting GIF frames for them — the
// terminal has nowhere to paint them, and the loop would run for as long as the
// popup stayed up.
//
// It asserts the two halves agree: no placeholder cells in the frame ⟺
// transcriptHidden(), the gate every animation decision hangs off.
func TestHiddenTranscriptStopsAnimation(t *testing.T) {
	// Every full-body overlay needs a state here, or a new popup would silently
	// keep animating whatever it covers. +3 for the Feed/Search/SQL tabs.
	if len(hidingStates) != len(bodyOverlays)+3 {
		t.Fatalf("%d hiding states for %d overlays + 3 tabs: add the new one to hidingStates",
			len(hidingStates), len(bodyOverlays))
	}

	// Baseline: the transcript is on screen, the GIF animates.
	base := animThumbModel(t)
	if base.transcriptHidden() {
		t.Fatal("the plain conversation view counts as hidden")
	}
	if framePlaceholders(base) == 0 {
		t.Fatal("no image placeholders in the frame: the fixture never displayed its thumbnail")
	}
	if !base.refreshAnimVisibility() {
		t.Fatal("an on-screen GIF thumbnail is not animating")
	}

	for _, st := range hidingStates {
		t.Run(st.name, func(t *testing.T) {
			m := animThumbModel(t)
			st.apply(m)

			if !m.transcriptHidden() {
				t.Errorf("transcriptHidden() = false while %s is up", st.name)
			}
			if n := framePlaceholders(m); n != 0 {
				t.Errorf("%d image placeholder cells survived into the frame behind %s", n, st.name)
			}
			if m.refreshAnimVisibility() {
				t.Errorf("the hidden thumbnail is still marked visible behind %s", st.name)
			}
			// The running loop must stop rather than keep feeding the terminal
			// frames for an image it cannot show.
			m.imgAnimating = true
			time.Sleep(45 * time.Millisecond)
			if cmd := m.advanceImageAnim(); cmd != nil || m.imgAnimating {
				t.Errorf("the animation loop kept running behind %s", st.name)
			}
		})
	}
}

// TestClosingOverlayResumesAnimation: the gate is only about what's in the
// frame, so putting the panes back has to re-arm the loop — the per-event
// kicker is what does it, and it runs after every message.
func TestClosingOverlayResumesAnimation(t *testing.T) {
	m := animThumbModel(t)
	m.reactionPickerPostID = "post1"
	m.refreshAnimVisibility()
	if cmd := m.maybeStartImageAnim(); cmd != nil {
		t.Fatal("the kicker armed the loop while the picker covered the transcript")
	}

	m.closeReactionPicker()
	if cmd := m.maybeStartImageAnim(); cmd == nil {
		t.Error("closing the picker left the thumbnail frozen: the loop was never re-armed")
	}
	if !m.imgAnimating {
		t.Error("imgAnimating stayed false after the transcript came back")
	}
}
