package replyto

import (
	"strings"
	"testing"

	"matterbox/internal/effects"
	"matterbox/internal/hidden"
)

const parentID = "8kq1h9wz3jby7rn5cxtd2fmp4a" // 26 chars, shaped like a real one

func TestRoundTrip(t *testing.T) {
	body := Attach("looks right to me", parentID)
	got, ok := Parse(body)
	if !ok || got != parentID {
		t.Fatalf("Parse = %q, %v; want %q, true", got, ok, parentID)
	}
	if vis := hidden.Strip(body); vis != "looks right to me" {
		t.Fatalf("visible text = %q; the parent must cost no visible space", vis)
	}
}

// The whole promise of the feature to everyone not running matterbox: the post
// on the wire is the text and nothing else.
func TestAttachAddsNoVisibleCharacters(t *testing.T) {
	body := Attach("ship it", parentID)
	for _, r := range strings.TrimPrefix(body, "ship it") {
		if r != '⁠' && !hidden.IsPayloadRune(r) {
			t.Fatalf("Attach emitted a visible rune %U", r)
		}
	}
}

func TestAttachIgnoresAnEmptyOrBogusParent(t *testing.T) {
	for _, id := range []string{"", "not an id", "has-dashes", strings.Repeat("x", maxIDLen+1)} {
		if got := Attach("body", id); got != "body" {
			t.Fatalf("Attach(%q) attached something: %q", id, got)
		}
	}
}

func TestParseReportsNothingForAPlainPost(t *testing.T) {
	for _, body := range []string{"", "plain text", "an emoji ❤️", "text " + hidden.Encode(effects.MagicEffects, []byte{1, 0})} {
		if got, ok := Parse(body); ok {
			t.Fatalf("Parse(%q) = %q, true; want no parent", body, got)
		}
	}
}

// A truncated or corrupted run must read as "no parent" rather than as a parent
// id that will never resolve.
func TestParseRejectsDamagedPayloads(t *testing.T) {
	cases := map[string][]byte{
		"version only":    {payloadVersion},
		"wrong version":   append([]byte{payloadVersion + 1}, parentID...),
		"non-id bytes":    append([]byte{payloadVersion}, "id with spaces"...),
		"absurdly long":   append([]byte{payloadVersion}, strings.Repeat("a", maxIDLen+1)...),
		"empty id":        {payloadVersion, 0},
		"truncated magic": nil,
	}
	for name, payload := range cases {
		body := "text" + hidden.Encode(Magic, payload)
		if name == "truncated magic" {
			body = "text" + hidden.Encode(Magic[:2], MarshalPayload(parentID))
		}
		if got, ok := Parse(body); ok {
			t.Fatalf("%s: Parse = %q, true; want no parent", name, got)
		}
	}
}

// A nested reply may also carry text effects. Both payloads have to survive on
// the same post, whichever is attached first.
func TestCoexistsWithTextEffects(t *testing.T) {
	spans := []effects.Span{{ID: effects.Shimmer, Start: 0, Len: 4}}
	fx := "ship it" + hidden.Encode(effects.MagicEffects, effects.MarshalPayload(spans))

	body := Attach(fx, parentID)
	if got, ok := Parse(body); !ok || got != parentID {
		t.Fatalf("parent lost behind the effects payload: %q, %v", got, ok)
	}
	raw, ok := hidden.Decode(effects.MagicEffects, body)
	if !ok {
		t.Fatal("effects payload lost behind the parent reference")
	}
	if got, ok := effects.UnmarshalPayload(raw); !ok || len(got) != 1 || got[0] != spans[0] {
		t.Fatalf("effect spans = %v, %v; want %v", got, ok, spans)
	}
	if vis := hidden.Strip(body); vis != "ship it" {
		t.Fatalf("visible text = %q", vis)
	}
}

func TestCarriesRulesOutPlainPosts(t *testing.T) {
	if Carries("plain") {
		t.Fatal("Carries said yes to a plain post")
	}
	if !Carries(Attach("x", parentID)) {
		t.Fatal("Carries said no to a post with a parent")
	}
	// A body carrying only some *other* channel must be ruled out too.
	if Carries("x" + hidden.Encode(effects.MagicEffects, []byte{1, 0})) {
		t.Fatal("Carries said yes to a text-effects payload")
	}
}
