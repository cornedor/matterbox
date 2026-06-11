package ui

import (
	"math/rand/v2"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// typingSeed is the placeholder body of the post created before the
// animation starts, and the char trailed on every in-progress frame as
// a fake cursor. It also keeps frames non-empty (an all-backspace frame
// becomes just the cursor), which matters because Mattermost rejects an
// empty edit. The final frame drops it so the message commits clean.
const typingSeed = "_"

// typoChance is the per-character probability that the animator first
// fat-fingers a wrong letter (then backspaces it) before typing the
// intended one. Only non-space characters are eligible.
const typoChance = 0.12

// typingAnim drives the "Typing animation" > command. It posts one
// message, then walks a precomputed list of frames, editing that post
// to each frame in turn so the message appears to be typed live. Only
// one animation runs at a time; a new run bumps seq so any in-flight
// ticks from the previous run are recognised as stale and dropped.
type typingAnim struct {
	active    bool
	seq       int
	channelID string
	postID    string
	frames    []typingFrame
	idx       int
}

// typingFrame is one rendered state of the message plus the delay to
// wait before showing it, so the cadence can vary like real typing.
type typingFrame struct {
	text  string
	delay time.Duration
}

// typingStartedMsg lands once the seed post has been created; it
// carries the new post's ID so the edit loop has a target.
type typingStartedMsg struct {
	seq  int
	post *model.Post
	err  error
}

// typingTickMsg fires on each animation step (one frame).
type typingTickMsg struct{ seq int }

// runTypingAnimation is the > command entry point. arg is the message
// the user wants "typed". It posts the seed and arms the first tick.
func runTypingAnimation(m *Model, arg string) tea.Cmd {
	text := strings.TrimSpace(arg)
	if text == "" {
		m.status = "typing: needs a message to type"
		return nil
	}
	channelID, label := m.indexTargetChannel()
	if channelID == "" {
		m.status = "typing: no channel selected"
		return nil
	}
	m.typing = typingAnim{
		active:    true,
		seq:       m.typing.seq + 1,
		channelID: channelID,
		frames:    buildTypingFrames(text),
	}
	m.status = "typing into " + label + "…"
	return m.typingSendCmd(m.typing.seq, channelID)
}

// buildTypingFrames expands target into the sequence of message bodies
// to render. Most characters add a single frame (the text so far);
// occasionally a wrong character is inserted for one frame and removed
// the next, so the reveal includes the odd typo-and-correction.
func buildTypingFrames(target string) []typingFrame {
	runes := []rune(target)
	frames := make([]typingFrame, 0, len(runes)+len(runes)/4)
	const wrong = "abcdefghijklmnopqrstuvwxyz"

	var typed []rune
	for _, r := range runes {
		if r != ' ' && rand.Float64() < typoChance {
			bad := append(append([]rune{}, typed...), rune(wrong[rand.IntN(len(wrong))]))
			frames = append(frames,
				typingFrame{text: string(bad), delay: keyDelay(false)},          // oops
				typingFrame{text: string(typed), delay: 180 * time.Millisecond}, // backspace
			)
		}
		typed = append(typed, r)
		frames = append(frames, typingFrame{text: string(typed), delay: keyDelay(r == ' ')})
	}
	return frames
}

// keyDelay returns a jittered inter-keystroke pause. Spaces get a
// slightly longer "thinking between words" beat.
func keyDelay(afterWord bool) time.Duration {
	base := 45 + rand.IntN(70) // 45–115ms
	if afterWord {
		base += 60 + rand.IntN(120)
	}
	return time.Duration(base) * time.Millisecond
}

// typingSendCmd posts the seed message and reports its ID back.
func (m Model) typingSendCmd(seq int, channelID string) tea.Cmd {
	client := m.client
	ctx := m.ctx
	return func() tea.Msg {
		p, err := client.Send(ctx, channelID, "", typingSeed, nil)
		return typingStartedMsg{seq: seq, post: p, err: err}
	}
}

// applyTypingStarted stores the seed post ID and schedules the first
// frame. Stale results (a newer run started meanwhile) are ignored.
func (m *Model) applyTypingStarted(msg typingStartedMsg) tea.Cmd {
	if !m.typing.active || msg.seq != m.typing.seq {
		return nil
	}
	if msg.err != nil {
		m.status = "typing: " + msg.err.Error()
		m.typing.active = false
		return nil
	}
	m.typing.postID = msg.post.Id
	return typingTickCmd(m.typing.seq, m.typing.frames[0].delay)
}

// applyTypingTick edits the post to the current frame and arms the next
// one. When the frames run out the run is marked done.
func (m *Model) applyTypingTick(msg typingTickMsg) tea.Cmd {
	if !m.typing.active || msg.seq != m.typing.seq {
		return nil
	}
	if m.typing.idx >= len(m.typing.frames) {
		m.typing.active = false
		m.status = "typing animation done"
		return nil
	}
	frame := m.typing.frames[m.typing.idx]
	m.typing.idx++

	// Trail the buffer with the seed char as a fake cursor while typing;
	// the final frame commits the clean message with no cursor.
	last := m.typing.idx >= len(m.typing.frames)
	body := frame.text
	if !last {
		body += typingSeed
	}
	editCmd := m.editPost(m.typing.postID, body)

	if last {
		// Last frame: edit it, then close out on the resulting tick.
		return tea.Batch(editCmd, typingTickCmd(m.typing.seq, 0))
	}
	next := m.typing.frames[m.typing.idx].delay
	return tea.Batch(editCmd, typingTickCmd(m.typing.seq, next))
}

// typingTickCmd schedules the next animation step after d.
func typingTickCmd(seq int, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return typingTickMsg{seq: seq} })
}
