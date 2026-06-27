package ui

import (
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
)

// everforestDark registers an Everforest (dark) chroma style so it can be picked
// with `code_theme: everforest-dark` like any built-in. chroma ships no
// Everforest style, so we build one from the palette — matching the Ghostty
// theme of the same name. Backgrounds are stripped at use time (highlight.go's
// codeHLStyle), so only the foreground accents matter here; those accents are
// shared across the hard/medium/soft Everforest variants, so this one entry
// covers all of them.
//
// Palette (Everforest dark): fg #d3c6aa · red #e67e80 · orange #e69875 · yellow
// #dbbc7f · green #a7c080 · aqua #83c092 · blue #7fbbb3 · purple #d699b6 · grey
// #7a8478. Mapping follows the usual Everforest editor theme: keywords red,
// strings green, numbers/constants purple, functions blue, types yellow,
// builtins aqua, comments grey-italic.
var everforestDark = styles.Register(chroma.MustNewStyle("everforest-dark", chroma.StyleEntries{
	chroma.Background:            "#d3c6aa bg:#2d353b",
	chroma.Comment:               "italic #7a8478",
	chroma.CommentPreproc:        "#83c092",
	chroma.Keyword:               "#e67e80",
	chroma.KeywordConstant:       "#d699b6",
	chroma.KeywordType:           "#dbbc7f",
	chroma.Operator:              "#e69875",
	chroma.OperatorWord:          "#e67e80",
	chroma.Punctuation:           "#d3c6aa",
	chroma.Name:                  "#d3c6aa",
	chroma.NameBuiltin:           "#83c092",
	chroma.NameBuiltinPseudo:     "#83c092",
	chroma.NameFunction:          "#7fbbb3",
	chroma.NameClass:             "#dbbc7f",
	chroma.NameDecorator:         "#83c092",
	chroma.NameException:         "#e67e80",
	chroma.NameTag:               "#e67e80",
	chroma.NameAttribute:         "#dbbc7f",
	chroma.NameConstant:          "#d699b6",
	chroma.NameVariable:          "#d3c6aa",
	chroma.LiteralString:         "#a7c080",
	chroma.LiteralStringChar:     "#a7c080",
	chroma.LiteralStringSymbol:   "#a7c080",
	chroma.LiteralStringEscape:   "#83c092",
	chroma.LiteralStringInterpol: "#e69875",
	chroma.LiteralNumber:         "#d699b6",
	chroma.GenericHeading:        "bold #d3c6aa",
	chroma.GenericSubheading:     "bold #d3c6aa",
	chroma.GenericDeleted:        "#e67e80",
	chroma.GenericInserted:       "#a7c080",
	chroma.GenericEmph:           "italic",
	chroma.GenericStrong:         "bold",
	chroma.Error:                 "#e67e80",
}))
