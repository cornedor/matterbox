// Package replyto carries the one thing Mattermost's data model has no room
// for: which message inside a thread a reply is answering.
//
// A Mattermost thread is flat. Every reply hangs off the same RootId, so a
// thread of forty messages is forty siblings and the only way to say "this
// answers what Bram said, not what Sanne said" is to hope the words make it
// obvious. Nothing in the API takes a second parent — but a post body can carry
// invisible bytes (see internal/hidden), and a parent id is only 26 of them.
//
// So a nested reply is an ordinary Mattermost reply — same RootId, same thread,
// identical in every other client — with the id of the message it answers
// smuggled along behind the text. matterbox reads it and draws the tree;
// everyone else sees the flat thread they always saw. Nothing is lost if the
// payload is stripped in transit: the reply degrades to exactly the flat reply
// it already is.
//
// The parent is stored as an id and nothing else. Author and text are
// deliberately not copied along: they would be a second, staler copy of a
// message that is right there in the thread, and an edit would make the quote
// lie.
package replyto

import (
	"strings"

	"matterbox/internal/hidden"
)

// Magic tags this channel's hidden payload. "MBR1" = MatterBox Reply, v1 —
// distinct from the game (MBG1) and text-effect (MBF1) channels so none of them
// ever reads another's bytes.
const Magic = "MBR1"

// payloadVersion prefixes the payload so a later format change is detectable
// rather than silently misread.
const payloadVersion = 1

// maxIDLen bounds what Parse will accept as a parent id. A Mattermost id is 26
// characters; the cap is generous but finite, so a corrupted run can't hand a
// caller an arbitrarily long string that looks like an id.
const maxIDLen = 64

// magicRunes is the encoded magic — the invisible run every payload of ours
// opens with. Testing a body for it is a plain substring search, which is what
// keeps the per-post check on the render path free. It is deliberately *not*
// hidden.Append's output: a body that already carried another channel's payload
// has a separator before this run, and the search must match either way.
var magicRunes = hidden.Encode(Magic, nil)

// Carries reports whether body looks like it holds a parent reference, cheaply
// enough to ask of every post being rendered. A true answer still has to be
// confirmed by Parse — this only rules bodies out.
func Carries(body string) bool { return strings.Contains(body, magicRunes) }

// Attach returns body with parentID smuggled onto the end of it, or body
// untouched when there is no parent to record. It goes through hidden.Append, so
// a body that already carries a text-effects payload keeps both: the two runs
// are parted rather than merged.
func Attach(body, parentID string) string {
	if !validID(parentID) {
		return body
	}
	return hidden.Append(body, Magic, MarshalPayload(parentID))
}

// Parse returns the parent id body carries, or ok=false when it carries none.
// A payload that survived the trip only in part — a truncated run, an id with
// bytes an id can't contain — reports ok=false rather than a half-read id: a
// reply that has lost its parent is a flat reply, which is a thing matterbox
// already knows how to draw.
func Parse(body string) (string, bool) {
	payload, ok := hidden.Decode(Magic, body)
	if !ok {
		return "", false
	}
	return UnmarshalPayload(payload)
}

// Detach removes the parent reference from a body, leaving any other channel's
// payload (and the visible text) untouched. The edit path uses it: the composer
// must show the text without the invisible bytes behind it, and the save
// re-attaches whatever the composer is now answering.
func Detach(body string) string { return hidden.Remove(body, Magic) }

// MarshalPayload encodes a parent id as the bytes that ride behind the text.
func MarshalPayload(parentID string) []byte {
	return append([]byte{payloadVersion}, parentID...)
}

// UnmarshalPayload decodes MarshalPayload's output (the bytes after the magic).
// Exported for `matterbox decode`, which reports what a post actually carries.
func UnmarshalPayload(b []byte) (string, bool) {
	if len(b) < 2 || b[0] != payloadVersion {
		return "", false
	}
	id := string(b[1:])
	if !validID(id) {
		return "", false
	}
	return id, true
}

// validID reports whether s could be a Mattermost post id. Mattermost ids are
// ASCII alphanumeric; anything else means the run was corrupted or was never
// ours, and is refused rather than passed on to a lookup that would silently
// find nothing.
func validID(s string) bool {
	if s == "" || len(s) > maxIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}
