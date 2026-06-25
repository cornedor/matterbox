package textwidth

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// benchInputs mirror what a TUI re-measures every keystroke: styled ASCII
// message lines and GFM-table rows dominate; CJK/emoji are the deferred tail.
var benchInputs = []struct {
	name string
	s    string
}{
	{"plainASCII", "the quick brown fox jumps over the lazy dog, then does it again"},
	{"styledASCII", "\x1b[38;5;4m12:03\x1b[0m \x1b[1malice\x1b[0m: hey did you see the latest build? \x1b[2m(edited)\x1b[0m"},
	{"tableBorder", "├────────────┼──────────────────┼─────────┼───────────────┤"},
	{"tableRow", "  \x1b[2m│\x1b[0m alice      \x1b[2m│\x1b[0m deploy succeeded \x1b[2m│\x1b[0m  12ms  \x1b[2m│\x1b[0m 2026-06-25    \x1b[2m│\x1b[0m"},
	{"blocks", "\x1b[38;5;2m█████████████▉\x1b[0m\x1b[38;5;8m░░░░░░\x1b[0m 71%"},
	{"cjk", "CJK 日本語 width テスト 続きのテキストをもっと長く"},
	{"emoji", "shipped it 🚀 great work everyone 🎉🎉 lgtm 👍"},
}

func BenchmarkWidth(b *testing.B) {
	for _, in := range benchInputs {
		b.Run(in.name, func(b *testing.B) {
			b.ReportAllocs()
			var w int
			for i := 0; i < b.N; i++ {
				w = Width(in.s)
			}
			_ = w
		})
	}
}

func BenchmarkAnsiStringWidth(b *testing.B) {
	for _, in := range benchInputs {
		b.Run(in.name, func(b *testing.B) {
			b.ReportAllocs()
			var w int
			for i := 0; i < b.N; i++ {
				w = ansi.StringWidth(in.s)
			}
			_ = w
		})
	}
}
