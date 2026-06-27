package ui

import (
	"strings"
	"testing"
)

func TestConvertPastedBoxTables(t *testing.T) {
	in := "" +
		"┌───────────────────────┬──────────────┬──────────────────┬───────────┬────────┐\n" +
		"│        Runner         │  Seq write   │ fsync 2000 files │ load (1m) │ iowait │\n" +
		"├───────────────────────┼──────────────┼──────────────────┼───────────┼────────┤\n" +
		"│ id=4 (65.108.235.69)  │ 245–679 MB/s │ 1.5–5.1s         │ 0.6–1.3   │ 0–5%   │\n" +
		"├───────────────────────┼──────────────┼──────────────────┼───────────┼────────┤\n" +
		"│ id=16 (168.119.64.38) │ 99 MB/s      │ 44.2s            │ 5.71      │ 12%    │\n" +
		"└───────────────────────┴──────────────┴──────────────────┴───────────┴────────┘"
	want := "" +
		"| Runner | Seq write | fsync 2000 files | load (1m) | iowait |\n" +
		"| --- | --- | --- | --- | --- |\n" +
		"| id=4 (65.108.235.69) | 245–679 MB/s | 1.5–5.1s | 0.6–1.3 | 0–5% |\n" +
		"| id=16 (168.119.64.38) | 99 MB/s | 44.2s | 5.71 | 12% |"
	got, ok := convertPastedBoxTables(in)
	if !ok {
		t.Fatalf("expected conversion, got ok=false")
	}
	if got != want {
		t.Fatalf("conversion mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestConvertPastedBoxTables_ASCIIWithProse(t *testing.T) {
	in := "" +
		"see this:\n" +
		"+-----+-----+\n" +
		"| a   | b   |\n" +
		"+-----+-----+\n" +
		"| 1   | 2   |\n" +
		"+-----+-----+\n" +
		"thanks"
	want := "" +
		"see this:\n" +
		"| a | b |\n" +
		"| --- | --- |\n" +
		"| 1 | 2 |\n" +
		"thanks"
	got, ok := convertPastedBoxTables(in)
	if !ok || got != want {
		t.Fatalf("ascii+prose mismatch ok=%v\n got:\n%s\nwant:\n%s", ok, got, want)
	}
}

func TestConvertPastedBoxTables_NoConversion(t *testing.T) {
	cases := map[string]string{
		"plain prose":      "hello there\nhow are you",
		"already markdown": "| a | b |\n| --- | --- |\n| 1 | 2 |",
		"single bar line":  "a | b is fine",
		"horizontal rule":  "intro\n\n---\n\nmore",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := convertPastedBoxTables(in)
			if ok {
				t.Fatalf("expected no conversion, got ok=true:\n%s", got)
			}
			if got != in {
				t.Fatalf("text changed despite ok=false\n got:\n%s\nwant:\n%s", got, in)
			}
		})
	}
}

func TestConvertPastedBoxTables_FencedTableLeftAlone(t *testing.T) {
	in := "" +
		"```\n" +
		"┌───┬───┐\n" +
		"│ a │ b │\n" +
		"└───┴───┘\n" +
		"```"
	got, ok := convertPastedBoxTables(in)
	if ok {
		t.Fatalf("table inside a fence must not convert, got:\n%s", got)
	}
	if got != in {
		t.Fatalf("fenced content changed:\n%s", got)
	}
}

func TestConvertPastedBoxTables_RaggedRowsPadded(t *testing.T) {
	in := "" +
		"┌───┬───┬───┐\n" +
		"│ a │ b │ c │\n" +
		"├───┼───┼───┤\n" +
		"│ 1 │ 2 │\n" +
		"└───┴───┴───┘"
	want := "" +
		"| a | b | c |\n" +
		"| --- | --- | --- |\n" +
		"| 1 | 2 |  |"
	got, ok := convertPastedBoxTables(in)
	if !ok || got != want {
		t.Fatalf("ragged mismatch ok=%v\n got:\n%s\nwant:\n%s", ok, got, want)
	}
}

func TestConvertPastedBoxTables_EscapesPipe(t *testing.T) {
	in := "" +
		"┌─────────┬───┐\n" +
		"│ a | b   │ c │\n" +
		"├─────────┼───┤\n" +
		"│ x       │ y │\n" +
		"└─────────┴───┘"
	got, ok := convertPastedBoxTables(in)
	if !ok {
		t.Fatalf("expected conversion")
	}
	// The literal pipe inside the cell must be escaped so it doesn't split columns.
	if !strings.Contains(got, `a \| b`) {
		t.Fatalf("expected escaped pipe in output:\n%s", got)
	}
}
