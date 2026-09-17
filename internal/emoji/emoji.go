// Package emoji resolves Mattermost's emoji shortcodes to unicode glyphs.
//
// The table is generated from webapp/channels/src/utils/emoji.json — the file
// the official web client renders from — so a shortcode draws here the way it
// draws there, and the picker offers only names the server will resolve.
//
// That verbatim copy is the point. Deriving the set from a general-purpose
// emoji library instead gets about 1700 of the 4464 shortcodes wrong, in ways
// that are invisible until someone compares two clients side by side:
// skin-tone variants compose the modifier after the ZWJ sequence rather than
// after the person it applies to (:man_farmer_light_skin_tone: as 👨‍🌾🏻
// instead of 👨🏻‍🌾), the gender-neutral names resolve to gendered sequences
// (:runner: as 🏃‍♂️ instead of 🏃), and a third of the offered names are
// spellings Mattermost has never heard of, which travel to everyone else as
// literal text.
//
//go:generate go run ./gen
package emoji

import (
	"strings"
	"sync"
)

// vs16 is the emoji variation selector (U+FE0F): presentational, and present
// on one side of a comparison as often as not, so reverse lookups fall back to
// matching without it.
const vs16 = "️"

var (
	once      sync.Once
	glyphs    map[string]string // shortcode -> glyph
	canonical map[string]string // glyph -> canonical shortcode
)

// build fills the lookup maps from the generated table. Deferred to first use
// so a CLI verb that never renders a message doesn't pay for it.
func build() {
	once.Do(func() {
		glyphs = make(map[string]string, len(names))
		canonical = make(map[string]string, len(entries))
		for _, e := range entries {
			glyphs[e.name] = e.glyph
			for _, a := range e.aliases {
				glyphs[a] = e.glyph
			}
			canonical[e.glyph] = e.name
		}
		// A second pass for the VS16-less spelling, so it never displaces a
		// glyph that owns that exact sequence itself.
		for _, e := range entries {
			if bare := strings.ReplaceAll(e.glyph, vs16, ""); bare != e.glyph {
				if _, taken := canonical[bare]; !taken {
					canonical[bare] = e.name
				}
			}
		}
	})
}

// Glyph returns the unicode glyph for a bare shortcode (no colons), or "" when
// Mattermost doesn't know the name — the caller's cue to try the custom
// (server) emoji path. Names match case-insensitively, as the web client's
// renderer does.
func Glyph(name string) string {
	build()
	if g, ok := glyphs[name]; ok {
		return g
	}
	return glyphs[strings.ToLower(name)]
}

// Names returns every shortcode Mattermost accepts, sorted, aliases included.
// The slice is shared and must not be modified: the emoji picker walks it on
// every keystroke, so it is deliberately not copied.
func Names() []string { return names }

// Name maps a glyph back to the canonical shortcode Mattermost names it by,
// tolerating a missing or extra variation selector. Used to turn an emoji
// someone typed elsewhere (a Telegram reaction) into a Mattermost reaction.
func Name(glyph string) (string, bool) {
	build()
	if n, ok := canonical[glyph]; ok {
		return n, true
	}
	n, ok := canonical[strings.ReplaceAll(glyph, vs16, "")]
	return n, ok
}

// SourceRef is the mattermost/mattermost commit the table was generated from.
func SourceRef() string { return sourceRef }
