# oksvg (vendored)

A copy of [srwiley/oksvg](https://github.com/srwiley/oksvg) at
`v0.0.0-20221011165216-be6e8873101c`, BSD-3-Clause (see LICENSE — the copyright
is Steven R Wiley's and stays with it).

Vendored rather than imported because upstream stopped taking changes in 2022 and
three of its bugs are visible on ordinary files. Every edit is marked with a
`matterbox:` comment:

| file | fix |
| --- | --- |
| `path_cursor.go` | `AddArcFromA` read the command's *first* parameter set for every arc in it, so a single `a` carrying several arcs — what every SVG optimiser emits — drew the first arc's radii repeatedly. |
| `icon_cursor.go` | `scale(s)` was read as `scale(s, 0)`, flattening the Y axis to nothing. |
| `svg_path.go` | Stroke widths were passed to the rasteriser unscaled while path coordinates went through the transform, so a stroke inside a scaled group came out at its unscaled width. Adds `strokeScale`. |
| `path_cursor.go` | An arc's two flags are single digits that may be written with no separator (`a7 7 0 100 14` is flags 1, 0 then x=0); the general number scanner read `100` as one number and the arc was dropped. Adds `getArcPoints`. |

Measured against librsvg on the Ghostscript tiger (nested `translate → matrix →
scale`, 305 stroked paths), the three together move ink coverage from +14.3
percentage points at 200px to +0.3, and hold within half a point at every size.

Three of the four were previously worked around by rewriting path and transform
attributes before parsing. Those passes are gone — about 150 lines, plus ~9ms and
1MB of regex rewriting on every decode of a large drawing — which is the other
reason to own this. The stroke bug could not be worked around from outside at
all: the accumulated matrix is unexported.

## Updating

There is nothing upstream to merge. Treat this as ours: keep the `matterbox:`
markers so the diff against the original stays legible, and add tests to
`internal/svgimg` rather than here — nothing in this directory has tests of its
own, by design, so the package stays a faithful copy plus marked edits.
