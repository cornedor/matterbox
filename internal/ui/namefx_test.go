package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/effects"
)

// Every plain-text consumer of a channel name (filter, switcher, breadcrumbs,
// palette entries) reads through displayChannel/channelLabel, which strip the
// effects payload — no invisible runes leak past the two styled renders.
func TestDisplayChannelStripsPayload(t *testing.T) {
	c := &model.Channel{Name: "party", DisplayName: compileEffects("\\rainbow{party}")}
	if got := displayChannel(c); got != "party" {
		t.Errorf("displayChannel = %q, want the bare visible name", got)
	}
}

// channelLabelFX marks the name's spans as sentinels for the styled renders;
// everything else — a payload-less channel, a DM — falls through to the plain
// label.
func TestChannelLabelFX(t *testing.T) {
	m := navModel()
	c := &model.Channel{Type: model.ChannelTypeOpen, Name: "party", DisplayName: compileEffects("\\rainbow{party}")}
	want := "#" + string(effStart(effects.Rainbow)) + "party" + string(effSentinelEnd)
	if got := m.channelLabelFX(c); got != want {
		t.Errorf("channelLabelFX = %q, want %q", got, want)
	}

	plain := &model.Channel{Type: model.ChannelTypeOpen, Name: "general", DisplayName: "general"}
	if got := m.channelLabelFX(plain); got != m.channelLabel(plain) {
		t.Errorf("payload-less channel: FX label %q != plain label", got)
	}
}

// Interactive and geometric effects make no sense in a channel name: their
// spans render as plain text, while colour effects keep theirs.
func TestNameEffectSentinelsDropsInteractive(t *testing.T) {
	if got := nameEffectSentinels(compileEffects("\\copy{token} \\rainbow{hi}")); got !=
		"token "+string(effStart(effects.Rainbow))+"hi"+string(effSentinelEnd) {
		t.Errorf("copy span survived into a name: %q", got)
	}
	if got := nameEffectSentinels(compileEffects("\\scroll{whee}")); got != "whee" {
		t.Errorf("scroll span survived into a name: %q", got)
	}
}

// resolveStaticLine bakes steady colours in — no sentinels left, deterministic
// (the resting phase), width unchanged — so the result can live in the
// fingerprint-cached sidebar and title renders.
func TestResolveStaticLine(t *testing.T) {
	line := "#" + string(effStart(effects.Rainbow)) + "party" + string(effSentinelEnd)
	got := resolveStaticLine(line)
	if hasEffectSentinel(got) {
		t.Error("sentinels left unresolved")
	}
	if !strings.Contains(got, "\x1b[38;2;") {
		t.Error("no colour painted")
	}
	if ansi.Strip(got) != "#party" {
		t.Errorf("visible text = %q, want %q", ansi.Strip(got), "#party")
	}
	if again := resolveStaticLine(line); again != got {
		t.Error("static resolve is not deterministic")
	}
	if resolveStaticLine("plain") != "plain" {
		t.Error("a plain line was rewritten")
	}
}

// The sidebar paints a name's effects only on a plain row: the selected row's
// bar and unread/mention colouring win, so state styling keeps its meaning.
func TestSidebarPaintsNameEffectsOnPlainRowsOnly(t *testing.T) {
	m := navModel()
	m.unread = map[string]int{}
	m.mentions = map[string]int{}
	m.channels["t1"][0].DisplayName = compileEffects("\\rainbow{general}") // selected (channelIdx 0)
	m.channels["t1"][1].DisplayName = compileEffects("\\rainbow{random}")  // plain row

	rows := strings.Split(m.renderChannelsPane(20), "\n")
	var selRow, plainRow string
	for _, r := range rows {
		switch {
		case strings.Contains(ansi.Strip(r), "> #general"):
			selRow = r
		case strings.Contains(ansi.Strip(r), "#random"):
			plainRow = r
		}
	}
	if selRow == "" || plainRow == "" {
		t.Fatalf("rows not found in:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.Contains(plainRow, "\x1b[38;2;") {
		t.Errorf("plain row not painted: %q", plainRow)
	}
	if strings.Contains(selRow, "\x1b[38;2;") {
		t.Errorf("selected row painted — the selection bar should win: %q", selRow)
	}

	// An unread row keeps its unread styling, not the effect's colours.
	m.unread["c2"] = 3
	if m.vcache != nil {
		m.vcache.sidebar.valid = false
	}
	rows = strings.Split(m.renderChannelsPane(20), "\n")
	for _, r := range rows {
		if strings.Contains(ansi.Strip(r), "#random") && strings.Contains(r, "\x1b[38;2;") {
			t.Errorf("unread row painted — unread styling should win: %q", r)
		}
	}
}

// The messages-pane title shows the open channel's name effects without any
// animation gate: the colours are baked in statically, not painted per frame.
func TestTitleLinePaintsNameStatically(t *testing.T) {
	m := navModel()
	m.channels["t1"][0].DisplayName = compileEffects("\\rainbow{general}")
	m.msgsView.SetHeight(20)
	// Deliberately no refreshEffectsVisibility: the name must not depend on it.

	title := firstLine(m.renderMessagesPane(38, 80))
	if !strings.Contains(ansi.Strip(title), "#general") {
		t.Fatalf("title line missing the name: %q", title)
	}
	if !strings.Contains(title, "\x1b[38;2;") {
		t.Error("the name's effect was not painted")
	}
	if hasEffectSentinel(title) {
		t.Error("unresolved sentinels on the title line")
	}
}

// An effect-only rename (same visible name, different colours) must invalidate
// the sidebar cache: the fingerprint carries the raw display name.
func TestChannelsFingerprintSeesEffectOnlyRename(t *testing.T) {
	m := navModel()
	vis := m.visibleChannels()
	m.channels["t1"][0].DisplayName = compileEffects("\\rainbow{general}")
	fp1 := m.channelsFingerprint(vis, 0, 10, 12, "h")
	m.channels["t1"][0].DisplayName = compileEffects("\\ok{general}")
	fp2 := m.channelsFingerprint(vis, 0, 10, 12, "h")
	if fp1 == fp2 {
		t.Error("effect-only rename produced an identical fingerprint — stale sidebar")
	}
}
