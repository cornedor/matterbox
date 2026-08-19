package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/editor"
)

func TestParseSlash(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantArgs string
		wantOK   bool
	}{
		{"/me waves", "me", "waves", true},
		{"/me", "me", "", true},
		{"/ME Waves Hello", "me", "Waves Hello", true}, // name lowercased, args preserved
		{"/shrug", "shrug", "", true},
		{"/dm @alice hey there", "dm", "@alice hey there", true},
		{"/me   leading spaces", "me", "leading spaces", true},
		{"hello /me", "", "", false},          // slash not at start
		{"/", "", "", false},                  // bare slash
		{"/ foo", "", "", false},              // space after slash → not a command
		{"/123", "", "", false},               // must start with a letter
		{"", "", "", false},                   // empty
		{"/help\nmore", "help", "more", true}, // newline splits name from args
	}
	for _, c := range cases {
		name, args, ok := parseSlash(c.in)
		if ok != c.wantOK || name != c.wantName || args != c.wantArgs {
			t.Errorf("parseSlash(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, name, args, ok, c.wantName, c.wantArgs, c.wantOK)
		}
	}
}

func TestLookupSlash(t *testing.T) {
	if _, ok := lookupSlash("me"); !ok {
		t.Error("lookupSlash(me) not found")
	}
	if _, ok := lookupSlash("msg"); !ok { // alias of dm
		t.Error("lookupSlash(msg) alias not found")
	}
	if c, ok := lookupSlash("find"); !ok || c.name != "search" { // alias of search
		t.Errorf("lookupSlash(find) = (%q,%v), want search,true", c.name, ok)
	}
	if _, ok := lookupSlash("nope"); ok {
		t.Error("lookupSlash(nope) should not match")
	}
}

func TestSplitDMArgs(t *testing.T) {
	cases := []struct{ in, spec, msg string }{
		{"@alice", "@alice", ""},
		{"@alice hey there", "@alice", "hey there"},
		{"@a,@b,@c group hi", "@a,@b,@c", "group hi"},
		{"  @alice   hi  ", "@alice", "hi"},
		{"", "", ""},
	}
	for _, c := range cases {
		spec, msg := splitDMArgs(c.in)
		if spec != c.spec || msg != c.msg {
			t.Errorf("splitDMArgs(%q) = (%q,%q), want (%q,%q)", c.in, spec, msg, c.spec, c.msg)
		}
	}
}

func TestSlashShrugTransform(t *testing.T) {
	const shrug = `¯\_(ツ)_/¯`
	// Empty args → just the kaomoji.
	if got := shrugText(""); got != shrug {
		t.Errorf("shrugText(\"\") = %q, want %q", got, shrug)
	}
	if got := shrugText("well"); got != "well "+shrug {
		t.Errorf("shrugText(well) = %q, want %q", got, "well "+shrug)
	}
}

func TestSlashHelpRows(t *testing.T) {
	rows := slashHelpRows()
	if len(rows) != len(slashRegistry()) {
		t.Fatalf("slashHelpRows len = %d, want %d", len(rows), len(slashRegistry()))
	}
	for _, r := range rows {
		if r.keys == "" || r.desc == "" {
			t.Errorf("help row has empty field: %+v", r)
		}
		if r.keys[0] != '/' {
			t.Errorf("help row usage should start with /: %q", r.keys)
		}
	}
}

func TestMeEmoteLine(t *testing.T) {
	m := &Model{userNames: map[string]string{"u1": "alice"}}
	// Id "" skips the markdown cache; Type "me" with the server's *...* message.
	p := &model.Post{UserId: "u1", Type: model.PostTypeMe, Message: "*waves at everyone*", CreateAt: 1_700_000_000_000}
	plain := ansi.Strip(m.meEmoteLine(p))
	if !strings.HasPrefix(plain, "* alice waves at everyone") {
		t.Errorf("meEmoteLine = %q, want it to start with %q", plain, "* alice waves at everyone")
	}
}

func TestFirstLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello", "hello"},
		{"  hello  ", "hello"},
		{"first\nsecond", "first"},
		{"\n\nbody", "body"},
		{"", ""},
	}
	for _, c := range cases {
		if got := firstLine(c.in); got != c.want {
			t.Errorf("firstLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// newSlashTestModel builds a minimal Model whose composer is ready for the "/"
// autocomplete to run against. The team-cache maps must be non-nil so the
// lazy-fetch guard can write to them.
func newSlashTestModel(value string) *Model {
	m := &Model{
		serverCmds:    map[string][]serverCommand{},
		serverCmdsReq: map[string]bool{},
	}
	m.input = editor.New()
	m.input.SetWidth(60)
	m.input.SetValue(value)
	return m
}

func TestUpdateSlashTrigger(t *testing.T) {
	// The popup opens only while the command word is being typed: line 0, a
	// leading "/", and no whitespace before the cursor (the cursor sits at the
	// end of the value after SetValue).
	cases := []struct {
		text string
		want bool
	}{
		{"/", true},            // bare slash lists every command
		{"/m", true},           // partial command word
		{"/me", true},          // full built-in
		{"/zzzznope", false},   // matches nothing → stays closed
		{"/me ", false},        // trailing space → now typing args
		{"/me waves", false},   // in the argument portion
		{"/ foo", false},       // space right after "/" → not a command
		{"hello /me", false},   // "/" not at line start
		{"", false},            // empty composer
		{"just text", false},   // no leading slash
		{"/me\nsecond", false}, // cursor on line 2, not the command line
	}
	for _, tc := range cases {
		m := newSlashTestModel(tc.text)
		m.updateSlash()
		if m.slash.active != tc.want {
			t.Errorf("updateSlash(%q): active = %v, want %v", tc.text, m.slash.active, tc.want)
		}
	}
}

func TestUpdateSlashCursorAtLineStart(t *testing.T) {
	// Cursor moved left of the leading "/" (e.g. Home/ctrl+a): col == 0 used
	// to slice runes[1:0] and panic. The popup must just close.
	m := newSlashTestModel("/me")
	m.input.SetCursorOffset(0)
	if row, col := m.input.CursorRowCol(); row != 0 || col != 0 {
		t.Fatalf("setup: cursor = (%d,%d), want (0,0)", row, col)
	}
	m.updateSlash() // must not panic
	if m.slash.active {
		t.Error("updateSlash should stay closed with the cursor left of the slash")
	}
}

func TestUpdateSlashSuppressedWhileEditing(t *testing.T) {
	// A leading "/" in an edited post is literal text, not a command — the
	// popup must stay closed.
	m := newSlashTestModel("/me")
	m.editingPostID = "post123"
	m.updateSlash()
	if m.slash.active {
		t.Error("updateSlash should stay closed while editing a post")
	}
}

func TestSlashKaomojiIsRegistryCommand(t *testing.T) {
	c, ok := lookupSlash("kaomoji")
	if !ok {
		t.Fatal("/kaomoji missing from slashRegistry")
	}
	if c.run == nil {
		t.Fatal("/kaomoji has no runner")
	}
	// Typing it behaves like every other command: the slash popup lists it,
	// nothing opens until enter — so a future /kaomoji-something stays typeable.
	m := newSlashTestModel("/kaomoji")
	m.updateSlash()
	if m.kaomojiPicker.active {
		t.Fatal("typing /kaomoji must not open the picker; that happens on send")
	}
	if !m.slash.active {
		t.Fatal("typing /kaomoji should keep the slash popup open like every other command")
	}
	next, _ := m.runSlashCommand("kaomoji", "")
	got := next.(Model)
	if !got.kaomojiPicker.active {
		t.Fatal("running /kaomoji should open the picker")
	}
	if got.input.Value() != "" {
		t.Fatalf("running /kaomoji should consume the composer, left %q", got.input.Value())
	}
}

func TestSlashMatches(t *testing.T) {
	const team = "team1"
	m := newSlashTestModel("/")
	m.serverCmds[team] = []serverCommand{
		{trigger: "jira", desc: "manage Jira", hint: "[issue]"},
		{trigger: "me", desc: "server me"}, // duplicates a built-in
	}

	// A built-in surfaces and is not flagged as a server command.
	got := m.slashMatches("me", team)
	if len(got) == 0 || got[0].trigger != "me" {
		t.Fatalf(`slashMatches("me") = %+v, want "me" first`, got)
	}
	if got[0].server {
		t.Error(`"me" should resolve to the built-in (server=false), not the server duplicate`)
	}
	// The server-side "me" must be deduped away — only one "me" row total.
	var meCount int
	for _, c := range got {
		if c.trigger == "me" {
			meCount++
		}
	}
	if meCount != 1 {
		t.Errorf(`"me" appears %d times, want 1 (built-in wins the dedup)`, meCount)
	}

	// A server-only command surfaces with the cloud flag and its hint.
	got = m.slashMatches("jira", team)
	if len(got) == 0 || got[0].trigger != "jira" {
		t.Fatalf(`slashMatches("jira") = %+v, want "jira"`, got)
	}
	if !got[0].server {
		t.Error(`"jira" should be flagged server=true`)
	}
	if got[0].hint != "[issue]" {
		t.Errorf(`"jira" hint = %q, want "[issue]"`, got[0].hint)
	}

	// An alias matches but the row shows the primary trigger ("msg" → "dm").
	got = m.slashMatches("msg", team)
	if len(got) == 0 || got[0].trigger != "dm" {
		t.Fatalf(`slashMatches("msg") = %+v, want primary trigger "dm"`, got)
	}

	// The bare list is capped at slashLimit.
	if got := m.slashMatches("", team); len(got) > slashLimit {
		t.Errorf("bare list returned %d rows, want <= %d", len(got), slashLimit)
	}

	// With no cached commands for the team, built-ins still match.
	if got := m.slashMatches("help", "unknownteam"); len(got) == 0 {
		t.Error(`"help" should still match a built-in for an uncached team`)
	}
}

func TestAcceptSlash(t *testing.T) {
	m := newSlashTestModel("/me")
	m.updateSlash()
	if !m.slash.active {
		t.Fatal("popup should be active for /me")
	}
	// Select the "me" row explicitly so the test doesn't depend on ranking.
	idx := -1
	for i, it := range m.slash.items {
		if it.trigger == "me" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("'me' not among items: %+v", m.slash.items)
	}
	m.slash.idx = idx
	if _, ok := m.acceptSlash(); !ok {
		t.Fatal("acceptSlash returned ok=false")
	}
	if got := m.input.Value(); got != "/me " {
		t.Errorf("after accept, composer = %q, want %q", got, "/me ")
	}
	if m.slash.active {
		t.Error("popup should be closed after accept")
	}
}

func TestRenderSlashPopupCloud(t *testing.T) {
	m := newSlashTestModel("/")
	m.slash.active = true
	m.slash.items = []slashCandidate{
		{trigger: "me", desc: "emote"},                // built-in: no cloud
		{trigger: "jira", desc: "Jira", server: true}, // server: cloud
	}
	out := m.renderSlashPopup(80)
	if !strings.Contains(out, slashCloud) {
		t.Errorf("popup should contain the cloud glyph for the server command:\n%s", out)
	}
	if !strings.Contains(out, "/jira") || !strings.Contains(out, "/me") {
		t.Errorf("popup missing command rows:\n%s", out)
	}
}

func TestRenderSlashPopupNoWrap(t *testing.T) {
	// A long description must be truncated to a single line, not wrapped — so
	// the dropdown's row count equals the item count plus its border rows.
	m := newSlashTestModel("/")
	m.slash.active = true
	m.slash.items = []slashCandidate{
		{trigger: "dm", hint: "@user[,@user…] [message]",
			desc: "open (creating if new) a DM / group DM, optionally sending a message"},
	}
	out := m.renderSlashPopup(60)
	for _, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w > 60 {
			t.Errorf("row exceeds pane width: %d > 60\n%q", w, line)
		}
	}
	if n := strings.Count(out, "\n") + 1; n != 3 { // top border + 1 row + bottom border
		t.Errorf("popup has %d lines, want 3 (one un-wrapped row in a border)", n)
	}
}

func TestUpdateSlashArgPopup(t *testing.T) {
	// A command that declares argValues keeps the popup up over its argument:
	// "/kaomoji " lists every kaomoji, and typing narrows it.
	m := newSlashTestModel("/kaomoji ")
	m.updateSlash()
	if !m.slash.active || !m.slash.arg {
		t.Fatalf("/kaomoji + space should open the argument popup (active=%v arg=%v)", m.slash.active, m.slash.arg)
	}
	if m.slash.cmd != "kaomoji" {
		t.Errorf("popup cmd = %q, want %q", m.slash.cmd, "kaomoji")
	}
	if len(m.slash.items) == 0 {
		t.Fatal("empty argument list for /kaomoji")
	}
	// The rows are kaomoji names with the kaomoji itself as the hint.
	if it := m.slash.items[0]; it.trigger == "" || it.desc == "" {
		t.Errorf("argument row = %+v, want a name and its kaomoji", it)
	}

	m = newSlashTestModel("/kaomoji tablef")
	m.updateSlash()
	if !m.slash.arg || len(m.slash.items) == 0 || m.slash.items[0].trigger != "tableflip" {
		t.Fatalf(`"/kaomoji tablef" should narrow to tableflip, got %+v`, m.slash.items)
	}

	// The alias reaches the same argument list as the primary trigger.
	m = newSlashTestModel("/template ")
	m.updateSlash()
	if m.slash.active && m.slash.cmd != "template" {
		t.Errorf("alias popup cmd = %q, want %q", m.slash.cmd, "template")
	}

	// A command with free-text arguments keeps the popup closed, as before.
	for _, text := range []string{"/me waves", "/shimmer hi"} {
		m := newSlashTestModel(text)
		m.updateSlash()
		if m.slash.active {
			t.Errorf("updateSlash(%q): popup should stay closed for a free-text argument", text)
		}
	}
}

func TestAcceptSlashArgument(t *testing.T) {
	m := newSlashTestModel("/kaomoji shr")
	m.updateSlash()
	if !m.slash.arg {
		t.Fatal("expected the argument popup")
	}
	if m.slash.items[0].trigger != "shrug" {
		t.Fatalf(`"shr" should rank shrug first, got %+v`, m.slash.items)
	}
	if _, ok := m.acceptSlash(); !ok {
		t.Fatal("acceptSlash returned ok=false")
	}
	// The argument is filled in place — no trailing space, so the popup closes
	// and the next enter runs the command instead of re-accepting.
	if got := m.input.Value(); got != "/kaomoji shrug" {
		t.Errorf("after accept, composer = %q, want %q", got, "/kaomoji shrug")
	}
	if m.slash.active {
		t.Error("popup should be closed after accepting an argument")
	}
}

func TestAcceptSlashOpensArgumentPopup(t *testing.T) {
	// Accepting a command that has argValues rolls straight into its argument
	// list, in the same keypress.
	m := newSlashTestModel("/kaomo")
	m.updateSlash()
	if _, ok := m.acceptSlash(); !ok {
		t.Fatal("acceptSlash returned ok=false")
	}
	if got := m.input.Value(); got != "/kaomoji " {
		t.Fatalf("after accept, composer = %q, want %q", got, "/kaomoji ")
	}
	if !m.slash.active || !m.slash.arg {
		t.Errorf("accepting /kaomoji should open its argument popup (active=%v arg=%v)", m.slash.active, m.slash.arg)
	}
}

func TestSlashKaomojiByName(t *testing.T) {
	// "/kaomoji shrug" inserts straight into the composer; an unknown name is
	// reported and inserts nothing.
	m := newSlashTestModel("")
	m.focus = focusInput
	if cmd := slashKaomoji(m, "shrug"); cmd == nil {
		t.Fatal("slashKaomoji returned no Cmd")
	}
	if got := m.input.Value(); got != `¯\_(ツ)_/¯` {
		t.Errorf("composer = %q, want the shrug kaomoji", got)
	}
	if m.kaomojiPicker.active {
		t.Error("a named /kaomoji should not open the picker")
	}

	m = newSlashTestModel("")
	slashKaomoji(m, "nosuchface")
	if m.input.Value() != "" {
		t.Errorf("unknown name inserted %q, want nothing", m.input.Value())
	}
	if !strings.Contains(m.status, "nosuchface") {
		t.Errorf("status = %q, want it to name the missing kaomoji", m.status)
	}
}

func TestRenderSlashPopupArgumentRows(t *testing.T) {
	// Argument rows are bare values: no leading "/", no cloud column.
	m := newSlashTestModel("/kaomoji ")
	m.slash = slashState{active: true, arg: true, cmd: "kaomoji", items: []slashCandidate{
		{trigger: "shrug", desc: `¯\_(ツ)_/¯`},
	}}
	out := m.renderSlashPopup(60)
	if !strings.Contains(out, "shrug") || !strings.Contains(out, `¯\_(ツ)_/¯`) {
		t.Errorf("popup missing the argument row:\n%s", out)
	}
	if strings.Contains(out, "/shrug") {
		t.Errorf("argument rows must not be prefixed with a slash:\n%s", out)
	}
}
