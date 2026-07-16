package ui

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/game/kurve"
)

// Achtung, die Kurve: up to six curves race across an arena leaving a solid
// trail, steering only left or right, and the last curve still moving takes the
// round. The whole match lives inside a Mattermost post — the same transport
// Gorillas rides (internal/ui/gorillas.go), pushed at a game it was not obviously
// built for.
//
// Gorillas is turn-based: a discrete shot maps cleanly onto a discrete post edit.
// Achtung is real-time, and its world grows without bound — a trail is hundreds
// of cells a second, not a handful of craters. Streaming the trail would blow the
// post size in seconds, so it is never streamed. What travels is the *recipe*: a
// seed plus each player's short log of steering changes, from which the joiner
// replays the identical simulation and rebuilds every pixel. See internal/game/
// kurve, and its FromState in particular.
//
// The wiring is Gorillas' wiring, widened to a crowd:
//
//	The host's post IS the world. The host is the sole simulator; it rewrites
//	that post every tick and no one else touches it. Each joiner replies once per
//	steering change, and that reply IS their controller. So no post is ever
//	written by two clients, and none of this needs conflict resolution.
//
//	Joining is a reaction. React :video_game: to a game post in its lobby and you
//	are the next player — the magic prefix on the payload (MBK1 vs Gorillas' MBG1)
//	is what lets both games share the one reaction without ever being confused.
//	Several people can react; the host seats each one, keyed by user id, and starts
//	the match on its own word once the lobby has filled.
//
// The one thing the medium cannot hide is latency: a joiner's steering has to
// round-trip through a post edit and back as streamed state, so their curve
// answers a beat after the key. That is the honest cost of playing a twitch game
// down a wire made of chat messages, and it is why steering is a held *level*
// (press to set a direction, it holds) rather than a per-frame stream — one edit
// per change keeps the wire quiet and degrades gracefully when a change is late.
const (
	// kurveJoinEmoji claims the next player slot. Shared with Gorillas on purpose;
	// the payload magic disambiguates.
	kurveJoinEmoji = "video_game"

	// kurveFrameDelay is one tick: a simulation step, a render, and a PATCH of the
	// world post. A hair slower than Gorillas' flight stream, because a whole tick
	// of work rides each one and the curves are tuned to that cadence.
	kurveFrameDelay = 45 * time.Millisecond

	// The ASCII board other Mattermost clients see, in characters. Wider than tall
	// because a terminal cell is ~twice as tall as it is wide, so a square-ish
	// arena needs a wide char grid to look square.
	kurveBoardCols = 56
	kurveBoardRows = 21
)

// kurvePlayer is the host's registry entry for one joiner, keyed in kurveState by
// user id. It ties that user's controller post and steering sequence to their
// curve index, so a controller edit from any of up to five joiners routes to the
// right player.
type kurvePlayer struct {
	idx     int    // player index in the match (1..k; 0 is the host)
	replyID string // their controller post's id, once it has been seen
	lastSeq uint8  // last controller sequence consumed, to drop a re-delivered edit
}

// kurveState is one open game. Zero value = closed. It mirrors gorillasState; see
// there for the reasoning behind gen, solo, the host/joiner post split, and the
// rendering/pending frame coalescing.
type kurveState struct {
	active bool
	gen    int

	role int  // 0 = host (owns the world post, runs the simulation), 1 = joiner
	solo bool // hotseat: one client drives two curves

	channelID string
	postID    string // the world post — the host's

	match *kurve.Match

	rend       kurve.Renderer
	imgID      uint32
	rows, cols int

	rendering bool
	pending   bool

	// players is the host's controller-post registry, keyed by joiner user id: how
	// the host maps a controller reply (identified by its author) to a curve.
	// Empty on a joiner.
	players map[string]*kurvePlayer

	// me is this client's own player index. 0 for the host; a joiner learns its
	// index (1..k) from the roster the host broadcasts in the state. replyID and
	// seq are the joiner's own controller post and its change counter.
	me      int
	replyID string
	seq     uint8

	names  []string // display names in index order, one per player (index 0 = host)
	status string
}

// myIndex is the curve this client steers online: the host is always 0, a joiner
// is whatever slot the host seated it in.
func (g *kurveState) myIndex() int {
	if g.role == 0 {
		return 0
	}
	return g.me
}

// scorerName resolves a player index to a display name, tolerating an index that
// somehow falls outside the roster.
func (g *kurveState) scorerName(i int) string {
	if i >= 0 && i < len(g.names) {
		return g.names[i]
	}
	return "someone"
}

type (
	// kurvePostedMsg lands when the host's world post exists.
	kurvePostedMsg struct {
		gen  int
		post *model.Post
		err  error
	}
	// kurveJoinedMsg lands when a joiner's controller reply exists.
	kurveJoinedMsg struct {
		gen  int
		post *model.Post
		err  error
	}
	// kurveTickMsg advances the simulation one tick.
	kurveTickMsg struct{ gen int }
	// kurveResumedMsg carries a reopened game's thread back, so a player who stepped
	// away can re-attach to the controller posts.
	kurveResumedMsg struct {
		gen    int
		thread *model.PostList
		err    error
	}
	// kurveFrameMsg carries an encoded kitty frame back from the render Cmd.
	kurveFrameMsg struct {
		gen int
		seq string
		err error
	}
)

// runKurve is the `> Achtung, die Kurve` command.
func runKurve(m *Model, _ string) tea.Cmd { return m.startKurve(false) }

// runKurveSolo is the hotseat command: one client, two curves. As with Gorillas
// it exists because you cannot react your way into your own game — and because
// the post still streams, the whole thing stays watchable from any client.
func runKurveSolo(m *Model, _ string) tea.Cmd { return m.startKurve(true) }

func (m *Model) startKurve(solo bool) tea.Cmd {
	channelID, label := m.indexTargetChannel()
	if channelID == "" {
		m.status = "kurve: no channel selected"
		return nil
	}
	if m.emojiImg == nil || !m.emojiImg.active() {
		m.status = "kurve: needs a terminal with Kitty graphics support"
		return nil
	}

	match := kurve.NewMatch(uint16(rand.IntN(1 << 16)))
	me := m.kurveName(m.me.Id)

	names := []string{me}
	status := "waiting for someone to react :" + kurveJoinEmoji + ": to join"
	if solo {
		// Hotseat is a two-player game the host owns outright, so it skips the lobby
		// and starts counting down at once.
		match.AddPlayer(m.me.Id)
		match.Start()
		names = append(names, me+" (2)")
		status = "hotseat — you steer both curves"
	}

	m.kurve = kurveState{
		active:    true,
		gen:       m.kurve.gen + 1,
		role:      0,
		solo:      solo,
		channelID: channelID,
		match:     match,
		imgID:     m.emojiImg.allocID(),
		players:   map[string]*kurvePlayer{},
		names:     names,
		status:    status,
	}
	m.sizeKurve()

	gen := m.kurve.gen
	body := kurveBody(match, names)
	client, ctx := m.client, m.ctx
	m.status = "kurve: posted a game in " + label

	cmds := []tea.Cmd{
		func() tea.Msg {
			p, err := client.Send(ctx, channelID, "", body, nil)
			return kurvePostedMsg{gen: gen, post: p, err: err}
		},
		m.kurveFrameCmd(),
	}
	if solo {
		cmds = append(cmds, kurveTickCmd(gen))
	}
	return tea.Batch(cmds...)
}

// kurveBody builds the post: a header with the scoreboard, an ASCII board other
// clients can watch, then the invisible state blob.
func kurveBody(mt *kurve.Match, names []string) string {
	st := kurve.WireState(mt)
	var b strings.Builder
	fmt.Fprintf(&b, "🌀 **Achtung, die Kurve** — first to %d\n", kurve.WinScore)
	for i, name := range names {
		if i > 0 {
			b.WriteString(" · ")
		}
		score := 0
		if i < len(st.Scores) {
			score = int(st.Scores[i])
		}
		fmt.Fprintf(&b, "%s %d", name, score)
	}
	switch {
	case st.Phase == kurve.PhaseLobby:
		b.WriteString("  ·  _react :" + kurveJoinEmoji + ": to join_")
	case st.Phase == kurve.PhaseOver && st.Winner >= 0 && int(st.Winner) < len(names):
		b.WriteString("  ·  **" + names[st.Winner] + " wins**")
	}
	b.WriteByte('\n')
	b.WriteString(kurve.ASCIIBoard(mt.Sim, kurveBoardCols, kurveBoardRows))
	b.WriteByte('\n')
	b.WriteString(kurve.Encode(kurve.MarshalState(st)))
	return b.String()
}

// kurveController is a joiner's post body: a visible label and their steer.
func kurveController(in *kurve.Input) string {
	return "🌀 _controller_\n" + kurve.Encode(kurve.MarshalInput(in))
}

// applyKurvePosted records the world post's id. From here on every frame edits it.
func (m *Model) applyKurvePosted(msg kurvePostedMsg) tea.Cmd {
	if !m.kurve.active || msg.gen != m.kurve.gen {
		return nil
	}
	if msg.err != nil {
		m.status = "kurve: " + msg.err.Error()
		return m.closeKurve()
	}
	m.kurve.postID = msg.post.Id
	return nil
}

// kurveJoin opens the modal for a player who reacted into someone else's game and
// posts their controller reply. It seeds the roster from what is already on the
// wire; the host is the definitive authority on indices and broadcasts them back.
func (m *Model) kurveJoin(post *model.Post, st *kurve.State) tea.Cmd {
	if m.emojiImg == nil || !m.emojiImg.active() {
		m.status = "kurve: needs a terminal with Kitty graphics support"
		return nil
	}

	names := []string{m.kurveName(post.UserId)}
	me := 0
	for j, id := range st.Joiners {
		names = append(names, m.kurveName(id))
		if id == m.me.Id {
			me = j + 1
		}
	}

	m.kurve = kurveState{
		active:    true,
		gen:       m.kurve.gen + 1,
		role:      1,
		channelID: post.ChannelId,
		postID:    post.Id,
		match:     kurve.FromState(st),
		imgID:     m.emojiImg.allocID(),
		me:        me,
		names:     names,
		status:    "joining…",
	}
	m.sizeKurve()

	gen := m.kurve.gen
	body := kurveController(&kurve.Input{Seq: 0})
	client, ctx := m.client, m.ctx
	channelID, rootID := post.ChannelId, post.Id
	return tea.Batch(
		func() tea.Msg {
			p, err := client.Send(ctx, channelID, rootID, body, nil)
			return kurveJoinedMsg{gen: gen, post: p, err: err}
		},
		m.kurveFrameCmd(),
	)
}

func (m *Model) applyKurveJoined(msg kurveJoinedMsg) tea.Cmd {
	if !m.kurve.active || msg.gen != m.kurve.gen {
		return nil
	}
	if msg.err != nil {
		m.status = "kurve: " + msg.err.Error()
		return m.closeKurve()
	}
	m.kurve.replyID = msg.post.Id
	m.kurve.status = "waiting for the host…"
	return nil
}

// kurveResume steps a player back into a game they had closed. The world post
// carries the whole match — seed, scores, and every steering log — so this
// rebuilds it with FromState and re-establishes the wire, as the host (the post's
// author) or a joiner (found in State.Joiners). Because the host is the sole clock,
// a host that left froze the match; resuming restarts the ticks and the curves move
// again. Controller re-attachment happens in applyKurveResumed once the thread is
// in.
func (m *Model) kurveResume(post *model.Post, st *kurve.State, role int) tea.Cmd {
	if m.emojiImg == nil || !m.emojiImg.active() {
		m.status = "kurve: needs a terminal with Kitty graphics support"
		return nil
	}
	match := kurve.FromState(st)
	names := []string{m.kurveName(post.UserId)}
	me := 0
	players := map[string]*kurvePlayer{}
	for j, id := range st.Joiners {
		names = append(names, m.kurveName(id))
		if id == m.me.Id {
			me = j + 1
		}
		players[id] = &kurvePlayer{idx: j + 1}
	}

	m.kurve = kurveState{
		active:    true,
		gen:       m.kurve.gen + 1,
		role:      role,
		channelID: post.ChannelId,
		postID:    post.Id,
		match:     match,
		imgID:     m.emojiImg.allocID(),
		me:        me,
		names:     names,
	}
	if role == 0 {
		// The registry maps each joiner's id to their curve; the host needs it to
		// route controller edits, and it is rebuilt from the roster on the wire.
		m.kurve.players = players
	}
	m.kurve.status = m.kurveResumeStatus()
	m.sizeKurve()
	m.status = "kurve: rejoined"

	gen := m.kurve.gen
	client, ctx := m.client, m.ctx
	rootID := post.Id
	cmds := []tea.Cmd{
		m.kurveFrameCmd(),
		func() tea.Msg {
			pl, err := client.Thread(ctx, rootID)
			return kurveResumedMsg{gen: gen, thread: pl, err: err}
		},
	}
	if role == 0 {
		cmds = append(cmds, m.kurvePush())
		if match.Busy() {
			// The clock stopped when the host left; restart it so the curves move.
			cmds = append(cmds, kurveTickCmd(gen))
		}
	}
	return tea.Batch(cmds...)
}

// applyKurveResumed re-attaches to the controller posts after a resume. For a
// joiner that is their own post, so their steer count continues; for the host it is
// every joiner's, so a re-delivered edit is dropped and each curve holds the
// direction it was last steered. Unlike Gorillas the host routes inputs by thread
// root and author, not by post id, so a missing controller here is not fatal — the
// re-attach only tightens dedup and restores held directions.
func (m *Model) applyKurveResumed(msg kurveResumedMsg) tea.Cmd {
	g := &m.kurve
	if !g.active || msg.gen != g.gen {
		return nil
	}
	if msg.err != nil {
		m.status = "kurve: " + msg.err.Error()
		return nil
	}
	if g.role == 1 {
		if ctrl, in := kurveControllerInThread(msg.thread, m.me.Id); ctrl != nil {
			g.replyID = ctrl.Id
			g.seq = in.Seq
		}
		return nil
	}
	if msg.thread == nil {
		return nil
	}
	for _, p := range msg.thread.Posts {
		if p == nil || p.DeleteAt != 0 {
			continue
		}
		pl := g.players[p.UserId]
		if pl == nil {
			continue
		}
		payload, ok := kurve.Decode(p.Message)
		if !ok {
			continue
		}
		in, err := kurve.UnmarshalInput(payload)
		if err != nil {
			continue
		}
		pl.replyID = p.Id
		pl.lastSeq = in.Seq
		g.match.Steer(pl.idx, in.Dir) // honour the direction they hold right now
	}
	return nil
}

// kurveControllerInThread finds a controller reply authored by owner in a fetched
// thread, with the input it currently holds.
func kurveControllerInThread(pl *model.PostList, owner string) (*model.Post, *kurve.Input) {
	if pl == nil || owner == "" {
		return nil, nil
	}
	for _, p := range pl.Posts {
		if p == nil || p.DeleteAt != 0 || p.UserId != owner {
			continue
		}
		payload, ok := kurve.Decode(p.Message)
		if !ok {
			continue
		}
		if in, err := kurve.UnmarshalInput(payload); err == nil {
			return p, in
		}
	}
	return nil, nil
}

// kurveResumeStatus is the footer line for a game just reopened, phrased for the
// player who reopened it.
func (m *Model) kurveResumeStatus() string {
	g := &m.kurve
	st := g.match
	switch {
	case st.Phase == kurve.PhaseOver && st.Winner >= 0:
		return g.scorerName(int(st.Winner)) + " wins the match"
	case st.Phase == kurve.PhaseLobby && g.role == 0 && st.PlayerCount() > 1:
		return fmt.Sprintf("%d players — press enter to start", st.PlayerCount())
	case st.Phase == kurve.PhaseLobby && g.role == 0:
		return "waiting for someone to react :" + kurveJoinEmoji + ": to join"
	case st.Phase == kurve.PhaseLobby:
		return "waiting for the host to start…"
	case st.Phase == kurve.PhaseCountdown:
		return "get ready…"
	case st.Phase == kurve.PhaseRoundOver:
		return "round over"
	default: // PhaseRun
		idx := g.myIndex()
		if idx >= 0 && idx < len(st.Sim.Curves) && st.Sim.Curves[idx].Dead {
			return "you crashed — watching…"
		}
		return "steer!"
	}
}

// kurveRegister seats a joiner: it assigns them the next curve index, records the
// registry entry keyed by their user id, and appends their name. It is idempotent
// — a repeated reaction, or the reply and the reaction racing, both land here and
// the second call is a no-op. Returns the entry and whether it was newly created,
// or (nil,false) if the lobby is closed or full.
func (m *Model) kurveRegister(userID string) (*kurvePlayer, bool) {
	g := &m.kurve
	if pl := g.players[userID]; pl != nil {
		return pl, false
	}
	idx := g.match.AddPlayer(userID)
	if idx < 0 {
		return nil, false
	}
	pl := &kurvePlayer{idx: idx}
	g.players[userID] = pl
	g.names = append(g.names, m.kurveName(userID))
	g.status = fmt.Sprintf("%d players — press enter to start", g.match.PlayerCount())
	return pl, true
}

// kurveAcceptJoin is the host's side of the join handshake: someone reacted, so
// they take the next curve. Unlike the old two-player game this does not start the
// match — the lobby stays open for more players until the host presses enter.
func (m *Model) kurveAcceptJoin(userID string) tea.Cmd {
	g := &m.kurve
	if !g.active || g.role != 0 || g.solo ||
		g.match.Phase != kurve.PhaseLobby || userID == m.me.Id {
		return nil
	}
	if _, added := m.kurveRegister(userID); !added {
		return nil
	}
	return tea.Batch(m.kurvePush(), m.kurveFrameCmd())
}

// kurveStartMatch is the host committing the roster and starting the countdown —
// which also starts the clock. It needs at least one joiner.
func (m *Model) kurveStartMatch() tea.Cmd {
	g := &m.kurve
	if !g.active || g.role != 0 || g.solo || g.match.Phase != kurve.PhaseLobby {
		return nil
	}
	if !g.match.Start() {
		g.status = "waiting for someone to react :" + kurveJoinEmoji + ": to join"
		return nil
	}
	g.status = "get ready…"
	return tea.Batch(m.kurvePush(), m.kurveFrameCmd(), kurveTickCmd(g.gen))
}

// kurvePush writes the current state back to the world post. Host only.
func (m *Model) kurvePush() tea.Cmd {
	g := &m.kurve
	if g.role != 0 || g.postID == "" {
		return nil
	}
	return m.editPost(g.postID, kurveBody(g.match, g.names))
}

// kurveTickCmd schedules the next tick.
func kurveTickCmd(gen int) tea.Cmd {
	return tea.Tick(kurveFrameDelay, func(time.Time) tea.Msg {
		return kurveTickMsg{gen: gen}
	})
}

// applyKurveTick advances the world one tick: step the rules, stream the new
// state, redraw. Host only — a joiner is driven by the host's edits arriving over
// the websocket, not by a clock of its own.
func (m *Model) applyKurveTick(msg kurveTickMsg) tea.Cmd {
	g := &m.kurve
	if !g.active || msg.gen != g.gen || g.role != 0 {
		return nil
	}

	ev := g.match.Step()
	switch ev.Kind {
	case kurve.EvRound:
		if ev.Draw {
			g.status = "draw"
		} else {
			g.status = g.scorerName(ev.Scorer) + " takes the round"
		}
	case kurve.EvMatch:
		g.status = g.scorerName(ev.Scorer) + " wins the match"
	case kurve.EvCountdown:
		g.status = "get ready…"
	case kurve.EvRunning:
		// The round no longer ends on the host's own crash — the others play on — so
		// tell the host it is watching rather than leaving it a stale "steer!".
		if !g.solo && g.match.Sim.Curves[0].Dead {
			g.status = "you crashed — watching…"
		} else {
			g.status = "steer!"
		}
	}

	var next tea.Cmd
	if g.match.Busy() {
		next = kurveTickCmd(g.gen)
	}
	return tea.Batch(m.kurveFrameCmd(), m.kurvePush(), next)
}

// kurveApplyState adopts a state that arrived from the host. Joiner only: it
// replays the streamed steering logs into a fresh world, rebuilds the roster (and
// finds its own curve in it), and redraws.
func (m *Model) kurveApplyState(st *kurve.State) tea.Cmd {
	g := &m.kurve
	if !g.active || g.role != 1 {
		return nil
	}
	g.match = kurve.FromState(st)

	// Keep the host at index 0 (its name was learnt from the world post's author
	// when we joined) and rebuild the joiners from the wire, in index order.
	host := ""
	if len(g.names) > 0 {
		host = g.names[0]
	}
	names := []string{host}
	g.me = 0
	for j, id := range st.Joiners {
		names = append(names, m.kurveName(id))
		if id == m.me.Id {
			g.me = j + 1
		}
	}
	g.names = names

	switch {
	case st.Phase == kurve.PhaseOver && st.Winner >= 0:
		g.status = g.scorerName(int(st.Winner)) + " wins the match"
	case st.Phase == kurve.PhaseLobby:
		g.status = "waiting for the host to start…"
	case st.Phase == kurve.PhaseCountdown:
		g.status = "get ready…"
	case st.Phase == kurve.PhaseRoundOver:
		g.status = "round over"
	case g.me > 0 && g.me < len(g.match.Sim.Curves) && g.match.Sim.Curves[g.me].Dead:
		g.status = "you crashed — watching…"
	default:
		g.status = "steer!"
	}
	return m.kurveFrameCmd()
}

// kurveApplyInput is the host consuming a joiner's controller post: a steering
// change for whichever curve that user owns, found by user id in the registry.
// The sequence number lets a re-delivered edit be ignored; applying a held
// direction is idempotent regardless.
func (m *Model) kurveApplyInput(userID string, in *kurve.Input) tea.Cmd {
	g := &m.kurve
	if !g.active || g.role != 0 {
		return nil
	}
	pl := g.players[userID]
	if pl == nil || in.Seq == pl.lastSeq {
		return nil
	}
	pl.lastSeq = in.Seq
	g.match.Steer(pl.idx, in.Dir)
	return nil
}

// kurveFrameCmd renders and encodes the current frame off the UI goroutine. Like
// Gorillas it gates on g.rendering so only one frame is ever in flight against the
// shared Renderer buffer, and a state that arrives mid-render sets g.pending
// rather than starting a second render.
func (m *Model) kurveFrameCmd() tea.Cmd {
	g := &m.kurve
	if !g.active || g.cols == 0 || g.rows == 0 || g.match == nil {
		return nil
	}
	if g.rendering {
		g.pending = true
		return nil
	}
	g.rendering = true

	gen, id := g.gen, g.imgID
	rows, cols := g.rows, g.cols
	pxW, pxH := cols*m.cellPxOr(8), rows*m.cellPxHOr(16)
	rend := &g.rend
	// Capture the Sim pointer (not g.match): newRound swaps it, and the render must
	// read one consistent world even if the clock moves on underneath.
	sim, phase, countdown := g.match.Sim, g.match.Phase, g.match.Countdown

	return func() tea.Msg {
		img := rend.Render(sim, phase, countdown, pxW, pxH)
		seq, err := kittyTransmitImage(id, img, rows, cols)
		return kurveFrameMsg{gen: gen, seq: seq, err: err}
	}
}

// applyKurveFrame writes the encoded frame out of band. Nothing re-renders:
// re-transmitting under the same image id repaints the placeholder cells already
// on screen, so a moving curve costs the View() hot path nothing.
func (m *Model) applyKurveFrame(msg kurveFrameMsg) tea.Cmd {
	g := &m.kurve
	if !g.active || msg.gen != g.gen {
		return nil
	}
	g.rendering = false
	if msg.err != nil {
		g.status = "render: " + msg.err.Error()
		return nil
	}
	var next tea.Cmd
	if g.pending {
		g.pending = false
		next = m.kurveFrameCmd()
	}
	return tea.Batch(tea.Raw(msg.seq), next)
}

// handleKurveKey routes keys while the modal owns the screen. Steering is a held
// level: a press sets a direction that holds until the next press. The host also
// starts the match from the lobby with enter.
func (m *Model) handleKurveKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		return m, m.closeKurve()
	case "enter":
		if m.kurve.role == 0 && !m.kurve.solo {
			return m, m.kurveStartMatch()
		}
	}
	if player, dir, ok := m.kurveSteerKey(msg.String()); ok {
		return m, m.kurveSteer(player, dir)
	}
	return m, nil
}

// kurveSteerKey maps a keystroke to (player, direction). Online, every steer key
// drives your own curve. Hotseat splits the keyboard: WASD-ish left for player
// one, the arrows for player two.
func (m *Model) kurveSteerKey(key string) (player int, dir kurve.Dir, ok bool) {
	g := &m.kurve
	if g.solo {
		switch key {
		case "a":
			return 0, kurve.Left, true
		case "d":
			return 0, kurve.Right, true
		case "w", "s":
			return 0, kurve.Straight, true
		case "left":
			return 1, kurve.Left, true
		case "right":
			return 1, kurve.Right, true
		case "up", "down":
			return 1, kurve.Straight, true
		}
		return 0, 0, false
	}

	me := g.myIndex()
	switch key {
	case "left", "a":
		return me, kurve.Left, true
	case "right", "d":
		return me, kurve.Right, true
	case "up", "down", "space", "s", "w":
		return me, kurve.Straight, true
	}
	return 0, 0, false
}

// kurveSteer applies a steering direction. The host and hotseat feed the
// simulation directly; a joiner writes it to their controller post and lets the
// host apply it — the same host-simulates-everything split as Gorillas' fire.
func (m *Model) kurveSteer(player int, dir kurve.Dir) tea.Cmd {
	g := &m.kurve
	if g.match == nil || !g.match.CanSteer(player) {
		return nil
	}
	if g.role == 1 {
		g.seq++
		return m.editPost(g.replyID, kurveController(&kurve.Input{Dir: dir, Seq: g.seq}))
	}
	g.match.Steer(player, dir)
	return nil
}

// closeKurve tears the game down and frees the terminal's copy of the image.
func (m *Model) closeKurve() tea.Cmd {
	g := &m.kurve
	if !g.active {
		return nil
	}
	id, gen := g.imgID, g.gen
	m.kurve = kurveState{gen: gen + 1}
	m.status = "kurve: closed"
	if id != 0 {
		return tea.Raw(kittyDelete(id))
	}
	return nil
}

// sizeKurve fits the arena to the terminal. The arena is 4:3 in field units and a
// field unit is square, so unlike Gorillas the only correction here is the
// terminal cell's own ~2:1 shape — measured in cells the box is nothing like the
// box measured in pixels.
func (m *Model) sizeKurve() {
	g := &m.kurve
	maxCols := max(m.width-6, 20)
	maxRows := max(m.height-8, 8)

	cw, ch := m.cellPxOr(8), m.cellPxHOr(16)
	cols := maxCols
	rows := int(float64(cols) * float64(cw) / (kurve.DisplayAspect * float64(ch)))
	if rows > maxRows {
		rows = maxRows
		cols = int(float64(rows) * kurve.DisplayAspect * float64(ch) / float64(cw))
	}
	g.cols = max(min(cols, maxCols), 10)
	g.rows = max(min(rows, maxRows), 5)
}

// renderKurve draws the modal. The field is a grid of Kitty placeholder cells —
// the pixels arrive out of band — so this stays cheap no matter what the curves
// are doing.
func (m *Model) renderKurve(_ int) string {
	g := &m.kurve
	if !g.active || g.match == nil {
		return ""
	}
	st := g.match

	title := " 🌀 Achtung, die Kurve "
	field := kittyPlaceholder(g.imgID, g.rows, g.cols)

	var footer strings.Builder
	mine := g.myIndex()
	for i, name := range g.names {
		if i > 0 {
			footer.WriteString("   ")
		}
		score := 0
		if i < len(st.Scores) {
			score = int(st.Scores[i])
		}
		label := name
		if !g.solo && i == mine {
			label += " (you)"
		}
		fmt.Fprintf(&footer, "%s %d", label, score)
	}
	fmt.Fprintf(&footer, "   · first to %d\n", kurve.WinScore)

	switch {
	case st.Phase == kurve.PhaseLobby && g.role == 0:
		fmt.Fprintf(&footer, "%s   · enter start · esc quit", g.status)
	case st.Phase == kurve.PhaseLobby:
		footer.WriteString(g.status + "   · esc quit")
	case st.Phase == kurve.PhaseCountdown:
		fmt.Fprintf(&footer, "get ready — %d…   · ← left · → right · space straight · esc quit", countdownNumber(st.Countdown))
	case st.Phase == kurve.PhaseOver:
		footer.WriteString(g.status + "   · esc quit")
	default:
		if g.solo {
			footer.WriteString("P1 a/d/s · P2 ←/→/↓ · esc quit")
		} else {
			footer.WriteString(g.status + "   · ← left · → right · space straight · esc quit")
		}
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(focusedColor).
		Padding(0, 1)

	return box.Render(lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render(title),
		field,
		lipgloss.NewStyle().Foreground(dimColor).Render(footer.String()),
	))
}

// countdownNumber maps the countdown's remaining ticks onto a 3-2-1.
func countdownNumber(ticks int) int {
	n := ticks/11 + 1
	return max(min(n, 3), 1)
}

// kurvePost reports whether postID belongs to the open game — the world post or
// any joiner's controller — so its frame churn is not persisted; the world post is
// edited ~20 times a second.
func (m *Model) kurvePost(postID string) bool {
	g := &m.kurve
	if !g.active || postID == "" {
		return false
	}
	if postID == g.postID || postID == g.replyID {
		return true
	}
	for _, pl := range g.players {
		if pl.replyID == postID {
			return true
		}
	}
	return false
}

// kurveName resolves a user id to something printable.
func (m *Model) kurveName(userID string) string {
	if n := m.userNames[userID]; n != "" {
		return n
	}
	return "…"
}

// kurveReaction is the join handshake, from both sides — the same two-clients-one-
// event trick as Gorillas.
func (m *Model) kurveReaction(r *model.Reaction) tea.Cmd {
	if r.EmojiName != kurveJoinEmoji {
		return nil
	}
	if m.kurve.active && m.kurve.role == 0 && r.PostId == m.kurve.postID {
		return m.kurveAcceptJoin(r.UserId)
	}
	if r.UserId != m.me.Id || m.kurve.active {
		return nil
	}
	p := m.findPostByID(r.PostId)
	if p == nil || p.UserId == m.me.Id {
		return nil
	}
	payload, ok := kurve.Decode(p.Message)
	if !ok {
		return nil
	}
	st, err := kurve.UnmarshalState(payload)
	if err != nil || st.Phase != kurve.PhaseLobby {
		return nil // not our game, or one already under way
	}
	return m.kurveJoin(p, st)
}

// kurveWSPosted lets the host discover a joiner's controller reply. As with
// Gorillas it does not insist the author already reacted: the reaction and the
// reply race, and the reply can win — either one seats the player, keyed by id.
func (m *Model) kurveWSPosted(p *model.Post) tea.Cmd {
	g := &m.kurve
	if !g.active || g.role != 0 || p.RootId != g.postID || p.UserId == m.me.Id {
		return nil
	}
	payload, ok := kurve.Decode(p.Message)
	if !ok {
		return nil
	}
	if _, err := kurve.UnmarshalInput(payload); err != nil {
		return nil
	}
	// If the roster has already closed and this author never joined, the reply is
	// a straggler with no seat — ignore it. Otherwise seat them (idempotently) and
	// record their controller post so its churn is not persisted.
	if g.match.Phase != kurve.PhaseLobby && g.players[p.UserId] == nil {
		return nil
	}
	pl, added := m.kurveRegister(p.UserId)
	if pl == nil {
		return nil // lobby full
	}
	pl.replyID = p.Id
	if added {
		return tea.Batch(m.kurvePush(), m.kurveFrameCmd())
	}
	return nil
}

// kurveWSEdited routes an edited post into the game: the world post feeds a
// joiner, a controller post feeds the host. The only place either client learns
// anything from the other.
func (m *Model) kurveWSEdited(p *model.Post) tea.Cmd {
	g := &m.kurve
	if !g.active {
		return nil
	}
	payload, ok := kurve.Decode(p.Message)
	if !ok {
		return nil
	}
	switch {
	case g.role == 1 && p.Id == g.postID:
		st, err := kurve.UnmarshalState(payload)
		if err != nil {
			return nil
		}
		return m.kurveApplyState(st)
	case g.role == 0 && p.RootId == g.postID && p.UserId != m.me.Id:
		in, err := kurve.UnmarshalInput(payload)
		if err != nil {
			return nil
		}
		return m.kurveApplyInput(p.UserId, in)
	}
	return nil
}

// resizeKurve re-fits the field to a resized terminal and re-transmits it. Driven
// from the resize settle, like Gorillas, so the placeholder grid and the image
// the terminal holds change size together.
func (m *Model) resizeKurve() tea.Cmd {
	if !m.kurve.active {
		return nil
	}
	m.sizeKurve()
	return m.kurveFrameCmd()
}
