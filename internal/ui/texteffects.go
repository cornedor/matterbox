package ui

import (
	"matterbox/internal/effects"
	"matterbox/internal/hidden"
)

// compileEffects turns composer markup into the text that goes on the wire. It
// parses \name{...} effect directives (see internal/effects); if the message
// uses any, it returns the plain visible text with an invisible MBF1 payload
// appended, so matterbox can animate the marked spans while every other client
// sees only the clean text.
//
// A message that uses no recognised effect is returned exactly as typed. The
// effect grammar — including the \\ escape — stays dormant unless an effect is
// actually present, so ordinary backslashes in code, paths, or regexes are never
// disturbed.
func compileEffects(text string) string {
	visible, spans := effects.Parse(text)
	if len(spans) == 0 {
		return text
	}
	return visible + hidden.Encode(effects.MagicEffects, effects.MarshalPayload(spans))
}
