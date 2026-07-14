package ui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"matterbox/internal/editor"
	"matterbox/internal/effects"
)

// The composer preview. A directive only fires if its name is one matterbox
// knows, and the whole point of that gate is that an unknown one stays literal
// text — which means a typo fails *silently*. So the composer paints what it
// recognises: `\shimmer{today}` shows its syntax dimmed and `today` in the
// colour it will be sent as, while `\shimer{today}` just sits there as text. The
// difference is the feedback.
//
// The preview is deliberately static — one colour per span, not the animated
// gradient. The composer already runs a shimmer loop for a recognised /command,
// and a second animation next to it would be noise; the message pane is where
// the effect performs.

// syncComposerDecorations rebuilds the editor's inline decorations from every
// source that has a claim on them: the effect previews and the grammar
// underlines. It is the single owner of the decoration slice — SetDecorations
// replaces the lot, so whoever calls it last would otherwise silently erase the
// other's work. Call it whenever the draft, the findings, or the effects change.
//
// Effect regions come first because the editor resolves an overlap by taking the
// first decoration that covers a rune: an effect body wins the colour over a
// grammar underline sitting inside it. The underline is the thing that loses in
// that rare overlap, which is the right way round — a misspelling inside
// `\shimmer{}` is still visible in the message, whereas a body that refuses to
// take its colour looks like the effect is broken.
func (m *Model) syncComposerDecorations() {
	decos := m.effectDecorations()
	decos = append(decos, m.grammarDecorations()...)
	if len(decos) == 0 {
		m.input.ClearDecorations()
		return
	}
	m.input.SetDecorations(decos)
}

// effectDecorations previews the recognised directives in the composer: the
// syntax dimmed (it disappears on send), the body in the effect's own colour.
// Returns nil when the draft holds no recognised directive, which is the case
// for essentially every message — the scan is a walk of the composer text, and
// runs only when the draft changes.
func (m *Model) effectDecorations() []editor.Decoration {
	regions := effects.Highlight(m.input.Value())
	if len(regions) == 0 {
		return nil
	}
	dim := lipgloss.NewStyle().Foreground(dimColor)
	decos := make([]editor.Decoration, 0, len(regions))
	for _, r := range regions {
		style := dim
		if r.Body {
			style = lipgloss.NewStyle().Foreground(effectPreviewColor(r.ID))
		}
		decos = append(decos, editor.Decoration{Start: r.Start, End: r.End, Style: style})
	}
	return decos
}

// effectPreviewColor is the single colour that stands in for an effect in the
// composer, taken from the same gradient the message pane animates — so the
// preview and the post are recognisably the same effect rather than two
// unrelated palettes.
func effectPreviewColor(id byte) color.Color {
	return effectColor(id, 0, 1, effectStaticPhase)
}
