package forge

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The review half of the forge integration: a change request's full diff, the
// inline conversations already on it, and the call that adds one. It is a
// separate interface from Provider because not every forge has it yet — the UI
// type-asserts for Reviewer and simply doesn't offer the diff view when the
// provider behind the reference doesn't implement it.
//
// Everything here is wire-neutral: a forge flattens its own diff payload into
// Diff/FileDiff, and the hunk parsing (which is the same unified-diff format
// everywhere) happens once, here, in ParseUnifiedDiff.

// Reviewer is a forge that can serve a change request's diff and take inline
// review notes on it. Implementations are safe for concurrent use, like
// Provider.
type Reviewer interface {
	// Diff returns the full diff of the change request, with the commit refs
	// an inline note has to be anchored against.
	Diff(ctx context.Context, repo string, number int) (*Diff, error)
	// Threads returns the conversations on the change request, in the order the
	// forge keeps them: the inline ones anchored to a file and line, and the
	// ones on the change request as a whole (Thread.Inline reports which).
	// System notes — "changed the title", "added 3 commits" — are left out.
	Threads(ctx context.Context, repo string, number int) ([]Thread, error)
	// AddNote posts an inline note: a new conversation on a line, or a reply to
	// an existing one (NewNote.ReplyTo).
	AddNote(ctx context.Context, repo string, number int, n NewNote) error
	// ResolveThread marks an inline conversation resolved, or reopens it
	// (resolved false) — the tick a reviewer and an author trade until a merge
	// request has none left.
	ResolveThread(ctx context.Context, repo string, number int, threadID string, resolved bool) error
}

// DiffRefs are the three commits a forge needs to place a note on a line: the
// merge base, the branch point the diff is computed from, and the head being
// reviewed. Posting an inline note without them is rejected, so Diff carries
// them and the UI hands them straight back in NewNote.
type DiffRefs struct {
	BaseSHA  string
	StartSHA string
	HeadSHA  string
}

// FileDiff is one changed file. Diff is the raw unified diff body for it —
// hunk headers and their lines, without the ---/+++ file header, which is how
// both GitLab and GitHub hand it over.
type FileDiff struct {
	OldPath string
	NewPath string
	Diff    string
	New     bool
	Deleted bool
	Renamed bool
	Binary  bool
	// Generated marks a file the forge itself flags as generated (lockfiles,
	// vendored code). Shown collapsed-by-default would be a reasonable next
	// step; for now it is only a label.
	Generated bool
}

// Path is the file's name for display and for anchoring a note: the new path,
// falling back to the old one for a deletion.
func (f FileDiff) Path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

// Diff is a change request's whole diff. Truncated is set when the forge
// refused to hand over all of it (GitLab's overflow flag, a page cap we hit),
// so the view can say the review is incomplete rather than quietly showing part
// of it.
type Diff struct {
	Refs      DiffRefs
	Files     []FileDiff
	Truncated bool
}

// Note is one message in an inline conversation.
type Note struct {
	ID      string
	Author  string
	Body    string
	Created time.Time
}

// Thread is one conversation on a change request: either anchored to a line of
// the diff (Inline) or on the change request as a whole. For an anchored one,
// exactly one of OldLine / NewLine is usually set — a removed line has only an
// old number, an added line only a new one — while a context line carries both,
// and we match on either.
type Thread struct {
	ID       string
	Path     string // new path, or the old path when the file was deleted
	OldPath  string
	OldLine  int
	NewLine  int
	Resolved bool
	Notes    []Note
}

// Inline reports whether the conversation hangs off a line of the diff. The
// diff view draws only these (it has nowhere to put the others); the reference
// panel lists both.
func (t Thread) Inline() bool {
	return t.Path != "" && (t.NewLine > 0 || t.OldLine > 0)
}

// Line is where an inline conversation sits, as the forge numbers it: the
// new-side line when it has one, otherwise the old-side line. old reports which
// it is, so a caller can say "the line that was removed" rather than print a
// number that is not in the file any more.
func (t Thread) Line() (n int, old bool) {
	if t.NewLine > 0 {
		return t.NewLine, false
	}
	return t.OldLine, true
}

// InlineThreads is the subset anchored to a line.
func InlineThreads(ts []Thread) []Thread {
	out := make([]Thread, 0, len(ts))
	for _, t := range ts {
		if t.Inline() {
			out = append(out, t)
		}
	}
	return out
}

// NewNote is an inline note to post. ReplyTo names an existing Thread to answer;
// empty starts a new conversation, which is when the position fields matter.
type NewNote struct {
	Body    string
	ReplyTo string

	Refs    DiffRefs
	OldPath string
	NewPath string
	OldLine int // 0 = the line does not exist on the old side
	NewLine int // 0 = the line does not exist on the new side
}

// DiffLineKind classifies one row of a parsed diff.
type DiffLineKind uint8

const (
	DiffContext DiffLineKind = iota // unchanged line, present on both sides
	DiffAdd                         // +
	DiffDel                         // -
	DiffHunk                        // @@ … @@ header
	DiffMeta                        // "\ No newline at end of file" and friends
)

// DiffLine is one parsed row: the text without its marker, plus the line
// numbers it has on each side (0 where it has none). Those numbers are the
// whole point — they are what an inline note is anchored to.
type DiffLine struct {
	Kind    DiffLineKind
	Text    string
	OldLine int
	NewLine int
}

// ParseUnifiedDiff turns a unified diff body into rows with line numbers on
// both sides. It tolerates the file headers some forges prepend (diff --git,
// index, ---/+++) by ignoring everything before the first hunk, so the same
// parser handles a raw `git diff` and a forge's per-file payload.
//
// A "-" inside a hunk is a removed line, never a header: the skip only applies
// before the first @@, which is why the loop tracks that rather than matching
// on the prefix alone.
func ParseUnifiedDiff(diff string) []DiffLine {
	if diff == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	out := make([]DiffLine, 0, len(lines))
	oldNo, newNo := 0, 0
	inHunk := false
	for _, ln := range lines {
		if strings.HasPrefix(ln, "@@") {
			o, n, ok := parseHunkHeader(ln)
			if !ok {
				continue
			}
			oldNo, newNo, inHunk = o, n, true
			out = append(out, DiffLine{Kind: DiffHunk, Text: ln})
			continue
		}
		if !inHunk {
			// Pre-hunk preamble: file headers, mode changes, the "Binary files
			// … differ" line. Only the last is worth showing.
			if strings.HasPrefix(ln, "Binary files") || strings.HasPrefix(ln, "GIT binary patch") {
				out = append(out, DiffLine{Kind: DiffMeta, Text: ln})
			}
			continue
		}
		switch {
		case ln == "":
			// A context line whose single leading space was stripped somewhere
			// along the way. Treated as the empty line it is, so the numbering
			// downstream stays aligned with the file.
			out = append(out, DiffLine{Kind: DiffContext, OldLine: oldNo, NewLine: newNo})
			oldNo++
			newNo++
		case ln[0] == '+':
			out = append(out, DiffLine{Kind: DiffAdd, Text: ln[1:], NewLine: newNo})
			newNo++
		case ln[0] == '-':
			out = append(out, DiffLine{Kind: DiffDel, Text: ln[1:], OldLine: oldNo})
			oldNo++
		case ln[0] == '\\': // "\ No newline at end of file"
			out = append(out, DiffLine{Kind: DiffMeta, Text: ln})
		default: // ' ' and anything else: context
			out = append(out, DiffLine{Kind: DiffContext, Text: ln[1:], OldLine: oldNo, NewLine: newNo})
			oldNo++
			newNo++
		}
	}
	return out
}

// parseHunkHeader reads the starting line numbers out of "@@ -12,7 +14,9 @@ …".
// The counts are ignored: we number lines as we walk them, so a header that
// lies about its length (or omits the count, as a one-line hunk does) costs
// nothing.
func parseHunkHeader(s string) (oldStart, newStart int, ok bool) {
	rest := strings.TrimPrefix(s, "@@")
	end := strings.Index(rest, "@@")
	if end < 0 {
		return 0, 0, false
	}
	var gotOld, gotNew bool
	for _, f := range strings.Fields(rest[:end]) {
		switch {
		case strings.HasPrefix(f, "-") && !gotOld:
			oldStart, gotOld = leadingInt(f[1:]), true
		case strings.HasPrefix(f, "+") && !gotNew:
			newStart, gotNew = leadingInt(f[1:]), true
		}
	}
	return oldStart, newStart, gotOld && gotNew
}

// leadingInt reads the digits at the head of s ("12,7" → 12).
func leadingInt(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.Atoi(s[:i])
	return n
}

// Store is a tiny per-provider memo for anything else keyed by change request —
// the diff, in practice. Cache does the same job for *Change and predates
// generics; both are here because a diff is fetched by a different call and
// dropped by the same Invalidate. The zero value is ready to use.
type Store[T any] struct {
	mu sync.Mutex
	m  map[string]T
}

// Get returns the stored value, if any.
func (s *Store[T]) Get(repo string, number int) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[cacheKey(repo, number)]
	return v, ok
}

// Put stores a value.
func (s *Store[T]) Put(repo string, number int, v T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]T{}
	}
	s.m[cacheKey(repo, number)] = v
}

// Invalidate drops one entry.
func (s *Store[T]) Invalidate(repo string, number int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, cacheKey(repo, number))
}
