package gotreesitter

import (
	"fmt"
	"sort"
)

// Rewriter collects source-text edits and applies them atomically.
// Edits target byte ranges (usually from Node.StartByte/EndByte).
// Apply returns new source bytes and InputEdit records for incremental reparsing.
type Rewriter struct {
	source []byte
	edits  []rewriteEdit
}

type rewriteEdit struct {
	startByte uint32
	endByte   uint32
	newText   []byte
}

// NewRewriter creates a Rewriter for the given source text.
func NewRewriter(source []byte) *Rewriter {
	return &Rewriter{source: source}
}

// Replace replaces the source text covered by node with newText.
func (r *Rewriter) Replace(node *Node, newText []byte) {
	r.edits = append(r.edits, rewriteEdit{
		startByte: node.StartByte(),
		endByte:   node.EndByte(),
		newText:   newText,
	})
}

// ReplaceRange replaces bytes in [startByte, endByte) with newText.
func (r *Rewriter) ReplaceRange(startByte, endByte uint32, newText []byte) {
	r.edits = append(r.edits, rewriteEdit{
		startByte: startByte,
		endByte:   endByte,
		newText:   newText,
	})
}

// InsertBefore inserts text immediately before node.
func (r *Rewriter) InsertBefore(node *Node, text []byte) {
	pos := node.StartByte()
	r.edits = append(r.edits, rewriteEdit{
		startByte: pos,
		endByte:   pos,
		newText:   text,
	})
}

// InsertAfter inserts text immediately after node.
func (r *Rewriter) InsertAfter(node *Node, text []byte) {
	pos := node.EndByte()
	r.edits = append(r.edits, rewriteEdit{
		startByte: pos,
		endByte:   pos,
		newText:   text,
	})
}

// Delete removes the source text covered by node.
func (r *Rewriter) Delete(node *Node) {
	r.edits = append(r.edits, rewriteEdit{
		startByte: node.StartByte(),
		endByte:   node.EndByte(),
		newText:   nil,
	})
}

// Apply sorts edits, validates no overlaps, applies them, and returns the
// new source bytes plus InputEdit records for incremental reparsing.
// Returns error if edits overlap.
func (r *Rewriter) Apply() (newSource []byte, edits []InputEdit, err error) {
	if len(r.edits) == 0 {
		out := make([]byte, len(r.source))
		copy(out, r.source)
		return out, nil, nil
	}

	// Sort by startByte, then by endByte for stability.
	sorted := make([]rewriteEdit, len(r.edits))
	copy(sorted, r.edits)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].startByte != sorted[j].startByte {
			return sorted[i].startByte < sorted[j].startByte
		}
		return sorted[i].endByte < sorted[j].endByte
	})

	// Validate no overlaps: edit N's endByte <= edit N+1's startByte.
	// Zero-width insertions at the same point are allowed only if they don't
	// also overlap with a ranged edit.
	for i := 1; i < len(sorted); i++ {
		prev := sorted[i-1]
		cur := sorted[i]
		if prev.endByte > cur.startByte {
			return nil, nil, fmt.Errorf("rewrite: overlapping edits at bytes [%d,%d) and [%d,%d)",
				prev.startByte, prev.endByte, cur.startByte, cur.endByte)
		}
		// Two zero-width insertions at the same point overlap.
		if prev.startByte == prev.endByte && cur.startByte == cur.endByte &&
			prev.startByte == cur.startByte {
			return nil, nil, fmt.Errorf("rewrite: overlapping insertions at byte %d", prev.startByte)
		}
	}

	// Build new source and InputEdit records.
	edits = make([]InputEdit, 0, len(sorted))
	var buf []byte
	pos := uint32(0)
	delta := int64(0) // cumulative byte offset shift

	for _, e := range sorted {
		// Copy unchanged bytes before this edit.
		if e.startByte > pos {
			buf = append(buf, r.source[pos:e.startByte]...)
		}

		// Compute InputEdit.
		startPoint := r.byteToPoint(e.startByte)
		oldEndPoint := r.byteToPoint(e.endByte)
		newEndByte := uint32(int64(e.startByte) + delta + int64(len(e.newText)))
		newEndPoint := computeNewEndPoint(startPoint, e.newText)

		edits = append(edits, InputEdit{
			StartByte:   uint32(int64(e.startByte) + delta),
			OldEndByte:  uint32(int64(e.endByte) + delta),
			NewEndByte:  newEndByte,
			StartPoint:  startPoint,
			OldEndPoint: oldEndPoint,
			NewEndPoint: newEndPoint,
		})

		// Apply the edit.
		buf = append(buf, e.newText...)
		delta += int64(len(e.newText)) - int64(e.endByte-e.startByte)
		pos = e.endByte
	}

	// Copy remaining bytes after last edit.
	if pos < uint32(len(r.source)) {
		buf = append(buf, r.source[pos:]...)
	}

	return buf, edits, nil
}

// ApplyToTree is a convenience that calls Apply(), then tree.Edit() for each
// edit, returning the new source ready for ParseIncremental.
func (r *Rewriter) ApplyToTree(tree *Tree) ([]byte, error) {
	newSource, edits, err := r.Apply()
	if err != nil {
		return nil, err
	}
	for _, e := range edits {
		tree.Edit(e)
	}
	return newSource, nil
}

// byteToPoint scans source to compute the row/col Point for a byte offset.
func (r *Rewriter) byteToPoint(offset uint32) Point {
	if offset == 0 {
		return Point{Row: 0, Column: 0}
	}
	if int(offset) > len(r.source) {
		offset = uint32(len(r.source))
	}
	row := uint32(0)
	col := uint32(0)
	for i := uint32(0); i < offset; i++ {
		if r.source[i] == '\n' {
			row++
			col = 0
		} else {
			col++
		}
	}
	return Point{Row: row, Column: col}
}

// computeNewEndPoint computes the endpoint after inserting newText starting at startPoint.
func computeNewEndPoint(startPoint Point, newText []byte) Point {
	row := startPoint.Row
	col := startPoint.Column
	for _, b := range newText {
		if b == '\n' {
			row++
			col = 0
		} else {
			col++
		}
	}
	return Point{Row: row, Column: col}
}
