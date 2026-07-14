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

// decompileEffects is compileEffects' inverse, for editing: it turns a post body
// that carries an effects payload back into the markup that produced it, so
// re-opening your own `\shimmer{today}` in the composer shows exactly that rather
// than the bare word (with an invisible payload silently riding along behind it,
// which the next edit would then misalign).
//
// A body carrying no effects payload is returned untouched — including a body
// carrying some *other* channel's payload, such as a Gorillas game post, whose
// bytes are not ours to rewrite. Spans that no longer fit the text are dropped,
// the same way the renderer drops them: the effect stops applying, the words are
// never corrupted.
func decompileEffects(body string) string {
	spans, ok := decodeEffectSpans(body)
	if !ok || len(spans) == 0 {
		return body
	}
	visible := hidden.Strip(body)
	n := len([]rune(visible))
	kept := make([]effects.Span, 0, len(spans))
	for _, s := range spans {
		if s.Len > 0 && s.Start >= 0 && s.Start+s.Len <= n && effects.Name(s.ID) != "" {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		return visible
	}
	return effects.Reconstruct(visible, kept)
}
