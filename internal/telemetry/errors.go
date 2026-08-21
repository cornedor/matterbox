package telemetry

// Error tracking: the `$exception` half of telemetry, separate from the event
// catalogue and sent to PostHog's error-tracking view rather than to product
// analytics.
//
// The two halves answer different questions and are deliberately not the same
// pipe. An event says "this happened, here is the shape of it"; an exception
// says "this is broken, here is where in the code". So `operation_failed` stays
// a catalogued, bucketed, queryable event, and the exception raised alongside it
// carries the one thing an event cannot: a stack.
//
// A stack is also the most dangerous thing this package sends, which is why
// none of it comes from the PostHog SDK's own extractor. NewDefaultException
// captures runtime.Frame.File verbatim — an absolute path recorded at compile
// time, so on a matterbox built from source (the normal way to get it) every
// frame reads /home/<the user>/…/internal/ui/view.go — plus raw instruction
// addresses and a $debug_images entry identifying their executable. All three
// are exactly what docs/telemetry.md promises never to send.
//
// So frames are rebuilt here, and the rule is inverted from the SDK's: a frame
// is dropped unless it can be *proved* to be matterbox's own code, by reducing
// its recorded path against this build's module root (see repoFile). A frame
// from the standard library, a dependency or a vendored tree does not reduce,
// so it is dropped rather than filtered — anything unrecognised fails closed.
// What survives is `internal/ui/view.go:1602 internal/ui.(*Model).View`, which
// is a line of a public repository and says nothing about the machine it ran on.

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/posthog/posthog-go"
)

// moduleRoot is the build-time directory every matterbox source path sits
// under, worked out from this file's own recorded path rather than hardcoded:
// the compiler records absolute paths (matterbox is not built with -trimpath),
// and they differ per machine, so the prefix has to be discovered at run time.
//
// Derived by dropping three segments — internal/telemetry/<this file> — which
// survives renaming this file. It does not survive moving the package, and
// TestModuleRootResolves fails loudly if it ever stops resolving, because a
// broken prefix means every frame silently fails the repoFile check and
// exceptions arrive with no stack at all.
var moduleRoot = func() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	dir := strings.ReplaceAll(file, `\`, "/")
	for range 3 {
		i := strings.LastIndexByte(dir, '/')
		if i < 0 {
			return ""
		}
		dir = dir[:i]
	}
	return dir + "/"
}()

// reRepoFile bounds what a reduced path may look like: a .go file at the repo
// root (main.go) or under internal/. It is the second gate after the module
// prefix, and it is what keeps a vendored dependency — which *would* sit under
// the module root — from being reported as our own code.
var reRepoFile = regexp.MustCompile(`^(internal/[\w./-]+|[\w.-]+)\.go$`)

// modulePrefix is the import-path prefix the compiler puts on our own function
// names ("matterbox/internal/ui.(*Model).View"). Stripped so a frame reads the
// same as one from ScrubStack, which the panic_recovered event already uses.
const modulePrefix = "matterbox/"

// exceptionCaps bound what one session may report. A failure inside a retry
// loop or a render path can repeat thousands of times, and neither the user's
// bandwidth nor the issue list benefits from the four-hundredth copy: the first
// few carry every bit of information the rest do.
const (
	maxExceptionsPerSession = 25
	maxExceptionsPerIssue   = 3
)

var seen struct {
	mu    sync.Mutex
	total int
	byFP  map[string]int
}

// admit reports whether an exception with this fingerprint still has room in
// the session's budget, counting it if so.
func admit(fingerprint string) bool {
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if seen.total >= maxExceptionsPerSession {
		return false
	}
	if seen.byFP == nil {
		seen.byFP = make(map[string]int, 8)
	}
	if seen.byFP[fingerprint] >= maxExceptionsPerIssue {
		return false
	}
	seen.byFP[fingerprint]++
	seen.total++
	return true
}

// resetExceptionBudget clears the per-session caps. Called by Close so a
// process that stops and restarts telemetry (and every test that does) starts
// from a clean budget.
func resetExceptionBudget() {
	seen.mu.Lock()
	seen.total, seen.byFP = 0, nil
	seen.mu.Unlock()
}

// build identifies the binary, for the version/tags an issue is attributed to.
// Set by the command layer, which owns the build stamp; absent is fine, the
// properties are simply left off.
var build struct {
	mu      sync.RWMutex
	version string
	tags    string
}

// SetBuild records which build is running, so an error report can say which
// version it came from — the first question asked of any crash, and one that
// cannot be recovered afterwards. Both values are build strings, validated the
// same way as app_started's, and describe the binary rather than the person.
func SetBuild(version, tags string) {
	build.mu.Lock()
	build.version, build.tags = version, tags
	build.mu.Unlock()
}

func buildInfo() (version, tags string) {
	build.mu.RLock()
	defer build.mu.RUnlock()
	return build.version, build.tags
}

// Error reports err to PostHog's error tracking, grouped by where: a short,
// stable, hand-written label from FailureSites naming the operation that broke
// ("ws.connect", "store.migrate"), never anything derived from user data.
//
// The message is scrubbed (see Scrub) and the stack is reduced to matterbox's
// own frames, so a path, URL, id or quoted message body in the error becomes a
// placeholder and no frame outside this repository is reported.
//
// Prefer OperationFailed in emit.go, which records the same failure as a
// catalogued event *and* raises this for the classes worth an issue. Call Error
// directly only for a failure with no event to hang it on.
func Error(where string, err error) {
	if !active.Load() || err == nil {
		return
	}
	report(where, classOf(where), Scrub(err.Error()), stackFrames(1), true)
}

// ReportPanic reports a panic. Call it from a deferred function while the panic
// is still unwinding — the goroutine's stack is intact until that function
// returns, which is what makes the frames the ones that led to the panic rather
// than the ones that handled it.
//
// It reports and returns: what to do with the panic is the caller's decision,
// and swallowing one here would turn a crash into a session running on state
// nobody can reason about. See Crash for the usual deferred form.
func ReportPanic(where string, v any) {
	if v == nil {
		return
	}
	frames := stackFrames(1)
	text := panicText(v)
	// panic_recovered carries the same failure as a catalogued event, so the
	// analytics side can count crashes without going through error tracking.
	PanicRecovered(where, text, stackText(frames))
	report(where, "internal", text, frames, false)
}

// Crash is the deferred form: it reports the in-flight panic and re-panics, so
// the crash still happens exactly as it would have.
//
//	defer telemetry.Crash(telemetry.SiteRender)
//
// Re-panicking rather than recovering is the point. Whatever already handles
// panics at this level keeps handling them — in the TUI that is bubbletea,
// which restores the terminal and prints the trace — and this only adds a
// report on the way past. Go prints the original frames under the re-panic, so
// nothing a person debugging locally would have seen is lost.
//
// Guard the defer with Enabled() on hot paths: registering it costs a little,
// and an opted-out session should pay one atomic load and nothing else.
func Crash(where string) {
	v := recover()
	if v == nil {
		return
	}
	ReportPanic(where, v)
	panic(v)
}

// report builds and queues one $exception. Everything it puts on the wire is
// either a validated label from the catalogue's closed sets, a build string, a
// scrubbed message or a frame that passed repoFile — there is no path by which
// a caller-supplied string reaches PostHog unchecked.
func report(where, class, message string, frames []posthog.StackFrame, handled bool) {
	if !active.Load() {
		return
	}
	mu.Lock()
	c, id := client, distinctID
	mu.Unlock()
	if c == nil {
		return
	}

	where = site(where)
	class = errorClass(class)
	if message == "" {
		message = "<redacted>"
	}

	top := topFrame(frames)
	kind := "error"
	title := where
	if !handled {
		kind = "panic"
		title = "panic in " + where
		if top != "" {
			title = "panic in " + top
		}
	}
	// Group by code location, not by message or line number. A scrubbed message
	// varies with whatever the OS said, and line numbers move with every commit,
	// so PostHog's default fingerprint would scatter one bug across a dozen
	// issues and make "is this still happening after the fix" unanswerable.
	fingerprint := kind + "|" + where + "|" + top

	if !admit(fingerprint) {
		return
	}

	props := posthog.Properties{
		"where":   where,
		"class":   class,
		"handled": handled,
		"os":      OSName(),
		"arch":    ArchName(),
	}
	if v, tags := buildInfo(); v != "" {
		props["version"] = v
		if tags != "" {
			props["build_tags"] = tags
		}
	}

	_ = c.Enqueue(posthog.Exception{
		DistinctId: id,
		Timestamp:  time.Now().UTC(),
		Properties: props,
		ExceptionList: []posthog.ExceptionItem{{
			Type:       title,
			Value:      message,
			Mechanism:  &posthog.ExceptionMechanism{Handled: &handled},
			Stacktrace: &posthog.ExceptionStacktrace{Type: "raw", Frames: frames},
		}},
		ExceptionFingerprint: &fingerprint,
	})
}

// stackFrames captures the current goroutine's stack and keeps only the frames
// that are provably matterbox's own, in PostHog's wire order: outermost first,
// the failure last. skip counts callers to drop, not counting stackFrames
// itself, so a caller passes 1 for "start above me".
func stackFrames(skip int) []posthog.StackFrame {
	var pcs [64]uintptr
	// +2: runtime.Callers itself and stackFrames.
	n := runtime.Callers(skip+2, pcs[:])
	if n == 0 {
		return nil
	}
	out := make([]posthog.StackFrame, 0, n)
	it := runtime.CallersFrames(pcs[:n])
	for {
		f, more := it.Next()
		if fn, file, ok := repoFrame(f); ok {
			out = append(out, posthog.StackFrame{
				Filename: file,
				LineNo:   f.Line,
				Function: fn,
				InApp:    true,
				Platform: "go",
				Lang:     "go",
			})
		}
		if !more {
			break
		}
	}
	// The runtime yields innermost first; PostHog's wire order is the reverse.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// repoFrame reduces one runtime frame to the function name and repository-
// relative file matterbox may report, and says no to everything else. The file
// check is the real gate: a frame whose recorded path does not sit under this
// build's module root cannot be our code, whatever it claims to be called.
func repoFrame(f runtime.Frame) (fn, file string, ok bool) {
	file = repoFile(f.File)
	if file == "" {
		return "", "", false
	}
	fn = strings.TrimPrefix(f.Function, modulePrefix)
	// Generic instantiations arrive as "load[go.shape.string]". The type
	// arguments are compile-time names, but they add nothing to a stack and
	// would need their own validation, so the suffix goes.
	if i := strings.IndexByte(fn, '['); i > 0 {
		fn = fn[:i]
	}
	if !reFrame.MatchString(fn) {
		return "", "", false
	}
	return fn, file, true
}

// repoFile turns an absolute build-time source path into its path inside the
// matterbox repository, or "" when it is not one of ours. Fails closed: an
// unresolvable module root, a path outside it, or a shape reRepoFile does not
// recognise all yield "", and the frame is dropped rather than guessed at.
func repoFile(abs string) string {
	if moduleRoot == "" || abs == "" {
		return ""
	}
	p := strings.ReplaceAll(abs, `\`, "/")
	if !strings.HasPrefix(p, moduleRoot) {
		return ""
	}
	rel := p[len(moduleRoot):]
	if !reRepoFile.MatchString(rel) {
		return ""
	}
	return rel
}

// topFrame names the innermost matterbox function in a wire-order stack, which
// is the last element. It is what an issue is grouped by.
func topFrame(frames []posthog.StackFrame) string {
	if len(frames) == 0 {
		return ""
	}
	return frames[len(frames)-1].Function
}

// stackText renders reduced frames back into the innermost-first list of
// function names that the panic_recovered event's `frames` property expects, so
// the two reports of one panic agree with each other.
func stackText(frames []posthog.StackFrame) string {
	var b strings.Builder
	for i := len(frames) - 1; i >= 0; i-- {
		b.WriteString(modulePrefix)
		b.WriteString(frames[i].Function)
		b.WriteByte('\n')
	}
	return b.String()
}

// reTypeName bounds what a Go type name may look like before it is reported.
// Type names are code identifiers, but this is the one place a value's own
// String method could get a word in, so the shape is checked rather than
// trusted.
var reTypeName = regexp.MustCompile(`^[\w.*\[\]/-]{1,80}$`)

// panicText renders a panic value as text that is safe to send.
//
// A string or an error is scrubbed and reported, because that covers very
// nearly every panic — including the runtime's own, which are errors, and
// whose text ("runtime error: index out of range [5] with length 3") is the
// most useful thing in the whole report.
//
// Anything else is reported as its *type* and nothing more. Formatting an
// arbitrary value with %v would print its fields, and a panic carrying a post,
// a draft or a config struct would then put a message body on the wire with
// nothing quoted for the scrubber to catch. The type name still says what
// broke, and it cannot say anything about the person.
func panicText(v any) string {
	var s string
	switch t := v.(type) {
	case nil:
		return ""
	case error:
		s = t.Error()
	case string:
		s = t
	default:
		name := fmt.Sprintf("%T", t)
		if !reTypeName.MatchString(name) {
			return "<redacted>"
		}
		return "panic of type " + name
	}
	if s = Scrub(s); s == "" {
		return "<redacted>"
	}
	return s
}

// site validates a failure-site label against the catalogue's closed set. An
// unknown label is a call site that invented one, and reporting it verbatim is
// the one way a free-form string could reach an id — so it becomes the generic
// bucket instead, which still records that something broke.
func site(where string) string {
	if inSet(FailureSites, where) {
		return where
	}
	if strict.Load() {
		panic("telemetry: failure site not in catalogue: " + where)
	}
	return "ui.other"
}

// errorClass validates a failure class the same way.
func errorClass(class string) string {
	if inSet(ErrorClasses, class) {
		return class
	}
	if strict.Load() && class != "" {
		panic("telemetry: error class not in catalogue: " + class)
	}
	return "unknown"
}

// classOf guesses a class from a failure site, for Error's callers who have a
// site but no classification. Coarse on purpose: the site is the useful
// grouping, and the class only separates "the world" from "our code".
func classOf(where string) string {
	switch {
	case strings.HasPrefix(where, "api."), strings.HasPrefix(where, "ws."),
		strings.HasPrefix(where, "embed."), strings.HasPrefix(where, "llm."),
		where == "telegram":
		return "network"
	case strings.HasPrefix(where, "store."):
		return "disk"
	case strings.HasPrefix(where, "auth."):
		return "auth"
	case strings.HasPrefix(where, "config."):
		return "config"
	}
	return "unknown"
}

// environmentClasses are the failure classes that describe the world rather
// than a defect: the network went away, the server said 500, the token
// expired, the file wasn't there. They are worth counting — operation_failed
// does that, bucketed and queryable — but they are not worth an issue, because
// there is no code change at the end of them and a flaky connection would
// otherwise bury every real bug under thousands of duplicates.
//
// Everything else is treated as ours until shown otherwise: a parse failure, a
// bad config we wrote, an unsupported path we took, an internal error, or an
// unclassified one.
var environmentClasses = map[string]bool{
	"network":      true,
	"server":       true,
	"rate_limited": true,
	"auth":         true,
	"permission":   true,
	"not_found":    true,
}

// worthAnIssue reports whether a failure of this class should also be raised as
// a PostHog exception, on top of the operation_failed event that always fires.
func worthAnIssue(class string) bool { return !environmentClasses[class] }
