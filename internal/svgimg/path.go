package svgimg

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
)

// SVG path data lets a command letter be followed by any number of parameter
// sets — "a6 6 0 0 0-6 6 6 6 0 0 0 6 6" is two arcs sharing one `a`. Our
// rasteriser mishandles that repetition for arcs (it replays the first arc's
// radii for every later set), which turns an optimiser's output — Inkscape's
// "optimised SVG", svgo, scour all emit the compact form — into a blob.
//
// Rather than fork the rasteriser, we hand it path data in the one shape it
// gets right: every command letter carrying exactly one parameter set. The
// rewrite is purely lexical (regroup the same numbers under repeated letters),
// so it cannot change what a path means.

// arity is how many numbers one parameter set of each command takes.
var arity = map[byte]int{
	'M': 2, 'm': 2, 'L': 2, 'l': 2, 'H': 1, 'h': 1, 'V': 1, 'v': 1,
	'C': 6, 'c': 6, 'S': 4, 's': 4, 'Q': 4, 'q': 4, 'T': 2, 't': 2,
	'A': 7, 'a': 7, 'Z': 0, 'z': 0,
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isSep(b byte) bool {
	return b == ' ' || b == ',' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}

// scanNum reads one number from d starting at i, skipping any separators first.
//
// flag asks for an arc's large-arc/sweep flag, which is a single 0 or 1 and may
// be written with no separator at all ("a7 7 0 100 14" is flags 1 and 0 followed
// by x=0) — lexing those as one number is the other way optimiser output goes
// wrong, so the caller says which two positions are flags.
func scanNum(d string, i int, flag bool) (num string, next int, ok bool) {
	for i < len(d) && isSep(d[i]) {
		i++
	}
	if i >= len(d) {
		return "", i, false
	}
	if flag && (d[i] == '0' || d[i] == '1') {
		return d[i : i+1], i + 1, true
	}
	start := i
	if d[i] == '+' || d[i] == '-' {
		i++
	}
	var seenDigit, seenDot bool
	for i < len(d) {
		switch {
		case isDigit(d[i]):
			seenDigit = true
			i++
		case d[i] == '.' && !seenDot:
			// A second '.' starts the next number: ".5.5" is two numbers.
			seenDot = true
			i++
		case (d[i] == 'e' || d[i] == 'E') && seenDigit:
			j := i + 1
			if j < len(d) && (d[j] == '+' || d[j] == '-') {
				j++
			}
			if j < len(d) && isDigit(d[j]) {
				i = j + 1
				continue
			}
			return d[start:i], i, true
		default:
			return d[start:i], i, seenDigit
		}
	}
	return d[start:i], i, seenDigit
}

// normalizePath rewrites path data so every command letter carries exactly one
// parameter set. Numbers are copied through verbatim — only the grouping and the
// separators change. Anything it cannot lex is dropped from that point in the
// command, which is what the rasteriser would have done with it anyway.
func normalizePath(d string) string {
	var b strings.Builder
	b.Grow(len(d) + len(d)/4)
	for i := 0; i < len(d); {
		cmd := d[i]
		n, known := arity[cmd]
		if !known {
			i++
			continue
		}
		i++
		if n == 0 {
			b.WriteByte(cmd)
			b.WriteByte(' ')
			continue
		}
		for first := true; ; first = false {
			nums, j, ok := scanSet(d, i, cmd, n)
			if !ok {
				break
			}
			out := cmd
			if !first {
				// A repeated moveto continues as a lineto (SVG 1.1 §8.3.2).
				switch cmd {
				case 'M':
					out = 'L'
				case 'm':
					out = 'l'
				}
			}
			b.WriteByte(out)
			for _, s := range nums {
				b.WriteByte(' ')
				b.WriteString(s)
			}
			b.WriteByte(' ')
			i = j
		}
	}
	return strings.TrimSpace(b.String())
}

// scanSet reads one full parameter set for cmd, or reports !ok if a whole set is
// not there (the normal way a command's run of sets ends).
func scanSet(d string, i int, cmd byte, n int) (nums []string, next int, ok bool) {
	nums = make([]string, 0, n)
	for k := 0; k < n; k++ {
		isFlag := (cmd == 'A' || cmd == 'a') && (k == 3 || k == 4)
		num, j, got := scanNum(d, i, isFlag)
		if !got {
			return nil, i, false
		}
		nums = append(nums, num)
		i = j
	}
	return nums, i, true
}

// looksLikePathData guards the rewrite: only a value that is entirely path
// grammar — starting with a moveto, as every valid path must — is touched, so a
// `d=` attribute that is not path data is passed through untouched.
func looksLikePathData(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || (s[0] != 'M' && s[0] != 'm') {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if _, ok := arity[c]; ok {
			continue
		}
		switch {
		case isDigit(c), isSep(c), c == '.', c == '-', c == '+', c == 'e', c == 'E':
		default:
			return false
		}
	}
	return true
}

// The same library reads `scale(s)` as `scale(s, 0)`, flattening the Y axis to
// nothing, where SVG defines a single argument as a uniform scale — `scale(s, s)`.
// A drawing built as "shrink everything by a tenth" therefore collapses into a
// line. Writing the second argument out explicitly is the whole fix. (The other
// single-argument transforms it accepts — translate, rotate, skewX, skewY — all
// match the spec, so only scale is rewritten.)

var transformAttrRe = regexp.MustCompile(`(?s)(\s(?:transform|gradientTransform|patternTransform)\s*=\s*)("[^"]*"|'[^']*')`)

// normalizeTransformAttrs makes every one-argument scale() two-argument.
func normalizeTransformAttrs(raw []byte) []byte {
	return transformAttrRe.ReplaceAllFunc(raw, func(m []byte) []byte {
		i := bytes.IndexAny(m, `"'`)
		if i < 0 || len(m) < i+2 {
			return m
		}
		quote, val := m[i], string(m[i+1:len(m)-1])
		fixed := expandUniformScale(val)
		if fixed == val {
			return m
		}
		out := make([]byte, 0, len(m)+8)
		out = append(out, m[:i+1]...)
		out = append(out, fixed...)
		return append(out, quote)
	})
}

var scaleCallRe = regexp.MustCompile(`(?i)\bscale\s*\(([^)]*)\)`)

// expandUniformScale rewrites scale(s) as scale(s s), leaving a scale that
// already names both axes — and anything it cannot read as a lone number —
// exactly as it was.
func expandUniformScale(v string) string {
	return scaleCallRe.ReplaceAllStringFunc(v, func(call string) string {
		open := strings.Index(call, "(")
		if open < 0 {
			return call
		}
		args := strings.TrimSpace(call[open+1 : len(call)-1])
		if args == "" || strings.ContainsAny(args, ",") || len(strings.FieldsFunc(args, isSep2)) != 1 {
			return call
		}
		if _, err := strconv.ParseFloat(args, 64); err != nil {
			return call
		}
		return call[:open] + "(" + args + " " + args + ")"
	})
}

func isSep2(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f'
}
