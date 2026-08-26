package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/replyto"
)

// TestWriteScreenshot renders the TUI over a set of invented teams, channels
// and messages and writes the frame — raw ANSI, exactly what the terminal would
// receive — to the file named by MATTERBOX_SCREENSHOT_OUT. It is skipped unless
// that variable is set, so `go test ./...` never touches the filesystem. Pass
// -count=1: a cached pass writes nothing.
//
// The point is a screenshot for the website that carries no real conversation:
// every name, channel and message below is made up. Size comes from
// MATTERBOX_SCREENSHOT_SIZE ("COLSxROWS", default 150x40).
//
// MATTERBOX_SCREENSHOT_VARIANT picks what is on screen: the default opens the
// thread pane beside the channel, "narrow" drops it (for a frame too narrow to
// hold it), and "feed" sits on the Feed tab instead.
//
//	MATTERBOX_SCREENSHOT_OUT=/tmp/shot.ans go test -count=1 ./internal/ui -run WriteScreenshot
func TestWriteScreenshot(t *testing.T) {
	out := os.Getenv("MATTERBOX_SCREENSHOT_OUT")
	if out == "" {
		t.Skip("set MATTERBOX_SCREENSHOT_OUT to write a screenshot")
	}
	cols, rows := 150, 40
	if s := os.Getenv("MATTERBOX_SCREENSHOT_SIZE"); s != "" {
		if _, err := fmt.Sscanf(s, "%dx%d", &cols, &rows); err != nil {
			t.Fatalf("bad MATTERBOX_SCREENSHOT_SIZE %q (want COLSxROWS): %v", s, err)
		}
	}

	m := demoModel(cols, rows, os.Getenv("MATTERBOX_SCREENSHOT_VARIANT"))
	frame := m.renderViewContent()
	if err := os.WriteFile(out, []byte(frame+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The frame carries images as Kitty placeholder cells, which name an id and
	// nothing else. Whoever renders the frame elsewhere needs the placement too,
	// so write it beside the frame.
	if err := os.WriteFile(out+".images.json", demoImagesJSON(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%dx%d)", out, cols, rows)
}

// demoUsers are the invented people in the screenshot: id → username.
var demoUsers = map[string]string{
	"u-me":    "corne",
	"u-sofie": "sofie",
	"u-jonas": "jonas",
	"u-mira":  "mira",
	"u-teun":  "teun",
	"u-bot":   "deploybot",
}

// demoModel builds a fully rendered model over the invented server: on the
// Orbit team with #releases open, or on the Feed tab when variant says so.
// The thread pane comes along unless the frame is too narrow for it.
func demoModel(cols, rows int, variant string) Model {
	m := newTestModel()
	m.width, m.height = cols, rows
	m.me = &model.User{Id: "u-me", Username: "corne"}
	for id, name := range demoUsers {
		m.userNames[id] = name
	}
	m.statuses = map[string]string{
		"u-sofie": model.StatusOnline,
		"u-jonas": model.StatusAway,
		"u-mira":  model.StatusOnline,
		"u-teun":  model.StatusDnd,
	}

	m.teams = []*model.Team{
		{Id: "t-orbit", Name: "orbit", DisplayName: "Orbit"},
		{Id: "t-lab", Name: "lab", DisplayName: "Lab"},
	}
	m.teamOrder = []string{"orbit", "lab"}

	open := func(id, name, display, purpose string) *model.Channel {
		return &model.Channel{
			Id: id, TeamId: "t-orbit", Type: model.ChannelTypeOpen,
			Name: name, DisplayName: display, Purpose: purpose,
		}
	}
	dm := func(id, partner string, last int64) *model.Channel {
		return &model.Channel{
			Id: id, Type: model.ChannelTypeDirect,
			Name: "u-me__" + partner, LastPostAt: last,
		}
	}
	now := time.Now()
	ms := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }

	m.bucketChannels([]*model.Channel{
		open("c-general", "general", "general", "Anything goes"),
		open("c-releases", "releases", "releases", "Ship notes and release chatter"),
		open("c-terminal", "terminal", "terminal", "Terminals, fonts, escape codes"),
		open("c-random", "random", "random", ""),
		{Id: "c-lab-1", TeamId: "t-lab", Type: model.ChannelTypeOpen, Name: "hardware", DisplayName: "hardware"},
		dm("c-dm-sofie", "u-sofie", ms(4*time.Minute)),
		dm("c-dm-jonas", "u-jonas", ms(2*time.Hour)),
		dm("c-dm-mira", "u-mira", ms(26*time.Hour)),
	})
	m.unread = map[string]int{"c-general": 3, "c-dm-sofie": 2, "c-random": 1}
	m.mentions = map[string]int{"c-dm-sofie": 1}

	// Tab order is Feed(0), DMs(1), Search(2), then the teams — land on Orbit.
	m.teamIdx = teamTabIndex(&m, "t-orbit")
	m.openChannelID = "c-releases"
	m.channelIdx = channelIndex(&m, "c-releases")

	m.posts = demoPosts(now)
	m.postIdx = len(m.posts) - 1
	m.focus = focusMessages
	m.status = ""
	m.loading = false

	if variant == "feed" {
		return feedModel(m, now)
	}

	// Open the thread pane on the release thread: the nested tree is the thing
	// worth showing, and it is what matterbox draws that no other client does.
	if variant != "narrow" {
		m.threadOpen = true
		m.threadChannelID = "c-releases"
		m.threadRootID = "proot"
		m.threadPosts = nestSortThread(demoThread(now))
		m.threadIdx = len(m.threadPosts) - 1
	}

	m.resizeMessagesViewport()
	m.resizeInput()
	installDemoPhoto(&m)
	m.renderMessages()
	m.renderThread()
	m.msgsView.GotoBottom()
	m.threadView.GotoBottom()
	return m
}

// feedModel puts the model on the Feed tab with the unread bubbles built: the
// aggregated view of everything waiting across channels and DMs.
func feedModel(m Model, now time.Time) Model {
	for i := 0; i < 16; i++ {
		if kind, _, _ := m.tabAt(i); kind == tabFeed {
			m.teamIdx = i
			break
		}
	}
	m.focus = focusFeed
	m.threadOpen = false
	m.feed.entries = demoFeed(now)
	m.feed.built = true
	m.feed.idx = 0

	// resizeMessagesViewport sizes every pane's viewport, the feed's included;
	// the bubbles are only painted into it by renderFeedResults.
	m.resizeMessagesViewport()
	m.resizeInput()
	m.renderFeedResults()
	return m
}

// demoFeed is the unread feed: a busy channel with three messages waiting, a DM
// that mentions you, and a quiet channel with one — each with a couple of
// already-read messages above the divider for context.
func demoFeed(now time.Time) []feedEntry {
	at := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	p := func(id, ch, user string, d time.Duration, msg string) *model.Post {
		return &model.Post{Id: id, ChannelId: ch, UserId: user, CreateAt: at(d), Message: msg}
	}
	return []feedEntry{
		{
			channelID: "c-general",
			context: []*model.Post{
				p("g1", "c-general", "u-teun", 95*time.Minute, "Coffee machine is fixed. That is the whole announcement."),
				p("g2", "c-general", "u-mira", 71*time.Minute, "Heroic. Who do we thank?"),
			},
			unread: []*model.Post{
				p("g3", "c-general", "u-teun", 24*time.Minute, "Whoever left the descaling kit on my desk, anonymously."),
				p("g4", "c-general", "u-sofie", 12*time.Minute, "Heads up: the office router reboots at 19:00, so `listen` drops its socket for a minute."),
				p("g5", "c-general", "u-jonas", 3*time.Minute, "It reconnects on its own — you lose the WebSocket, not the cache."),
			},
		},
		{
			channelID: "c-dm-sofie",
			mention:   true,
			context: []*model.Post{
				p("d1", "c-dm-sofie", "u-me", 3*time.Hour, "Sent you the branch — the octant table is the only interesting part."),
			},
			unread: []*model.Post{
				p("d2", "c-dm-sofie", "u-sofie", 9*time.Minute, "Read it. One question about the tie-breaking and then I am happy."),
				p("d3", "c-dm-sofie", "u-sofie", 4*time.Minute, "@corne why keep the earlier family on an exact tie?"),
			},
		},
		{
			channelID: "c-random",
			context: []*model.Post{
				p("r1", "c-random", "u-mira", 5*time.Hour, "My terminal now has more fonts than my laptop has RAM."),
			},
			unread: []*model.Post{
				p("r2", "c-random", "u-teun", 47*time.Minute, "That is not a problem, that is a hobby."),
			},
		},
	}
}

// demoPhotoID is the file the screenshot's one image attachment hangs off, and
// demoPhotoW/H its pixel size — the aspect the thumbnail is fitted to.
const (
	demoPhotoID = "fphoto"
	demoPhotoW  = 4416
	demoPhotoH  = 2944
)

// demoImages records the placement installed for this frame, keyed by the id
// its placeholder cells carry. Populated by installDemoPhoto.
var demoImages []demoImage

type demoImage struct {
	ID   uint32 `json:"id"`
	Name string `json:"name"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func demoImagesJSON() []byte {
	b, err := json.Marshal(demoImages)
	if err != nil {
		return []byte("[]")
	}
	return b
}

func demoPhotoFile() *model.FileInfo {
	return &model.FileInfo{
		Id: demoPhotoID, Name: "DSCF5311.jpg", Extension: "jpg",
		MimeType: "image/jpeg", Width: demoPhotoW, Height: demoPhotoH, Size: 6_412_800,
	}
}

// installDemoPhoto puts a built thumbnail in place for the attachment, as if it
// had been fetched, decoded and transmitted to the terminal. That leaves the
// frame carrying the Kitty unicode placeholder cells a real run would emit —
// which is exactly what the website's converter turns back into an <img>.
//
// The placement has to be the one this layout would have chosen, or sight()
// decides it no longer fits and throws it away: same box the panes agree on,
// same cell size, same aspect fit.
func installDemoPhoto(m *Model) {
	// New(nil, …) leaves thumbnails off, the config default; the frame is for a
	// terminal that can draw them.
	m.inlineImg = newInlineImages("auto")
	m.cellPxW, m.cellPxH = 10, 20
	m.emojiImg.setProbeOK()
	m.emojiImg.setColorProfile(true)
	box := m.thumbFitBox()
	if box == 0 {
		return
	}
	cols, rows := inlineThumbCells(demoPhotoW, demoPhotoH, box, m.cellPxW, m.cellPxH)
	const id = 0xC0FFEE
	m.inlineImg.markReady(demoPhotoID, readyInlineImg{
		id: id, rows: rows, cols: cols, box: box,
		placeholder: kittyPlaceholder(id, rows, cols),
		frameSeqs:   []string{""},
	})
	demoImages = []demoImage{{ID: id, Name: "DSCF5311.jpg", Cols: cols, Rows: rows}}
}

// demoThread is the reply tree hanging off proot, with two replies pointing at
// a specific earlier reply rather than at the root — the out-of-band parent link
// the thread pane draws as a tree.
func demoThread(now time.Time) []*model.Post {
	at := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	r := func(id, user string, d time.Duration, msg string) *model.Post {
		return &model.Post{Id: id, UserId: user, ChannelId: "c-releases", RootId: "proot", CreateAt: at(d), Message: msg}
	}
	return []*model.Post{
		{Id: "proot", ChannelId: "c-releases", UserId: "u-sofie", CreateAt: at(88 * time.Minute),
			Message: "Cutting 0.9 tomorrow — anything still open?"},
		r("th1", "u-teun", 80*time.Minute, "Only the docs pass, and that is not a blocker."),
		r("th2", "u-mira", 74*time.Minute, replyto.Attach("It is if the rules examples are wrong — people copy those verbatim.", "th1")),
		r("th3", "u-teun", 70*time.Minute, replyto.Attach("Fair. I'll take the rules page then.", "th2")),
		r("th4", "u-me", 2*time.Minute, "Fixed in docs/rules.md — `every: 15m`."),
	}
}

// teamTabIndex finds the tab index of a team id, falling back to the last tab.
func teamTabIndex(m *Model, teamID string) int {
	for i := 0; i < 16; i++ {
		if kind, id, _ := m.tabAt(i); kind == tabTeam && id == teamID {
			return i
		}
	}
	return 0
}

// channelIndex is the position of a channel in the current sidebar list.
func channelIndex(m *Model, id string) int {
	for i, c := range m.visibleChannels() {
		if c.Id == id {
			return i
		}
	}
	return 0
}

// demoPosts is the invented conversation. Times run from ~90 minutes ago to
// just now so the pane shows a natural spread of timestamps.
func demoPosts(now time.Time) []*model.Post {
	at := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	p := func(id, user string, d time.Duration, msg string) *model.Post {
		return &model.Post{Id: id, UserId: user, ChannelId: "c-releases", CreateAt: at(d), Message: msg}
	}
	react := func(post *model.Post, emoji string, users ...string) *model.Post {
		if post.Metadata == nil {
			post.Metadata = &model.PostMetadata{}
		}
		for _, u := range users {
			post.Metadata.Reactions = append(post.Metadata.Reactions,
				&model.Reaction{PostId: post.Id, UserId: u, EmojiName: emoji})
		}
		return post
	}

	root := p("proot", "u-sofie", 88*time.Minute,
		"Cutting 0.9 tomorrow — anything still open?")
	root.ReplyCount = 4
	// An image attachment, so the frame shows what an inline thumbnail looks
	// like. The bytes never come from a server here — demoModel installs the
	// placement directly (see demoPhoto).
	root.Metadata = &model.PostMetadata{Files: []*model.FileInfo{demoPhotoFile()}}

	posts := []*model.Post{
		root,
		react(p("p2", "u-jonas", 84*time.Minute,
			"The octant fallback for terminals without the new block glyphs — small, up in an hour."),
			"+1", "u-sofie", "u-mira"),
		p("p3", "u-mira", 61*time.Minute,
			"Semantic search reindexes on every start since the embedding-dim change:\n\n```sh\nmatterbox embed --reset\n```"),
		react(p("p4", "u-teun", 52*time.Minute,
			"Confirmed on a cold cache: 12k messages, ~40s on CPU. Fine, just quiet about it."),
			"eyes", "u-sofie"),
		p("p5", "u-bot", 44*time.Minute,
			"pipeline #4812 passed on `main` — linux/amd64, linux/arm64, darwin/arm64 built in 2m18s"),
		p("p6", "u-sofie", 33*time.Minute,
			"Nice. @corne did drag-and-drop get the macOS paste-is-a-path check?"),
		react(p("p7", "u-me", 29*time.Minute,
			"Both now. A paste that is nothing but existing file paths becomes an attachment; `attach_on_drop: false` opts out."),
			"tada", "u-sofie", "u-jonas", "u-mira"),
		p("p8", "u-jonas", 18*time.Minute,
			"Then we're good. I'll tag once the octant patch lands."),
		p("p9", "u-mira", 6*time.Minute,
			"One more: the rules docs still say `every:` takes seconds."),
	}

	reply := p("p10", "u-me", 2*time.Minute, "Fixed in docs/rules.md — `every: 15m`.")
	reply.RootId = "proot"
	posts = append(posts, reply)
	return posts
}
