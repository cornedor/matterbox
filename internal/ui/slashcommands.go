package ui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/effects"
	"matterbox/internal/mm"
)

// Slash commands are typed into the composer and intercepted on send: a leading
// "/" followed by a letter is a command, not a message (matching Mattermost and
// most chat apps). Known commands are handled here; anything unrecognised is
// forwarded to the Mattermost server's command-execute endpoint, so server- and
// plugin-provided commands still work as a fallback.
//
// This is the composer "/" dispatcher; the ctrl+k ">" palette (commands.go) is a
// separate thing.

// slashCommand is one registered "/" command. run mutates the model via the
// pointer and returns any async work as a Cmd. args is the text after the
// command word (already trimmed); aliases are alternative names. The display
// fields (args usage hint, desc) drive /help.
type slashCommand struct {
	name    string
	aliases []string
	args    string // usage hint shown in /help, e.g. "<action>"
	desc    string
	run     func(m *Model, args string) tea.Cmd
	// argValues, when set, keeps the autocomplete popup up past the command
	// word: it supplies the rows offered for the command's argument, in
	// preferred order (the generic code fuzzy-filters and caps them). Commands
	// whose argument is free text (/me, the effect commands) leave it nil.
	argValues func(m *Model) []slashArg
}

// slashArg is one argument-completion row: value is inserted after the command
// word, desc is the dimmed hint beside it (the kaomoji itself, a template's
// first line, …).
type slashArg struct {
	value string
	desc  string
}

// slashRegistry is the ordered list of built-in "/" commands. Order is the
// display order in /help.
func slashRegistry() []slashCommand {
	cmds := []slashCommand{
		{name: "me", args: "<action>", desc: "send an action / emote message", run: slashMe},
		{name: "shrug", args: "[message]", desc: `append ¯\_(ツ)_/¯ to your message`, run: slashShrug},
		{name: "kaomoji", args: "<name> [message]", desc: "send a kaomoji, appended to your message if you add one",
			run: slashKaomoji, argValues: kaomojiArgs},
		{name: "tmpl", aliases: []string{"template"}, args: "[name]", desc: "insert a saved template (no name: pick from the list)",
			run: slashTemplate, argValues: templateArgs},
		{name: "dm", aliases: []string{"msg"}, args: "@user[,@user…] [message]",
			desc: "open (creating if new) a DM / group DM, optionally sending a message", run: slashDM},
		{name: "search", aliases: []string{"find"}, args: "<query>",
			desc: "open the Search tab and run a query", run: slashSearch},
		{name: "help", aliases: []string{"commands"}, desc: "list the available slash commands", run: slashHelp},
	}
	return append(cmds, slashEffectCommands()...)
}

// slashEffectCommands generates one command per text effect (/shimmer <text>),
// which applies it to the whole message — the shorthand for wrapping everything
// you were going to type in \shimmer{…} anyway. Generated from effects.All() so
// a new effect cannot be added without its command appearing too.
func slashEffectCommands() []slashCommand {
	cmds := make([]slashCommand, 0, len(effects.All()))
	for _, e := range effects.All() {
		id := e.ID
		cmds = append(cmds, slashCommand{
			name: e.Name,
			args: "<text>",
			desc: "send the whole message with the " + e.Name + " effect (" + e.Desc + ")",
			run:  func(m *Model, args string) tea.Cmd { return m.sendEffectMessage(id, args) },
		})
	}
	return cmds
}

// sendEffectMessage posts args as an ordinary message carrying one effect over
// the whole of it.
func (m *Model) sendEffectMessage(id byte, args string) tea.Cmd {
	text := strings.TrimSpace(args)
	if text == "" {
		m.status = "usage: /" + effects.Name(id) + " <text>"
		return nil
	}
	return m.sendComposedText(wholeMessageEffect(id, text))
}

// lookupSlash finds a registered command by name or alias (case-insensitive
// callers pass an already-lowercased name).
func lookupSlash(name string) (slashCommand, bool) {
	for _, c := range slashRegistry() {
		if c.name == name {
			return c, true
		}
		for _, a := range c.aliases {
			if a == name {
				return c, true
			}
		}
	}
	return slashCommand{}, false
}

// parseSlash splits composer text into a command name + argument string. ok is
// false (so it's sent as a normal message) unless the text is "/" followed by a
// letter — this leaves "/", "/ foo", and message bodies that merely contain a
// slash untouched.
func parseSlash(text string) (name, args string, ok bool) {
	if len(text) < 2 || text[0] != '/' {
		return "", "", false
	}
	rest := text[1:]
	r, _ := utf8.DecodeRuneInString(rest)
	if !unicode.IsLetter(r) {
		return "", "", false
	}
	if i := strings.IndexAny(rest, " \t\n"); i >= 0 {
		return strings.ToLower(rest[:i]), strings.TrimSpace(rest[i+1:]), true
	}
	return strings.ToLower(rest), "", true
}

// runSlashCommand consumes the composer (clearing the draft) and dispatches to
// the named command, or to the server fallback when it's unknown. Called from
// the composer-send handler once parseSlash has matched.
func (m Model) runSlashCommand(name, args string) (tea.Model, tea.Cmd) {
	draftCmd := m.consumeComposer()
	if c, ok := lookupSlash(name); ok {
		return m, tea.Batch(draftCmd, c.run(&m, args))
	}
	return m, tea.Batch(draftCmd, m.execServerCommand(name, args))
}

// consumeComposer clears the composer the same way a normal send does (input,
// undo history, autocomplete popups, grammar) and drops the channel's saved
// draft so the typed command doesn't linger. Returns the draft-clear Cmd.
func (m *Model) consumeComposer() tea.Cmd {
	m.input.Reset()
	m.history.reset()
	m.syncInputHeight()
	m.closeMention()
	m.closeEmoji()
	m.closeSlash()
	m.closeLang()
	m.closeEffectPopup()
	m.clearGrammar()
	if !m.threadOpen && m.openChannelID != "" {
		return m.clearDraft(m.openChannelID)
	}
	return nil
}

// composerTarget returns the (channel, root) a composed message should go to:
// the open thread's channel + root when a thread is focused, otherwise the open
// channel with no root. Mirrors the targeting in the normal send handler.
func (m *Model) composerTarget() (channelID, rootID string) {
	if m.threadOpen {
		return m.threadChannelID, m.threadRootID
	}
	return m.openChannelID, ""
}

// sendComposedText posts text to the current composer target with an optimistic
// stub, exactly like a hand-typed message. Used by client-local commands that
// produce a normal post (e.g. /shrug).
func (m *Model) sendComposedText(text string) tea.Cmd {
	channelID, rootID := m.composerTarget()
	if channelID == "" {
		m.status = "no channel open"
		return nil
	}
	// A /shimmer sent while a nested reply is being composed is still that
	// reply, so it hangs where the strip above the composer says it will.
	text = m.attachReplyParent(text, rootID)
	m.clearReplyParent()
	m.appendOptimistic(channelID, rootID, text, nil)
	m.resizeMessagesViewport()
	if !m.threadOpen {
		m.postIdx = len(m.posts) - 1
	}
	m.renderMessages()
	m.renderThread()
	m.status = "sending…"
	return m.sendMessage(channelID, rootID, text, nil)
}

// slashExecMsg carries the result of a server-side command execution.
type slashExecMsg struct {
	resp *model.CommandResponse
	err  error
}

// execServerCommand runs "/name args" against the Mattermost server in the
// current channel. Used for /me and for the unknown-command fallback.
func (m Model) execServerCommand(name, args string) tea.Cmd {
	channelID, _ := m.composerTarget()
	if channelID == "" {
		return func() tea.Msg { return slashExecMsg{err: fmt.Errorf("no channel open")} }
	}
	teamID := m.commandTeamID(channelID)
	cmdText := "/" + name
	if a := strings.TrimSpace(args); a != "" {
		cmdText += " " + a
	}
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		resp, err := client.ExecuteCommand(ctx, channelID, teamID, cmdText)
		return slashExecMsg{resp: resp, err: err}
	}
}

// commandTeamID picks the team to run a slash command under. A regular channel
// carries its own team; a DM / group-DM has none, so fall back to the focused
// real team (the DM tab is synthetic) or, failing that, the first team.
func (m *Model) commandTeamID(channelID string) string {
	if c := m.findChannel(channelID); c != nil && c.TeamId != "" {
		return c.TeamId
	}
	if id := m.currentTeamID(); m.isRealTeamID(id) {
		return id
	}
	if len(m.teams) > 0 {
		return m.teams[0].Id
	}
	return ""
}

// isRealTeamID reports whether id names one of the user's actual teams (as
// opposed to a synthetic DM / Search / Feed tab id).
func (m *Model) isRealTeamID(id string) bool {
	for _, t := range m.teams {
		if t != nil && t.Id == id {
			return true
		}
	}
	return false
}

// --- individual commands ------------------------------------------------

// slashMe sends an action/emote ("* alice waves"). It's a real Mattermost "me"
// post, so it's delegated to the server's /me command.
func slashMe(m *Model, args string) tea.Cmd {
	if strings.TrimSpace(args) == "" {
		m.status = "usage: /me <action>"
		return nil
	}
	if ch, _ := m.composerTarget(); ch == "" {
		m.status = "/me: no channel open"
		return nil
	}
	m.status = "sending…"
	return m.execServerCommand("me", args)
}

// slashShrug appends the shrug kaomoji to the (optional) message and sends it as
// a normal post.
func slashShrug(m *Model, args string) tea.Cmd {
	return m.sendComposedText(shrugText(args))
}

// shrugText appends the shrug kaomoji to the (optional) message text.
func shrugText(args string) string {
	return kaomojiMessage(args, `¯\_(ツ)_/¯`)
}

// kaomojiMessage puts face after the (optional) message text — the post both
// /shrug and /kaomoji compose.
func kaomojiMessage(message, face string) string {
	if text := strings.TrimSpace(message); text != "" {
		return text + " " + face
	}
	return face
}

// slashDM opens (creating if needed) a DM or group DM with the named user(s) and
// optionally sends a trailing message. The first whitespace-separated token is
// the recipient spec (@user, or @a,@b for a group DM); the rest is the message.
func slashDM(m *Model, args string) tea.Cmd {
	args = strings.TrimSpace(args)
	if args == "" {
		m.status = "usage: /dm @user[,@user…] [message]"
		return nil
	}
	spec, message := splitFirstArg(args)
	if m.me == nil {
		m.status = "/dm: user not loaded yet"
		return nil
	}
	m.status = "opening DM…"
	client, ctx, meID := m.client, m.ctx, m.me.Id
	return func() tea.Msg {
		ch, err := mm.ResolveRecipients(ctx, client, meID, spec)
		return groupDMResolvedMsg{ch: ch, err: err, message: message}
	}
}

// splitFirstArg separates a command's first whitespace-separated token from
// the optional trailing message — /dm's recipient spec ("@alice", "@a,@b") and
// /kaomoji's name. Neither carries whitespace, so the first run of it is an
// unambiguous boundary (a newline counts: the message may be multi-line).
func splitFirstArg(args string) (first, message string) {
	args = strings.TrimSpace(args)
	if i := strings.IndexAny(args, " \t\n"); i >= 0 {
		return args[:i], strings.TrimSpace(args[i+1:])
	}
	return args, ""
}

// slashSearch opens the Search tab and runs the query (empty just opens it).
func slashSearch(m *Model, args string) tea.Cmd {
	cmd := m.openSearchTab()
	q := strings.TrimSpace(args)
	if q == "" {
		return cmd
	}
	m.search.input.SetValue(q)
	m.search.input.CursorEnd()
	return tea.Batch(cmd, m.scheduleSearch())
}

// slashHelp raises the slash-command reference popup.
func slashHelp(m *Model, _ string) tea.Cmd {
	m.openHelpSheet()
	return nil
}

// slashKaomoji sends the named kaomoji as a post, after the optional message
// that follows the name — /shrug for the whole set. The name is the first
// argument (what the "/kaomoji " autocomplete fills in); the modal picker,
// which inserts into the composer instead of sending, lives on the palette.
func slashKaomoji(m *Model, args string) tea.Cmd {
	name, message := splitFirstArg(args)
	if name == "" {
		m.status = "usage: /kaomoji <name> [message]"
		return nil
	}
	it, ok := m.findKaomoji(name)
	if !ok {
		m.status = "kaomoji not found: " + name
		return nil
	}
	cmd := m.sendComposedText(kaomojiMessage(message, it.text))
	if cmd == nil { // no channel open; nothing was sent, so don't count a pick
		return nil
	}
	return tea.Batch(cmd, m.bumpKaomojiStat(it.text))
}

// slashTemplate inserts the named template, or opens the Templates sheet when
// no name was given.
func slashTemplate(m *Model, args string) tea.Cmd {
	return m.insertTemplate(args)
}

// slashHelpRows renders the registry as cheatsheet rows for the /help popup.
func slashHelpRows() []keysSheetRow {
	cmds := slashRegistry()
	rows := make([]keysSheetRow, 0, len(cmds))
	for _, c := range cmds {
		usage := "/" + c.name
		if c.args != "" {
			usage += " " + c.args
		}
		rows = append(rows, keysSheetRow{keys: usage, desc: c.desc})
	}
	return rows
}

// firstLine returns the first non-empty line of s, trimmed — used to squeeze a
// command's ephemeral response into the single-line footer.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// --- "/" command autocomplete popup -------------------------------------
//
// As you type a "/" command into the composer, a dropdown offers matching
// commands: the built-in ones (slashRegistry) plus the server/plugin commands
// the Mattermost server advertises for the active team, which are cached
// per-team (serverCmds) and tagged with a cloud glyph so it's clear they're
// remote. The popup mirrors the @-mention / :emoji pickers (mention.go /
// emoji.go): a state struct, a per-keystroke updateSlash, accept/close, and a
// render helper slotted into the same composer popup chain.

// slashLimit caps the popup so it stays a few rows tall.
const slashLimit = 8

// slashCloud tags server/plugin command rows in the popup. Built-in rows get a
// blank of the same column width so the command text stays aligned.
const slashCloud = "☁"

// serverCommand is one cached server-advertised slash command (built-in or
// plugin) for a team. trigger is the command word without the leading "/".
type serverCommand struct {
	trigger string
	desc    string // auto_complete_desc
	hint    string // auto_complete_hint, e.g. "[message]"
}

// slashState tracks an in-progress "/" completion. Commands live at the very
// start of the composer (parseSlash requires it), so there's no captured line —
// it's always line 0. The popup runs in two modes: over the command word
// itself (arg false; query is the text between "/" and the cursor), and over a
// registered command's argument (arg true; cmd names it, query is the argument
// text up to the cursor). start is the rune index on line 0 where the token
// being completed begins, so accept knows what to replace. Like the emoji
// picker it matches from a local set (built-ins + the per-team cache) with no
// fetch sequence; a cold team cache is filled by a one-shot background fetch
// (serverCommandsMsg) after which the items are recomputed in place.
type slashState struct {
	active bool
	arg    bool   // completing a command's argument, not its name
	cmd    string // in arg mode, the command word the rows belong to
	start  int    // rune index on line 0 where the completed token starts
	query  string
	items  []slashCandidate
	idx    int
}

// slashLine is the composer's first line parsed around the cursor: the command
// word, and — once whitespace has ended it — the argument being typed. ok is
// false whenever the line isn't a "/" command line up to the cursor at all.
type slashLine struct {
	runes []rune
	col   int    // cursor column, clamped into runes
	name  string // command word (lower-cased); only set in arg mode
	arg   bool   // the cursor sits in the argument portion
	start int    // rune index where the token under the cursor begins
	query string // that token's text up to the cursor, lower-cased
}

// slashLineAt parses the composer's first line around the cursor for both the
// popup (updateSlash) and its accept. Pointer receiver: this runs per
// keystroke, so it must not copy the ~133KB Model.
func (m *Model) slashLineAt() (slashLine, bool) {
	row, col := m.input.CursorRowCol()
	if row != 0 {
		return slashLine{}, false
	}
	lines := strings.Split(m.input.Value(), "\n")
	if len(lines) == 0 {
		return slashLine{}, false
	}
	runes := []rune(lines[0])
	if col > len(runes) {
		col = len(runes)
	}
	if len(runes) == 0 || runes[0] != '/' {
		return slashLine{}, false
	}
	if col == 0 {
		// Cursor sits left of the leading "/" (e.g. after Home/ctrl+a):
		// nothing of the command word is typed up to the cursor.
		return slashLine{}, false
	}
	sp := -1
	for i := 1; i < col; i++ {
		if unicode.IsSpace(runes[i]) {
			sp = i
			break
		}
	}
	if sp < 0 {
		// Still on the command word.
		return slashLine{runes: runes, col: col, start: 1, query: strings.ToLower(string(runes[1:col]))}, true
	}
	// Past it: the argument starts after the whitespace run (which the cursor
	// may still be inside, giving an empty query — "list everything").
	start := sp
	for start < col && unicode.IsSpace(runes[start]) {
		start++
	}
	return slashLine{
		runes: runes,
		col:   col,
		name:  strings.ToLower(string(runes[1:sp])),
		arg:   true,
		start: start,
		query: strings.ToLower(string(runes[start:col])),
	}, true
}

// slashCandidate is one popup row: the command word, its description and
// argument hint, and whether it came from the server (→ cloud glyph).
type slashCandidate struct {
	trigger string
	desc    string
	hint    string
	server  bool
}

// updateSlash recomputes the "/" popup after the composer has processed a key.
// While the command word is being typed it lists matching commands (a bare "/"
// lists every one); once whitespace ends the word the command is locked in
// (matching parseSlash) and a command that declares argValues keeps the popup
// going over its argument. It's suppressed while editing an existing post,
// whose leading "/" is literal text. Returns a Cmd that lazily fetches the
// active team's command list when the cache is cold, or nil.
func (m *Model) updateSlash() tea.Cmd {
	if m.editingPostID != "" {
		m.closeSlash()
		return nil
	}
	ln, ok := m.slashLineAt()
	if !ok {
		m.closeSlash()
		return nil
	}
	if ln.arg {
		m.updateSlashArg(ln)
		return nil
	}
	ch, _ := m.composerTarget()
	teamID := m.commandTeamID(ch)
	cmd := m.ensureServerCommands(teamID)
	if m.slash.active && !m.slash.arg && m.slash.query == ln.query {
		return cmd
	}
	m.slash = slashState{active: true, start: ln.start, query: ln.query, items: m.slashMatches(ln.query, teamID)}
	if len(m.slash.items) == 0 {
		m.closeSlash()
	}
	return cmd
}

// updateSlashArg drives the popup's second half: the rows a command offers for
// its argument, e.g. every kaomoji after "/kaomoji ". Commands without
// argValues — /me, the effect commands, anything server-side — close it so
// typing a free-text argument is unobstructed.
func (m *Model) updateSlashArg(ln slashLine) {
	c, ok := lookupSlash(ln.name)
	if !ok || c.argValues == nil {
		m.closeSlash()
		return
	}
	// The @-mention / :emoji pickers are recomputed just before this one and
	// can legitimately trigger inside an argument ("/tmpl @al"); leave them the
	// single popup slot, so only one is ever active.
	if m.mention.active || m.emoji.active {
		m.closeSlash()
		return
	}
	if m.slash.active && m.slash.arg && m.slash.cmd == ln.name &&
		m.slash.query == ln.query && m.slash.start == ln.start {
		return
	}
	items := slashArgMatches(ln.query, c.argValues(m))
	if len(items) == 0 {
		m.closeSlash()
		return
	}
	m.slash = slashState{active: true, arg: true, cmd: ln.name, start: ln.start, query: ln.query, items: items}
}

// slashArgMatches fuzzy-filters a command's argument rows against the typed
// query and caps the popup height. The sort is stable within a match band, so
// a list the command handed over in a meaningful order (kaomoji: most-used
// first) keeps it — in particular for the empty query, where everything ties.
func slashArgMatches(query string, vals []slashArg) []slashCandidate {
	type cand struct {
		slashCandidate
		band  int
		score int
	}
	cands := make([]cand, 0, len(vals))
	for _, v := range vals {
		if v.value == "" {
			continue
		}
		band, score, ok := fuzzyScore(strings.ToLower(v.value), query)
		if !ok {
			continue
		}
		cands = append(cands, cand{slashCandidate{trigger: v.value, desc: v.desc}, band, score})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.band != b.band {
			return a.band < b.band
		}
		return a.score < b.score
	})
	if len(cands) > slashLimit {
		cands = cands[:slashLimit]
	}
	out := make([]slashCandidate, len(cands))
	for i, c := range cands {
		out[i] = c.slashCandidate
	}
	return out
}

// closeSlash clears the popup.
func (m *Model) closeSlash() {
	if !m.slash.active {
		return
	}
	m.slash = slashState{}
}

// ensureServerCommands returns a Cmd that fetches and caches teamID's
// autocomplete command list the first time it's needed, or nil when the team
// is unknown or a fetch has already been started. Once-per-team, mirroring the
// custom-emoji list cache.
func (m *Model) ensureServerCommands(teamID string) tea.Cmd {
	if teamID == "" || m.serverCmdsReq[teamID] {
		return nil
	}
	m.serverCmdsReq[teamID] = true
	return m.fetchServerCommands(teamID)
}

// serverCommandsMsg carries a team's fetched autocomplete command list (or the
// error). teamID keys it back into the cache.
type serverCommandsMsg struct {
	teamID string
	cmds   []serverCommand
	err    error
}

// fetchServerCommands loads a team's autocomplete-enabled commands in the
// background and maps them to serverCommand for the popup cache.
func (m Model) fetchServerCommands(teamID string) tea.Cmd {
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		cmds, err := client.AutocompleteCommands(ctx, teamID)
		if err != nil {
			return serverCommandsMsg{teamID: teamID, err: err}
		}
		out := make([]serverCommand, 0, len(cmds))
		for _, c := range cmds {
			if c == nil || c.Trigger == "" {
				continue
			}
			out = append(out, serverCommand{
				trigger: strings.ToLower(c.Trigger),
				desc:    c.AutoCompleteDesc,
				hint:    c.AutoCompleteHint,
			})
		}
		return serverCommandsMsg{teamID: teamID, cmds: out}
	}
}

// slashMatches merges the built-in commands (slashRegistry) with teamID's
// cached server commands and returns up to slashLimit ranked candidates for
// the query. Built-ins are added first and win a trigger/alias clash, so a
// command we handle locally (e.g. /me) shows once and without a cloud glyph;
// server-only commands carry server=true. Ranking mirrors the emoji/mention
// pickers: match quality (fuzzyScore band), then built-in before server, then
// finer match position, then trigger name. Pointer receiver — this runs per
// keystroke while the popup is open, so it must not copy the ~133KB Model.
func (m *Model) slashMatches(query, teamID string) []slashCandidate {
	type cand struct {
		slashCandidate
		band  int
		score int
	}
	var cands []cand
	seen := map[string]struct{}{}
	// consider adds c if any of names fuzzy-matches the query and none has been
	// claimed yet (so a built-in's aliases also block a duplicate server entry).
	// Matching uses the best score across all names; the row shows c.trigger.
	consider := func(c slashCandidate, names ...string) {
		for _, n := range names {
			if _, dup := seen[n]; dup {
				return
			}
		}
		band, score, ok := bestFuzzy(query, names)
		if !ok {
			return
		}
		for _, n := range names {
			seen[n] = struct{}{}
		}
		cands = append(cands, cand{slashCandidate: c, band: band, score: score})
	}
	for _, b := range slashRegistry() {
		names := append([]string{b.name}, b.aliases...)
		consider(slashCandidate{trigger: b.name, desc: b.desc, hint: b.args}, names...)
	}
	for _, s := range m.serverCmds[teamID] {
		consider(slashCandidate{trigger: s.trigger, desc: s.desc, hint: s.hint, server: true}, s.trigger)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.band != b.band {
			return a.band < b.band
		}
		if a.server != b.server {
			return !a.server // built-in before server within a tier
		}
		if a.score != b.score {
			return a.score < b.score
		}
		return a.trigger < b.trigger
	})
	if len(cands) > slashLimit {
		cands = cands[:slashLimit]
	}
	out := make([]slashCandidate, len(cands))
	for i, c := range cands {
		out[i] = c.slashCandidate
	}
	return out
}

// bestFuzzy returns the best (lowest band, then lowest score) fuzzyScore of
// query against any of names, so a command matches on its aliases too.
func bestFuzzy(query string, names []string) (band, score int, ok bool) {
	for _, n := range names {
		b, s, o := fuzzyScore(n, query)
		if !o {
			continue
		}
		if !ok || b < band || (b == band && s < score) {
			band, score, ok = b, s, true
		}
	}
	return band, score, ok
}

// acceptSlash takes the highlighted row: over the command word it replaces the
// typed "/<query>" with "/<trigger> " (a trailing space so an argument can
// follow, which immediately opens the argument popup for a command that has
// one); over an argument it fills the value in place, with no trailing space,
// so the popup closes and the next enter runs the command. Returns (cmd, true)
// on success, or (nil, false) when there's nothing usable so the caller falls
// through to the default key handler.
func (m *Model) acceptSlash() (tea.Cmd, bool) {
	if !m.slash.active || m.slash.idx < 0 || m.slash.idx >= len(m.slash.items) {
		return nil, false
	}
	it := m.slash.items[m.slash.idx]
	if it.trigger == "" {
		return nil, false
	}
	ln, ok := m.slashLineAt()
	if !ok || ln.arg != m.slash.arg {
		return nil, false
	}
	lines := strings.Split(m.input.Value(), "\n")
	if len(lines) == 0 {
		return nil, false
	}
	// Keep whatever follows the cursor on the line either way.
	if ln.arg {
		lines[0] = string(ln.runes[:ln.start]) + it.trigger + string(ln.runes[ln.col:])
	} else {
		lines[0] = "/" + it.trigger + " " + string(ln.runes[ln.col:])
	}
	m.history.checkpoint(m.composerContextKey(), m.input.Value())
	m.input.SetValue(strings.Join(lines, "\n"))
	m.syncInputHeight()
	wasArg := m.slash.arg
	m.closeSlash()
	if wasArg {
		// Leave the popup shut: the filled-in argument still matches its own
		// row, so re-opening here would make the next enter re-accept it
		// instead of running the command. The next keystroke reopens it.
		return m.updateCommandHighlight(), true
	}
	// The accepted "/trigger " is a recognised command — light it up (bold +
	// animated) straight away rather than waiting for the next keystroke, and
	// re-run the popup so a command with argValues offers them in the same
	// keypress.
	return tea.Batch(m.updateCommandHighlight(), m.updateSlash()), true
}

// slashPopupStyle reuses the mention/emoji dropdown frame vocabulary.
var slashPopupStyle = lipgloss.NewStyle().
	Border(border).
	BorderForeground(focusedColor).
	Padding(0, 1)

// renderSlashPopup returns the "/" dropdown — commands, or a command's
// argument values — or "" when it shouldn't show. Server/plugin commands are
// tagged with the cloud glyph (built-ins get a same-width blank so the command
// column lines up); the description is dimmed to the side. Mirrors
// renderEmojiPopup. width is the messages-pane width; each row is truncated to
// fit so a long description stays on one line instead of wrapping (the popup's
// border+padding eat 4 columns).
func (m *Model) renderSlashPopup(width int) string {
	if !m.slash.active || len(m.slash.items) == 0 {
		return ""
	}
	maxw := width - 6
	if maxw < 12 {
		maxw = 12
	}
	dim := lipgloss.NewStyle().Foreground(dimColor)
	rows := make([]string, 0, len(m.slash.items))
	for i, it := range m.slash.items {
		// Argument rows are the bare value (a kaomoji name, a template name);
		// command rows carry the "/", the cloud column and the usage hint.
		head := it.trigger
		if !m.slash.arg {
			icon := "  " // built-in: blank under the cloud column
			if it.server {
				icon = slashCloud + " "
			}
			head = icon + "/" + it.trigger
			if it.hint != "" {
				head += " " + it.hint
			}
		}
		if i == m.slash.idx {
			// Don't dim on the highlighted row — the dim foreground against the
			// selection background is barely legible (as in the emoji popup).
			// Truncate before styling so the selection bar covers exactly the
			// visible text.
			line := head
			if it.desc != "" {
				line += "  " + it.desc
			}
			rows = append(rows, selectedRow.Render(ansi.Truncate(line, maxw, "…")))
			continue
		}
		line := head
		if it.desc != "" {
			line += "  " + dim.Render(it.desc)
		}
		rows = append(rows, ansi.Truncate(line, maxw, "…"))
	}
	return slashPopupStyle.Render(strings.Join(rows, "\n"))
}
