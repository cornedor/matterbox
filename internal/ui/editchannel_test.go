package ui

import (
	"errors"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/hidden"
)

// editChanTestModel is a Model with one team holding an open channel with every
// editable field filled in, and that channel open.
func editChanTestModel() Model {
	m := Model{
		keys:   newKeyMap("ctrl"),
		width:  100,
		height: 44,
		teams:  []*model.Team{{Id: "t1", Name: "eng", DisplayName: "Engineering"}},
		channels: map[string][]*model.Channel{
			"t1": {{
				Id: "c1", TeamId: "t1", Type: model.ChannelTypeOpen,
				Name: "general", DisplayName: "General",
				Purpose: "everything else", Header: "read the docs",
			}},
		},
		me:            &model.User{Id: "me", Username: "me"},
		openChannelID: "c1",
		unread:        map[string]int{},
		mentions:      map[string]int{},
	}
	return m
}

// pressEdit sends one named key to the edit-channel modal.
func pressEdit(t *testing.T, m *Model, name string) {
	t.Helper()
	out, _ := m.handleEditChannelKey(keyMsg(t, name))
	*m = out.(Model)
}

// typeIntoEdit feeds a string to the modal's focused row, key by key.
func typeIntoEdit(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		pressEdit(t, m, string(r))
	}
}

// TestEditChannelCommandsInPalette: the three edit entries are offered for an
// open channel, each opening the form on its own row.
func TestEditChannelCommandsInPalette(t *testing.T) {
	m := editChanTestModel()
	cmds, ok := m.editChannelCommands()
	if !ok || len(cmds) != 3 {
		t.Fatalf("editChannelCommands() = %d entries, ok=%v; want 3, true", len(cmds), ok)
	}
	wantRows := map[string]int{
		"Rename #General":          ceDisplayName,
		"Edit purpose of #General": cePurpose,
		"Edit header of #General":  ceHeader,
	}
	for _, c := range cmds {
		row, named := wantRows[c.name]
		if !named {
			t.Fatalf("unexpected palette entry %q", c.name)
		}
		if c.argPrompt != "" {
			t.Errorf("%q: argPrompt = %q, want none — it opens the form", c.name, c.argPrompt)
		}
		mm := editChanTestModel()
		c.run(&mm, "")
		if mm.chanEdit == nil {
			t.Fatalf("%q didn't open the edit form", c.name)
		}
		if mm.chanEdit.row != row {
			t.Errorf("%q opened on row %d, want %d", c.name, mm.chanEdit.row, row)
		}
	}
}

// TestEditChannelCommandsGated: there's nothing to edit without an open channel,
// and DMs have no name/purpose/header of their own.
func TestEditChannelCommandsGated(t *testing.T) {
	none := Model{}
	if _, ok := none.editChannelCommands(); ok {
		t.Error("edit commands offered with no open channel")
	}

	dm := editChanTestModel()
	dm.channels["t1"][0].Type = model.ChannelTypeDirect
	if _, ok := dm.editChannelCommands(); ok {
		t.Error("edit commands offered for a DM")
	}
}

// TestOpenEditChannelPrefills: the form opens holding the channel's current
// values, so a header edit doesn't wipe the name.
func TestOpenEditChannelPrefills(t *testing.T) {
	m := editChanTestModel()
	m.openEditChannel("c1", ceHeader)

	st := m.chanEdit
	if st == nil {
		t.Fatal("openEditChannel left the modal closed")
	}
	want := map[int]string{
		ceDisplayName: "General",
		ceURL:         "general",
		cePurpose:     "everything else",
		ceHeader:      "read the docs",
	}
	for row, val := range want {
		if got := st.inputs[row].Value(); got != val {
			t.Errorf("row %d prefilled with %q, want %q", row, got, val)
		}
	}
	if st.row != ceHeader {
		t.Errorf("focused row = %d, want ceHeader (%d)", st.row, ceHeader)
	}
}

// TestEditChannelPatchesOnlyChangedFields: editing the purpose sends the purpose
// — not a rename the user may not be allowed to make.
func TestEditChannelPatchesOnlyChangedFields(t *testing.T) {
	m := editChanTestModel()
	m.openEditChannel("c1", cePurpose)
	m.chanEdit.inputs[cePurpose].SetValue("  planning  ")

	patch, _, msg := m.chanEdit.patch()
	if msg != "" {
		t.Fatalf("errMsg = %q, want a valid form", msg)
	}
	if patch == nil {
		t.Fatal("patch = nil, want the changed purpose")
	}
	if patch.Purpose == nil || *patch.Purpose != "planning" {
		t.Errorf("Purpose = %v, want the trimmed edit", patch.Purpose)
	}
	if patch.DisplayName != nil || patch.Name != nil || patch.Header != nil {
		t.Errorf("patch carries untouched fields: %+v", patch)
	}
}

// TestEditChannelHeaderEffects: effect markup in the header compiles to the
// wire form on save (clean visible text + invisible payload, so other clients
// see plain text), and the form re-opens showing the markup that produced it —
// the same round-trip a message edit makes.
func TestEditChannelHeaderEffects(t *testing.T) {
	m := editChanTestModel()
	m.channels["t1"][0].Header = compileEffects("\\rainbow{welcome}")

	m.openEditChannel("c1", ceHeader)
	if got := m.chanEdit.inputs[ceHeader].Value(); got != "\\rainbow{welcome}" {
		t.Fatalf("prefill = %q, want the decompiled markup", got)
	}

	// Untouched form: the recompiled markup matches the stored wire form byte
	// for byte, so no patch is built.
	patch, _, msg := m.chanEdit.patch()
	if msg != "" || patch != nil {
		t.Fatalf("untouched form built patch %+v (err %q); want none", patch, msg)
	}

	// An edited header goes to the wire compiled.
	m.chanEdit.inputs[ceHeader].SetValue("\\shimmer{ship day}")
	patch, _, msg = m.chanEdit.patch()
	if msg != "" || patch == nil || patch.Header == nil {
		t.Fatalf("patch = %+v (err %q), want a header patch", patch, msg)
	}
	if *patch.Header != compileEffects("\\shimmer{ship day}") {
		t.Errorf("Header = %q, want the compiled wire form", *patch.Header)
	}
	if hidden.Strip(*patch.Header) != "ship day" {
		t.Errorf("visible header = %q, want the markup gone", hidden.Strip(*patch.Header))
	}
}

// TestEditChannelNameEffects: the display name makes the same effects
// round-trip the header does — markup shown in the form, compiled wire form in
// the patch, untouched form sends nothing.
func TestEditChannelNameEffects(t *testing.T) {
	m := editChanTestModel()
	m.channels["t1"][0].DisplayName = compileEffects("\\rainbow{General}")

	m.openEditChannel("c1", ceDisplayName)
	if got := m.chanEdit.inputs[ceDisplayName].Value(); got != "\\rainbow{General}" {
		t.Fatalf("prefill = %q, want the decompiled markup", got)
	}

	patch, _, msg := m.chanEdit.patch()
	if msg != "" || patch != nil {
		t.Fatalf("untouched form built patch %+v (err %q); want none", patch, msg)
	}

	m.chanEdit.inputs[ceDisplayName].SetValue("\\ok{Shipping}")
	patch, _, msg = m.chanEdit.patch()
	if msg != "" || patch == nil || patch.DisplayName == nil {
		t.Fatalf("patch = %+v (err %q), want a display-name patch", patch, msg)
	}
	if *patch.DisplayName != compileEffects("\\ok{Shipping}") {
		t.Errorf("DisplayName = %q, want the compiled wire form", *patch.DisplayName)
	}
	if hidden.Strip(*patch.DisplayName) != "Shipping" {
		t.Errorf("visible name = %q, want the markup gone", hidden.Strip(*patch.DisplayName))
	}
}

// TestEditChannelNoChangesCloses: submitting an untouched form is a no-op, not
// an empty PATCH.
func TestEditChannelNoChangesCloses(t *testing.T) {
	m := editChanTestModel()
	m.openEditChannel("c1", ceDisplayName)

	out, cmd := m.submitEditChannel()
	m = out.(Model)
	if m.chanEdit != nil {
		t.Error("the modal stayed open after an unchanged submit")
	}
	if cmd != nil {
		t.Error("an unchanged submit fired a request; want none")
	}
	if m.status != "no changes" {
		t.Errorf("status = %q, want \"no changes\"", m.status)
	}
}

// TestEditChannelValidation: each client-checkable rule reports on the offending
// row, and never builds a patch.
func TestEditChannelValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*channelEditState)
		wantRow int
		wantMsg string
	}{
		{"no display name", func(st *channelEditState) {
			st.inputs[ceDisplayName].SetValue("   ")
		}, ceDisplayName, "display name is required"},
		{"bad slug", func(st *channelEditState) {
			st.inputs[ceURL].SetValue("Not A Slug")
		}, ceURL, "URL must be"},
		{"purpose too long", func(st *channelEditState) {
			st.inputs[cePurpose].CharLimit = 0
			st.inputs[cePurpose].SetValue(strings.Repeat("x", model.ChannelPurposeMaxRunes+1))
		}, cePurpose, "purpose is too long"},
		{"header too long", func(st *channelEditState) {
			st.inputs[ceHeader].CharLimit = 0
			st.inputs[ceHeader].SetValue(strings.Repeat("x", model.ChannelHeaderMaxRunes+1))
		}, ceHeader, "header is too long"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := editChanTestModel()
			m.openEditChannel("c1", ceDisplayName)
			c.mutate(m.chanEdit)

			patch, row, msg := m.chanEdit.patch()
			if patch != nil {
				t.Fatalf("built a patch for an invalid form: %+v", patch)
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

// TestEditChannelSubmitParksOnBadRow: a failed validation moves the cursor to
// the field that's wrong rather than firing the request.
func TestEditChannelSubmitParksOnBadRow(t *testing.T) {
	m := editChanTestModel()
	m.openEditChannel("c1", ceHeader)
	m.chanEdit.inputs[ceDisplayName].SetValue("")

	out, _ := m.submitEditChannel()
	m = out.(Model)
	if m.chanEdit == nil {
		t.Fatal("the modal closed on an invalid submit")
	}
	if m.chanEdit.row != ceDisplayName {
		t.Errorf("cursor on row %d, want the offending display-name row", m.chanEdit.row)
	}
	if m.chanEdit.submitting {
		t.Error("marked submitting despite failing validation")
	}
}

// TestApplyChannelPatched: the server's record is folded into the sidebar's
// channel, and a rename re-sorts the bucket with the cursor following the row.
func TestApplyChannelPatched(t *testing.T) {
	m := editChanTestModel()
	m.channels["t1"] = append(m.channels["t1"],
		&model.Channel{Id: "c2", TeamId: "t1", Type: model.ChannelTypeOpen, Name: "alpha", DisplayName: "Alpha"})
	m.openEditChannel("c1", ceDisplayName)

	patched := &model.Channel{
		Id: "c1", TeamId: "t1", Type: model.ChannelTypeOpen,
		Name: "zulu-talk", DisplayName: "Zulu Talk", Purpose: "chat", Header: "hi",
	}
	out, _ := m.applyChannelPatched(channelPatchedMsg{channelID: "c1", ch: patched})
	m = out.(Model)

	if m.chanEdit != nil {
		t.Error("the modal stayed open after a successful patch")
	}
	c := m.findChannel("c1")
	if c.DisplayName != "Zulu Talk" || c.Name != "zulu-talk" || c.Purpose != "chat" || c.Header != "hi" {
		t.Errorf("channel not updated in place: %+v", c)
	}
	// Alpha now sorts before Zulu Talk, so the row moved — the cursor must follow.
	if got := m.channels["t1"][m.channelIdx].Id; got != "c1" {
		t.Errorf("cursor on %q after the rename re-sorted the bucket, want c1", got)
	}
	if !strings.Contains(m.status, "Zulu Talk") {
		t.Errorf("status = %q, want it to name the renamed channel", m.status)
	}
}

// TestApplyChannelPatchedError: a rejected patch keeps the form open with the
// server's reason, folded onto one line.
func TestApplyChannelPatchedError(t *testing.T) {
	m := editChanTestModel()
	m.openEditChannel("c1", ceDisplayName)
	m.chanEdit.submitting = true

	out, cmd := m.applyChannelPatched(channelPatchedMsg{
		channelID: "c1",
		err:       errors.New("you do not have permission\nto rename this channel"),
	})
	m = out.(Model)

	if m.chanEdit == nil {
		t.Fatal("the modal closed on a failed patch; want it kept open")
	}
	if m.chanEdit.submitting {
		t.Error("still submitting after the error came back")
	}
	if !strings.Contains(m.chanEdit.errMsg, "permission") {
		t.Errorf("errMsg = %q, want the server's reason", m.chanEdit.errMsg)
	}
	if strings.Contains(m.chanEdit.errMsg, "\n") {
		t.Errorf("errMsg = %q, want it folded onto one line", m.chanEdit.errMsg)
	}
	if cmd != nil {
		t.Error("a failed patch returned a Cmd; want none")
	}
	if c := m.findChannel("c1"); c.DisplayName != "General" {
		t.Errorf("channel changed locally despite the failure: %q", c.DisplayName)
	}
}

// TestApplyChannelPatchedAfterDrop: a patch landing after the channel is gone
// (archived, left) reports rather than panicking.
func TestApplyChannelPatchedAfterDrop(t *testing.T) {
	m := editChanTestModel()
	m.channels["t1"] = nil
	out, _ := m.applyChannelPatched(channelPatchedMsg{
		channelID: "c1",
		ch:        &model.Channel{Id: "c1", DisplayName: "Gone"},
	})
	if got := out.(Model).status; got != "" {
		t.Errorf("status = %q, want the vanished channel to be ignored quietly", got)
	}
}

// TestEditChannelKeys: tab wraps through the rows and esc closes without saving.
func TestEditChannelKeys(t *testing.T) {
	m := editChanTestModel()
	m.openEditChannel("c1", ceDisplayName)

	pressEdit(t, &m, "tab")
	if m.chanEdit.row != ceURL {
		t.Errorf("row after tab = %d, want ceURL (%d)", m.chanEdit.row, ceURL)
	}
	pressEdit(t, &m, "shift+tab")
	pressEdit(t, &m, "shift+tab") // wraps backwards off the top
	if m.chanEdit.row != ceHeader {
		t.Errorf("row after wrapping backwards = %d, want ceHeader (%d)", m.chanEdit.row, ceHeader)
	}
	typeInto := "!"
	typeIntoEdit(t, &m, typeInto)
	if got := m.chanEdit.inputs[ceHeader].Value(); got != "read the docs!" {
		t.Errorf("header = %q, want the typed edit appended", got)
	}

	pressEdit(t, &m, "esc")
	if m.chanEdit != nil {
		t.Error("esc left the edit modal open")
	}
	if c := m.findChannel("c1"); c.Header != "read the docs" {
		t.Errorf("esc saved the edit: header = %q", c.Header)
	}
}

// TestEditChannelIsModal: the form is a body overlay, so the composer beneath it
// doesn't keep the terminal cursor and the pass-through globals stand down.
func TestEditChannelIsModal(t *testing.T) {
	m := editChanTestModel()
	m.focus = focusInput
	if m.inModal() {
		t.Fatal("inModal() is true with no modal open")
	}
	m.openEditChannel("c1", ceDisplayName)
	if !m.inModal() {
		t.Error("inModal() = false with the edit form open")
	}
	if !m.bodyOverlayActive() {
		t.Error("bodyOverlayActive() = false; the composer would keep the terminal cursor")
	}
}

// TestRenderEditChannel: the form names the channel and shows every field.
func TestRenderEditChannel(t *testing.T) {
	m := editChanTestModel()
	if got := m.renderEditChannel(); got != "" {
		t.Errorf("renderEditChannel() with the modal closed = %q, want empty", got)
	}
	m.openEditChannel("c1", ceDisplayName)
	out := m.renderEditChannel()
	for _, want := range []string{"Edit #General", "Display name", "URL", "Purpose", "Header", "General"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered form is missing %q\n---\n%s", want, out)
		}
	}
}

// TestRenderEditChannelFitsTerminal: no long value or server error may push the
// modal past the terminal's width.
func TestRenderEditChannelFitsTerminal(t *testing.T) {
	for _, w := range []int{48, 60, 80, 120} {
		m := editChanTestModel()
		m.width = w
		m.openEditChannel("c1", ceDisplayName)
		m.chanEdit.label = "#" + strings.Repeat("long-channel-", 6)
		m.chanEdit.errMsg = strings.Repeat("very long server error ", 6)

		out := m.renderEditChannel()
		outer, _, _ := ccDims(w)
		for i, line := range strings.Split(out, "\n") {
			if got := lipgloss.Width(line); got > outer {
				t.Errorf("width=%d: line %d is %d cols wide, want <= %d\n%s", w, i, got, outer, line)
				break
			}
		}
	}
}
