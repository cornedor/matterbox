package vapor

import "math"

func min3(a, b, c float64) float64 { return math.Min(a, math.Min(b, c)) }
func max3(a, b, c float64) float64 { return math.Max(a, math.Max(b, c)) }

// fillTri rasterizes terrain triangle (a,b,c) — vertex indices into the
// projected arrays — as a flat, depth-shaded, fog-faded facet with z-testing.
func (s *Scene) fillTri(a, b, c int) {
	// world-space normal for flat shading
	ux, uy, uz := s.wpx[b]-s.wpx[a], s.wpy[b]-s.wpy[a], s.wpz[b]-s.wpz[a]
	vx, vy, vz := s.wpx[c]-s.wpx[a], s.wpy[c]-s.wpy[a], s.wpz[c]-s.wpz[a]
	nx := uy*vz - uz*vy
	ny := uz*vx - ux*vz
	nz := ux*vy - uy*vx
	nl := math.Sqrt(nx*nx + ny*ny + nz*nz)
	shade := 0.3
	if nl > 0 {
		d := (nx*s.lx + ny*s.ly + nz*s.lz) / nl
		if d < 0 {
			d = -d // light both facings
		}
		shade = 0.28 + 0.72*d
	}

	avgH := (s.wpy[a] + s.wpy[b] + s.wpy[c]) / 3
	hNorm := 0.0
	if s.maxH > 0 {
		hNorm = avgH / s.maxH
	}
	col := scale(lerp(faceLow, faceHigh, clamp01(hNorm)), shade)

	avgDepth := (s.dep[a] + s.dep[b] + s.dep[c]) / 3
	fog := clamp01((avgDepth - s.fogStart) / (s.fogEnd - s.fogStart))

	// fog + the per-pixel sun reflection are applied inside rasterFill so the
	// reflection can be added before fog fades it into the horizon.
	s.rasterFill(a, b, c, col, fog)
}

// rasterFill fills the triangle (a,b,c) — vertex indices — with a flat diffuse
// base colour, depth-tested, plus a per-pixel Blinn-Phong specular reflection of
// the sun driven by the interpolated heightfield normal. The reflection tracks
// the bumpy floor: facets facing the sun glint where the reflected ray aligns
// with the view, breaking up with the terrain. fog fades base + reflection into
// the horizon.
func (s *Scene) rasterFill(a, b, c int, baseCol RGB, fog float64) {
	W, H := s.W, s.H
	x0, y0, i0 := s.sx[a], s.sy[a], s.inv[a]
	x1, y1, i1 := s.sx[b], s.sy[b], s.inv[b]
	x2, y2, i2 := s.sx[c], s.sy[c], s.inv[c]
	minX := int(math.Floor(min3(x0, x1, x2)))
	maxX := int(math.Ceil(max3(x0, x1, x2)))
	minY := int(math.Floor(min3(y0, y1, y2)))
	maxY := int(math.Ceil(max3(y0, y1, y2)))
	if minX < 0 {
		minX = 0
	}
	if minY < 0 {
		minY = 0
	}
	if maxX > W-1 {
		maxX = W - 1
	}
	if maxY > H-1 {
		maxY = H - 1
	}
	if minX > maxX || minY > maxY {
		return
	}

	det := (y1-y2)*(x0-x2) + (x2-x1)*(y0-y2)
	if math.Abs(det) < 1e-9 {
		return
	}
	inv := 1.0 / det

	// per-vertex world position & heightfield normal for the reflection.
	p0x, p0y, p0z := s.wpx[a], s.wpy[a], s.wpz[a]
	p1x, p1y, p1z := s.wpx[b], s.wpy[b], s.wpz[b]
	p2x, p2y, p2z := s.wpx[c], s.wpy[c], s.wpz[c]
	n0x, n0y, n0z := s.wnx[a], s.wny[a], s.wnz[a]
	n1x, n1y, n1z := s.wnx[b], s.wny[b], s.wnz[b]
	n2x, n2y, n2z := s.wnx[c], s.wny[c], s.wnz[c]

	camY, camZ := s.camY, s.camZ
	sdx, sdy, sdz := s.sunDirX, s.sunDirY, s.sunDirZ
	stops := s.sunStops

	for py := minY; py <= maxY; py++ {
		fy := float64(py) + 0.5
		row := py * W
		for px := minX; px <= maxX; px++ {
			fx := float64(px) + 0.5
			w0 := ((y1-y2)*(fx-x2) + (x2-x1)*(fy-y2)) * inv
			w1 := ((y2-y0)*(fx-x2) + (x0-x2)*(fy-y2)) * inv
			w2 := 1 - w0 - w1
			if w0 < 0 || w1 < 0 || w2 < 0 {
				continue
			}
			z := w0*i0 + w1*i1 + w2*i2
			if z <= s.zbuf[row+px] {
				continue
			}
			col := baseCol
			invz := 1.0 / z
			wa, wb, wc := w0*i0, w1*i1, w2*i2
			// perspective-correct interpolated world position & normal
			wx := (wa*p0x + wb*p1x + wc*p2x) * invz
			wy := (wa*p0y + wb*p1y + wc*p2y) * invz
			wz := (wa*p0z + wb*p1z + wc*p2z) * invz
			nx := (wa*n0x + wb*n1x + wc*n2x) * invz
			ny := (wa*n0y + wb*n1y + wc*n2y) * invz
			nz := (wa*n0z + wb*n1z + wc*n2z) * invz
			nl := math.Sqrt(nx*nx + ny*ny + nz*nz)
			if nl > 0 {
				nx, ny, nz = nx/nl, ny/nl, nz/nl
				ndotl := nx*sdx + ny*sdy + nz*sdz
				if ndotl > 0 { // surface faces the sun
					vx, vy, vz := -wx, camY-wy, camZ-wz // view dir (surface -> camera)
					vl := math.Sqrt(vx*vx + vy*vy + vz*vz)
					if vl > 0 {
						vx, vy, vz = vx/vl, vy/vl, vz/vl
						// Phong: reflect the sun dir about N, dot with V.
						r2 := 2 * ndotl
						rdotv := (r2*nx-sdx)*vx + (r2*ny-sdy)*vy + (r2*nz-sdz)*vz
						if rdotv > 0 {
							// fast pow: rdotv^32 via repeated squaring (5 squares).
							x := rdotv
							x *= x // ^2
							x *= x // ^4
							x *= x // ^8
							x *= x // ^16
							x *= x // ^32
							spec := x * reflStrength
							// map the sun gradient by camera depth: near floor
							// reflects the top of the sun, far floor the bottom,
							// then fog fades it into the horizon below.
							gt := clamp01((wz - camZ - 4) / 22)
							sc := gradientAt(stops, gt)
							col = RGB{
								col.R + spec*sc.R,
								col.G + spec*sc.G,
								col.B + spec*sc.B,
							}
						}
					}
				}
			}
			col = lerp(col, fogColor, fog)
			s.zbuf[row+px] = z
			s.buf[row+px] = col
		}
	}
}

// drawEdge draws a wireframe grid line between two vertices, depth-tested with a
// small bias so the line wins against the facets it borders, and tinted magenta
// down the centre of the valley (the sun's reflection) fading to cyan outward.
func (s *Scene) drawEdge(a, b int) {
	avgDepth := (s.dep[a] + s.dep[b]) / 2
	fog := clamp01((avgDepth - s.fogStart) / (s.fogEnd - s.fogStart))

	wxMid := (s.wpx[a] + s.wpx[b]) / 2
	mid := math.Exp(-(wxMid*wxMid)/(s.xSpan*s.xSpan*0.10)) * (1 - fog)
	col := lerp(gridCyan, gridMag, clamp01(mid*0.3))
	col = lerp(col, fogColor, fog*0.9)

	s.drawLine(s.sx[a], s.sy[a], s.inv[a], s.sx[b], s.sy[b], s.inv[b], col)
}

func (s *Scene) drawLine(x0, y0, i0, x1, y1, i1 float64, col RGB) {
	cx0, cy0, cx1, cy1, t0, t1, ok := clipLB(x0, y0, x1, y1, float64(s.W-1), float64(s.H-1))
	if !ok {
		return
	}
	ci0 := i0 + t0*(i1-i0)
	ci1 := i0 + t1*(i1-i0)

	dx := cx1 - cx0
	dy := cy1 - cy0
	steps := int(math.Max(math.Abs(dx), math.Abs(dy))) + 1
	W := s.W
	for k := 0; k <= steps; k++ {
		t := float64(k) / float64(steps)
		px := int(cx0 + dx*t + 0.5)
		py := int(cy0 + dy*t + 0.5)
		if px < 0 || px >= W || py < 0 || py >= s.H {
			continue
		}
		z := ci0 + (ci1-ci0)*t
		idx := py*W + px
		if z*1.03 >= s.zbuf[idx] {
			s.zbuf[idx] = z
			s.buf[idx] = col
		}
	}
}

// clipLB is Liang–Barsky clipping of a segment to [0,maxX]x[0,maxY], returning
// the clipped endpoints and the parameter range [t0,t1] along the original line.
func clipLB(x0, y0, x1, y1, maxX, maxY float64) (nx0, ny0, nx1, ny1, t0, t1 float64, ok bool) {
	dx := x1 - x0
	dy := y1 - y0
	t0, t1 = 0, 1
	p := [4]float64{-dx, dx, -dy, dy}
	q := [4]float64{x0, maxX - x0, y0, maxY - y0}
	for i := 0; i < 4; i++ {
		if p[i] == 0 {
			if q[i] < 0 {
				return
			}
			continue
		}
		r := q[i] / p[i]
		if p[i] < 0 {
			if r > t1 {
				return
			}
			if r > t0 {
				t0 = r
			}
		} else {
			if r < t0 {
				return
			}
			if r < t1 {
				t1 = r
			}
		}
	}
	return x0 + t0*dx, y0 + t0*dy, x0 + t1*dx, y0 + t1*dy, t0, t1, true
}
