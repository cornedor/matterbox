// Copyright 2017 The oksvg Authors. All rights reserved.
// created: 2/12/2017 by S.R.Wiley
//
// utils.go implements translation of an SVG2.0 path into a rasterx Path.

package oksvg

import (
	"errors"
	"log"
	"math"
	"unicode"

	"github.com/srwiley/rasterx"

	"golang.org/x/image/math/fixed"
)

type (
	// ErrorMode is the for setting how the parser reacts to unparsed elements
	ErrorMode uint8
	// PathCursor is used to parse SVG format path strings into a rasterx Path
	PathCursor struct {
		rasterx.Path
		placeX, placeY         float64
		cntlPtX, cntlPtY       float64
		pathStartX, pathStartY float64
		points                 []float64
		lastKey                uint8
		ErrorMode              ErrorMode
		inPath                 bool
	}
)

const (
	// IgnoreErrorMode skips un-parsed SVG elements.
	IgnoreErrorMode ErrorMode = iota

	// WarnErrorMode outputs a warning when an un-parsed SVG element is found.
	WarnErrorMode

	// StrictErrorMode causes an error when an un-parsed SVG element is found.
	StrictErrorMode
)

var (
	errParamMismatch  = errors.New("param mismatch")
	errCommandUnknown = errors.New("unknown command")
	errZeroLengthID   = errors.New("zero length id")
)

// ReadFloat reads a floating point value and adds it to the cursor's points slice.
func (c *PathCursor) ReadFloat(numStr string) error {
	last := 0
	isFirst := true
	for i, n := range numStr {
		if n == '.' {
			if isFirst {
				isFirst = false
				continue
			}
			f, err := parseFloat(numStr[last:i], 64)
			if err != nil {
				return err
			}
			c.points = append(c.points, f)
			last = i
		}
	}
	f, err := parseFloat(numStr[last:], 64)
	if err != nil {
		return err
	}
	c.points = append(c.points, f)
	return nil
}

// GetPoints reads a set of floating point values from the SVG format number string,
// and add them to the cursor's points slice.
func (c *PathCursor) GetPoints(dataPoints string) error {
	lastIndex := -1
	c.points = c.points[0:0]
	lr := ' '
	for i, r := range dataPoints {
		if !unicode.IsNumber(r) && r != '.' && !(r == '-' && lr == 'e') && r != 'e' {
			if lastIndex != -1 {
				if err := c.ReadFloat(dataPoints[lastIndex:i]); err != nil {
					return err
				}
			}
			if r == '-' {
				lastIndex = i
			} else {
				lastIndex = -1
			}
		} else if lastIndex == -1 {
			lastIndex = i
		}
		lr = r
	}
	if lastIndex != -1 && lastIndex != len(dataPoints) {
		if err := c.ReadFloat(dataPoints[lastIndex:]); err != nil {
			return err
		}
	}
	return nil
}

// EllipseAt adds a path of an elipse centered at cx, cy of radius rx and ry
// to the PathCursor
func (c *PathCursor) EllipseAt(cx, cy, rx, ry float64) {
	c.placeX, c.placeY = cx+rx, cy
	c.points = c.points[0:0]
	c.points = append(c.points, rx, ry, 0.0, 1.0, 0.0, c.placeX, c.placeY)
	c.Path.Start(fixed.Point26_6{
		X: fixed.Int26_6(c.placeX * 64),
		Y: fixed.Int26_6(c.placeY * 64)})
	c.placeX, c.placeY = rasterx.AddArc(c.points, cx, cy, c.placeX, c.placeY, &c.Path)
	c.Path.Stop(true)
}

// AddArcFromA adds a path of an arc element to the cursor path to the PathCursor
func (c *PathCursor) AddArcFromA(points []float64) {
	cx, cy := rasterx.FindEllipseCenter(&points[0], &points[1], points[2]*math.Pi/180, c.placeX,
		c.placeY, points[5], points[6], points[4] == 0, points[3] == 0)
	// matterbox: was c.points, which is every parameter set of the command. A
	// command may carry several ("a6 6 0 0 0-6 6 6 6 0 0 0 6 6" is two arcs), and
	// addSeg hands us one at a time — reading from the front replayed the first
	// arc's radii for all of them, which is what an SVG optimiser emits.
	c.placeX, c.placeY = rasterx.AddArc(points, cx, cy, c.placeX, c.placeY, &c.Path)
}

// CompilePath translates the svgPath description string into a rasterx path.
// All valid SVG path elements are interpreted to rasterx equivalents.
// The resulting path element is stored in the PathCursor.
func (c *PathCursor) CompilePath(svgPath string) error {
	c.init()
	lastIndex := -1
	for i, v := range svgPath {
		if unicode.IsLetter(v) && v != 'e' {
			if lastIndex != -1 {
				if err := c.addSeg(svgPath[lastIndex:i]); err != nil {
					return err
				}
			}
			lastIndex = i
		}
	}
	if lastIndex != -1 {
		if err := c.addSeg(svgPath[lastIndex:]); err != nil {
			return err
		}
	}
	return nil
}

func reflect(px, py, rx, ry float64) (x, y float64) {
	return px*2 - rx, py*2 - ry
}

func (c *PathCursor) valsToAbs(last float64) {
	for i := 0; i < len(c.points); i++ {
		last += c.points[i]
		c.points[i] = last
	}
}

func (c *PathCursor) pointsToAbs(sz int) {
	lastX := c.placeX
	lastY := c.placeY
	for j := 0; j < len(c.points); j += sz {
		for i := 0; i < sz; i += 2 {
			c.points[i+j] += lastX
			c.points[i+1+j] += lastY
		}
		lastX = c.points[(j+sz)-2]
		lastY = c.points[(j+sz)-1]
	}
}

func (c *PathCursor) hasSetsOrMore(sz int, rel bool) bool {
	if !(len(c.points) >= sz && len(c.points)%sz == 0) {
		return false
	}
	if rel {
		c.pointsToAbs(sz)
	}
	return true
}

func (c *PathCursor) reflectControlQuad() {
	switch c.lastKey {
	case 'q', 'Q', 'T', 't':
		c.cntlPtX, c.cntlPtY = reflect(c.placeX, c.placeY, c.cntlPtX, c.cntlPtY)
	default:
		c.cntlPtX, c.cntlPtY = c.placeX, c.placeY
	}
}

func (c *PathCursor) reflectControlCube() {
	switch c.lastKey {
	case 'c', 'C', 's', 'S':
		c.cntlPtX, c.cntlPtY = reflect(c.placeX, c.placeY, c.cntlPtX, c.cntlPtY)
	default:
		c.cntlPtX, c.cntlPtY = c.placeX, c.placeY
	}
}

// addSeg decodes an SVG seqment string into equivalent raster path commands saved
// in the cursor's Path
func (c *PathCursor) addSeg(segString string) error {
	// Parse the string describing the numeric points in SVG format.
	//
	// matterbox: an arc's two flags are single digits that may be written with no
	// separator at all ("a7 7 0 100 14" is flags 1 and 0 then x=0), which the
	// general number scanner reads as one number. Arcs therefore get their own
	// scan; every other command is unambiguous.
	if segString[0] == 'a' || segString[0] == 'A' {
		if err := c.getArcPoints(segString[1:]); err != nil {
			return err
		}
	} else if err := c.GetPoints(segString[1:]); err != nil {
		return err
	}
	l := len(c.points)
	k := segString[0]
	rel := false
	switch k {
	case 'z':
		fallthrough
	case 'Z':
		if len(c.points) != 0 {
			return errParamMismatch
		}
		if c.inPath {
			c.Path.Stop(true)
			c.placeX = c.pathStartX
			c.placeY = c.pathStartY
			c.inPath = false
		}
	case 'm':
		rel = true
		fallthrough
	case 'M':
		if !c.hasSetsOrMore(2, rel) {
			return errParamMismatch
		}
		c.pathStartX, c.pathStartY = c.points[0], c.points[1]
		c.inPath = true
		c.Path.Start(fixed.Point26_6{X: fixed.Int26_6((c.pathStartX) * 64), Y: fixed.Int26_6((c.pathStartY) * 64)})
		for i := 2; i < l-1; i += 2 {
			c.Path.Line(fixed.Point26_6{
				X: fixed.Int26_6((c.points[i]) * 64),
				Y: fixed.Int26_6((c.points[i+1]) * 64)})
		}
		c.placeX = c.points[l-2]
		c.placeY = c.points[l-1]
	case 'l':
		rel = true
		fallthrough
	case 'L':
		if !c.hasSetsOrMore(2, rel) {
			return errParamMismatch
		}
		for i := 0; i < l-1; i += 2 {
			c.Path.Line(fixed.Point26_6{
				X: fixed.Int26_6((c.points[i]) * 64),
				Y: fixed.Int26_6((c.points[i+1]) * 64)})
		}
		c.placeX = c.points[l-2]
		c.placeY = c.points[l-1]
	case 'v':
		c.valsToAbs(c.placeY)
		fallthrough
	case 'V':
		if !c.hasSetsOrMore(1, false) {
			return errParamMismatch
		}
		for _, p := range c.points {
			c.Path.Line(fixed.Point26_6{
				X: fixed.Int26_6((c.placeX) * 64),
				Y: fixed.Int26_6((p) * 64)})
		}
		c.placeY = c.points[l-1]
	case 'h':
		c.valsToAbs(c.placeX)
		fallthrough
	case 'H':
		if !c.hasSetsOrMore(1, false) {
			return errParamMismatch
		}
		for _, p := range c.points {
			c.Path.Line(fixed.Point26_6{
				X: fixed.Int26_6((p) * 64),
				Y: fixed.Int26_6((c.placeY) * 64)})
		}
		c.placeX = c.points[l-1]
	case 'q':
		rel = true
		fallthrough
	case 'Q':
		if !c.hasSetsOrMore(4, rel) {
			return errParamMismatch
		}
		for i := 0; i < l-3; i += 4 {
			c.Path.QuadBezier(
				fixed.Point26_6{
					X: fixed.Int26_6((c.points[i]) * 64),
					Y: fixed.Int26_6((c.points[i+1]) * 64)},
				fixed.Point26_6{
					X: fixed.Int26_6((c.points[i+2]) * 64),
					Y: fixed.Int26_6((c.points[i+3]) * 64)})
		}
		c.cntlPtX, c.cntlPtY = c.points[l-4], c.points[l-3]
		c.placeX = c.points[l-2]
		c.placeY = c.points[l-1]
	case 't':
		rel = true
		fallthrough
	case 'T':
		if !c.hasSetsOrMore(2, rel) {
			return errParamMismatch
		}
		for i := 0; i < l-1; i += 2 {
			c.reflectControlQuad()
			c.Path.QuadBezier(
				fixed.Point26_6{
					X: fixed.Int26_6((c.cntlPtX) * 64),
					Y: fixed.Int26_6((c.cntlPtY) * 64)},
				fixed.Point26_6{
					X: fixed.Int26_6((c.points[i]) * 64),
					Y: fixed.Int26_6((c.points[i+1]) * 64)})
			c.lastKey = k
			c.placeX = c.points[i]
			c.placeY = c.points[i+1]
		}
	case 'c':
		rel = true
		fallthrough
	case 'C':
		if !c.hasSetsOrMore(6, rel) {
			return errParamMismatch
		}
		for i := 0; i < l-5; i += 6 {
			c.Path.CubeBezier(
				fixed.Point26_6{
					X: fixed.Int26_6((c.points[i]) * 64),
					Y: fixed.Int26_6((c.points[i+1]) * 64)},
				fixed.Point26_6{
					X: fixed.Int26_6((c.points[i+2]) * 64),
					Y: fixed.Int26_6((c.points[i+3]) * 64)},
				fixed.Point26_6{
					X: fixed.Int26_6((c.points[i+4]) * 64),
					Y: fixed.Int26_6((c.points[i+5]) * 64)})
		}
		c.cntlPtX, c.cntlPtY = c.points[l-4], c.points[l-3]
		c.placeX = c.points[l-2]
		c.placeY = c.points[l-1]
	case 's':
		rel = true
		fallthrough
	case 'S':
		if !c.hasSetsOrMore(4, rel) {
			return errParamMismatch
		}
		for i := 0; i < l-3; i += 4 {
			c.reflectControlCube()
			c.Path.CubeBezier(fixed.Point26_6{
				X: fixed.Int26_6((c.cntlPtX) * 64), Y: fixed.Int26_6((c.cntlPtY) * 64)},
				fixed.Point26_6{
					X: fixed.Int26_6((c.points[i]) * 64), Y: fixed.Int26_6((c.points[i+1]) * 64)},
				fixed.Point26_6{
					X: fixed.Int26_6((c.points[i+2]) * 64), Y: fixed.Int26_6((c.points[i+3]) * 64)})
			c.lastKey = k
			c.cntlPtX, c.cntlPtY = c.points[i], c.points[i+1]
			c.placeX = c.points[i+2]
			c.placeY = c.points[i+3]
		}
	case 'a', 'A':
		if !c.hasSetsOrMore(7, false) {
			return errParamMismatch
		}
		for i := 0; i < l-6; i += 7 {
			if k == 'a' {
				c.points[i+5] += c.placeX
				c.points[i+6] += c.placeY
			}
			c.AddArcFromA(c.points[i:])
		}
	default:
		if c.ErrorMode == StrictErrorMode {
			return errCommandUnknown
		}
		if c.ErrorMode == WarnErrorMode {
			log.Println("Ignoring svg command " + string(k))
		}
	}
	// So we know how to extend some segment types
	c.lastKey = k
	return nil
}

func (c *PathCursor) init() {
	c.placeX = 0.0
	c.placeY = 0.0
	c.points = c.points[0:0]
	c.lastKey = ' '
	c.Path.Clear()
	c.inPath = false
}

// getArcPoints reads an arc command's parameters into the cursor, in sets of
// seven, treating the fourth and fifth of each set as single-digit flags.
//
// matterbox addition. See addSeg for why arcs cannot share the general scanner.
func (c *PathCursor) getArcPoints(dataPoints string) error {
	c.points = c.points[0:0]
	for i := 0; ; {
		set := [7]float64{}
		var k int
		for k = 0; k < 7; k++ {
			num, next, ok := scanArcNum(dataPoints, i, k == 3 || k == 4)
			if !ok {
				break
			}
			v, err := parseFloat(num, 64)
			if err != nil {
				return err
			}
			set[k] = v
			i = next
		}
		if k == 0 {
			return nil // no set left to read: the command is done
		}
		if k < 7 {
			return errParamMismatch // a partial set is malformed
		}
		c.points = append(c.points, set[:]...)
	}
}

// scanArcNum reads one number from d at i, skipping separators first. With flag
// set it takes a single 0 or 1, which is how an arc's large-arc and sweep flags
// may be written when an optimiser has stripped every separator it can.
//
// matterbox addition.
func scanArcNum(d string, i int, flag bool) (num string, next int, ok bool) {
	for i < len(d) && isPathSep(d[i]) {
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
		case d[i] >= '0' && d[i] <= '9':
			seenDigit = true
			i++
		case d[i] == '.' && !seenDot:
			seenDot = true // a second dot starts the next number: ".5.5" is two
			i++
		case (d[i] == 'e' || d[i] == 'E') && seenDigit:
			j := i + 1
			if j < len(d) && (d[j] == '+' || d[j] == '-') {
				j++
			}
			if j < len(d) && d[j] >= '0' && d[j] <= '9' {
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

// isPathSep reports whether b separates two numbers in path data.
//
// matterbox addition.
func isPathSep(b byte) bool {
	return b == ' ' || b == ',' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}
