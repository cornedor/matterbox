package welcome

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"

	"matterbox/internal/vapor"
)

// previewFallbackTheme is the chroma style assumed when no code_theme is set or
// the configured name isn't among styles.Names(). Mirrors the app's default
// (config.defaultCodeTheme / internal/ui highlight.go); monokai is always
// registered, so the cycler and the preview always have a valid anchor.
const previewFallbackTheme = "monokai"

// previewSnippet is the Go sample the Advanced-step theme preview highlights. A
// few lines that exercise the token types whose colours differ most between
// themes — keyword, function name, type, comment, and a string literal — so
// cycling themes shows a representative change.
const previewSnippet = "func greet(name string) string {\n" +
	"    // a friendly hello\n" +
	"    return fmt.Sprintf(\"Hi, %s!\", name)\n" +
	"}"

// previewBgFallback backs the snippet when a chroma style declares no background
// of its own (rare). A near-black keeps light foregrounds legible.
var previewBgFallback = vapor.RGB{R: 24, G: 22, B: 34}

// previewSeg is a run of snippet text sharing one foreground colour (and the
// underline attribute) — the unit drawCodeLine paints.
type previewSeg struct {
	text      string
	fg        vapor.RGB
	underline bool
}

// allThemeNames returns every registered chroma style name, sorted. Called at
// wizard construction (runtime), by which point every package-level
// styles.Register — including internal/ui's everforest-dark, linked into the
// matterbox binary — has run, so the bundled theme appears with the built-ins.
func allThemeNames() []string { return styles.Names() }

// themeIndex finds want in names, falling back to the default theme and then 0
// so a config naming an unknown (or empty) theme still lands on something valid.
func themeIndex(names []string, want string) int {
	if want == "" {
		want = previewFallbackTheme
	}
	for i, n := range names {
		if n == want {
			return i
		}
	}
	for i, n := range names {
		if n == previewFallbackTheme {
			return i
		}
	}
	return 0
}

// buildCodePreview highlights previewSnippet under the named chroma style and
// returns the style's background colour, the snippet's display width (longest
// line, for a snug band), and one slice of coloured segments per line. The
// foreground colours come straight from the theme; unlike the in-app renderer
// (which strips backgrounds to sit on the chat pane), the preview keeps the
// theme's own background so light and dark themes read true. An unknown name
// resolves to the default style via styles.Get's fallback.
func buildCodePreview(themeName string) (bg vapor.RGB, width int, lines [][]previewSeg) {
	style := styles.Get(themeName)
	base := style.Get(chroma.Background)
	bg = chromaRGB(base.Background, previewBgFallback)
	defaultFg := chromaRGB(base.Colour, valueC)

	lexer := lexers.Get("go")
	if lexer == nil {
		lexer = lexers.Fallback
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, previewSnippet)
	if err != nil {
		return bg, 0, nil
	}

	var cur []previewSeg
	curLen := 0
	bump := func() {
		lines = append(lines, cur)
		if curLen > width {
			width = curLen
		}
		cur, curLen = nil, 0
	}
	for _, tok := range it.Tokens() {
		entry := style.Get(tok.Type)
		fg := chromaRGB(entry.Colour, defaultFg)
		under := entry.Underline == chroma.Yes
		// A coalesced token can straddle line breaks; split so each line gets its
		// own segments.
		parts := strings.Split(tok.Value, "\n")
		for i, part := range parts {
			if i > 0 {
				bump()
			}
			if part != "" {
				cur = append(cur, previewSeg{text: part, fg: fg, underline: under})
				curLen += len([]rune(part))
			}
		}
	}
	bump()
	return bg, width, lines
}

// chromaRGB converts a chroma colour to a vapor.RGB, using fallback when the
// colour is unset — chroma leaves many token types without an explicit colour,
// expecting them to inherit the style's default foreground.
func chromaRGB(c chroma.Colour, fallback vapor.RGB) vapor.RGB {
	if !c.IsSet() {
		return fallback
	}
	return vapor.RGB{R: float64(c.Red()), G: float64(c.Green()), B: float64(c.Blue())}
}

// drawCodeLine paints one preview line: a band of the theme background bw cells
// wide (clamped to the available width w), then the coloured segments over it.
func drawCodeLine(grid [][]cell, x, y, w int, bg vapor.RGB, segs []previewSeg, bw int) {
	if bw > w {
		bw = w
	}
	for i := 0; i < bw; i++ {
		setCell(grid, x+i, y, cell{R: ' ', Fg: bg, Bg: bg, HasBg: true})
	}
	col := 0
	for _, seg := range segs {
		for _, r := range seg.text {
			if col >= bw {
				return
			}
			setCell(grid, x+col, y, cell{R: r, Fg: seg.fg, Bg: bg, HasBg: true, Underline: seg.underline})
			col++
		}
	}
}
