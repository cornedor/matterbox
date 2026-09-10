package telemetry

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestCatalogueIsWellFormed runs the catalogue's own structural checks: every
// event named in snake case, every event with a stated purpose, every enum with
// values to compare against.
func TestCatalogueIsWellFormed(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestDocsAreCurrent is what lets docs/telemetry.md be trusted: it is generated
// from the catalogue, and this fails the moment the checked-in file stops
// matching. A new event therefore cannot ship without also appearing in the
// published list of what matterbox sends.
func TestDocsAreCurrent(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "telemetry.md")
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v — run `go generate ./internal/telemetry`", path, err)
	}
	if string(onDisk) != Markdown() {
		t.Errorf("docs/telemetry.md is stale; run `go generate ./internal/telemetry`")
	}
}

// TestReportableKeysAreNotText is a privacy invariant, not a style check. The
// unhandled_key event names the keystroke that did nothing, and the only way
// that can never reconstruct typed text is if no single-character keystroke is
// reportable at all. Adding "z" to the list would leak a keystroke from someone
// typing into a pane they mistook for the composer.
func TestReportableKeysAreNotText(t *testing.T) {
	for _, k := range ReportableKeys {
		if len([]rune(k)) <= 1 {
			t.Errorf("ReportableKeys contains the single-character keystroke %q: "+
				"a bare character is typed text and must never be reported", k)
		}
	}
	// The catalogue must actually use this set for the key property, or the
	// invariant above protects nothing.
	spec, ok := Spec("unhandled_key")
	if !ok {
		t.Fatal("unhandled_key is not catalogued")
	}
	p, ok := spec.prop("key")
	if !ok {
		t.Fatal("unhandled_key has no key property")
	}
	if !sameSet(p.Values, ReportableKeys) {
		t.Error("unhandled_key.key does not draw from ReportableKeys")
	}
}

// TestCaptureDropsUncataloguedEvent covers the outer gate: an event nobody
// declared goes nowhere, so a call site cannot invent one.
func TestCaptureDropsUncataloguedEvent(t *testing.T) {
	t.Cleanup(Close)
	in, url := newIngest(t)
	cfg := consentingConfig(t)
	t.Setenv(KeyEnv, "phc_test")
	t.Setenv(HostEnv, url)
	Start(cfg)

	Capture("definitely_not_catalogued", map[string]any{"anything": 1})
	Close()

	if body := in.all(); strings.Contains(body, "definitely_not_catalogued") {
		t.Errorf("an uncatalogued event reached the wire: %s", body)
	}
}

// TestSanitizeDropsUserContent is the guarantee the whole catalogue exists for.
// A call site that mistakenly passes a message body, a username, a channel name
// or a server URL must not leak it — the property is either undeclared (dropped)
// or declared as an enum whose set the value isn't in (also dropped).
func TestSanitizeDropsUserContent(t *testing.T) {
	spec, ok := Spec("message_sent")
	if !ok {
		t.Fatal("message_sent is not catalogued")
	}
	leaky := map[string]any{
		// Undeclared properties: the shapes a careless call site would add.
		"text":        "shall we ship it on friday?",
		"username":    "ana.dorrestijn",
		"channel":     "incidents-payments",
		"server_url":  "https://chat.example.com",
		"post_id":     "8xj3k1p9qwertyuiopasdfghjk",
		"attachments": 2, // declared, but as an enum of buckets — 2 is not a label
		// Declared and valid, so this one must survive.
		"surface": "composer",
	}
	clean, dropped := spec.sanitize(leaky)

	for _, name := range []string{"text", "username", "channel", "server_url", "post_id"} {
		if _, present := clean[name]; present {
			t.Errorf("property %q survived sanitisation", name)
		}
	}
	if _, present := clean["attachments"]; present {
		t.Error("a raw integer passed to a bucketed enum survived sanitisation")
	}
	if clean["surface"] != "composer" {
		t.Errorf("a valid property was dropped: surface = %v", clean["surface"])
	}
	if len(dropped) != 6 {
		t.Errorf("dropped = %v, want all six bad properties", dropped)
	}
}

// TestCounterMapDropsUnknownKeys: the snapshot's maps are whitelists too, so an
// action id that isn't in the registry — or a channel name mistakenly used as a
// key — never lands in a property.
func TestCounterMapDropsUnknownKeys(t *testing.T) {
	spec, _ := Spec("usage_snapshot")
	clean, _ := spec.sanitize(map[string]any{
		"actions": map[string]int{
			"react":              3,
			"incidents-payments": 1, // a channel name is not an action id
			"open_thread":        0, // zero counts are noise
		},
	})
	got, ok := clean["actions"].(map[string]int)
	if !ok {
		t.Fatalf("actions property has type %T, want map[string]int", clean["actions"])
	}
	if got["react"] != 3 {
		t.Errorf("a real action was dropped: %v", got)
	}
	if _, present := got["incidents-payments"]; present {
		t.Error("a non-whitelisted counter key survived")
	}
	if _, present := got["open_thread"]; present {
		t.Error("a zero count survived")
	}
}

// TestFramesRejectPathsAndArguments: panic reports carry stack frames, which is
// only safe because nothing but matterbox function names gets through.
func TestFramesRejectPathsAndArguments(t *testing.T) {
	spec, _ := Spec("panic_recovered")
	clean, _ := spec.sanitize(map[string]any{
		"frames": []string{
			"internal/ui.(*Model).renderMessages",
			"/home/ana/go/pkg/mod/charm.land/bubbletea.Run",
			"github.com/posthog/posthog-go.(*client).Enqueue",
			"internal/store.Upsert",
		},
	})
	got, ok := clean["frames"].([]string)
	if !ok {
		t.Fatalf("frames has type %T", clean["frames"])
	}
	want := []string{"internal/ui.(*Model).renderMessages", "internal/store.Upsert"}
	if !sameSet(got, want) {
		t.Errorf("frames = %v, want only matterbox frames %v", got, want)
	}
}

// TestScrubRedactsIdentifiers walks the shapes that actually turn up in error
// text from this app.
func TestScrubRedactsIdentifiers(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"path with username", "open /home/ana/.config/matterbox/messages.db: permission denied",
			"open <path>: permission denied"},
		{"server url", "Post https://chat.example.com/api/v4/posts: dial tcp: timeout",
			"Post <url>: dial tcp: timeout"},
		{"websocket url", "websocket: bad handshake for wss://chat.example.com/api/v4/websocket",
			"websocket: bad handshake for <url>"},
		{"quoted message body", `sending post "shall we ship it?" failed`,
			"sending post <quoted> failed"},
		{"mattermost id", "channel 8xj3k1p9qwertyuiopasdfghjk not found",
			"channel <id> not found"},
		{"mention", "user @ana.dorrestijn is not a member",
			"user <mention> is not a member"},
		{"email", "no account for ana@example.com", "no account for <email>"},
		{"token", "invalid token abcdefghijklmnopqrstuvwxyz0123456789",
			"invalid token <token>"},
		{"nothing left to say", "/home/ana/x.db", "<redacted>"},
		{"clean error is untouched", "permission denied", "permission denied"},
		// The shapes Go's net package produces. None of these carry a scheme,
		// so reURL never sees them, and every one of them names either the
		// Mattermost server or the machine matterbox is running on.
		{"dns lookup", "ws dial: dial tcp: lookup chat.example.com: no such host",
			"ws dial: dial tcp: lookup <host>: no such host"},
		{"single-label host lookup", "dial tcp: lookup mattermost: no such host",
			"dial tcp: lookup <host>: no such host"},
		{"server ip and port", "dial tcp 168.119.88.168:443: connect: network is unreachable",
			"dial tcp <addr>: connect: network is unreachable"},
		{"both ends of a connection", "read tcp 192.168.1.124:49704->168.119.88.168:443: read: connection reset by peer",
			"read tcp <addr>-><addr>: read: connection reset by peer"},
		{"bracketed ipv6", "dial tcp [2001:db8::1]:443: connect: refused",
			"dial tcp <addr>: connect: refused"},
		// "i/o" losing its slash to the path pattern is the trade the path
		// pattern already made; the address is the part that matters here.
		{"host with a port", "dial tcp chat.example.com:443: i/o timeout",
			"dial tcp <addr>: i<path> timeout"},
		{"host in a certificate error", "x509: certificate is valid for other.example.org, not chat.example.com",
			"x509: certificate is valid for <host>, not <host>"},
		// A dotted Mattermost error id is not a hostname, and it is the only
		// descriptive part of the message.
		{"error id survives", "NewWebSocketClient: model.websocket_client.connect_fail.app_error",
			"NewWebSocketClient: model.websocket_client.connect_fail.app_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Scrub(c.in); got != c.want {
				t.Errorf("Scrub(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

// TestScrubIsIdempotentAndBounded: the placeholders must not themselves be
// redactable, and no error text may be long enough to smuggle something past
// the patterns.
func TestScrubIsIdempotentAndBounded(t *testing.T) {
	in := "open /home/ana/x: failed for @bob at https://example.com"
	once := Scrub(in)
	if twice := Scrub(once); twice != once {
		t.Errorf("Scrub is not idempotent:\n once %q\ntwice %q", once, twice)
	}
	long := strings.Repeat("the operation failed because of reasons ", 40)
	if got := Scrub(long); len([]rune(got)) > scrubMax+1 {
		t.Errorf("Scrub returned %d runes, want <= %d", len([]rune(got)), scrubMax+1)
	}
}

// TestScrubErrorTextPropertyIsScrubbed proves the scrubbing is not merely
// available but actually applied by the catalogue, which is what makes it a
// guarantee rather than a helper nobody called.
func TestScrubErrorTextPropertyIsScrubbed(t *testing.T) {
	spec, _ := Spec("operation_failed")
	clean, dropped := spec.sanitize(map[string]any{
		"where":  "store.open",
		"class":  "disk",
		"detail": errors.New(`open /home/ana/.config/matterbox/messages.db: no space left`).Error(),
	})
	if len(dropped) > 0 {
		t.Fatalf("valid properties were dropped: %v", dropped)
	}
	detail, _ := clean["detail"].(string)
	if strings.Contains(detail, "/home/ana") {
		t.Errorf("detail still carries a path: %q", detail)
	}
	if !strings.Contains(detail, "no space left") {
		t.Errorf("detail lost the useful part: %q", detail)
	}
}

// TestBucketsOnlyReturnDeclaredLabels: every bucket function's output must be a
// member of the set the catalogue declares for it, or a property would be
// silently dropped in production. Checked across the boundaries and well past
// them rather than at a couple of sample points.
func TestBucketsOnlyReturnDeclaredLabels(t *testing.T) {
	ints := []int{-5, -1, 0, 1, 2, 5, 6, 20, 21, 40, 41, 100, 101, 160, 161, 500,
		501, 1000, 1001, 2000, 2001, 100000}
	check := func(name string, set []string, got string, in any) {
		if !inSet(set, got) {
			t.Errorf("%s(%v) = %q, which is not in its declared set %v", name, in, got, set)
		}
	}
	for _, n := range ints {
		check("Count", CountBuckets, Count(n), n)
		check("Length", LengthBuckets, Length(n), n)
		check("Cols", ColsBuckets, Cols(n), n)
		check("Rows", RowsBuckets, Rows(n), n)
		check("Rank", RankBuckets, Rank(n), n)
	}
	for _, ms := range []int64{-1, 0, 1, 9, 10, 49, 50, 199, 200, 999, 1000, 4999,
		5000, 29999, 30000, 1 << 40} {
		check("Millis", MillisBuckets, Millis(ms), ms)
	}
	for _, sec := range []int64{-1, 0, 4, 5, 29, 30, 119, 120, 599, 600, 3599,
		3600, 14399, 14400, 1 << 30} {
		check("Seconds", SecondsBuckets, Seconds(sec), sec)
	}
	for _, by := range []int64{-1, 0, 1 << 15, 1 << 16, 1 << 20, 1 << 24, 1 << 27, 1 << 40} {
		check("Bytes", BytesBuckets, Bytes(by), by)
	}
}

// TestEveryEmitterUsesACataloguedEvent walks the source of emit.go and checks
// that every event name it passes to Capture is declared. This is the check
// that keeps the docs complete: an emitter naming an undeclared event would be
// a silent no-op in production, and its event would be missing from the docs.
func TestEveryEmitterUsesACataloguedEvent(t *testing.T) {
	for _, file := range []string{"emit.go", "counters.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		calls := regexp.MustCompile(`(?:capture|Capture)\("([a-z_]+)"`).FindAllStringSubmatch(string(src), -1)
		if len(calls) == 0 {
			t.Errorf("%s: found no Capture calls — has the emitter layer moved?", file)
		}
		for _, m := range calls {
			if _, ok := Spec(m[1]); !ok {
				t.Errorf("%s emits %q, which is not in the catalogue (so it would "+
					"be dropped at runtime and missing from docs/telemetry.md)", file, m[1])
			}
		}
	}
}

// TestEveryCataloguedEventIsEmitted is the honesty check on docs/telemetry.md.
// An event in the catalogue is a promise that matterbox sends it, so this walks
// the whole repository for a call to the event's emitter and fails when there
// isn't one — unless the event is marked Planned, which is how the catalogue
// says "designed, not yet wired" out loud instead of quietly overstating what
// the build does.
//
// Scanning the source is crude but it is the only check that cannot be satisfied
// by writing an emitter nobody calls.
func TestEveryCataloguedEventIsEmitted(t *testing.T) {
	repo := repoSource(t)
	for _, e := range Events {
		// usage_snapshot is emitted by the package's own flush path rather than
		// from a call site, so there is nothing outside to look for.
		if e.Emitter == "Flush" {
			continue
		}
		// Trigger names the call the app makes when it isn't the emitter
		// itself; see EventSpec.Trigger.
		fn := e.Emitter
		if e.Trigger != "" {
			fn = e.Trigger
		}
		called := strings.Contains(repo, "telemetry."+fn+"(")
		switch {
		case called && e.Planned:
			t.Errorf("event %q is emitted from the app but still marked Planned — "+
				"drop the flag and run `go generate ./internal/telemetry`", e.Name)
		case !called && !e.Planned:
			t.Errorf("event %q has no call to telemetry.%s anywhere in the repo. "+
				"Either wire it up or mark it Planned, so docs/telemetry.md doesn't "+
				"promise an event that never arrives.", e.Name, fn)
		}
	}
}

// TestEmittersExist: every catalogued Emitter must name a real function in
// emit.go, or the test above would silently pass by looking for nothing.
func TestEmittersExist(t *testing.T) {
	src, err := os.ReadFile("emit.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, e := range Events {
		if e.Emitter == "Flush" {
			continue // lives in counters.go
		}
		if !strings.Contains(body, "func "+e.Emitter+"(") {
			t.Errorf("event %q names emitter %q, which is not defined in emit.go",
				e.Name, e.Emitter)
		}
	}
}

// repoSource concatenates every non-test Go file in the repository, for the
// call-site scan above. Test files are excluded on purpose: a call from a test
// is not the app sending an event.
func repoSource(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..")
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip the telemetry package itself — the emitters are defined
			// there, and its own tests call them.
			if path == filepath.Join(root, "internal", "telemetry") {
				return fs.SkipDir
			}
			if name := d.Name(); name == ".git" || name == "server" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b.Write(src)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.Len() == 0 {
		t.Fatal("found no source to scan — has the layout changed?")
	}
	return b.String()
}

// TestSnapshotResetsAndSkipsIdleWindows: an idle session must send nothing at
// all, and a flushed window must not be counted twice.
func TestSnapshotResetsAndSkipsIdleWindows(t *testing.T) {
	t.Cleanup(func() { active.Store(false); tally = &counters{} })
	tally = &counters{}
	active.Store(true)

	if props := tally.snapshot(false); props != nil {
		t.Errorf("an untouched window produced a snapshot: %v", props)
	}

	Action("react", "focus:messages")
	Action("react", "focus:messages")
	Mouse("channel")
	props := tally.snapshot(false)
	if props == nil {
		t.Fatal("a window with activity produced no snapshot")
	}
	if got := props["actions"].(map[string]int)["react"]; got != 2 {
		t.Errorf("actions[react] = %d, want 2", got)
	}
	if got := props["surfaces"].(map[string]int)["focus:messages"]; got != 2 {
		t.Errorf("surfaces[focus:messages] = %d, want 2", got)
	}
	if used, _ := props["actions_used"].([]string); !sameSet(used, []string{"react"}) {
		t.Errorf("actions_used = %v, want [react]", used)
	}
	if props := tally.snapshot(false); props != nil {
		t.Errorf("the window was not reset after a flush: %v", props)
	}
	// Session totals survive a flush — they describe the session, not the window.
	if actions, _, _, _ := tally.sessionTotals(); actions != 2 {
		t.Errorf("session action total = %d, want 2", actions)
	}
}

// TestMashDetectionFiresOnce: repeated presses report a single friction signal
// at the threshold, not one per press — a stuck key must not become a flood.
func TestMashDetectionFiresOnce(t *testing.T) {
	t.Cleanup(func() { active.Store(false); tally = &counters{} })
	tally = &counters{}
	active.Store(true)

	fired := 0
	for i := 0; i < 10; i++ {
		if mashed, _ := Action("send", "focus:input"); mashed {
			fired++
		}
	}
	if fired != 1 {
		t.Errorf("mash reported %d times over 10 presses, want 1", fired)
	}
	// A different action resets the run.
	Action("up", "focus:messages")
	if mashed, _ := Action("send", "focus:input"); mashed {
		t.Error("mash fired again immediately after the run was broken")
	}
}

// TestCountersAreNoopWhenDisabled: the opted-out path must not even allocate,
// since these sit on the keystroke path.
func TestCountersAreNoopWhenDisabled(t *testing.T) {
	t.Cleanup(func() { tally = &counters{} })
	tally = &counters{}
	active.Store(false)

	Action("react", "focus:messages")
	Mouse("channel")
	Palette("summarize")
	Slash("me")
	Feature("ai_search")
	Friction("unhandled_key")
	countMessageSent()
	countChannelOpened()

	if tally.actions != nil || tally.mouse != nil || tally.palette != nil ||
		tally.slash != nil || tally.features != nil || tally.friction != nil {
		t.Error("counters allocated with telemetry disabled")
	}
	if tally.totalActions != 0 || tally.messagesSent != 0 || tally.channelsOpened != 0 {
		t.Error("session totals moved with telemetry disabled")
	}
}
