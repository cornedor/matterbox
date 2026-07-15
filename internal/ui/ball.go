package ui

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Ball-box geometry and run limits. The box is fixed-size so the frame
// width never reflows mid-animation; ballMaxFrames is a safety cap so a
// fat-fingered "120 60" can't queue a runaway stream of edits.
const (
	ballBoxW      = 24
	ballBoxH      = 10
	ballMaxFPS    = 60
	ballMaxFrames = 5000
)

// fence is the code-block delimiter the box is wrapped in so Mattermost
// renders it monospaced and preserves the leading whitespace.
const fence = "```"

// ballAnim drives the "Bouncing ball" > command: one post is created,
// then edited once per frame to move a ball around inside a code-block
// box. Like typingAnim, seq guards against a superseding run; the post
// is flagged via animatingPost so its frame churn isn't persisted.
type ballAnim struct {
	active    bool
	seq       int
	channelID string
	postID    string
	w, h      int // inner field dimensions (excluding the border)
	x, y      int // ball position within [0,w) × [0,h)
	dx, dy    int // velocity, each ±1
	frame     int // frames rendered so far
	total     int // frames to render before stopping
	delay     time.Duration
}

// ballStartedMsg lands once the seed post exists, carrying its ID.
type ballStartedMsg struct {
	seq  int
	post *model.Post
	err  error
}

// ballTickMsg fires on each animation frame.
type ballTickMsg struct{ seq int }

// runBouncingBall is the > command entry point. arg is "duration fps"
// (duration in seconds; comma or space separated). It validates both,
// posts the opening frame, and arms the loop.
func runBouncingBall(m *Model, arg string) tea.Cmd {
	fields := strings.Fields(strings.ReplaceAll(arg, ",", " "))
	if len(fields) != 2 {
		m.status = "ball: needs duration(s) and fps, e.g. 8 30"
		return nil
	}
	dur, err := strconv.ParseFloat(strings.TrimSuffix(fields[0], "s"), 64)
	if err != nil || dur <= 0 {
		m.status = "ball: duration must be a positive number of seconds"
		return nil
	}
	fps, err := strconv.Atoi(fields[1])
	if err != nil || fps < 1 || fps > ballMaxFPS {
		m.status = fmt.Sprintf("ball: fps must be between 1 and %d", ballMaxFPS)
		return nil
	}
	total := int(dur * float64(fps))
	if total < 1 {
		m.status = "ball: duration × fps is less than one frame"
		return nil
	}
	if total > ballMaxFrames {
		m.status = fmt.Sprintf("ball: %d frames is too many (max %d) — lower duration or fps", total, ballMaxFrames)
		return nil
	}
	channelID, label := m.indexTargetChannel()
	if channelID == "" {
		m.status = "ball: no channel selected"
		return nil
	}

	dx, dy := 1, 1
	if rand.IntN(2) == 0 {
		dx = -1
	}
	if rand.IntN(2) == 0 {
		dy = -1
	}
	m.ball = ballAnim{
		active:    true,
		seq:       m.ball.seq + 1,
		channelID: channelID,
		w:         ballBoxW,
		h:         ballBoxH,
		x:         rand.IntN(ballBoxW),
		y:         rand.IntN(ballBoxH),
		dx:        dx,
		dy:        dy,
		total:     total,
		delay:     time.Second / time.Duration(fps),
	}
	m.status = fmt.Sprintf("bouncing ball in %s · %d frames @ %d fps…", label, total, fps)
	return m.ballSendCmd(m.ball.seq, channelID, renderBallFrame(m.ball.w, m.ball.h, m.ball.x, m.ball.y))
}

// step advances the ball one frame, reflecting off the four walls. If a
// dimension is too narrow to move into (degenerate 1-wide box) the ball
// holds its position on that axis rather than escaping.
func (b *ballAnim) step() {
	b.x, b.dx = bounce(b.x, b.dx, b.w)
	b.y, b.dy = bounce(b.y, b.dy, b.h)
}

// bounce returns the next position and (possibly flipped) velocity for
// one axis, reflecting at the [0,size) walls and clamping to the
// current position when neither direction is in-bounds.
func bounce(pos, vel, size int) (int, int) {
	if pos+vel < 0 || pos+vel >= size {
		vel = -vel
	}
	if pos+vel < 0 || pos+vel >= size {
		return pos, vel // too narrow to move
	}
	return pos + vel, vel
}

// renderBallFrame draws the fenced box with the ball at (x,y).
func renderBallFrame(w, h, x, y int) string {
	var b strings.Builder
	b.Grow((w + 4) * (h + 2))
	b.WriteString(fence + "\n")
	b.WriteByte('+')
	b.WriteString(strings.Repeat("-", w))
	b.WriteString("+\n")
	for row := 0; row < h; row++ {
		b.WriteByte('|')
		for col := 0; col < w; col++ {
			if col == x && row == y {
				b.WriteByte('O')
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteString("|\n")
	}
	b.WriteByte('+')
	b.WriteString(strings.Repeat("-", w))
	b.WriteString("+\n")
	b.WriteString(fence)
	return b.String()
}

// ballSendCmd posts the opening frame and reports its ID back.
func (m Model) ballSendCmd(seq int, channelID, body string) tea.Cmd {
	client := m.client
	ctx := m.ctx
	return func() tea.Msg {
		p, err := client.Send(ctx, channelID, "", body, nil)
		return ballStartedMsg{seq: seq, post: p, err: err}
	}
}

// applyBallStarted records the post ID and schedules the first frame.
func (m *Model) applyBallStarted(msg ballStartedMsg) tea.Cmd {
	if !m.ball.active || msg.seq != m.ball.seq {
		return nil
	}
	if msg.err != nil {
		m.status = "ball: " + msg.err.Error()
		m.ball.active = false
		return nil
	}
	m.ball.postID = msg.post.Id
	return ballTickCmd(m.ball.seq, m.ball.delay)
}

// applyBallTick moves the ball, edits the post to the new frame, and
// arms the next tick until the frame budget is spent.
func (m *Model) applyBallTick(msg ballTickMsg) tea.Cmd {
	if !m.ball.active || msg.seq != m.ball.seq {
		return nil
	}
	if m.ball.frame >= m.ball.total {
		m.ball.active = false
		m.status = "bouncing ball done"
		return nil
	}
	m.ball.step()
	m.ball.frame++
	editCmd := m.editPost(m.ball.postID, renderBallFrame(m.ball.w, m.ball.h, m.ball.x, m.ball.y))
	return tea.Batch(editCmd, ballTickCmd(m.ball.seq, m.ball.delay))
}

// ballTickCmd schedules the next frame after d.
func ballTickCmd(seq int, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return ballTickMsg{seq: seq} })
}

// animatingPost reports whether postID is the target of a running
// animation (typing or bouncing ball). Frame edits on these posts are
// intentionally not persisted: the per-frame churn would otherwise
// flood the local edit-history (post_revisions, captured by a posts
// UPDATE trigger) and pollute the message cache with throwaway frames.
func (m *Model) animatingPost(postID string) bool {
	if postID == "" {
		return false
	}
	// A running Gorillas game rewrites its world post ~30 times a second, and the
	// joiner's client would otherwise persist every one of those frames. Achtung,
	// die Kurve does the same with its own world post.
	if m.gorillasPost(postID) || m.kurvePost(postID) {
		return true
	}
	if m.typing.active && postID == m.typing.postID {
		return true
	}
	if m.ball.active && postID == m.ball.postID {
		return true
	}
	return false
}
