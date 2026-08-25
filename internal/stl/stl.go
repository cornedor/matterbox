// Package stl parses STL meshes and renders them with a small software
// rasterizer. It exists so matterbox can show an uploaded .stl inline in the
// transcript and let you orbit it in a modal, and it deliberately depends on
// nothing but the standard library: the renderer's output is an *image.RGBA,
// which is exactly what the Kitty graphics path already transmits (see
// internal/ui/stlview.go).
//
// STL is the plainest 3D format there is — an unstructured soup of triangles
// with no units, no colour, no topology and no guarantee that the normals or
// the winding mean anything. Everything here is written for that: normals are
// recomputed from the geometry rather than trusted, shading is two-sided so an
// inside-out model still reads as solid, and the camera fits itself to whatever
// bounding box the file turns out to have.
package stl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// Vec3 is a point or direction in model space. float32 because a mesh is mostly
// vertex storage and STL itself only ever holds float32 — a 500k-triangle model
// is 18MB of Vec3 either way, and doubling that buys nothing.
type Vec3 struct{ X, Y, Z float32 }

func (a Vec3) sub(b Vec3) Vec3 { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }

func (a Vec3) cross(b Vec3) Vec3 {
	return Vec3{a.Y*b.Z - a.Z*b.Y, a.Z*b.X - a.X*b.Z, a.X*b.Y - a.Y*b.X}
}

func (a Vec3) dot(b Vec3) float32 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }

func (a Vec3) norm() Vec3 {
	l := float32(math.Sqrt(float64(a.dot(a))))
	if l == 0 {
		return Vec3{}
	}
	return Vec3{a.X / l, a.Y / l, a.Z / l}
}

// Triangle is one facet. The file's own normal is dropped on purpose: a great
// many STL exporters write zeroes, garbage, or a normal that disagrees with the
// winding, and the cross product of the edges is both free and right.
type Triangle struct{ A, B, C Vec3 }

// Mesh is a parsed STL: the facets plus the axis-aligned bounds, which is all
// the camera needs to frame it.
type Mesh struct {
	Tris     []Triangle
	Min, Max Vec3
	// Closed reports that the facets form a closed surface wound consistently
	// outward — established, and if necessary made true, by orient(). It is what
	// lets the renderer drop back faces instead of drawing them (see draw): on a
	// closed solid every back face is behind a front one by definition, so
	// culling them is free, and on anything else it would punch holes.
	Closed bool
}

// Center is the middle of the bounding box — the point the camera orbits. Not
// the centroid: a model with dense detail in one corner should still sit in the
// middle of the frame.
func (m *Mesh) Center() Vec3 {
	return Vec3{(m.Min.X + m.Max.X) / 2, (m.Min.Y + m.Max.Y) / 2, (m.Min.Z + m.Max.Z) / 2}
}

// Size is the bounding box's extent per axis — the dimensions shown in the
// viewer's caption. STL carries no units; by overwhelming convention (3D
// printing) they are millimetres, but nothing here assumes that.
func (m *Mesh) Size() Vec3 {
	return m.Max.sub(m.Min)
}

// Radius is the radius of a sphere around the bounding box, centred on Center.
// The camera fits the model by inscribing this sphere in the viewport, so the
// model can be spun to any angle without ever clipping the frame — a box's
// silhouette changes as it turns, and fitting the box instead would make the
// model breathe in and out of the edges as you orbit.
func (m *Mesh) Radius() float32 {
	s := m.Size()
	r := float32(math.Sqrt(float64(s.X*s.X+s.Y*s.Y+s.Z*s.Z))) / 2
	if r <= 0 {
		return 1 // a degenerate or single-point mesh still needs a camera distance
	}
	return r
}

// MaxTriangles bounds a parse. 2M triangles is a 100MB binary STL and about
// 48MB of Mesh — past anything that gets posted in a chat, and past what the
// rasterizer can turn around at interactive speed. Refusing is better than
// silently rendering part of a model.
const MaxTriangles = 2_000_000

var (
	// ErrNotSTL means the bytes are neither a binary nor an ASCII STL.
	ErrNotSTL = errors.New("not an STL file")
	// ErrTooLarge means the file is a valid STL with more facets than
	// MaxTriangles.
	ErrTooLarge = errors.New("STL too large")
	// ErrEmpty means it parsed but holds no facets, so there is nothing to draw.
	ErrEmpty = errors.New("STL holds no triangles")
)

// binaryHeader is the fixed prologue of a binary STL: 80 bytes of free-form
// comment followed by a uint32 facet count.
const binaryHeader = 84

// binaryFacet is one binary facet on disk: a normal and three vertices (12
// float32) plus a 16-bit "attribute byte count" that is almost always zero.
const binaryFacet = 50

// Looks reports whether these bytes are plausibly an STL, cheaply enough to
// call on a sniff of the head of a file. Used to gate the render path so a
// mislabelled MIME type can't send a PNG to the STL parser or vice versa. A
// valid-but-oversized file counts: it is an STL, and saying so lets Decode
// explain why it won't draw instead of claiming the format is wrong.
func Looks(b []byte) bool {
	if _, _, ok := binaryClaim(b); ok {
		return true
	}
	return asciiStart(b) >= 0
}

// binaryClaim reads a binary STL's header. n is the facet count it claims, avail
// is how many whole facets the bytes actually carry, and ok reports whether this
// is a binary STL at all.
//
// The format has no magic number, so this is the whole of format detection, and
// it is arithmetic rather than a keyword sniff on purpose. The usual "does it
// start with the word solid" test is wrong often enough to matter: several CAD
// packages write "solid <name>" into the 80-byte binary header, and that file
// then parses as an ASCII STL with zero facets. Reading the count and checking
// it against the file's length gets those right.
//
// The bound at MaxTriangles is also what keeps a non-STL out. Bytes 80..84 of
// an ASCII STL are text, and the smallest little-endian uint32 four printable
// bytes can spell is 0x09090909 — 151 million, far past the bound — so an ASCII
// file can never be mistaken for a binary one here. The same bound rejects the
// arbitrary uint32 a PNG or a zip happens to have at that offset.
func binaryClaim(b []byte) (n, avail int, ok bool) {
	// Under one whole facet there is nothing to draw and nothing to be confident
	// about, so such a file is never binary.
	if len(b) < binaryHeader+binaryFacet {
		return 0, 0, false
	}
	n = int(binary.LittleEndian.Uint32(b[80:84]))
	avail = (len(b) - binaryHeader) / binaryFacet
	return n, avail, n > 0 && n <= MaxTriangles
}

// isBinary reports whether Decode should take the binary path. A file that
// claims more facets than it carries is a truncated download and still counts:
// decodeBinary draws the facets that arrived, which beats refusing a model whose
// last few triangles are missing.
func isBinary(b []byte) bool {
	_, _, ok := binaryClaim(b)
	return ok
}

// Decode parses an STL, binary or ASCII.
func Decode(b []byte) (*Mesh, error) {
	var m *Mesh
	var err error
	switch {
	case isBinary(b):
		m, err = decodeBinary(b)
	case asciiStart(b) >= 0:
		m, err = decodeASCII(b)
	default:
		// A real binary STL bigger than we will draw: say so, rather than
		// claiming the format is wrong. The length check is what separates it
		// from a non-STL whose bytes 80..84 happen to spell a huge number — that
		// file is nowhere near long enough to hold the facets it "claims".
		if len(b) >= binaryHeader+binaryFacet {
			if n := int64(binary.LittleEndian.Uint32(b[80:84])); n > MaxTriangles {
				if int64(len(b)) >= int64(binaryHeader)+n*binaryFacet {
					return nil, fmt.Errorf("%w: %d facets", ErrTooLarge, n)
				}
			}
		}
		return nil, ErrNotSTL
	}
	if err != nil {
		return nil, err
	}
	if len(m.Tris) == 0 {
		return nil, ErrEmpty
	}
	m.bounds()
	m.orient()
	return m, nil
}

// bounds fills Min/Max from the facets. Done once after parsing rather than
// incrementally, so the parse loop stays a tight copy.
func (m *Mesh) bounds() {
	inf := float32(math.Inf(1))
	m.Min = Vec3{inf, inf, inf}
	m.Max = Vec3{-inf, -inf, -inf}
	for _, t := range m.Tris {
		for _, v := range [3]Vec3{t.A, t.B, t.C} {
			m.Min.X = min(m.Min.X, v.X)
			m.Min.Y = min(m.Min.Y, v.Y)
			m.Min.Z = min(m.Min.Z, v.Z)
			m.Max.X = max(m.Max.X, v.X)
			m.Max.Y = max(m.Max.Y, v.Y)
			m.Max.Z = max(m.Max.Z, v.Z)
		}
	}
}

// orient works out whether the mesh is a closed solid, and if it is, makes sure
// it is wound outward — the two facts the renderer needs before it can cull back
// faces, which is worth about a third of the rasterizer on a real part.
//
// Two questions, because one test doesn't cover it. closedSurface asks whether
// the facets meet edge to edge with agreeing winding, which catches an open
// surface, a mesh with holes, and scattered flipped facets. windingClosure asks
// whether the normals sum to nothing, which is what catches a whole region
// flipped as a block — the defect closedSurface is blind to and the one that
// would look worst. Both are deliberately conservative: a mesh that fails either
// simply doesn't get culled and renders exactly as it always has.
//
// Consistent winding still leaves the direction free: a mesh can be uniformly
// inside-out, and culling that one would drop the faces you can see rather than
// the ones you can't. The signed volume settles it — positive for outward, and
// negative means every facet gets its winding reversed here, once, so the hot
// loop never has to think about it again.
func (m *Mesh) orient() {
	if len(m.Tris) < 4 || !m.closedSurface() || m.windingClosure() > 0.01 {
		return
	}
	if m.signedVolume() < 0 {
		for i := range m.Tris {
			m.Tris[i].B, m.Tris[i].C = m.Tris[i].C, m.Tris[i].B
		}
	}
	m.Closed = true
}

// closedSurface reports whether the facets form a closed surface: every edge
// travelled once in each direction. Allowing for the few that aren't — a real
// downloaded STL is routinely 99.7% manifold rather than 100%, and refusing to
// cull over a handful of bad edges in a hundred thousand would mean never
// culling anything.
//
// Counted in buckets rather than in a map of edges. An edge hashes the same
// either way round and carries the sign of the direction it was travelled, so
// summing those signed hashes per bucket leaves a bucket at zero exactly when
// everything that landed in it was matched. The number of non-zero buckets then
// estimates the number of unmatched edges — buckets are the balls-in-bins
// correction below — for a fixed few hundred KB, where a map of six million
// edges (a mesh at MaxTriangles) would be tens of megabytes and seconds.
func (m *Mesh) closedSurface() bool {
	edges := 3 * len(m.Tris)
	k := 1 << 10
	for k < edges/16 && k < 1<<16 {
		k <<= 1
	}
	buckets := make([]uint64, k)
	mask := uint64(k - 1)
	acc := func(a, b Vec3) {
		if h, sign := edgeParity(a, b); h != 0 {
			buckets[h&mask] += sign
		}
	}
	for i := range m.Tris {
		t := &m.Tris[i]
		acc(t.A, t.B)
		acc(t.B, t.C)
		acc(t.C, t.A)
	}
	var hit int
	for _, v := range buckets {
		if v != 0 {
			hit++
		}
	}
	if hit == 0 {
		return true
	}
	if hit >= k {
		return false // saturated: nowhere near closed
	}
	// hit buckets were touched by at least one unmatched edge; invert the
	// balls-in-bins expectation to get back to how many edges that was.
	bad := -float64(k) * math.Log(1-float64(hit)/float64(k))
	return bad <= float64(edges/2)/100 // ≤1% of the surface's edges
}

// edgeParity hashes an edge irrespective of which way it is travelled, and
// returns that hash together with a value that negates with the direction —
// backwards being decided by the two vertex hashes, an order both facets sharing
// the edge agree on. A degenerate edge (both ends the same vertex) has no
// opposite to cancel against and is skipped; real files do contain them, and
// they rasterize to nothing anyway.
func edgeParity(a, b Vec3) (h, sign uint64) {
	ha, hb := hashVec(a), hashVec(b)
	if ha == hb {
		return 0, 0
	}
	lo, hi := ha, hb
	if lo > hi {
		lo, hi = hi, lo
	}
	h = mix64(lo^mix64(hi)) | 1 // never zero: that is the "skip me" signal
	if ha < hb {
		return h, h
	}
	return h, -h
}

// hashVec hashes a vertex by its exact bits. Exact rather than quantised on
// purpose: two facets share a vertex only if the file spells it identically, and
// rounding to make near-misses match would be a guess about tolerance that a
// hole in the render would then pay for.
func hashVec(v Vec3) uint64 {
	h := uint64(math.Float32bits(v.X))
	h = h*0x9E3779B97F4A7C15 ^ uint64(math.Float32bits(v.Y))
	h = h*0x9E3779B97F4A7C15 ^ uint64(math.Float32bits(v.Z))
	return mix64(h)
}

// mix64 is splitmix64's finalizer: the avalanche that makes comparing two hashes
// a fair coin toss, which edgeParity's sign depends on.
func mix64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 27
	x *= 0x94D049BB133111EB
	x ^= x >> 31
	return x
}

// windingClosure is what closedSurface can't see: a patch of facets flipped as a
// block. Only the patch's *boundary* edges come out unmatched, so a big enough
// flipped region hides inside any edge tolerance — and it is the one defect that
// would be glaring, since culling makes the whole patch see-through.
//
// The area-weighted normals of a closed, consistently wound surface sum to zero.
// Flip a patch and they instead sum to twice that patch's own area vector, so
// this returns the fraction of the total area that is pointing the wrong way,
// give or take. Near zero for a clean mesh, and for scattered single-facet flips
// it stays near zero too — which is exactly the case closedSurface does catch.
func (m *Mesh) windingClosure() float64 {
	var sum Vec3
	var area float64
	for i := range m.Tris {
		t := &m.Tris[i]
		c := t.B.sub(t.A).cross(t.C.sub(t.A))
		sum.X, sum.Y, sum.Z = sum.X+c.X, sum.Y+c.Y, sum.Z+c.Z
		area += math.Sqrt(float64(c.X*c.X + c.Y*c.Y + c.Z*c.Z))
	}
	if area == 0 {
		return 1
	}
	return math.Sqrt(float64(sum.X*sum.X+sum.Y*sum.Y+sum.Z*sum.Z)) / area
}

// signedVolume is six times the volume the facets enclose, positive when they
// are wound outward. Computed about the centre of the bounding box rather than
// the origin so a part modelled a long way off it doesn't lose the answer in
// float cancellation.
func (m *Mesh) signedVolume() float64 {
	c := m.Center()
	var v float64
	for i := range m.Tris {
		t := &m.Tris[i]
		a, b, cc := t.A.sub(c), t.B.sub(c), t.C.sub(c)
		v += float64(a.dot(b.cross(cc)))
	}
	return v
}

func decodeBinary(b []byte) (*Mesh, error) {
	n, avail, ok := binaryClaim(b)
	if !ok {
		return nil, ErrNotSTL
	}
	// Trust the count only as far as the bytes actually present, so a truncated
	// download yields the facets it does hold instead of reading off the end.
	n = min(n, avail)
	m := &Mesh{Tris: make([]Triangle, n)}
	for i := range n {
		f := b[binaryHeader+i*binaryFacet:]
		// f[0:12] is the stored normal, deliberately skipped — see Triangle.
		m.Tris[i] = Triangle{
			A: vecAt(f[12:]),
			B: vecAt(f[24:]),
			C: vecAt(f[36:]),
		}
	}
	return m, nil
}

func vecAt(b []byte) Vec3 {
	return Vec3{
		math.Float32frombits(binary.LittleEndian.Uint32(b[0:4])),
		math.Float32frombits(binary.LittleEndian.Uint32(b[4:8])),
		math.Float32frombits(binary.LittleEndian.Uint32(b[8:12])),
	}
}

// asciiStart returns the index of the "solid" keyword that opens an ASCII STL,
// or -1. Leading whitespace is skipped; anything else (a BOM, a stray byte)
// means this isn't one.
func asciiStart(b []byte) int {
	i := 0
	for i < len(b) && isSpace(b[i]) {
		i++
	}
	if i+5 > len(b) {
		return -1
	}
	if b[i] != 's' && b[i] != 'S' {
		return -1
	}
	if !equalFold(b[i:i+5], "solid") {
		return -1
	}
	return i
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func equalFold(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range b {
		c := b[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != s[i] {
			return false
		}
	}
	return true
}

// decodeASCII parses the textual form. It scans for `vertex` keywords and takes
// every three it finds as a facet, ignoring the surrounding
// solid/facet/loop/endloop scaffolding entirely — that structure carries no
// information the vertices don't, and half-broken files (a missing `endloop`, a
// facet split across a concatenated multi-solid file) are common enough that
// insisting on it only loses meshes we could have drawn.
//
// The tokenizer hands back subslices rather than strings so a large ASCII file
// doesn't allocate per token.
func decodeASCII(b []byte) (*Mesh, error) {
	m := &Mesh{}
	var pend [3]Vec3
	var have int
	t := tokenizer{b: b}
	for {
		tok, ok := t.next()
		if !ok {
			break
		}
		if !equalFold(tok, "vertex") {
			continue
		}
		var v Vec3
		var bad bool
		for c := range 3 {
			num, ok := t.next()
			if !ok {
				bad = true
				break
			}
			f, err := parseFloat(num)
			if err != nil {
				bad = true
				break
			}
			switch c {
			case 0:
				v.X = f
			case 1:
				v.Y = f
			case 2:
				v.Z = f
			}
		}
		if bad {
			// A malformed vertex abandons the facet it belonged to rather than
			// shifting every later vertex into the wrong slot.
			have = 0
			continue
		}
		pend[have] = v
		have++
		if have == 3 {
			if len(m.Tris) >= MaxTriangles {
				return nil, fmt.Errorf("%w: over %d facets", ErrTooLarge, MaxTriangles)
			}
			m.Tris = append(m.Tris, Triangle{pend[0], pend[1], pend[2]})
			have = 0
		}
	}
	return m, nil
}

// tokenizer splits on ASCII whitespace, returning subslices of the input.
type tokenizer struct {
	b []byte
	i int
}

func (t *tokenizer) next() ([]byte, bool) {
	for t.i < len(t.b) && isSpace(t.b[t.i]) {
		t.i++
	}
	if t.i >= len(t.b) {
		return nil, false
	}
	start := t.i
	for t.i < len(t.b) && !isSpace(t.b[t.i]) {
		t.i++
	}
	return t.b[start:t.i], true
}

// parseFloat reads one STL coordinate. strconv handles both the plain decimal
// and exponent forms STL writers emit; the string conversion allocates, which is
// fine because only the (rare, and small) ASCII form reaches here — the binary
// path never parses text at all.
func parseFloat(b []byte) (float32, error) {
	f, err := strconv.ParseFloat(string(b), 32)
	return float32(f), err
}
