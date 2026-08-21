package telemetry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/posthog/posthog-go"
)

// TestModuleRootResolves guards the assumption everything else here rests on.
// moduleRoot is derived by walking up from this file's recorded path, so moving
// the telemetry package breaks it — and a broken prefix is silent: every frame
// would fail repoFile, and exceptions would arrive with no stack rather than
// with a wrong one.
func TestModuleRootResolves(t *testing.T) {
	if moduleRoot == "" {
		t.Fatal("moduleRoot did not resolve; has internal/telemetry moved?")
	}
	if !strings.HasSuffix(moduleRoot, "/") {
		t.Errorf("moduleRoot %q should end in a separator", moduleRoot)
	}
	// It must name a real directory holding the repository's go.mod.
	if _, err := os.Stat(filepath.Join(moduleRoot, "go.mod")); err != nil {
		t.Errorf("moduleRoot %q does not hold go.mod: %v", moduleRoot, err)
	}
	_, self, _, _ := runtime.Caller(0)
	if got := repoFile(self); got != "internal/telemetry/errors_test.go" {
		t.Errorf("repoFile(this file) = %q, want internal/telemetry/errors_test.go", got)
	}
}

// TestRepoFileFailsClosed is the privacy check on the path reduction. Anything
// that is not provably a matterbox source path must reduce to "" so the frame
// is dropped — a dependency, the standard library, a vendored copy under our
// own module root, or a path we cannot place at all.
func TestRepoFileFailsClosed(t *testing.T) {
	for _, abs := range []string{
		"",
		"/usr/lib/golang/src/runtime/panic.go",
		"/home/ana/go/pkg/mod/github.com/posthog/posthog-go@v1.23.1/posthog.go",
		"/home/ana/Development/matterbox-other/internal/ui/view.go",
		moduleRoot + "vendor/github.com/foo/bar.go",
		moduleRoot + "server/main.go",
		"internal/ui/view.go", // relative: not something the compiler records
	} {
		if got := repoFile(abs); got != "" {
			t.Errorf("repoFile(%q) = %q, want \"\" — a frame that is not provably "+
				"ours must be dropped, not reported", abs, got)
		}
	}
	for abs, want := range map[string]string{
		moduleRoot + "main.go":             "main.go",
		moduleRoot + "internal/ui/view.go": "internal/ui/view.go",
	} {
		if got := repoFile(abs); got != want {
			t.Errorf("repoFile(%q) = %q, want %q", abs, got, want)
		}
	}
}

// TestStackFramesAreRepoOnly is the end-to-end version of the check above: a
// real captured stack must contain only matterbox frames, and no frame may
// carry an absolute path, a home directory or a module-cache path.
func TestStackFramesAreRepoOnly(t *testing.T) {
	frames := viaHelper()
	if len(frames) == 0 {
		t.Fatal("captured no frames at all")
	}
	home, _ := os.UserHomeDir()
	for _, f := range frames {
		if strings.HasPrefix(f.Filename, "/") || strings.Contains(f.Filename, `\`) {
			t.Errorf("frame carries an absolute path: %q", f.Filename)
		}
		if home != "" && strings.Contains(f.Filename, home) {
			t.Errorf("frame carries the home directory: %q", f.Filename)
		}
		if strings.Contains(f.Filename, "pkg/mod") || strings.Contains(f.Filename, "vendor/") {
			t.Errorf("frame is not ours: %q", f.Filename)
		}
		if !reRepoFile.MatchString(f.Filename) {
			t.Errorf("frame file %q is not a recognised repository path", f.Filename)
		}
		if !reFrame.MatchString(f.Function) {
			t.Errorf("frame function %q is not a recognised matterbox function", f.Function)
		}
		if f.InstructionAddr != "" || f.SymbolAddr != "" || f.ImageAddr != "" {
			t.Errorf("frame %q carries a raw address, which identifies the binary", f.Function)
		}
		if f.LineNo <= 0 {
			t.Errorf("frame %q has no line number", f.Function)
		}
	}
	// Wire order: outermost first, the failure last. The helper is called by
	// the test, so the helper must be the last frame.
	last := frames[len(frames)-1].Function
	if !strings.HasSuffix(last, "viaHelper") {
		t.Errorf("innermost frame is %q, want the helper — frames are in the wrong order", last)
	}
}

func viaHelper() []posthog.StackFrame { return stackFrames(0) }

// TestScrubbedMessageSurvivesReport: the description on a report is the
// scrubber's output, not the raw error.
func TestScrubbedMessageSurvivesReport(t *testing.T) {
	in, url := newIngest(t)
	startForTest(t, url)

	Error("store.open", errors.New(
		`open /home/ana/.config/matterbox/messages.db: permission denied`))
	Close()

	body := in.all()
	if strings.Contains(body, "/home/ana") {
		t.Errorf("the raw path reached the wire:\n%s", body)
	}
	for _, want := range []string{"$exception", "store.open", "permission denied", `\u003cpath\u003e`} {
		if !strings.Contains(body, want) {
			t.Errorf("report missing %q:\n%s", want, body)
		}
	}
}

// TestExceptionSiteIsValidated: a call site that invents a label must not put
// it on the wire, because that is the one route by which a free-form string
// could become an id.
func TestExceptionSiteIsValidated(t *testing.T) {
	in, url := newIngest(t)
	startForTest(t, url)

	report("cdorrestijn@emico.nl", "not-a-class", "boom", nil, true)
	Close()

	body := in.all()
	if strings.Contains(body, "emico.nl") {
		t.Errorf("an invented failure site reached the wire:\n%s", body)
	}
	if strings.Contains(body, "not-a-class") {
		t.Errorf("an invented error class reached the wire:\n%s", body)
	}
	for _, want := range []string{`"where":"ui.other"`, `"class":"unknown"`} {
		if !strings.Contains(body, want) {
			t.Errorf("report missing %q:\n%s", want, body)
		}
	}
}

// TestReportPanicCapturesTheFramesThatPanicked: the whole point of taking the
// stack inside the deferred handler is that the panicking frames are still
// there. If they were not, a report would name only the handler.
func TestReportPanicCapturesTheFramesThatPanicked(t *testing.T) {
	in, url := newIngest(t)
	startForTest(t, url)

	func() {
		defer func() {
			if v := recover(); v != nil {
				ReportPanic("render", v)
			}
		}()
		willPanic()
	}()
	Close()

	body := in.all()
	if !strings.Contains(body, "willPanic") {
		t.Errorf("the panicking function is missing from the report:\n%s", body)
	}
	for _, want := range []string{`"handled":false`, "panic in", "panic_recovered"} {
		if !strings.Contains(body, want) {
			t.Errorf("report missing %q:\n%s", want, body)
		}
	}
}

func willPanic() { panic("boom in a helper") }

// TestCrashRepanics: Crash must report and then let the crash happen, so
// whatever handles panics above it goes on handling them.
func TestCrashRepanics(t *testing.T) {
	_, url := newIngest(t)
	startForTest(t, url)

	var got any
	func() {
		defer func() { got = recover() }()
		func() {
			defer Crash("ui.other")
			panic("still fatal")
		}()
	}()
	if got != "still fatal" {
		t.Errorf("Crash swallowed the panic (recovered %v); it must re-panic", got)
	}
}

// TestCrashWithoutPanicIsNoop: the deferred call runs on every normal return
// too, which is the common case by an enormous margin.
func TestCrashWithoutPanicIsNoop(t *testing.T) {
	_, url := newIngest(t)
	startForTest(t, url)
	func() { defer Crash("ui.other") }()
}

// TestErrorTrackingIsOffWithoutConsent: the reporting entry points must be
// safe, and silent, for someone who never opted in.
func TestErrorTrackingIsOffWithoutConsent(t *testing.T) {
	in, url := newIngest(t)
	t.Cleanup(Close)
	Close()
	t.Setenv(KeyEnv, "phc_test")
	t.Setenv(HostEnv, url)

	Error("store.open", errors.New("boom"))
	ReportPanic("render", "boom")
	OperationFailed(Failure{Where: "store.open", Class: "disk", Err: errors.New("boom")})

	if body := in.all(); body != "" {
		t.Errorf("an opted-out process sent something:\n%s", body)
	}
}

// TestOnlyDefectsBecomeIssues: operation_failed always fires, but the class
// decides whether it is also an issue. A flaky network must not create one.
func TestOnlyDefectsBecomeIssues(t *testing.T) {
	for class, wantIssue := range map[string]bool{
		"network": false, "server": false, "rate_limited": false,
		"auth": false, "permission": false, "not_found": false,
		"parse": true, "config": true, "internal": true,
		"disk": true, "unsupported": true, "unknown": true,
	} {
		if got := worthAnIssue(class); got != wantIssue {
			t.Errorf("worthAnIssue(%q) = %v, want %v", class, got, wantIssue)
		}
	}

	in, url := newIngest(t)
	startForTest(t, url)
	OperationFailed(Failure{Where: "ws.connect", Class: "network", Err: errors.New("dial tcp: refused")})
	Close()
	body := in.all()
	if !strings.Contains(body, "operation_failed") {
		t.Errorf("the event should always fire:\n%s", body)
	}
	if strings.Contains(body, "$exception") {
		t.Errorf("a network failure raised an issue:\n%s", body)
	}
}

// TestExceptionBudget: a failure in a loop must not flood the issue list.
func TestExceptionBudget(t *testing.T) {
	in, url := newIngest(t)
	startForTest(t, url)

	for range 50 {
		Error("store.query", errors.New("locked"))
	}
	Close()

	if n := strings.Count(in.all(), `"event":"$exception"`); n != maxExceptionsPerIssue {
		t.Errorf("sent %d reports for one issue, want the cap of %d",
			n, maxExceptionsPerIssue)
	}
}

// TestPanicTextIsScrubbed: a panic value can be any type at all, including one
// holding whatever the code was working on.
func TestPanicTextIsScrubbed(t *testing.T) {
	type post struct{ Message, Channel string }
	for _, tc := range []struct {
		in   any
		deny string
	}{
		{post{Message: "shall we ship it?", Channel: "hj4x8ke5tbn9jjxq3xr3kjhbxr"}, "ship it"},
		{post{Message: "unquoted secret"}, "secret"},
		{errors.New("post to https://chat.emico.io/api failed"), "emico.io"},
		{"user cdorrestijn@emico.nl not found", "emico.nl"},
		{fmt.Errorf("open /home/ana/notes.txt: no such file"), "/home/ana"},
	} {
		got := panicText(tc.in)
		if strings.Contains(got, tc.deny) {
			t.Errorf("panicText(%v) = %q, which still contains %q", tc.in, got, tc.deny)
		}
		if got == "" {
			t.Errorf("panicText(%v) came back empty; PostHog rejects an empty description", tc.in)
		}
	}
	if got := panicText(nil); got != "" {
		t.Errorf("panicText(nil) = %q, want empty", got)
	}
}

// TestReportedFramesMatchTheEventFrames: one panic produces both a
// panic_recovered event and an exception, and they must not disagree about
// where it happened.
func TestReportedFramesMatchTheEventFrames(t *testing.T) {
	frames := viaHelper()
	text := stackText(frames)
	got := ScrubStack(text)
	if len(got) != len(frames) {
		t.Fatalf("stackText/ScrubStack round trip lost frames: %d in, %d out\n%s",
			len(frames), len(got), text)
	}
	// ScrubStack yields innermost first; frames are outermost first.
	for i, f := range got {
		if want := frames[len(frames)-1-i].Function; f != want {
			t.Errorf("frame %d = %q, want %q", i, f, want)
		}
	}
}

// TestNoAbsolutePathReachesTheWire is the blunt end-to-end assertion: whatever
// the report machinery does, a home directory must never appear in a payload.
func TestNoAbsolutePathReachesTheWire(t *testing.T) {
	in, url := newIngest(t)
	startForTest(t, url)

	func() {
		defer func() {
			if v := recover(); v != nil {
				ReportPanic("render", v)
			}
		}()
		willPanic()
	}()
	Close()

	body := in.all()
	if body == "" {
		t.Fatal("nothing was sent, so this proves nothing")
	}
	for _, deny := range []string{moduleRoot, "/home/", "pkg/mod", "$debug_images"} {
		if strings.Contains(body, deny) {
			t.Errorf("payload contains %q:\n%s", deny, body)
		}
	}
	// And the frames that did arrive are repository paths.
	if !regexp.MustCompile(`"filename":"internal/telemetry/errors_test\.go"`).MatchString(body) {
		t.Errorf("expected a repository-relative filename in:\n%s", body)
	}
}

// startForTest opens a client pointed at the fake ingest, opted in, and tears
// it down afterwards.
func startForTest(t *testing.T, url string) {
	t.Helper()
	Close() // drop any client a previous test left open
	cfg := consentingConfig(t)
	t.Setenv(KeyEnv, "phc_test")
	t.Setenv(HostEnv, url)
	Start(cfg)
	if !Enabled() {
		t.Fatal("telemetry did not start")
	}
	t.Cleanup(Close)
}
