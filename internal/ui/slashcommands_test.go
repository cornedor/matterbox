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
