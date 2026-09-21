package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"matterbox/internal/forge"
)

// The file tree down the left of the diff review view: every changed file in
// the change request, nested under its directories, with what happened to it.
//
// A review is two questions — "what changed overall" and "what does this line
// do" — and a single scrolling diff only answers the second. The tree answers
// the first, and doubles as the way to get to a file without scrolling past the
// ones before it.
//
// The two panels are one selection seen twice: moving in the tree moves the
// diff to that file, and scrolling the diff moves the tree's highlight to the
// file you are in. fileHead and treeOf (built with the rows, see diffBuild) are
// the two directions of that mapping.

// diffTreeRow is one row of the tree: a directory header, or a file with its
// counts. Directory rows are not selectable — there is nothing to show for one,
// and skipping them makes ↑/↓ step file to file.
type diffTreeRow struct {
	depth int
	label string // "internal/" for a directory, "diffview.go" for a file
	file  int    // index into diff.Files, -1 for a directory
	diffFileStat
}

// diffFileStat is what the tree says about a file: how much it changed and how
// much has been said about it.
type diffFileStat struct {
	adds     int
	dels     int
	threads  int // conversations still open
	resolved int // conversations already answered
}

const (
	// diffTreeMinWidth / diffTreeMaxWidth bound the tree column. Below the
	// minimum the panel shows nothing but truncated names, so the view drops it
	// (see diffTreeWidth) rather than squeeze both panels.
	diffTreeMinWidth = 18
	diffTreeMaxWidth = 40
	// diffPanesMinWidth is the narrowest view that gets a tree at all. Under it
	// the diff takes the whole frame, and tab has nothing to switch to.
	diffPanesMinWidth = 64
	// diffTreeIndent is how far one directory level indents its children.
	diffTreeIndent = 2
)

var (
	diffTreeDirStyle  = lipgloss.NewStyle().Foreground(dimColor).Bold(true)
	diffTreeFileStyle = lipgloss.NewStyle()
	// A folded file is dimmed in the tree, so "I have dealt with that one"
	// reads at a glance.
	diffTreeFoldedStyle = lipgloss.NewStyle().Foreground(dimColor)
	diffTreeAddStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	diffTreeDelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	diffTreeSelStyle    = lipgloss.NewStyle().Background(diffCursorBg)
	// diffTreeDivider separates the two panels. Dim: it is a seam, not a border.
	diffTreeDivider = lipgloss.NewStyle().Foreground(dimColor).Render("│")
)

// buildDiffTree lays the changed files out as a tree, in the order the forge
// listed them, and reports which tree row each file landed on.
//
// Directory rows are emitted when the path prefix changes, one row per
// component — no folding of single-child chains. A Go path nests three or four
// deep and the indent costs six spaces; folding would save those at the price of
// a second, cleverer notion of depth, and the horizontal pan already covers the
// deep cases.
func buildDiffTree(d *forge.Diff, stats []diffFileStat) ([]diffTreeRow, []int) {
	if d == nil {
		return nil, nil
	}
	var rows []diffTreeRow
	treeOf := make([]int, len(d.Files))
	var prev []string
	for fi, f := range d.Files {
		parts := strings.Split(f.Path(), "/")
		dirs, base := parts[:len(parts)-1], parts[len(parts)-1]
		common := 0
		for common < len(dirs) && common < len(prev) && dirs[common] == prev[common] {
			common++
		}
		for i := common; i < len(dirs); i++ {
			rows = append(rows, diffTreeRow{depth: i, label: dirs[i] + "/", file: -1})
		}
		treeOf[fi] = len(rows)
		row := diffTreeRow{depth: len(dirs), label: base, file: fi}
		if fi < len(stats) {
			row.diffFileStat = stats[fi]
		}
		rows = append(rows, row)
		prev = dirs
	}
	return rows, treeOf
}

// diffTreeWidth is how wide the tree column is for a view of the given inner
// width, or 0 when the view is too narrow to carry one.
func diffTreeWidth(inner int) int {
	if inner < diffPanesMinWidth {
		return 0
	}
	w := inner / 4
	if w < diffTreeMinWidth {
		w = diffTreeMinWidth
	}
	if w > diffTreeMaxWidth {
		w = diffTreeMaxWidth
	}
	return w
}

// diffTreeShown reports whether the tree is on screen — what decides whether
// tab has anywhere to go. Computed from the terminal width rather than from the
// last frame's geometry, because the footer is rendered before the body: asking
// the frame would answer for the frame before this one.
func (m *Model) diffTreeShown() bool {
	if m.diff == nil || len(m.diff.tree) == 0 {
		return false
	}
	return diffTreeWidth(max(1, max(1, m.width-2)-4)) > 0
}

// --- moving ----------------------------------------------------------------

// treeSelect puts the tree's highlight on a row and scrolls the tree to it.
func (d *diffState) treeSelect(i int) {
	if len(d.tree) == 0 {
		return
	}
	d.treeIdx = min(max(i, 0), len(d.tree)-1)
	d.clampTree()
}

// clampTree keeps the tree's window around its highlight, the same way
// clampCursor does for the diff.
func (d *diffState) clampTree() {
	h := d.treeH
	if h <= 0 || len(d.tree) == 0 {
		return
	}
	if d.treeIdx < d.treeTop+diffScrollOff {
		d.treeTop = d.treeIdx - diffScrollOff
	}
	if d.treeIdx > d.treeTop+h-1-diffScrollOff {
		d.treeTop = d.treeIdx - h + 1 + diffScrollOff
	}
	d.treeTop = min(max(d.treeTop, 0), max(0, len(d.tree)-h))
}

// treeMove steps the highlight by n *files* — directory rows are stepped over,
// so ↑/↓ walk the files rather than stopping on headers nothing can be done
// with. The diff follows to the file that lands under it.
func (d *diffState) treeMove(n int) {
	if len(d.tree) == 0 {
		return
	}
	step := 1
	if n < 0 {
		step, n = -1, -n
	}
	i := d.treeIdx
	for ; n > 0; n-- {
		next := i
		for j := i + step; j >= 0 && j < len(d.tree); j += step {
			if d.tree[j].file >= 0 {
				next = j
				break
			}
		}
		if next == i {
			break // no further file in that direction
		}
		i = next
	}
	d.treeSelect(i)
	d.openTreeFile()
}

// treeHome / treeEnd jump to the first and last file.
func (d *diffState) treeHome() {
	for i, r := range d.tree {
		if r.file >= 0 {
			d.treeSelect(i)
			d.treeTop = 0
			d.openTreeFile()
			return
		}
	}
}

func (d *diffState) treeEnd() {
	for i := len(d.tree) - 1; i >= 0; i-- {
		if d.tree[i].file >= 0 {
			d.treeSelect(i)
			d.openTreeFile()
			return
		}
	}
}

// openTreeFile scrolls the diff panel to the file the tree is pointing at, with
// its header at the top of the window — the same landing ]/[ gives.
func (d *diffState) openTreeFile() {
	if d.treeIdx < 0 || d.treeIdx >= len(d.tree) {
		return
	}
	fi := d.tree[d.treeIdx].file
	if fi < 0 || fi >= len(d.fileHead) {
		return
	}
	d.cursor = d.fileHead[fi]
	d.top = d.cursor
	d.clampCursor()
}

// syncTreeToCursor moves the tree's highlight to the file the diff cursor is
// in. Called after every move in the code panel, so the tree always says where
// you are.
func (d *diffState) syncTreeToCursor() {
	if d.cursor < 0 || d.cursor >= len(d.rows) {
		return
	}
	fi := d.rows[d.cursor].file
	if fi < 0 || fi >= len(d.treeOf) {
		return
	}
	d.treeSelect(d.treeOf[fi])
}

// --- rendering -------------------------------------------------------------

// renderTreeColumn draws h rows of the tree, each exactly w cells wide.
func (d *diffState) renderTreeColumn(w, h int) []string {
	d.treeH = h
	d.clampTree()
	out := make([]string, 0, h)
	for i := d.treeTop; i < len(d.tree) && i < d.treeTop+h; i++ {
		out = append(out, d.renderTreeRow(i, w))
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	return out
}

// renderTreeRow draws one row: the indent, the name, and — when the row is a
// file and there is room — its +/- counts and a marker for the conversations
// already on it.
func (d *diffState) renderTreeRow(i, w int) string {
	r := d.tree[i]
	indent := strings.Repeat(" ", min(r.depth*diffTreeIndent, max(0, w-1)))
	var name string
	switch {
	case r.file < 0:
		name = diffPiece(diffTreeDirStyle, r.label)
	case r.file < len(d.collapsed) && d.collapsed[r.file]:
		name = diffPiece(diffTreeFoldedStyle, r.label)
	default:
		name = diffPiece(diffTreeFileStyle, r.label)
	}
	left := indent + name
	body := left
	switch tail := treeRowTail(r); {
	case d.treeHScroll > 0:
		// Panning is for reading a path the column can't hold, so the counts
		// stand down and the whole width goes to the name.
		body = ansi.Cut(left, d.treeHScroll, d.treeHScroll+w)
	case tail != "":
		// The counts sit against the right edge, one cell clear of the divider,
		// and the name gives way to them: "+2 -1" is the column's reason to
		// exist, and ←/→ read back whatever the name lost.
		nameW := w - 1 - visualWidth(tail) - 1
		if nameW >= 8 {
			left = ansi.Truncate(left, nameW, "…")
			body = left + strings.Repeat(" ", w-1-visualWidth(left)-visualWidth(tail)) + tail
		} else {
			body = ansi.Truncate(left, w, "…")
		}
	default:
		body = ansi.Truncate(left, w, "…")
	}
	body = keepBG(body)
	if pad := w - visualWidth(body); pad > 0 {
		body += strings.Repeat(" ", pad)
	}
	if i != d.treeIdx || r.file < 0 {
		return body
	}
	// The highlight is bright while the tree has the keys and quiet while the
	// diff does, so which panel a keystroke lands in is never a guess.
	if d.treeFocus {
		return selectedRow.Render(body)
	}
	return diffTreeSelStyle.Render(body)
}

// treeRowTail is a file row's right-hand side: its conversation count, then its
// added/removed line counts.
func treeRowTail(r diffTreeRow) string {
	if r.file < 0 {
		return ""
	}
	var parts []string
	switch {
	case r.threads > 0:
		parts = append(parts, diffPiece(diffNoteHead, fmt.Sprintf("💬%d", r.threads)))
	case r.resolved > 0:
		// Everything said about this file has been answered — worth a mark, and
		// worth not being the same mark as "three people are waiting on you".
		parts = append(parts, diffPiece(diffTreeAddStyle, "✓"))
	}
	if r.adds > 0 {
		parts = append(parts, diffPiece(diffTreeAddStyle, fmt.Sprintf("+%d", r.adds)))
	}
	if r.dels > 0 {
		parts = append(parts, diffPiece(diffTreeDelStyle, fmt.Sprintf("-%d", r.dels)))
	}
	return strings.Join(parts, " ")
}
