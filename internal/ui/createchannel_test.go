package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// createChanTestModel is a Model with two teams, the second one focused, and no
// channels — the starting point for opening the create-channel form.
func createChanTestModel() Model {
	return Model{
		keys:   newKeyMap("ctrl"),
		width:  100,
		height: 44,
		teams: []*model.Team{
			{Id: "t1", Name: "eng", DisplayName: "Engineering"},
			{Id: "t2", Name: "ops", DisplayName: "Operations"},
		},
		channels: map[string][]*model.Channel{},
		me:       &model.User{Id: "me", Username: "me"},
	}
}

// typeInto feeds a string to the focused row one key at a time, the way the
// real event loop would.
func typeInto(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		out, _ := m.handleCreateChannelKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		*m = out.(Model)
	}
}

// press sends one named key (esc, tab, space, …) to the modal.
func press(t *testing.T, m *Model, name string) {
	t.Helper()
	var msg tea.KeyPressMsg
	switch name {
	case "tab":
		msg = tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		msg = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "space":
		msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "left":
		msg = tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		msg = tea.KeyPressMsg{Code: tea.KeyRight}
	case "backspace":
		msg = tea.KeyPressMsg{Code: tea.KeyBackspace}
	default:
		t.Fatalf("press: unknown key %q", name)
	}
	out, _ := m.handleCreateChannelKey(msg)
	*m = out.(Model)
}

// TestSlugifyChannelName: display names become valid channel identifiers —
// lowercased, separator runs collapsed, no leading/trailing dash, junk dropped.
func TestSlugifyChannelName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Marketing", "marketing"},
		{"Q3 Planning", "q3-planning"},
		{"  Trim  Me  ", "trim-me"},
		{"Already-Slugged", "already-slugged"},
		{"snake_case_name", "snake-case-name"},
		{"Lots---of___seps", "lots-of-seps"},
		{"-leading and trailing-", "leading-and-trailing"},
		{"Ünïcôdé Náme", "ncd-nme"}, // non-ASCII is dropped, not transliterated
		{"C++ / Rust!", "c-rust"},
		{"2026 roadmap", "2026-roadmap"},
		{"", ""},
		{"🎉", ""},
	}
	for _, c := range cases {
		got := slugifyChannelName(c.in)
		if got != c.want {
			t.Errorf("slugifyChannelName(%q) = %q, want %q", c.in, got, c.want)
		}
		if got != "" && !model.IsValidChannelIdentifier(got) {
			t.Errorf("slugifyChannelName(%q) = %q, which the server would reject", c.in, got)
		}
	}
}

// TestSlugifyChannelNameTruncates: an over-long display name is cut to the
// server's limit without leaving a trailing dash behind.
func TestSlugifyChannelNameTruncates(t *testing.T) {
	got := slugifyChannelName(strings.Repeat("ab ", 40))
	if len(got) > model.ChannelNameMaxLength {
		t.Fatalf("slug length = %d, want <= %d", len(got), model.ChannelNameMaxLength)
	}
	if !model.IsValidChannelIdentifier(got) {
		t.Fatalf("truncated slug %q is not a valid channel identifier", got)
	}
}

// TestOpenCreateChannelDefaults: the form opens on the focused team, public,
// with the cursor in the display-name field.
func TestOpenCreateChannelDefaults(t *testing.T) {
	m := createChanTestModel()
	m.teamIdx = m.teamIdxForTest("t2")
	m.openCreateChannel()

	st := m.createChan
	if st == nil {
		t.Fatal("openCreateChannel left the modal closed")
	}
	if got := st.teams[st.teamIdx].Id; got != "t2" {
		t.Errorf("preselected team = %q, want the focused tab's team %q", got, "t2")
	}
	if st.typ != model.ChannelTypeOpen {
		t.Errorf("default type = %q, want public", st.typ)
	}
	if st.row != ccDisplayName {
		t.Errorf("focused row = %d, want ccDisplayName (%d)", st.row, ccDisplayName)
	}
}

// teamIdxForTest maps a team id to its tab index (teams sit after the virtual
// Feed/Search tabs).
func (m *Model) teamIdxForTest(teamID string) int {
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if _, id, _ := m.tabAt(i); id == teamID {
			return i
		}
	}
	return 0
}

// TestOpenCreateChannelNoTeams: with no team to create in, the command reports
// rather than opening an unusable form.
func TestOpenCreateChannelNoTeams(t *testing.T) {
	m := createChanTestModel()
	m.teams = nil
	if cmd := m.openCreateChannel(); cmd != nil {
		t.Error("openCreateChannel returned a Cmd with no teams; want none")
	}
	if m.createChan != nil {
		t.Error("the form opened with no teams to create in")
	}
	if !strings.Contains(m.status, "any team") {
		t.Errorf("status = %q, want the no-team hint", m.status)
	}
}

// TestCreateChannelURLTracksDisplayName: the slug follows the display name
// until the user edits it, then it's theirs.
func TestCreateChannelURLTracksDisplayName(t *testing.T) {
	m := createChanTestModel()
	m.openCreateChannel()
	typeInto(t, &m, "Q3 Planning")

	if got := m.createChan.inputs[ccURL].Value(); got != "q3-planning" {
		t.Fatalf("URL = %q, want the slugged display name %q", got, "q3-planning")
	}

	// Move to the URL row and edit it: the link breaks.
	press(t, &m, "tab")
	if m.createChan.row != ccURL {
		t.Fatalf("focused row = %d, want ccURL (%d)", m.createChan.row, ccURL)
	}
	typeInto(t, &m, "x")
	if !m.createChan.urlEdited {
		t.Fatal("typing in the URL row didn't detach it from the display name")
	}

	// Back to the display name: the URL must not be overwritten now.
	press(t, &m, "shift+tab")
	typeInto(t, &m, "!")
	if got := m.createChan.inputs[ccURL].Value(); got != "q3-planningx" {
		t.Errorf("URL = %q, want the user's edit %q preserved", got, "q3-planningx")
	}
}

// TestCreateChannelURLArrowsDontDetach: moving the cursor inside the URL row
// isn't an edit — the slug keeps tracking the display name.
func TestCreateChannelURLArrowsDontDetach(t *testing.T) {
	m := createChanTestModel()
	m.openCreateChannel()
	typeInto(t, &m, "Ops")
	press(t, &m, "tab")
	press(t, &m, "left")
	press(t, &m, "right")
	if m.createChan.urlEdited {
		t.Fatal("arrow keys in the URL row counted as an edit")
	}
	press(t, &m, "shift+tab")
	typeInto(t, &m, " Deck")
	if got := m.createChan.inputs[ccURL].Value(); got != "ops-deck" {
		t.Errorf("URL = %q, want it still tracking the display name (%q)", got, "ops-deck")
	}
}

// TestCreateChannelTypeToggle: the type row flips between public and private.
func TestCreateChannelTypeToggle(t *testing.T) {
	m := createChanTestModel()
	m.openCreateChannel()
	m.createChan.row = ccType

	press(t, &m, "right")
	if m.createChan.typ != model.ChannelTypePrivate {
		t.Fatalf("type = %q after toggle, want private", m.createChan.typ)
	}
	press(t, &m, "space")
	if m.createChan.typ != model.ChannelTypeOpen {
		t.Fatalf("type = %q after second toggle, want public", m.createChan.typ)
	}
}

// TestCreateChannelTeamSelector: ←/→ cycle the team, wrapping at both ends.
func TestCreateChannelTeamSelector(t *testing.T) {
	m := createChanTestModel()
	m.openCreateChannel()
	m.createChan.teamIdx = 0
	m.createChan.row = ccTeam

	press(t, &m, "right")
	if got := m.createChan.teams[m.createChan.teamIdx].Id; got != "t2" {
		t.Errorf("team after → = %q, want t2", got)
	}
	press(t, &m, "right") // wraps
	if got := m.createChan.teams[m.createChan.teamIdx].Id; got != "t1" {
		t.Errorf("team after wrap = %q, want t1", got)
	}
	press(t, &m, "left") // wraps the other way
	if got := m.createChan.teams[m.createChan.teamIdx].Id; got != "t2" {
		t.Errorf("team after ← = %q, want t2", got)
	}
}

// TestCreateChannelSpaceTypesInTextRows: space is a toggle on checkbox rows and
// a literal space inside a text field.
func TestCreateChannelSpaceTypesInTextRows(t *testing.T) {
	m := createChanTestModel()
	m.openCreateChannel()
	typeInto(t, &m, "a")
	press(t, &m, "space")
	typeInto(t, &m, "b")
	if got := m.createChan.inputs[ccDisplayName].Value(); got != "a b" {
		t.Errorf("display name = %q, want the space typed through (%q)", got, "a b")
	}

	m.createChan.row = ccAutoTranslation
	press(t, &m, "space")
	if !m.createChan.autoTranslation {
		t.Error("space on the auto-translate row didn't toggle it")
	}
}

// TestCreateChannelBannerRowsGatedOnToggle: the banner text/colour rows stay
// visible but read as inactive until the banner is switched on.
func TestCreateChannelBannerRowsGatedOnToggle(t *testing.T) {
	m := createChanTestModel()
	m.openCreateChannel()
	if m.ccRowEnabled(ccBannerText) || m.ccRowEnabled(ccBannerColor) {
		t.Error("banner text/colour read as active with the banner off")
	}
	m.createChan.row = ccBannerEnabled
	press(t, &m, "space")
	if !m.ccRowEnabled(ccBannerText) || !m.ccRowEnabled(ccBannerColor) {
		t.Error("banner text/colour still inactive after enabling the banner")
	}
}

// TestCreateChannelValidation: each client-checkable rule reports on the row
// that's wrong, and never builds a channel.
func TestCreateChannelValidation(t *testing.T) {
	// Fills a form that's valid except for whatever the case mutates.
	form := func(mutate func(*createChannelState)) *createChannelState {
		m := createChanTestModel()
		m.openCreateChannel()
		st := m.createChan
		st.inputs[ccDisplayName].SetValue("Marketing")
		st.inputs[ccURL].SetValue("marketing")
		mutate(st)
		return st
	}
	cases := []struct {
		name    string
		mutate  func(*createChannelState)
		wantRow int
		wantMsg string
	}{
		{"no display name", func(st *createChannelState) {
			st.inputs[ccDisplayName].SetValue("  ")
		}, ccDisplayName, "display name is required"},
		{"display name too long", func(st *createChannelState) {
			st.inputs[ccDisplayName].CharLimit = 0
			st.inputs[ccDisplayName].SetValue(strings.Repeat("x", model.ChannelDisplayNameMaxRunes+1))
		}, ccDisplayName, "display name is too long"},
		{"bad slug", func(st *createChannelState) {
			st.inputs[ccURL].SetValue("Not A Slug")
		}, ccURL, "URL must be"},
		{"banner without text", func(st *createChannelState) {
			st.bannerEnabled = true
			st.inputs[ccBannerColor].SetValue("#fff")
		}, ccBannerText, "banner text is required"},
		{"banner without colour", func(st *createChannelState) {
			st.bannerEnabled = true
			st.inputs[ccBannerText].SetValue("heads up")
		}, ccBannerColor, "hex value"},
		{"banner with bad colour", func(st *createChannelState) {
			st.bannerEnabled = true
			st.inputs[ccBannerText].SetValue("heads up")
			st.inputs[ccBannerColor].SetValue("blue")
		}, ccBannerColor, "hex value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch, row, msg := form(c.mutate).channel()
			if ch != nil {
				t.Fatalf("built a channel for an invalid form: %+v", ch)
			}
			if row != c.wantRow {
				t.Errorf("row = %d, want %d", row, c.wantRow)
			}
			if !strings.Contains(msg, c.wantMsg) {
				t.Errorf("errMsg = %q, want it to mention %q", msg, c.wantMsg)
			}
		})
	}
}

// TestCreateChannelEmptyURLFallsBackToSlug: clearing the URL row doesn't block
// the create — the display name still slugs.
func TestCreateChannelEmptyURLFallsBackToSlug(t *testing.T) {
	m := createChanTestModel()
	m.openCreateChannel()
	m.createChan.inputs[ccDisplayName].SetValue("Release Train")
	m.createChan.inputs[ccURL].SetValue("")

	ch, _, msg := m.createChan.channel()
	if msg != "" {
		t.Fatalf("errMsg = %q, want the URL derived from the display name", msg)
	}
	if ch.Name != "release-train" {
		t.Errorf("Name = %q, want %q", ch.Name, "release-train")
	}
}

// TestCreateChannelBuildsFullChannel: every form field lands on the record, and
// the optional pointers stay nil unless their toggle is on.
func TestCreateChannelBuildsFullChannel(t *testing.T) {
	m := createChanTestModel()
	m.openCreateChannel()
	st := m.createChan
	st.teamIdx = 1 // Operations
	st.typ = model.ChannelTypePrivate
	st.inputs[ccDisplayName].SetValue("  Incident Room  ")
	st.inputs[ccURL].SetValue("incident-room")
	st.inputs[ccPurpose].SetValue(" war room ")
	st.inputs[ccHeader].SetValue(" ping @ops ")
	st.groupConstrained = true
	st.autoTranslation = true
	st.bannerEnabled = true
	st.inputs[ccBannerText].SetValue(" sev1 in progress ")
	st.inputs[ccBannerColor].SetValue("#4578FF")

	ch, _, msg := st.channel()
	if msg != "" {
		t.Fatalf("errMsg = %q, want a valid form", msg)
	}
	if ch.TeamId != "t2" || ch.Type != model.ChannelTypePrivate {
		t.Errorf("team/type = %q/%q, want t2/P", ch.TeamId, ch.Type)
	}
	if ch.DisplayName != "Incident Room" || ch.Name != "incident-room" {
		t.Errorf("display/name = %q/%q, want them trimmed", ch.DisplayName, ch.Name)
	}
	if ch.Purpose != "war room" || ch.Header != "ping @ops" {
		t.Errorf("purpose/header = %q/%q, want them trimmed", ch.Purpose, ch.Header)
	}
	if !ch.AutoTranslation {
		t.Error("AutoTranslation not carried onto the channel")
	}
	if ch.GroupConstrained == nil || !*ch.GroupConstrained {
		t.Error("GroupConstrained not carried onto the channel")
	}
	if ch.BannerInfo == nil || ch.BannerInfo.Enabled == nil || !*ch.BannerInfo.Enabled {
		t.Fatal("BannerInfo not carried onto the channel")
	}
	if got := *ch.BannerInfo.Text; got != "sev1 in progress" {
		t.Errorf("banner text = %q, want it trimmed", got)
	}
	if got := *ch.BannerInfo.BackgroundColor; got != "#4578FF" {
		t.Errorf("banner colour = %q, want %q", got, "#4578FF")
	}
}

// TestCreateChannelOmitsUnsetOptionals: the fields older servers don't know
// about are absent from the payload unless the user asked for them.
func TestCreateChannelOmitsUnsetOptionals(t *testing.T) {
	m := createChanTestModel()
	m.openCreateChannel()
	m.createChan.inputs[ccDisplayName].SetValue("Plain")

	ch, _, msg := m.createChan.channel()
	if msg != "" {
		t.Fatalf("errMsg = %q, want a valid form", msg)
	}
	if ch.GroupConstrained != nil {
		t.Error("GroupConstrained set on a form that never toggled it")
	}
	if ch.BannerInfo != nil {
		t.Error("BannerInfo set on a form that never enabled the banner")
	}
	if ch.AutoTranslation {
		t.Error("AutoTranslation set on a form that never toggled it")
	}
}

// TestCreateChannelEscCloses: esc tears the modal down without creating.
func TestCreateChannelEscCloses(t *testing.T) {
	m := createChanTestModel()
	m.openCreateChannel()
	press(t, &m, "esc")
	if m.createChan != nil {
		t.Error("esc left the create-channel modal open")
	}
}

// TestCreateChannelIsModal: the form counts as a body overlay, so the real
// terminal cursor stays out of the composer beneath it (the textinput draws its
// own caret) and the pass-through globals stand down.
func TestCreateChannelIsModal(t *testing.T) {
	m := createChanTestModel()
	m.focus = focusInput
	if m.inModal() {
		t.Fatal("inModal() is true with no modal open")
	}
	m.openCreateChannel()
	if !m.inModal() {
		t.Error("inModal() = false with the create-channel form open")
	}
	if !m.bodyOverlayActive() {
		t.Error("bodyOverlayActive() = false; the composer would keep the terminal cursor")
	}
	if _, _, ok := m.editorCursor(); ok {
		t.Error("editorCursor() placed a cursor beneath the modal")
	}
}

// TestApplyChannelCreated: the new channel is spliced into its team's bucket in
// sorted position, the modal closes, and the channel opens.
func TestApplyChannelCreated(t *testing.T) {
	m := createChanTestModel()
	m.channels["t1"] = []*model.Channel{
		{Id: "c_alpha", TeamId: "t1", Type: model.ChannelTypeOpen, Name: "alpha", DisplayName: "Alpha"},
		{Id: "c_zulu", TeamId: "t1", Type: model.ChannelTypeOpen, Name: "zulu", DisplayName: "Zulu"},
	}
	m.openCreateChannel()

	created := &model.Channel{Id: "c_mike", TeamId: "t1", Type: model.ChannelTypeOpen, Name: "mike", DisplayName: "Mike"}
	out, cmd := m.applyChannelCreated(channelCreatedMsg{ch: created})
	m = out.(Model)

	if m.createChan != nil {
		t.Error("the modal stayed open after a successful create")
	}
	if cmd == nil {
		t.Error("no Cmd; want the new channel loaded")
	}
	var names []string
	for _, c := range m.channels["t1"] {
		names = append(names, c.Name)
	}
	want := []string{"alpha", "mike", "zulu"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("sidebar bucket = %v, want the new channel in sorted position %v", names, want)
	}
	if m.channels["t1"][m.channelIdx].Id != "c_mike" {
		t.Errorf("cursor on %q, want the freshly-created channel", m.channels["t1"][m.channelIdx].Id)
	}
	if m.focus != focusInput {
		t.Errorf("focus = %v, want the composer", m.focus)
	}
	if !strings.Contains(m.status, "Mike") {
		t.Errorf("status = %q, want it to name the new channel", m.status)
	}
}

// TestApplyChannelCreatedError: a rejected create keeps the form open with the
// server's reason folded onto the error row, so the user can fix and retry.
func TestApplyChannelCreatedError(t *testing.T) {
	m := createChanTestModel()
	m.openCreateChannel()
	m.createChan.submitting = true

	out, cmd := m.applyChannelCreated(channelCreatedMsg{err: errors.New("A channel with that URL already exists\non this team")})
	m = out.(Model)

	if m.createChan == nil {
		t.Fatal("the modal closed on a failed create; want it kept open to retry")
	}
	if m.createChan.submitting {
		t.Error("still submitting after the error came back")
	}
	if !strings.Contains(m.createChan.errMsg, "already exists") {
		t.Errorf("errMsg = %q, want the server's reason", m.createChan.errMsg)
	}
	if strings.Contains(m.createChan.errMsg, "\n") {
		t.Errorf("errMsg = %q, want it folded onto one line", m.createChan.errMsg)
	}
	if cmd != nil {
		t.Error("a failed create returned a Cmd; want none")
	}
}

// TestApplyChannelCreatedErrorAfterClose: the result of a create whose modal was
// dismissed mid-flight falls back to the status bar instead of panicking.
func TestApplyChannelCreatedErrorAfterClose(t *testing.T) {
	m := createChanTestModel()
	out, _ := m.applyChannelCreated(channelCreatedMsg{err: errors.New("boom")})
	m = out.(Model)
	if !strings.HasPrefix(m.status, "create channel: ") {
		t.Errorf("status = %q, want the create-channel error prefix", m.status)
	}
}

// TestCreateChannelInPalette: the > palette offers the command, and it opens the
// modal rather than a captive arg prompt.
func TestCreateChannelInPalette(t *testing.T) {
	m := createChanTestModel()
	var found *switcherCommand
	for i, c := range m.allCommands() {
		if c.name == "Create channel" {
			found = &m.allCommands()[i]
		}
	}
	if found == nil {
		t.Fatal("allCommands() has no \"Create channel\" entry")
	}
	if found.argPrompt != "" {
		t.Errorf("argPrompt = %q, want none — the command opens its own form", found.argPrompt)
	}
	found.run(&m, "")
	if m.createChan == nil {
		t.Error("running the palette command didn't open the form")
	}
}

// TestRenderCreateChannelShowsEveryOption: the modal surfaces all the create-API
// fields, not just the required pair.
func TestRenderCreateChannelShowsEveryOption(t *testing.T) {
	m := createChanTestModel()
	m.openCreateChannel()
	out := m.renderCreateChannel()
	for _, want := range []string{
		"Create a channel", "Team", "Type", "Public", "Display name",
		"URL", "Purpose", "Header", "Group sync", "Auto-translate",
		"Banner", "Banner text", "Banner colour", "Engineering",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered form is missing %q\n---\n%s", want, out)
		}
	}
}

// TestRenderCreateChannelClosed: a nil state renders nothing rather than
// panicking on the View path.
func TestRenderCreateChannelClosed(t *testing.T) {
	m := createChanTestModel()
	if got := m.renderCreateChannel(); got != "" {
		t.Errorf("renderCreateChannel() with the modal closed = %q, want empty", got)
	}
}

// TestRenderCreateChannelFitsTerminal: no row — long checkbox note, long team
// name, long banner text — may push the modal past the terminal's width.
func TestRenderCreateChannelFitsTerminal(t *testing.T) {
	for _, w := range []int{48, 60, 80, 120} {
		m := createChanTestModel()
		m.width = w
		m.teams = []*model.Team{{Id: "t1", Name: "eng", DisplayName: strings.Repeat("Long Team ", 8)}}
		m.openCreateChannel()
		m.createChan.bannerEnabled = true
		m.createChan.inputs[ccBannerText].SetValue(strings.Repeat("banner ", 20))
		m.createChan.errMsg = strings.Repeat("very long server error ", 6)

		out := m.renderCreateChannel()
		outer, _, _ := ccDims(w)
		for i, line := range strings.Split(out, "\n") {
			if got := lipgloss.Width(line); got > outer {
				t.Errorf("width=%d: line %d is %d cols wide, want <= %d\n%s", w, i, got, outer, line)
				break
			}
		}
		if outer > w {
			t.Errorf("width=%d: modal outer width %d overflows the terminal", w, outer)
		}
	}
}

// TestRenderCreateChannelResizes: inputs opened at one width are re-sized by the
// next render after the terminal changes, so a resize can't leave them stale.
func TestRenderCreateChannelResizes(t *testing.T) {
	m := createChanTestModel()
	m.width = 120
	m.openCreateChannel()
	wide := m.createChan.inputs[ccDisplayName].Width()

	m.width = 50
	m.renderCreateChannel()
	narrow := m.createChan.inputs[ccDisplayName].Width()

	if narrow >= wide {
		t.Errorf("input width %d after shrinking the terminal, want < %d", narrow, wide)
	}
	if want := ccValueWidth(50); narrow != want {
		t.Errorf("input width = %d, want the new value column %d", narrow, want)
	}
}
