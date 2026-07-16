package ui

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/game"
)

// Gorillas: two players lob explosive bananas across a city skyline, and the
// whole match lives inside a Mattermost post.
//
// The wiring, and why it is shaped this way:
//
//	The host's post IS the world. The host simulates, and rewrites that post ~30
//	times a second while a banana is in the air. The joiner never touches it.
//
//	The joiner replies in the thread, and that reply IS their controller. They
//	rewrite it once per shot. The host never touches it.
//
// So no post is ever written by two clients, and none of this needs conflict
// resolution — which matters, because Mattermost's edit API has no
// compare-and-swap and two clients editing one post would silently clobber each
// other. See internal/game/wire.go.
//
// Joining is a reaction. Anyone who reacts :video_game: to a game post in its
// lobby phase becomes player two, which makes a game discoverable without a
// command: it looks like a game, and reacting is the obvious thing to do.
//
// The rules themselves are not here — they live in game.Match, which is pure and
// is tested without a server or a terminal. This file is the plumbing: posts,
// websockets, ticks, keys, pixels.
const (
	// gorillasJoinEmoji is the reaction that claims player two.
	gorillasJoinEmoji = "video_game"

	// gorillasFrameDelay paces the flight stream. Each frame is one PATCH, and a
	// PATCH round-trips in ~24ms against a real server (see the live probe in
	// internal/mm), so this sits just inside what the wire can actually carry.
	gorillasFrameDelay = 33 * time.Millisecond

	// gorillasHeartbeat re-broadcasts the settled state while the host is parked
	// waiting for a shot. The turn hand-off is a single post edit, and the joiner
	// rebuilds whose turn it is entirely from those edits with no resync of its own
	// — so if the one edit that flips the turn is dropped (Mattermost does not
	// promise every post_edited in a rapid flight burst reaches every client), the
	// joiner deadlocks: it never learns it is up, its keys are swallowed by the
	// not-my-turn guard, and it cannot fire while the host waits forever. A slow
	// re-push of the resting state lets a joiner that missed the hand-off recover
	// within an interval, at the cost of one idle edit every couple of seconds.
	gorillasHeartbeat = 2 * time.Second

	// gorillasDT is simulated seconds per frame.
	gorillasDT = 0.05

	// The ASCII board that other Mattermost clients see, in characters.
	gorillasBoardCols = 64
	gorillasBoardRows = 18
)

// gorillasState is one open game. Zero value = closed.
type gorillasState struct {
	active bool

	// gen invalidates in-flight ticks and frames from a game that has been closed
	// or restarted, the same guard preview.go uses for its GIF loop. There is no
	// explicit cancel: a stale tick finds a gen that no longer matches and
	// evaporates.
	gen int

	// role: 0 = host (owns the world post and runs the simulation), 1 = joiner.
	role int
	// solo is hotseat: one client drives both gorillas. The post still streams for
	// real, so the match stays watchable from the webapp — which is how the whole
	// transport can be demonstrated without a second player.
	solo bool

	channelID string
	postID    string // the world post — the host's
	replyID   string // the controller post — the joiner's

	match *game.Match

	rend       game.Renderer
	imgID      uint32
	rows, cols int

	// rendering/pending coalesce frames. A render+encode takes ~6ms; if states
	// arrive faster than that (the joiner is fed by a ~30/s stream of WS edits), a
	// second render must not start while the first is still reading the shared
	// Renderer and World. Instead the newest state is remembered and drawn when
	// the outstanding frame lands, so the game drops frames rather than racing.
	rendering bool
	pending   bool

	angle, power string // what the player is typing
	onPower      bool

	seq     uint8 // joiner: shots fired
	lastSeq uint8 // host: last input consumed

	names  [2]string
	status string
}

type (
	// gorillasPostedMsg lands when the host's world post exists.
	gorillasPostedMsg struct {
		gen  int
		post *model.Post
		err  error
	}
	// gorillasJoinedMsg lands when the joiner's controller reply exists.
	gorillasJoinedMsg struct {
		gen  int
		post *model.Post
		err  error
	}
	// gorillasTickMsg advances the simulation one frame.
	gorillasTickMsg struct{ gen int }
	// gorillasHeartbeatMsg re-broadcasts the resting state so a joiner that dropped
	// the turn hand-off edit can catch up.
	gorillasHeartbeatMsg struct{ gen int }
	// gorillasResumedMsg carries the reopened game's thread back, so a player who
	// stepped away can re-attach to the joiner's controller post.
	gorillasResumedMsg struct {
		gen    int
		thread *model.PostList
		err    error
	}
	// gorillasFrameMsg carries an encoded kitty frame back from the render Cmd.
	gorillasFrameMsg struct {
		gen int
		seq string
		err error
	}
)

// runGorillas is the `> Gorillas` command: generate a city, post it, and wait for
// someone to react their way in.
func runGorillas(m *Model, _ string) tea.Cmd { return m.startGorillas(false) }

// runGorillasSolo is `> Gorillas (hotseat)`: one client, both gorillas. It exists
// because you cannot react your way into your own game — and because the post
// still streams, the whole transport can be watched live from any other
// Mattermost client without a second player being involved.
func runGorillasSolo(m *Model, _ string) tea.Cmd { return m.startGorillas(true) }

func (m *Model) startGorillas(solo bool) tea.Cmd {
	channelID, label := m.indexTargetChannel()
	if channelID == "" {
		m.status = "gorillas: no channel selected"
		return nil
	}
	if m.emojiImg == nil || !m.emojiImg.active() {
		m.status = "gorillas: needs a terminal with Kitty graphics support"
		return nil
	}

	match := game.NewMatch(uint16(rand.IntN(1 << 16)))
	me := m.gorillasName(m.me.Id)

	names := [2]string{me, "…"}
	status := "waiting for someone to react :" + gorillasJoinEmoji + ": to join"
	if solo {
		// Hotseat: no second client is coming, so seat ourselves opposite ourselves
		// and start immediately.
		match.Join(m.me.Id)
		names[1] = me + " (2)"
		status = "hotseat — you play both sides"
	}

	m.gorillas = gorillasState{
		active:    true,
		gen:       m.gorillas.gen + 1,
		role:      0,
		solo:      solo,
		channelID: channelID,
		match:     match,
		imgID:     m.emojiImg.allocID(),
		names:     names,
		status:    status,
	}
	m.sizeGorillas()

	gen := m.gorillas.gen
	body := gorillasBody(match, names)
	client, ctx := m.client, m.ctx
	m.status = "gorillas: posted a game in " + label
	return tea.Batch(
		func() tea.Msg {
			p, err := client.Send(ctx, channelID, "", body, nil)
			return gorillasPostedMsg{gen: gen, post: p, err: err}
		},
		m.gorillasFrameCmd(),
		gorillasHeartbeatCmd(gen),
	)
}

// gorillasBody builds the post: a header and an ASCII board that users on the
// official clients can actually watch, then the invisible state blob.
func gorillasBody(mt *game.Match, names [2]string) string {
	st, w := mt.State, mt.World
	var b strings.Builder
	b.WriteString("🎮 **Gorillas** — ")
	b.WriteString(names[0])
	b.WriteString(" vs ")
	if st.Joiner == "" {
		b.WriteString("_(react :" + gorillasJoinEmoji + ": to join)_")
	} else {
		b.WriteString(names[1])
	}
	b.WriteString(fmt.Sprintf(" · wind %s · %d–%d", windArrow(w.Wind), st.Scores[0], st.Scores[1]))
	if st.Phase == game.PhaseOver && st.Winner >= 0 {
		b.WriteString(" · **" + names[st.Winner] + " wins**")
	}
	b.WriteByte('\n')
	b.WriteString(game.ASCIIBoard(w, mt.Shot, gorillasBoardCols, gorillasBoardRows))
	b.WriteByte('\n')
	b.WriteString(game.Encode(game.MarshalState(st)))
	return b.String()
}

// gorillasController is the joiner's post body: a visible label and their shot.
func gorillasController(in *game.Input) string {
	return "🎮 _controller_\n" + game.Encode(game.MarshalInput(in))
}

func windArrow(wind int8) string {
	if wind == 0 {
		return "calm"
	}
	n := int(wind)
	dir := "→"
	if n < 0 {
		dir, n = "←", -n
	}
	return strings.Repeat(dir, min(max(n/4, 1), 5)) + " " + strconv.Itoa(int(wind))
}

// applyGorillasPosted records the world post's id. From here on, every frame is
// an edit of it.
func (m *Model) applyGorillasPosted(msg gorillasPostedMsg) tea.Cmd {
	if !m.gorillas.active || msg.gen != m.gorillas.gen {
		return nil
	}
	if msg.err != nil {
		m.status = "gorillas: " + msg.err.Error()
		return m.closeGorillas()
	}
	m.gorillas.postID = msg.post.Id
	return nil
}

// gorillasJoin opens the modal for a player who reacted their way into someone
// else's game, and posts their controller reply.
func (m *Model) gorillasJoin(post *model.Post, st *game.State) tea.Cmd {
	if m.emojiImg == nil || !m.emojiImg.active() {
		m.status = "gorillas: needs a terminal with Kitty graphics support"
		return nil
	}
	m.gorillas = gorillasState{
		active:    true,
		gen:       m.gorillas.gen + 1,
		role:      1,
		channelID: post.ChannelId,
		postID:    post.Id,
		match:     game.FromState(st),
		imgID:     m.emojiImg.allocID(),
		names:     [2]string{m.gorillasName(post.UserId), m.gorillasName(m.me.Id)},
		status:    "joining…",
	}
	m.sizeGorillas()

	gen := m.gorillas.gen
	// The controller starts empty: seq 0, no shot. The host is watching for it.
	body := gorillasController(&game.Input{Seq: 0})
	client, ctx := m.client, m.ctx
	channelID, rootID := post.ChannelId, post.Id
	return tea.Batch(
		func() tea.Msg {
			p, err := client.Send(ctx, channelID, rootID, body, nil)
			return gorillasJoinedMsg{gen: gen, post: p, err: err}
		},
		m.gorillasFrameCmd(),
	)
}

func (m *Model) applyGorillasJoined(msg gorillasJoinedMsg) tea.Cmd {
	if !m.gorillas.active || msg.gen != m.gorillas.gen {
		return nil
	}
	if msg.err != nil {
		m.status = "gorillas: " + msg.err.Error()
		return m.closeGorillas()
	}
	m.gorillas.replyID = msg.post.Id
	m.gorillas.status = "waiting for the host…"
	return nil
}

// gorillasResume steps a player back into a game they had closed. The world post
// held the whole match the entire time, so this rebuilds it from that post's state
// and re-establishes the wire — as the host (the post's author) or the joiner
// (State.Joiner), whichever this client was. The seq counters live only in the
// controller post, so they are recovered from the thread in applyGorillasResumed.
func (m *Model) gorillasResume(post *model.Post, st *game.State, role int) tea.Cmd {
	if m.emojiImg == nil || !m.emojiImg.active() {
		m.status = "gorillas: needs a terminal with Kitty graphics support"
		return nil
	}
	names := [2]string{m.gorillasName(post.UserId), "…"}
	if st.Joiner != "" {
		names[1] = m.gorillasName(st.Joiner)
	}
	m.gorillas = gorillasState{
		active:    true,
		gen:       m.gorillas.gen + 1,
		role:      role,
		channelID: post.ChannelId,
		postID:    post.Id,
		match:     game.FromState(st),
		imgID:     m.emojiImg.allocID(),
		names:     names,
	}
	m.gorillas.status = m.gorillas.resumeStatus()
	m.sizeGorillas()
	m.status = "gorillas: rejoined"

	gen := m.gorillas.gen
	client, ctx := m.client, m.ctx
	rootID := post.Id
	cmds := []tea.Cmd{
		m.gorillasFrameCmd(),
		func() tea.Msg {
			pl, err := client.Thread(ctx, rootID)
			return gorillasResumedMsg{gen: gen, thread: pl, err: err}
		},
	}
	if role == 0 {
		// Re-broadcast at once and keep the heartbeat going, so a joiner still parked
		// on the old state catches up without waiting for the next shot.
		cmds = append(cmds, m.gorillasPush(), gorillasHeartbeatCmd(gen))
	}
	return tea.Batch(cmds...)
}

// applyGorillasResumed re-attaches to the joiner's controller post after a resume,
// so the seq counters line up with the shots already fired: the host must not
// re-launch a shot it already saw, and the joiner's next shot must continue the
// count. The host also needs the controller's id to route its edits at all
// (gorillasWSEdited keys on it), which is why a failed thread fetch is fatal here.
func (m *Model) applyGorillasResumed(msg gorillasResumedMsg) tea.Cmd {
	g := &m.gorillas
	if !g.active || msg.gen != g.gen {
		return nil
	}
	if msg.err != nil {
		m.status = "gorillas: " + msg.err.Error()
		return m.closeGorillas()
	}
	ctrl, in := gorillasControllerInThread(msg.thread, g.match.State.Joiner)
	if ctrl == nil {
		return nil // still in the lobby, or the joiner has not replied yet
	}
	g.replyID = ctrl.Id
	if g.role == 0 {
		g.lastSeq = in.Seq // a shot already made must not fire again on our return
	} else {
		g.seq = in.Seq // our next shot continues the count
	}
	return nil
}

// gorillasControllerInThread finds the joiner's controller reply in a fetched
// thread, returning it and the input it currently holds.
func gorillasControllerInThread(pl *model.PostList, joiner string) (*model.Post, *game.Input) {
	if pl == nil || joiner == "" {
		return nil, nil
	}
	for _, p := range pl.Posts {
		if p == nil || p.DeleteAt != 0 || p.UserId != joiner {
			continue
		}
		payload, ok := game.Decode(p.Message)
		if !ok {
			continue
		}
		if in, err := game.UnmarshalInput(payload); err == nil {
			return p, in
		}
	}
	return nil, nil
}

// resumeStatus is the footer line for a game just reopened, phrased for the player
// who reopened it.
func (g *gorillasState) resumeStatus() string {
	st := g.match.State
	switch {
	case st.Phase == game.PhaseLobby:
		return "waiting for someone to react :" + gorillasJoinEmoji + ": to join"
	case st.Phase == game.PhaseOver && st.Winner >= 0:
		return g.names[st.Winner] + " wins the match"
	case st.Phase != game.PhaseAiming:
		return "…" // a banana or a fireball is mid-air; the picture says it
	case int(st.Turn) == g.role:
		return "your shot"
	default:
		return "waiting for " + g.names[1-g.role]
	}
}

// gorillasAcceptJoin is the host's side of the handshake: someone reacted, so
// they are player two and the match begins.
func (m *Model) gorillasAcceptJoin(userID string) tea.Cmd {
	g := &m.gorillas
	if !g.active || g.role != 0 || g.solo ||
		g.match.State.Phase != game.PhaseLobby || userID == m.me.Id {
		return nil
	}
	g.match.Join(userID)
	g.names[1] = m.gorillasName(userID)
	g.status = "your shot"
	return tea.Batch(m.gorillasPush(), m.gorillasFrameCmd())
}

// gorillasPush writes the current state back to the world post. Only the host
// ever calls this.
func (m *Model) gorillasPush() tea.Cmd {
	g := &m.gorillas
	if g.role != 0 || g.postID == "" {
		return nil
	}
	return m.editPost(g.postID, gorillasBody(g.match, g.names))
}

// gorillasFire launches a shot. The host simulates its own; the joiner writes
// theirs to their controller post and lets the host simulate it.
func (m *Model) gorillasFire(angle, power uint8) tea.Cmd {
	g := &m.gorillas
	if g.role == 1 {
		g.seq++
		g.status = "fired — waiting for the host…"
		return m.editPost(g.replyID, gorillasController(&game.Input{
			Angle: angle, Power: power, Seq: g.seq,
		}))
	}
	return m.gorillasLaunch(int(g.match.State.Turn), angle, power)
}

// gorillasLaunch starts the flight. Host only — it is the sole simulator, so
// there is exactly one authority on where a banana lands and a desync is not
// something the protocol can express.
func (m *Model) gorillasLaunch(player int, angle, power uint8) tea.Cmd {
	g := &m.gorillas
	g.match.Launch(player, angle, power)
	g.status = "…"
	return tea.Batch(m.gorillasPush(), gorillasTickCmd(g.gen))
}

func gorillasTickCmd(gen int) tea.Cmd {
	return tea.Tick(gorillasFrameDelay, func(time.Time) tea.Msg {
		return gorillasTickMsg{gen: gen}
	})
}

func gorillasHeartbeatCmd(gen int) tea.Cmd {
	return tea.Tick(gorillasHeartbeat, func(time.Time) tea.Msg {
		return gorillasHeartbeatMsg{gen: gen}
	})
}

// applyGorillasHeartbeat re-pushes the resting state to the world post so a joiner
// that dropped the edit handing it the turn recovers. Host only, and it edits
// nothing while a banana is in the air — the flight stream is already keeping the
// joiner in sync — or in hotseat, where there is no second client to resync. The
// loop self-terminates the moment any of those stop holding, when the game closes,
// or when the gen is bumped.
func (m *Model) applyGorillasHeartbeat(msg gorillasHeartbeatMsg) tea.Cmd {
	g := &m.gorillas
	if !g.active || msg.gen != g.gen || g.role != 0 || g.solo {
		return nil
	}
	var push tea.Cmd
	if g.restingInAiming() {
		push = m.gorillasPush()
	}
	return tea.Batch(push, gorillasHeartbeatCmd(g.gen))
}

// restingInAiming reports that the world is at rest waiting for a shot: it is the
// resting state the heartbeat re-broadcasts. Guarded on Busy so a heartbeat that
// fires mid-flight stays quiet — the flight stream is already keeping the joiner in
// sync, and splicing a stale frame into it would only jar the picture.
func (g *gorillasState) restingInAiming() bool {
	return g.match != nil && !g.match.Busy() && g.match.State.Phase == game.PhaseAiming
}

// applyGorillasTick advances the world one frame: step the rules, stream the new
// state, redraw. Host only — the joiner is driven by the host's edits arriving
// over the websocket, not by a clock of its own, and that now includes the
// explosion: it is in the state, so both players watch the same frame of it.
func (m *Model) applyGorillasTick(msg gorillasTickMsg) tea.Cmd {
	g := &m.gorillas
	if !g.active || msg.gen != g.gen || g.role != 0 {
		return nil
	}

	// shooter is read before Step, because a terminal event flips the turn.
	shooter := int(g.match.State.Turn)
	ev := g.match.Step(gorillasDT)
	switch ev.Kind {
	case game.EvNothing:
		return nil

	case game.EvFlying, game.EvBoom, game.EvDance:
		// Nothing to say; the picture says it.

	case game.EvMiss:
		g.status = g.names[shooter] + " missed"

	case game.EvBuilding:
		g.status = g.names[shooter] + " hit a building"

	case game.EvRound:
		g.status = fmt.Sprintf("%s scores — %d–%d",
			g.names[ev.Scorer], g.match.State.Scores[0], g.match.State.Scores[1])
		if ev.Self {
			g.status = g.names[ev.Hit] + " hit themselves! " + g.status
		}

	case game.EvMatch:
		g.status = g.names[ev.Scorer] + " wins the match"
	}

	// Keep the clock running for as long as anything is still moving — a banana in
	// the air, a fireball that has not finished collapsing, or a winner still
	// gloating. The last of those frames is what turns the round over, so it has to
	// be streamed like any other.
	var next tea.Cmd
	if g.match.Busy() {
		next = gorillasTickCmd(g.gen)
	}
	return tea.Batch(m.gorillasFrameCmd(), m.gorillasPush(), next)
}

// gorillasApplyState adopts a state that arrived from the host. Joiner only.
//
// The explosion arrives with it, on the frame the host is drawing — so the joiner
// does not have to infer a fireball from a crater appearing, which it could not
// do correctly anyway: the crater does not exist until the fireball is out.
func (m *Model) gorillasApplyState(st *game.State) tea.Cmd {
	g := &m.gorillas
	if !g.active || g.role != 1 {
		return nil
	}

	g.match = game.FromState(st)
	g.names[1] = m.gorillasName(m.me.Id)

	switch {
	case st.Phase == game.PhaseOver && st.Winner >= 0:
		g.status = g.names[st.Winner] + " wins the match"
	case st.Phase == game.PhaseFlight, st.Phase == game.PhaseBoom, st.Phase == game.PhaseDance:
		g.status = "…"
	case st.Turn == 1:
		g.status = "your shot"
	default:
		g.status = "waiting for " + g.names[0]
	}
	return m.gorillasFrameCmd()
}

// gorillasApplyInput is the host consuming the joiner's controller post. A shot
// only counts when its sequence number moves: the same angle and power twice in a
// row is an ordinary thing to do, and without the counter the host could not tell
// the second one from a re-delivery of the first.
func (m *Model) gorillasApplyInput(in *game.Input) tea.Cmd {
	g := &m.gorillas
	if !g.active || g.role != 0 || in.Seq == g.lastSeq {
		return nil
	}
	g.lastSeq = in.Seq
	if !g.match.MyTurn(1) {
		return nil // not their turn; ignore rather than argue
	}
	return m.gorillasLaunch(1, in.Angle, in.Power)
}

// gorillasFrameCmd renders and encodes the current frame off the UI goroutine.
//
// The Renderer and the World are shared mutable state, so only one frame may be
// in flight at a time: this gates on g.rendering, and the next tick is not armed
// until the frame lands. That is what makes a single shared buffer safe without a
// mutex — and why a state arriving mid-render sets g.pending rather than starting
// a second render.
func (m *Model) gorillasFrameCmd() tea.Cmd {
	g := &m.gorillas
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
	rend, world := &g.rend, g.match.World
	shot, boom, dance := g.match.Shot, g.match.Boom, g.match.Dance

	return func() tea.Msg {
		img := rend.Render(world, shot, boom, dance, pxW, pxH)
		seq, err := kittyTransmitImage(id, img, rows, cols)
		return gorillasFrameMsg{gen: gen, seq: seq, err: err}
	}
}

// applyGorillasFrame writes the encoded frame out of band. Nothing re-renders:
// re-transmitting under the same image id repaints the placeholder cells already
// on screen, so a banana in flight costs the View() hot path nothing at all.
func (m *Model) applyGorillasFrame(msg gorillasFrameMsg) tea.Cmd {
	g := &m.gorillas
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
		next = m.gorillasFrameCmd()
	}
	return tea.Batch(tea.Raw(msg.seq), next)
}

// handleGorillasKey routes keys while the modal owns the screen.
func (m *Model) handleGorillasKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	g := &m.gorillas
	switch msg.String() {
	case "esc", "q":
		return m, m.closeGorillas()
	case "backspace":
		switch {
		case g.onPower && g.power != "":
			g.power = g.power[:len(g.power)-1]
		case g.onPower:
			g.onPower = false
		case g.angle != "":
			g.angle = g.angle[:len(g.angle)-1]
		}
		return m, nil
	case "tab":
		g.onPower = !g.onPower
		return m, nil
	case "enter":
		return m, m.gorillasSubmit()
	}

	if !m.gorillasMyTurn() {
		return m, nil
	}
	if s := msg.String(); len(s) == 1 && s[0] >= '0' && s[0] <= '9' {
		if g.onPower {
			if len(g.power) < 3 {
				g.power += s
			}
		} else if len(g.angle) < 3 {
			g.angle += s
		}
	}
	return m, nil
}

// gorillasSubmit moves from the angle field to the power field, and from the
// power field to a launched banana.
func (m *Model) gorillasSubmit() tea.Cmd {
	g := &m.gorillas
	if !m.gorillasMyTurn() {
		return nil
	}
	if !g.onPower {
		if g.angle == "" {
			return nil
		}
		g.onPower = true
		return nil
	}
	angle, aerr := strconv.Atoi(g.angle)
	power, perr := strconv.Atoi(g.power)
	if aerr != nil || perr != nil || angle < 0 || angle > 180 || power < 1 || power > 255 {
		g.status = "angle 0–180, velocity 1–255"
		return nil
	}
	g.angle, g.power, g.onPower = "", "", false
	return m.gorillasFire(uint8(angle), uint8(power))
}

// gorillasMyTurn reports whether this client may fire right now. In hotseat both
// turns are ours.
func (m *Model) gorillasMyTurn() bool {
	g := &m.gorillas
	if !g.active || g.match == nil {
		return false
	}
	if g.solo {
		return g.match.State.Phase == game.PhaseAiming
	}
	return g.match.MyTurn(g.role)
}

// closeGorillas tears the game down and frees the terminal's copy of the image.
// The gen bump orphans any tick or frame still in flight.
func (m *Model) closeGorillas() tea.Cmd {
	g := &m.gorillas
	if !g.active {
		return nil
	}
	id, gen := g.imgID, g.gen
	m.gorillas = gorillasState{gen: gen + 1}
	m.status = "gorillas: closed"
	if id != 0 {
		return tea.Raw(kittyDelete(id))
	}
	return nil
}

// sizeGorillas fits the field to the terminal, in the shape a monitor would have
// given it: 4:3, not the frame buffer's 640×350.
//
// Two aspect corrections stack here, and it is easy to apply only one. The field
// wants to be shown at game.DisplayAspect — that is the CRT's doing, and it is
// why the sun is stored as an ellipse and comes out round. And a terminal cell is
// about twice as tall as it is wide, so the box measured in *cells* is nothing
// like the box measured in pixels. Miss the first and the city is a third too
// wide; miss the second and it is unrecognisable.
//
// The renderer needs none of this. It maps the field onto whatever pixel box it
// is handed, so the shape of that box is the whole question.
func (m *Model) sizeGorillas() {
	g := &m.gorillas
	maxCols := max(m.width-6, 20)
	maxRows := max(m.height-8, 8)

	cw, ch := m.cellPxOr(8), m.cellPxHOr(16)
	// cols·cw : rows·ch must come out as DisplayAspect : 1.
	cols := maxCols
	rows := int(float64(cols) * float64(cw) / (game.DisplayAspect * float64(ch)))
	if rows > maxRows {
		rows = maxRows
		cols = int(float64(rows) * game.DisplayAspect * float64(ch) / float64(cw))
	}
	g.cols = max(min(cols, maxCols), 10)
	g.rows = max(min(rows, maxRows), 5)
}

func (m *Model) cellPxOr(def int) int {
	if m.cellPxW > 0 {
		return m.cellPxW
	}
	return def
}

func (m *Model) cellPxHOr(def int) int {
	if m.cellPxH > 0 {
		return m.cellPxH
	}
	return def
}

// renderGorillas draws the modal. The field itself is a grid of Kitty placeholder
// cells — the pixels arrive out of band — so this stays cheap and stable no
// matter what the banana is doing.
func (m *Model) renderGorillas(_ int) string {
	g := &m.gorillas
	if !g.active || g.match == nil {
		return ""
	}
	st := g.match.State

	title := fmt.Sprintf(" 🎮 Gorillas — %s vs %s ", g.names[0], g.names[1])
	field := kittyPlaceholder(g.imgID, g.rows, g.cols)

	var footer strings.Builder
	fmt.Fprintf(&footer, "wind %s   %s %d–%d %s\n",
		windArrow(g.match.World.Wind), g.names[0], st.Scores[0], st.Scores[1], g.names[1])

	if m.gorillasMyTurn() {
		a, p := g.angle, g.power
		if g.onPower {
			p += "█"
		} else {
			a += "█"
		}
		who := ""
		if g.solo {
			who = g.names[st.Turn] + ": "
		}
		fmt.Fprintf(&footer, "%sAngle: %-4s Velocity: %-4s  ⏎ next/fire · tab · esc", who, a, p)
	} else {
		footer.WriteString(g.status + "   · esc quit")
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

// gorillasPost reports whether postID belongs to the open game. Its frame churn
// must not be persisted: the world post is edited ~30 times a second, and every
// one of those would otherwise land in SQLite and fire the revision trigger.
func (m *Model) gorillasPost(postID string) bool {
	g := &m.gorillas
	return g.active && postID != "" && (postID == g.postID || postID == g.replyID)
}

// gorillasName resolves a user id to something printable, falling back to an
// ellipsis while the username lookup is still in flight.
func (m *Model) gorillasName(userID string) string {
	if n := m.userNames[userID]; n != "" {
		return n
	}
	return "…"
}

// gorillasReaction is the join handshake, from both sides of it.
//
// Two clients see the same `reaction_added` event and draw opposite conclusions:
// the host learns who player two is, and the reactor learns they are player two.
// Neither has to ask the other anything.
func (m *Model) gorillasReaction(r *model.Reaction) tea.Cmd {
	if r.EmojiName != gorillasJoinEmoji {
		return nil
	}
	// Someone reacted to our game: they are player two.
	if m.gorillas.active && m.gorillas.role == 0 && r.PostId == m.gorillas.postID {
		return m.gorillasAcceptJoin(r.UserId)
	}
	// We reacted to somebody else's game: we are player two.
	if r.UserId != m.me.Id || m.gorillas.active {
		return nil
	}
	p := m.findPostByID(r.PostId)
	if p == nil || p.UserId == m.me.Id {
		return nil
	}
	payload, ok := game.Decode(p.Message)
	if !ok {
		return nil
	}
	st, err := game.UnmarshalState(payload)
	if err != nil || st.Phase != game.PhaseLobby {
		return nil // not a game, or a game already under way
	}
	return m.gorillasJoin(p, st)
}

// gorillasWSPosted lets the host discover the joiner's controller reply.
//
// It does not insist the reply's author already matches State.Joiner: the
// reaction and the reply are two independent events racing through the
// websocket, and the reply can win. Any thread reply on our game post that
// carries an Input payload and is not ours is the controller.
func (m *Model) gorillasWSPosted(p *model.Post) tea.Cmd {
	g := &m.gorillas
	if !g.active || g.role != 0 || g.replyID != "" || p.RootId != g.postID || p.UserId == m.me.Id {
		return nil
	}
	payload, ok := game.Decode(p.Message)
	if !ok {
		return nil
	}
	if _, err := game.UnmarshalInput(payload); err != nil {
		return nil
	}
	g.replyID = p.Id
	return nil
}

// gorillasWSEdited routes an edited post into the game: the world post feeds the
// joiner, the controller post feeds the host. This is the only place either
// client learns anything from the other.
func (m *Model) gorillasWSEdited(p *model.Post) tea.Cmd {
	g := &m.gorillas
	if !g.active {
		return nil
	}
	payload, ok := game.Decode(p.Message)
	if !ok {
		return nil
	}
	switch {
	case g.role == 1 && p.Id == g.postID:
		st, err := game.UnmarshalState(payload)
		if err != nil {
			return nil // a version we do not speak, or a mangled body: ignore it
		}
		return m.gorillasApplyState(st)
	case g.role == 0 && p.Id == g.replyID:
		in, err := game.UnmarshalInput(payload)
		if err != nil {
			return nil
		}
		return m.gorillasApplyInput(in)
	}
	return nil
}

// resizeGorillas re-fits the field to a resized terminal and re-transmits it.
//
// Deliberately driven from the resize *settle* rather than from layoutPanes: the
// placeholder grid in View() and the image the terminal is holding have to change
// size together. Re-fitting on every drag frame would move the grid while the
// image lagged a frame behind it, and the field would tear.
func (m *Model) resizeGorillas() tea.Cmd {
	if !m.gorillas.active {
		return nil
	}
	m.sizeGorillas()
	return m.gorillasFrameCmd()
}
