package ui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
	"github.com/mattermost/mattermost/server/public/model"
)

// summaryHTTPTimeout bounds a single chat-completions request. Local
// models can be slow on the first token, so this is generous.
const summaryHTTPTimeout = 5 * time.Minute

// summaryMaxDays caps the days field in the duration picker. A year is
// far more history than any local model wants to chew on, but it keeps
// the field from overflowing into nonsense.
const summaryMaxDays = 365

// summaryTranscriptCap bounds how many characters of transcript we send.
// The local context window is large, but a runaway channel shouldn't be
// able to balloon the request indefinitely; we keep the most recent tail.
const summaryTranscriptCap = 200_000

type summaryPhase int

const (
	summaryOff       summaryPhase = iota // not active
	summaryPicking                       // duration picker (channel path)
	summaryGathering                     // pulling messages (channel path)
	summaryStreaming                     // tokens arriving from the model
	summaryDone                          // final result (or error)
)

// summaryState owns the "> Summarize" flow: a duration picker, the
// in-flight streaming request, and the scrollable result popup. Only one
// summary runs at a time; opening a new one resets this wholesale.
type summaryState struct {
	phase summaryPhase

	// Duration picker (channel path). field selects which of the three
	// numeric inputs the up/down/digit keys act on; editing tracks whether
	// the next digit replaces (just navigated here) or appends.
	days    int
	hours   int
	minutes int
	field   int // 0=days, 1=hours, 2=minutes
	editing bool

	// Run context.
	channelID string // empty on the feed path
	label     string // channel breadcrumb, or "Unread Feed"
	window    string // "2h 30m"; empty on the feed path
	username  string // current user's name (prompt hint + result highlight)
	feedMode  bool
	count     int    // messages included in the transcript
	progress  string // gathering-phase status line
	spinner   spinner.Model
	seq       int // bumps each operation so stale responses are dropped

	// Streaming accumulators. rawContent is the model's answer stream (may
	// embed <think>…</think>); thinkingRaw is reasoning delivered in a
	// separate field (reasoning_content / reasoning). thinkExpanded toggles
	// the collapsed thinking section open.
	rawContent    string
	thinkingRaw   string
	thinkExpanded bool
	stream        chan summaryChunkMsg
	cancel        context.CancelFunc

	// Result viewport + terminal error.
	view viewport.Model
	err  error
}

// newSummaryState builds a fresh, inactive state with its own viewport.
func newSummaryState() summaryState {
	vp := viewport.New()
	vp.SoftWrap = true
	return summaryState{view: vp}
}

// active reports whether the summary modal owns the screen + keystrokes.
func (s summaryState) active() bool { return s.phase != summaryOff }

// runSummarize is the > command entry point. On the Feed tab it summarizes
// every unread message (plus a little read context) straight away; on a
// normal channel it opens the duration picker first.
func runSummarize(m *Model, _ string) tea.Cmd {
	if m.onFeedTab() {
		return m.startFeedSummary()
	}
	channelID, label := m.indexTargetChannel()
	if channelID == "" {
		m.status = "summary: no channel selected"
		return nil
	}
	m.openSummaryPicker(channelID, label)
	return nil
}

// openSummaryPicker resets state and shows the duration picker for the
// given channel, defaulting to the last hour.
func (m *Model) openSummaryPicker(channelID, label string) {
	m.summary = newSummaryState()
	m.summary.phase = summaryPicking
	m.summary.channelID = channelID
	m.summary.label = label
	m.summary.hours = 1 // sensible default window
	m.summary.field = 1 // start on the hours field (the default)
	if m.me != nil {
		m.summary.username = m.me.Username
	}
}

// closeSummary tears down the modal, cancelling any in-flight request.
func (m *Model) closeSummary() {
	if m.summary.cancel != nil {
		m.summary.cancel()
	}
	m.summary = newSummaryState()
}

// startChannelSummary turns the picked duration into a since-timestamp,
// switches to the gathering phase, and fires the fetch command.
func (m *Model) startChannelSummary() tea.Cmd {
	d := time.Duration(m.summary.days)*24*time.Hour +
		time.Duration(m.summary.hours)*time.Hour +
		time.Duration(m.summary.minutes)*time.Minute
	if d <= 0 {
		m.status = "summary: pick a window longer than zero"
		return nil
	}
	since := time.Now().Add(-d).UnixMilli()
	m.summary.window = formatWindow(m.summary.days, m.summary.hours, m.summary.minutes)
	m.summary.phase = summaryGathering
	m.summary.seq++
	m.summary.progress = "gathering messages…"
	m.beginSummarySpinner()
	names := snapshotNames(m.userNames)
	return tea.Batch(
		m.summary.spinner.Tick,
		m.summaryGatherChannelCmd(m.summary.seq, m.summary.channelID, since, names),
	)
}

// startFeedSummary builds a transcript from the already-loaded feed
// entries (unread + a little read context) and streams straight away.
func (m *Model) startFeedSummary() tea.Cmd {
	transcript, count := m.buildFeedTranscript()
	if count == 0 {
		if m.feed.loading || !m.feed.built {
			m.status = "summary: feed still loading — try again in a moment"
		} else {
			m.status = "summary: all caught up — nothing unread to summarize"
		}
		return nil
	}
	m.summary = newSummaryState()
	m.summary.feedMode = true
	m.summary.label = "Unread Feed"
	m.summary.count = count
	if m.me != nil {
		m.summary.username = m.me.Username
	}
	system, user := m.buildSummaryMessages(transcript)
	return m.startSummaryStream(system, user)
}

// startSummaryStream switches to the streaming phase, resets the
// accumulators, and opens the SSE request.
func (m *Model) startSummaryStream(system, user string) tea.Cmd {
	m.summary.seq++
	m.summary.phase = summaryStreaming
	m.summary.rawContent = ""
	m.summary.thinkingRaw = ""
	m.summary.thinkExpanded = false
	m.summary.err = nil
	m.beginSummarySpinner()
	m.sizeSummaryView()
	m.renderSummaryViewBody()
	return tea.Batch(m.summary.spinner.Tick, m.openSummaryStreamCmd(m.summary.seq, system, user))
}

// beginSummarySpinner installs a fresh dot spinner, styled like the upload
// indicator (a focused-colour dot).
func (m *Model) beginSummarySpinner() {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(focusedColor)
	m.summary.spinner = sp
}

// buildSummaryMessages assembles the system + user messages. The reader's
// @username is appended to the system prompt so the model can flag where
// they're addressed; the transcript is the user message.
func (m Model) buildSummaryMessages(transcript string) (system, user string) {
	system = m.summaryPrompt
	if u := m.summary.username; u != "" {
		system += fmt.Sprintf(
			"\n\nThe person reading this summary is @%s. Clearly flag any message that "+
				"mentions, is addressed to, or needs a response from @%s, and keep their "+
				"@%s spelling intact so it can be highlighted.", u, u, u)
	}
	if len(transcript) > summaryTranscriptCap {
		// Keep the most recent tail — recency matters most for a summary.
		transcript = "[…older messages truncated…]\n" + transcript[len(transcript)-summaryTranscriptCap:]
	}
	return system, transcript
}

// applySummaryGathered handles the channel-path fetch result: bail on
// error / empty, otherwise open the stream with the transcript.
func (m *Model) applySummaryGathered(msg summaryGatheredMsg) tea.Cmd {
	if m.summary.phase != summaryGathering || msg.seq != m.summary.seq {
		return nil
	}
	if msg.err != nil {
		m.summary.phase = summaryDone
		m.summary.err = msg.err
		m.sizeSummaryView()
		m.renderSummaryViewBody()
		return nil
	}
	if msg.count == 0 {
		m.summary.count = 0
		m.summary.phase = summaryDone
		body := "No messages in the last " + m.summary.window + "."
		if msg.latestMs > 0 {
			body += " The most recent message was " + humanizeSince(msg.latestMs) +
				" (" + time.UnixMilli(msg.latestMs).Local().Format("Jan 2 15:04") +
				") — re-run with a larger window to include it."
		}
		m.summary.rawContent = body
		m.sizeSummaryView()
		m.renderSummaryViewBody()
		m.summary.view.GotoTop()
		return nil
	}
	m.summary.count = msg.count
	system, user := m.buildSummaryMessages(msg.transcript)
	return m.startSummaryStream(system, user)
}

// applySummaryStreamOpened stores the live channel + cancel handle and
// schedules the first chunk read. A stale open is aborted immediately.
func (m *Model) applySummaryStreamOpened(msg summaryStreamOpenedMsg) tea.Cmd {
	if msg.seq != m.summary.seq || m.summary.phase != summaryStreaming {
		msg.cancel()
		return nil
	}
	m.summary.stream = msg.ch
	m.summary.cancel = msg.cancel
	return waitSummaryChunk(msg.seq, msg.ch)
}

// applySummaryChunk folds one streamed delta into the accumulators and
// re-renders, then re-schedules the next read (unless done / errored).
func (m *Model) applySummaryChunk(msg summaryChunkMsg) tea.Cmd {
	if msg.seq != m.summary.seq || m.summary.phase != summaryStreaming {
		return nil
	}
	if msg.err != nil {
		m.finishSummaryStream(msg.err)
		return nil
	}
	m.summary.rawContent += msg.content
	m.summary.thinkingRaw += msg.thinking
	if msg.done {
		m.finishSummaryStream(nil)
		return nil
	}
	m.renderSummaryViewBody()
	m.summary.view.GotoBottom() // follow the tail as tokens arrive
	return waitSummaryChunk(m.summary.seq, m.summary.stream)
}

// finishSummaryStream moves to the done phase, releasing the request.
func (m *Model) finishSummaryStream(err error) {
	if m.summary.cancel != nil {
		m.summary.cancel()
		m.summary.cancel = nil
	}
	m.summary.stream = nil
	m.summary.phase = summaryDone
	m.summary.err = err
	m.sizeSummaryView()
	m.renderSummaryViewBody()
	m.summary.view.GotoTop()
}

// currentSplit derives the answer text, the combined thinking text, and
// whether an inline <think> block is still open, from the accumulators.
func (m *Model) currentSplit() (answer, thinking string, open bool) {
	answer, inlineThink, open := splitThinking(m.summary.rawContent)
	thinking = strings.TrimSpace(m.summary.thinkingRaw)
	if inlineThink != "" {
		if thinking != "" {
			thinking += "\n"
		}
		thinking += inlineThink
	}
	return answer, strings.TrimSpace(thinking), open
}

// splitThinking separates a content stream into the visible answer and any
// <think>…</think> reasoning. open is true when a <think> tag has been
// seen without its closing tag yet (still reasoning).
func splitThinking(raw string) (answer, thinking string, open bool) {
	var ans, think strings.Builder
	rest := raw
	for {
		i := strings.Index(rest, "<think>")
		if i < 0 {
			ans.WriteString(rest)
			break
		}
		ans.WriteString(rest[:i])
		rest = rest[i+len("<think>"):]
		j := strings.Index(rest, "</think>")
		if j < 0 {
			think.WriteString(rest)
			open = true
			break
		}
		if think.Len() > 0 {
			think.WriteByte('\n')
		}
		think.WriteString(rest[:j])
		rest = rest[j+len("</think>"):]
	}
	return strings.TrimSpace(ans.String()), strings.TrimSpace(think.String()), open
}

// ---- transcript building -------------------------------------------------

// buildFeedTranscript renders the loaded feed entries into a transcript:
// one section per channel, the read context labelled, then the unread
// messages. Returns the text and the number of unread messages included.
func (m Model) buildFeedTranscript() (string, int) {
	var b strings.Builder
	total := 0
	for _, e := range m.feed.entries {
		if len(e.unread) == 0 {
			continue
		}
		header := "(unknown channel)"
		if ch := m.findChannel(e.channelID); ch != nil {
			header = m.channelBreadcrumb(ch)
		}
		b.WriteString("\n## " + header + "\n")
		if len(e.context) > 0 {
			b.WriteString("(earlier context, already read)\n")
			for _, p := range e.context {
				if line := m.summaryLine(p); line != "" {
					b.WriteString(line + "\n")
				}
			}
		}
		b.WriteString("(unread)\n")
		for _, p := range e.unread {
			if line := m.summaryLine(p); line != "" {
				b.WriteString(line + "\n")
				total++
			}
		}
	}
	return b.String(), total
}

// summaryLine formats one post for a transcript using the live name map.
func (m Model) summaryLine(p *model.Post) string {
	return summaryLineWith(p, m.userNames)
}

// summaryLineWith formats one post as "[Jan 2 15:04] @name: body", or ""
// for posts that shouldn't appear in a transcript (system / deleted /
// empty body).
func summaryLineWith(p *model.Post, names map[string]string) string {
	if p == nil || p.DeleteAt != 0 || p.IsSystemMessage() {
		return ""
	}
	body := strings.TrimSpace(p.Message)
	if body == "" {
		return ""
	}
	name := names[p.UserId]
	if name == "" {
		name = p.UserId
	}
	ts := time.UnixMilli(p.CreateAt).Local().Format("Jan 2 15:04")
	return fmt.Sprintf("[%s] @%s: %s", ts, name, body)
}

// snapshotNames copies the username map so a worker goroutine can read it
// without racing the UI goroutine's writes.
func snapshotNames(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// summaryGatherChannelCmd fetches every post in the channel created since
// sinceMs, resolves any unseen senders, and builds the transcript on a
// worker goroutine. names is a private snapshot the closure may mutate.
func (m Model) summaryGatherChannelCmd(seq int, channelID string, sinceMs int64, names map[string]string) tea.Cmd {
	client := m.client
	ctx := m.ctx
	return func() tea.Msg {
		pl, err := client.PostsSince(ctx, channelID, sinceMs)
		if err != nil {
			return summaryGatheredMsg{seq: seq, err: err}
		}
		var posts []*model.Post
		if pl != nil {
			// pl.Order is newest-first; flip to oldest-first.
			for i := len(pl.Order) - 1; i >= 0; i-- {
				if p, ok := pl.Posts[pl.Order[i]]; ok && p != nil {
					posts = append(posts, p)
				}
			}
		}
		need := map[string]struct{}{}
		for _, p := range posts {
			if _, ok := names[p.UserId]; !ok {
				need[p.UserId] = struct{}{}
			}
		}
		if len(need) > 0 {
			ids := make([]string, 0, len(need))
			for id := range need {
				ids = append(ids, id)
			}
			if us, e := client.UsersByIDs(ctx, ids); e == nil {
				for _, u := range us {
					names[u.Id] = u.Username
				}
			}
		}
		var b strings.Builder
		count := 0
		for _, p := range posts {
			if line := summaryLineWith(p, names); line != "" {
				b.WriteString(line)
				b.WriteByte('\n')
				count++
			}
		}
		if count == 0 {
			// Probe the channel's most recent real message so the empty result
			// can say how far back the last activity actually was (a quiet
			// channel reads as a confusing "nothing" otherwise).
			var latest int64
			if lp, e := client.Posts(ctx, channelID, 30); e == nil && lp != nil {
				for _, id := range lp.Order { // newest-first
					if p, ok := lp.Posts[id]; ok && summaryLineWith(p, names) != "" {
						latest = p.CreateAt
						break
					}
				}
			}
			return summaryGatheredMsg{seq: seq, count: 0, latestMs: latest}
		}
		return summaryGatheredMsg{seq: seq, transcript: b.String(), count: count}
	}
}

// ---- API (streaming) -----------------------------------------------------

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	Temperature float64       `json:"temperature"`
}

// chatResponse is the non-streaming shape, used only as a fallback when a
// server ignores stream:true and replies with a single JSON body.
type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// streamChunk is one server-sent chat-completions delta. Reasoning is
// accepted under either of the two field names servers commonly use.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"delta"`
	} `json:"choices"`
}

// chatCompletionsURL builds the chat-completions URL from a base endpoint,
// tolerating a trailing slash or an endpoint that already ends in "/v1".
func chatCompletionsURL(endpoint string) string {
	e := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if e == "" {
		e = "http://127.0.0.1:8321"
	}
	if strings.HasSuffix(e, "/v1") {
		return e + "/chat/completions"
	}
	return e + "/v1/chat/completions"
}

// openSummaryStreamCmd POSTs the streaming request and, on success, spawns
// the SSE-reading goroutine. It returns a summaryStreamOpenedMsg (with the
// chunk channel + cancel handle) or a terminal summaryChunkMsg on failure.
func (m Model) openSummaryStreamCmd(seq int, system, user string) tea.Cmd {
	endpoint := m.summaryEndpoint
	mdl := m.summaryModel
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), summaryHTTPTimeout)
		reqBody := chatRequest{
			Model: mdl,
			Messages: []chatMessage{
				{Role: "system", Content: system},
				{Role: "user", Content: user},
			},
			Stream:      true,
			Temperature: 0.3,
		}
		payload, err := json.Marshal(reqBody)
		if err != nil {
			cancel()
			return summaryChunkMsg{seq: seq, done: true, err: fmt.Errorf("encode request: %w", err)}
		}
		url := chatCompletionsURL(endpoint)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			cancel()
			return summaryChunkMsg{seq: seq, done: true, err: fmt.Errorf("build request: %w", err)}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			return summaryChunkMsg{seq: seq, done: true, err: fmt.Errorf("call %s: %w", url, err)}
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			cancel()
			msg := strings.TrimSpace(string(body))
			if len(msg) > 300 {
				msg = msg[:300] + "…"
			}
			if msg == "" {
				msg = resp.Status
			}
			return summaryChunkMsg{seq: seq, done: true, err: fmt.Errorf("%s: %s", resp.Status, msg)}
		}
		ch := make(chan summaryChunkMsg, 64)
		go streamSummary(ctx, seq, resp, ch)
		return summaryStreamOpenedMsg{seq: seq, ch: ch, cancel: cancel}
	}
}

// streamSummary parses the SSE body, pushing one summaryChunkMsg per delta
// onto ch and a final done chunk. It closes both the body and ch on
// return, and stops early if ctx is cancelled (the reader stopped caring).
func streamSummary(ctx context.Context, seq int, resp *http.Response, ch chan<- summaryChunkMsg) {
	defer resp.Body.Close()
	defer close(ch)
	send := func(c summaryChunkMsg) bool {
		select {
		case ch <- c:
			return true
		case <-ctx.Done():
			return false
		}
	}

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "event-stream") {
		// The server ignored stream:true — read the whole JSON body.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		var parsed chatResponse
		if err := json.Unmarshal(body, &parsed); err == nil {
			if parsed.Error != nil && parsed.Error.Message != "" {
				send(summaryChunkMsg{seq: seq, done: true, err: errors.New(parsed.Error.Message)})
				return
			}
			if len(parsed.Choices) > 0 {
				send(summaryChunkMsg{seq: seq, content: parsed.Choices[0].Message.Content})
			}
		}
		send(summaryChunkMsg{seq: seq, done: true})
		return
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil || len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		thinking := d.ReasoningContent
		if thinking == "" {
			thinking = d.Reasoning
		}
		if d.Content == "" && thinking == "" {
			continue
		}
		if !send(summaryChunkMsg{seq: seq, content: d.Content, thinking: thinking}) {
			return
		}
	}
	if err := sc.Err(); err != nil {
		send(summaryChunkMsg{seq: seq, done: true, err: err})
		return
	}
	send(summaryChunkMsg{seq: seq, done: true})
}

// waitSummaryChunk blocks for the next streamed chunk and returns it. A
// closed channel (goroutine finished without an explicit done) yields a
// terminal done chunk so the handler can wrap up.
func waitSummaryChunk(seq int, ch <-chan summaryChunkMsg) tea.Cmd {
	return func() tea.Msg {
		c, ok := <-ch
		if !ok {
			return summaryChunkMsg{seq: seq, done: true}
		}
		return c
	}
}

// ---- key handling --------------------------------------------------------

// handleSummaryKey owns every keystroke while the summary modal is open.
func (m Model) handleSummaryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.summary.phase {
	case summaryPicking:
		return m.handleSummaryPickerKey(msg)
	case summaryGathering:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc", "q":
			m.closeSummary()
		}
		return m, nil
	case summaryStreaming, summaryDone:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc", "q":
			m.closeSummary()
			return m, nil
		case "t":
			// Toggle the collapsed thinking section (no-op when there's none).
			if _, thinking, open := m.currentSplit(); thinking != "" || open {
				m.summary.thinkExpanded = !m.summary.thinkExpanded
				m.renderSummaryViewBody()
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.summary.view, cmd = m.summary.view.Update(msg)
		return m, cmd
	}
	return m, nil
}

// handleSummaryPickerKey drives the days/hours/minutes picker.
func (m Model) handleSummaryPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closeSummary()
		return m, nil
	case "left", "h", "shift+tab":
		if m.summary.field > 0 {
			m.summary.field--
			m.summary.editing = false
		}
		return m, nil
	case "right", "l", "tab":
		if m.summary.field < 2 {
			m.summary.field++
			m.summary.editing = false
		}
		return m, nil
	case "up", "k", "+", "=":
		m.summaryAdjust(1)
		return m, nil
	case "down", "j", "-", "_":
		m.summaryAdjust(-1)
		return m, nil
	case "backspace":
		cur, _ := m.summaryFieldRef()
		*cur /= 10
		m.summary.editing = true
		return m, nil
	case "enter":
		return m, m.startChannelSummary()
	}
	if s := msg.String(); len(s) == 1 && s[0] >= '0' && s[0] <= '9' {
		m.summaryDigit(int(s[0] - '0'))
	}
	return m, nil
}

// summaryFieldRef returns a pointer to the focused field and its max.
func (m *Model) summaryFieldRef() (*int, int) {
	switch m.summary.field {
	case 0:
		return &m.summary.days, summaryMaxDays
	case 1:
		return &m.summary.hours, 23
	default:
		return &m.summary.minutes, 59
	}
}

// summaryAdjust nudges the focused field by delta, clamped to its range.
func (m *Model) summaryAdjust(delta int) {
	cur, max := m.summaryFieldRef()
	*cur = clampInt(*cur+delta, 0, max)
	m.summary.editing = false
}

// summaryDigit types a digit into the focused field. The first digit after
// navigating to a field replaces it; subsequent digits shift in.
func (m *Model) summaryDigit(d int) {
	cur, max := m.summaryFieldRef()
	base := *cur
	if !m.summary.editing {
		base = 0
		m.summary.editing = true
	}
	*cur = clampInt(base*10+d, 0, max)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// humanizeSince renders how long ago a unix-ms timestamp was, coarsely
// ("just now", "45m ago", "21h ago", "3d ago").
func humanizeSince(ms int64) string {
	if ms <= 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	default:
		return strconv.Itoa(int(d.Hours())/24) + "d ago"
	}
}

// formatWindow renders a duration as "2d 3h 30m", dropping zero units.
func formatWindow(d, h, mn int) string {
	parts := make([]string, 0, 3)
	if d > 0 {
		parts = append(parts, strconv.Itoa(d)+"d")
	}
	if h > 0 {
		parts = append(parts, strconv.Itoa(h)+"h")
	}
	if mn > 0 {
		parts = append(parts, strconv.Itoa(mn)+"m")
	}
	if len(parts) == 0 {
		return "0m"
	}
	return strings.Join(parts, " ")
}

// ---- rendering -----------------------------------------------------------

// summaryDims returns the result popup's outer width and viewport height.
// The chrome reserves header + rule + a thinking-status row + rule + hint
// (5) plus the border (2) around the viewport.
func (m *Model) summaryDims() (outerW, innerH int) {
	outerW = m.width * 4 / 5
	if outerW > 96 {
		outerW = 96
	}
	if outerW < 30 {
		outerW = 30
	}
	if outerW > m.width-2 {
		outerW = m.width - 2
	}
	if outerW < 1 {
		outerW = 1
	}
	bodyH := m.height - 4
	if bodyH < 9 {
		bodyH = 9
	}
	innerH = bodyH - 7
	if innerH < 3 {
		innerH = 3
	}
	return outerW, innerH
}

// sizeSummaryView keeps the result viewport in sync with the terminal.
func (m *Model) sizeSummaryView() {
	w, h := m.summaryDims()
	inner := w - 4 // border (2) + padding (1) each side
	if inner < 1 {
		inner = 1
	}
	m.summary.view.SetWidth(inner)
	m.summary.view.SetHeight(h)
}

// renderSummaryViewBody populates the result viewport: the expanded
// thinking transcript (when toggled open), then the answer, highlighting
// any @mentions of the current user. Errors replace the body with a hint.
func (m *Model) renderSummaryViewBody() {
	dim := lipgloss.NewStyle().Foreground(dimColor)
	if m.summary.err != nil {
		body := "Summary failed:\n\n" + m.summary.err.Error() +
			"\n\nCheck that the server at " + m.summaryEndpoint +
			" is reachable and that the model \"" + m.summaryModel + "\" is loaded" +
			" (curl " + m.summaryEndpoint + "/v1/models)."
		m.summary.view.SetContent(lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(body))
		return
	}
	answer, thinking, _ := m.currentSplit()
	streaming := m.summary.phase == summaryStreaming

	var parts []string
	if m.summary.thinkExpanded && thinking != "" {
		quote := lipgloss.NewStyle().Foreground(dimColor).Italic(true)
		for _, l := range strings.Split(thinking, "\n") {
			parts = append(parts, quote.Render("  │ "+l))
		}
		parts = append(parts, "")
	}
	switch {
	case strings.TrimSpace(answer) == "":
		if streaming {
			parts = append(parts, dim.Render("…"))
		} else {
			parts = append(parts, dim.Render("(the model returned an empty summary)"))
		}
	case streaming:
		// Stream raw text for responsiveness — partial markdown (unclosed
		// fences, half-typed lists) renders badly and re-parsing every chunk
		// is wasteful. The polished markdown render happens once, on done.
		parts = append(parts, highlightMentions(answer, m.summary.username))
	default:
		parts = append(parts, m.renderMarkdownAnswer(answer))
	}
	m.summary.view.SetContent(strings.Join(parts, "\n"))
}

// renderMarkdownAnswer renders the finished answer as terminal markdown
// using glamour (the same renderer glow uses), wrapped to the viewport
// width, then highlights the reader's @mentions. Falls back to plain
// highlighted text if glamour is unavailable or errors.
func (m *Model) renderMarkdownAnswer(md string) string {
	width := m.summary.view.Width()
	if width < 8 {
		width = 8
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
		glamour.WithEmoji(),
	)
	if err != nil {
		return highlightMentions(md, m.summary.username)
	}
	out, err := r.Render(md)
	if err != nil {
		return highlightMentions(md, m.summary.username)
	}
	// glamour pads with leading/trailing blank lines; trim them so the
	// popup doesn't open with a gap above the first line.
	out = strings.Trim(out, "\n")
	return highlightMentions(out, m.summary.username)
}

// highlightMentions tints "@username" (and the bare username as a whole
// word) wherever it appears in the text, so the reader can spot where
// they're called out. Returns text unchanged when username is empty.
func highlightMentions(text, username string) string {
	if username == "" {
		return text
	}
	re, err := regexp.Compile(`@?\b` + regexp.QuoteMeta(username) + `\b`)
	if err != nil {
		return text
	}
	return re.ReplaceAllStringFunc(text, func(s string) string {
		return mentionStyle.Render(s)
	})
}

// renderSummaryPopup dispatches to the per-phase renderer.
func (m *Model) renderSummaryPopup() string {
	switch m.summary.phase {
	case summaryPicking:
		return m.renderSummaryPicker()
	case summaryGathering:
		return m.renderSummaryGathering()
	case summaryStreaming, summaryDone:
		return m.renderSummaryView()
	}
	return ""
}

// summaryPopupWidth is the outer width of the small picker / gathering
// popups (the result popup uses summaryDims instead).
const summaryPopupWidth = 56

func (m *Model) renderSummaryPicker() string {
	w, inner := smallPopupDims(m.width)
	dim := lipgloss.NewStyle().Foreground(dimColor)

	fieldBox := func(val int, unit string, focused bool) string {
		st := lipgloss.NewStyle().Padding(0, 2).Border(lipgloss.RoundedBorder())
		if focused {
			st = st.BorderForeground(focusedColor).Foreground(focusedColor).Bold(true)
		} else {
			st = st.BorderForeground(dimColor).Foreground(dimColor)
		}
		return st.Render(strconv.Itoa(val) + unit)
	}
	caption := func(label string, focused bool) string {
		st := lipgloss.NewStyle().Width(7).Align(lipgloss.Center)
		if focused {
			st = st.Foreground(focusedColor)
		} else {
			st = st.Foreground(dimColor)
		}
		return st.Render(label)
	}

	fields := lipgloss.JoinHorizontal(lipgloss.Center,
		fieldBox(m.summary.days, "d", m.summary.field == 0), "  ",
		fieldBox(m.summary.hours, "h", m.summary.field == 1), "  ",
		fieldBox(m.summary.minutes, "m", m.summary.field == 2),
	)
	captions := lipgloss.JoinHorizontal(lipgloss.Top,
		caption("days", m.summary.field == 0), "  ",
		caption("hours", m.summary.field == 1), "  ",
		caption("mins", m.summary.field == 2),
	)
	picker := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).
		Render(lipgloss.JoinVertical(lipgloss.Center, fields, captions))

	rows := []string{
		titleStyle.Render("Summarize " + truncate(m.summary.label, inner-12)),
		dim.Render("How far back should I read?"),
		"",
		picker,
		"",
		dim.Render(strings.Repeat("─", inner)),
		dim.Render("↑/↓ adjust · ←/→ field · type digits · ↵ run · esc cancel"),
	}
	return smallPopup(w, rows)
}

func (m *Model) renderSummaryGathering() string {
	w, inner := smallPopupDims(m.width)
	dim := lipgloss.NewStyle().Foreground(dimColor)
	rows := []string{
		titleStyle.Render(truncate("Summarize "+m.summary.label, inner)),
		"",
		m.summary.spinner.View() + " " + m.summary.progress,
		"",
		dim.Render("esc to cancel"),
	}
	return smallPopup(w, rows)
}

// smallPopupDims returns the outer width and inner content width for the
// fixed-size picker / gathering popups.
func smallPopupDims(termWidth int) (w, inner int) {
	w = summaryPopupWidth
	if cap := termWidth - 4; cap > 0 && w > cap {
		w = cap
	}
	if w < 24 {
		w = 24
	}
	inner = w - 4
	if inner < 1 {
		inner = 1
	}
	return w, inner
}

func smallPopup(w int, rows []string) string {
	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(focusedColor).
		Padding(0, 1).
		Width(w).
		Render(strings.Join(rows, "\n"))
}

// renderSummaryView draws the streaming / done result popup: a header (with
// a global loader while streaming), a thinking-status row (collapsed, with
// its own loader while the model reasons), the scrollable body, and a hint.
func (m *Model) renderSummaryView() string {
	outerW, _ := m.summaryDims()
	inner := outerW - 4
	if inner < 1 {
		inner = 1
	}
	dim := lipgloss.NewStyle().Foreground(dimColor)
	streaming := m.summary.phase == summaryStreaming

	title := "Summary"
	if streaming {
		title = m.summary.spinner.View() + " Summary"
	}
	sub := m.summary.label
	if m.summary.window != "" {
		sub += " · last " + m.summary.window
	}
	if m.summary.count > 0 {
		sub += " · " + plural(m.summary.count, "msg", "msgs")
	}
	sub += " · " + m.summaryModel
	header := titleStyle.Render(title) + "  " + dim.Render(truncate(sub, inner-14))

	rule := dim.Render(strings.Repeat("─", inner))
	thinkRow := m.thinkingStatusLine()

	var hint string
	if streaming {
		hint = dim.Render("esc to cancel")
	} else {
		hint = dim.Render("↑/↓ scroll · esc close")
		if m.summary.username != "" && m.summary.err == nil {
			hint += dim.Render(" · ") + mentionStyle.Render("@"+m.summary.username) + dim.Render(" highlighted")
		}
	}

	rows := []string{header, rule, thinkRow, m.summary.view.View(), rule, hint}
	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(focusedColor).
		Padding(0, 1).
		Width(outerW).
		Render(strings.Join(rows, "\n"))
}

// thinkingStatusLine renders the always-present row above the body: blank
// when there's no reasoning, a spinner + "Thinking…" while the model is
// still reasoning, or a collapsed/expanded toggle hint once it's done.
func (m *Model) thinkingStatusLine() string {
	style := lipgloss.NewStyle().Foreground(dimColor).Italic(true)
	answer, thinking, open := m.currentSplit()
	if thinking == "" && !open {
		return "" // reserved blank row keeps the viewport height stable
	}
	streaming := m.summary.phase == summaryStreaming
	active := streaming && (open || strings.TrimSpace(answer) == "")
	if active {
		return style.Render(m.summary.spinner.View() + " Thinking…")
	}
	arrow, verb := "▸", "expand"
	if m.summary.thinkExpanded {
		arrow, verb = "▾", "collapse"
	}
	n := len(strings.Split(thinking, "\n"))
	return style.Render(fmt.Sprintf("%s Thinking · %s · t to %s", arrow, plural(n, "line", "lines"), verb))
}
