package telemetry

import (
	"regexp"
	"strings"
)

// Error strings are the one place in telemetry where free-form text would
// otherwise reach PostHog, and they are full of exactly what must never be
// sent: `open /home/corne/.config/matterbox/messages.db: permission denied`
// carries a username, `post "shall we ship it?" rejected` carries a message,
// and a 404 from the API carries a channel id. Wrapping every error by hand at
// every call site would work until the first one that forgets.
//
// So Scrub is applied centrally, in Error and to any KindErrorText property,
// and it is deliberately aggressive: it destroys anything shaped like an
// identifier and keeps only the sentence around it. `permission denied` is the
// part that says what went wrong; the path never was.
//
// A scrubber cannot be perfect against arbitrary text, which is why it is the
// second line of defence and not the first — the first is that events carry
// whitelisted, bucketed properties (see catalogue.go) and no message content
// at any point.

// scrubMax caps a scrubbed string. Long error text is nearly always a wrapped
// chain repeating itself, and a cap bounds the damage from anything the
// patterns below fail to catch.
const scrubMax = 200

// The patterns are applied in this order, which matters: a URL contains
// slashes that the path pattern would otherwise eat first, and an email
// contains an @ that the mention pattern would claim.
var (
	// Any scheme://host/path, including ws:// and wss:// for the Mattermost
	// socket. Hosts are as identifying as paths here — a server URL names the
	// company running it.
	// The tail deliberately excludes trailing punctuation so `https://host/x:`
	// in "Post https://host/x: timeout" gives up its colon — the sentence
	// around a redaction is the part worth keeping. A colon *inside* the URL
	// still matches, so host:port survives.
	reURL = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'` + "`" + `]*[^\s"'` + "`" + `.,:;)\]]`)
	// Emails before mentions, since both contain @.
	reEmail = regexp.MustCompile(`\b[\w.+-]+@[\w-]+\.[\w.-]+\b`)
	// Absolute paths, ~-relative paths, and Windows drive paths. Requires at
	// least one separator so a bare word isn't mistaken for a path.
	reUnixPath = regexp.MustCompile(`(?:~|\.\.?)?/[^\s"'` + "`" + `:,)]*`)
	reWinPath  = regexp.MustCompile(`\b[A-Za-z]:\\[^\s"'` + "`" + `,)]*`)
	// A Mattermost id is 26 characters of lowercase alphanumeric. Post,
	// channel, user, team and file ids all share the shape, so one pattern
	// covers every id that could appear in an API error.
	reMMID = regexp.MustCompile(`\b[a-z0-9]{26}\b`)
	// @mention of a username.
	reMention = regexp.MustCompile(`@[\w.\-]+`)
	// Quoted spans. Errors quote the thing that failed, and the thing that
	// failed is usually the user's text — so the quotes go, contents and all.
	reQuoted = regexp.MustCompile(`"[^"]*"|'[^']*'|` + "`[^`]*`")
	// Any run of 7+ digits: timestamps in milliseconds, ports are shorter,
	// nothing legitimate in an error message needs that many.
	reLongNum = regexp.MustCompile(`\b\d{7,}\b`)
	// Long unbroken alphanumeric runs that survived the patterns above —
	// tokens, hashes, base64 fragments. 32+ so ordinary words are safe.
	reToken = regexp.MustCompile(`\b[A-Za-z0-9_\-]{32,}\b`)
	// Collapse the whitespace the substitutions leave behind.
	reSpace = regexp.MustCompile(`\s+`)
)

// Scrub redacts every identifier-shaped span in s and returns what is left:
// the shape of the failure, with the specifics replaced by a placeholder that
// says what was removed. An empty or all-redacted result comes back as
// "<redacted>" rather than "", so an event still records that something failed
// even when nothing about it was safe to keep.
//
// Scrub is idempotent — the placeholders it inserts contain no characters the
// patterns match — so it is safe to apply to an already-scrubbed string.
func Scrub(s string) string {
	if s == "" {
		return ""
	}
	s = reURL.ReplaceAllString(s, "<url>")
	s = reEmail.ReplaceAllString(s, "<email>")
	s = reWinPath.ReplaceAllString(s, "<path>")
	s = reUnixPath.ReplaceAllString(s, "<path>")
	s = reMMID.ReplaceAllString(s, "<id>")
	s = reMention.ReplaceAllString(s, "<mention>")
	s = reQuoted.ReplaceAllString(s, "<quoted>")
	s = reLongNum.ReplaceAllString(s, "<num>")
	s = reToken.ReplaceAllString(s, "<token>")
	s = strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
	// Placeholders only means every specific was removed and nothing was left
	// to say. Report that plainly instead of shipping "<path>: <quoted>".
	if s == "" || onlyPlaceholders(s) {
		return "<redacted>"
	}
	if len(s) > scrubMax {
		// Cut on a rune boundary — the text can contain anything, and half a
		// rune would reach PostHog as a replacement character.
		s = strings.ToValidUTF8(s[:scrubMax], "") + "…"
	}
	return s
}

// onlyPlaceholders reports whether s consists solely of the placeholders Scrub
// inserts plus punctuation — i.e. whether anything descriptive survived.
func onlyPlaceholders(s string) bool {
	rest := s
	for _, p := range []string{"<url>", "<email>", "<path>", "<id>", "<mention>", "<quoted>", "<num>", "<token>"} {
		rest = strings.ReplaceAll(rest, p, "")
	}
	return strings.TrimFunc(rest, func(r rune) bool {
		return r == ' ' || r == ':' || r == ',' || r == '.' || r == ';' ||
			r == '-' || r == '(' || r == ')' || r == '/' || r == '"' || r == '\''
	}) == ""
}

// ScrubError is Scrub over an error, returning "" for a nil one so callers can
// pass a possibly-nil error straight through.
func ScrubError(err error) string {
	if err == nil {
		return ""
	}
	return Scrub(err.Error())
}

// stackFrame matches a Go stack-trace function line belonging to matterbox
// itself: "matterbox/internal/ui.(*Model).renderMessages(...)". Capturing only
// our own frames is what makes a panic report safe to send — the frame names
// are code we wrote and pushed to a public repo, while the arguments (which
// can hold message text) and the absolute file paths (which hold a username)
// are dropped entirely.
var stackFrame = regexp.MustCompile(`\bmatterbox/(internal/[\w./]+|main)\.([\w.*()]+)`)

// ScrubStack reduces a runtime stack trace to a bounded list of matterbox
// frames, innermost first, as "internal/ui.(*Model).renderMessages". Frames
// from the standard library and from dependencies are dropped: they add no
// information a matterbox frame doesn't already imply, and every one of them
// is another chance to ship a path.
//
// The result is what makes a `panic_recovered` event actionable — it names the
// function to look at — without being a debugger's view of the user's data.
func ScrubStack(stack string) []string {
	const maxFrames = 12
	var out []string
	seen := make(map[string]bool, maxFrames)
	for _, mt := range stackFrame.FindAllStringSubmatch(stack, -1) {
		frame := mt[1] + "." + mt[2]
		// A recursive render walk repeats the same frame many times; the fact
		// that it recursed is already visible from the first occurrence.
		if seen[frame] {
			continue
		}
		seen[frame] = true
		out = append(out, frame)
		if len(out) == maxFrames {
			break
		}
	}
	return out
}
