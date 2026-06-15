package layoutviewer

// A minimal line-based diff producer for emit.go's UI render. hclwrite
// preserves byte positions for unchanged content, so the actual changes
// are tiny and well-bounded — a full Myers diff would be overkill. This
// uses the standard LCS-table approach over lines, then walks the table
// to emit context + add/remove lines, packaged as unified-diff hunks.

import (
	"bytes"
	"fmt"
	"strings"
)

const diffContextLines = 3

// unifiedDiff returns the unified diff of oldText → newText. Both must end
// with newline-or-empty so line splits are predictable. The output starts
// with `--- ` / `+++ ` headers and contains `@@` hunk markers, identical to
// what `git diff --no-prefix` emits — convenient for piping into colour
// renderers.
func unifiedDiff(oldText, newText, oldLabel, newLabel string) string {
	if oldText == newText {
		return ""
	}
	oldLines := splitLinesKeep(oldText)
	newLines := splitLinesKeep(newText)

	ops := myersDiff(oldLines, newLines)
	if len(ops) == 0 {
		return ""
	}

	hunks := groupHunks(ops, diffContextLines)
	if len(hunks) == 0 {
		return ""
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, "--- %s\n", oldLabel)
	fmt.Fprintf(&b, "+++ %s\n", newLabel)

	for _, h := range hunks {
		oldCount, newCount := 0, 0
		for _, op := range h.ops {
			switch op.kind {
			case opSame:
				oldCount++
				newCount++
			case opDel:
				oldCount++
			case opIns:
				newCount++
			}
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.oldStart+1, oldCount, h.newStart+1, newCount)
		for _, op := range h.ops {
			switch op.kind {
			case opSame:
				fmt.Fprintf(&b, " %s", op.line)
			case opDel:
				fmt.Fprintf(&b, "-%s", op.line)
			case opIns:
				fmt.Fprintf(&b, "+%s", op.line)
			}
			if !strings.HasSuffix(op.line, "\n") {
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func splitLinesKeep(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			if s != "" {
				out = append(out, s)
			}
			return out
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
}

type opKind int

const (
	opSame opKind = iota
	opDel
	opIns
)

type lineOp struct {
	kind opKind
	line string
}

// myersDiff returns a list of ops describing how to turn old into new. It
// computes the LCS via the standard table — O(n*m), fine for modules.tf-
// sized inputs.
func myersDiff(oldLines, newLines []string) []lineOp {
	n, m := len(oldLines), len(newLines)
	// LCS table.
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if oldLines[i-1] == newLines[j-1] {
				lcs[i][j] = lcs[i-1][j-1] + 1
			} else if lcs[i-1][j] >= lcs[i][j-1] {
				lcs[i][j] = lcs[i-1][j]
			} else {
				lcs[i][j] = lcs[i][j-1]
			}
		}
	}
	// Walk backwards to emit ops, then reverse.
	var ops []lineOp
	i, j := n, m
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && oldLines[i-1] == newLines[j-1]:
			ops = append(ops, lineOp{kind: opSame, line: oldLines[i-1]})
			i--
			j--
		case j > 0 && (i == 0 || lcs[i][j-1] >= lcs[i-1][j]):
			ops = append(ops, lineOp{kind: opIns, line: newLines[j-1]})
			j--
		default:
			ops = append(ops, lineOp{kind: opDel, line: oldLines[i-1]})
			i--
		}
	}
	for l, r := 0, len(ops)-1; l < r; l, r = l+1, r-1 {
		ops[l], ops[r] = ops[r], ops[l]
	}
	return ops
}

type hunk struct {
	oldStart int
	newStart int
	ops      []lineOp
}

// groupHunks slices the diff into hunks separated by `>2*context` runs of
// unchanged lines. Each hunk includes `context` lines on either side of its
// changes, matching standard unified-diff conventions.
func groupHunks(ops []lineOp, context int) []hunk {
	if context < 0 {
		context = 0
	}
	// Track current old/new positions while scanning.
	type indexed struct {
		op    lineOp
		oldIx int
		newIx int
	}
	indexed_ := make([]indexed, len(ops))
	oi, ni := 0, 0
	hasChange := false
	for k, op := range ops {
		indexed_[k] = indexed{op: op, oldIx: oi, newIx: ni}
		switch op.kind {
		case opSame:
			oi++
			ni++
		case opDel:
			oi++
			hasChange = true
		case opIns:
			ni++
			hasChange = true
		}
	}
	if !hasChange {
		return nil
	}

	var hunks []hunk
	k := 0
	for k < len(indexed_) {
		// Skip context-sized "all same" runs at the front.
		if indexed_[k].op.kind == opSame {
			// Find next change.
			next := k
			for next < len(indexed_) && indexed_[next].op.kind == opSame {
				next++
			}
			if next >= len(indexed_) {
				break
			}
			// Include up to `context` lines before the change.
			start := next - context
			if start < k {
				start = k
			}
			k = start
		}
		// Start a hunk here.
		h := hunk{oldStart: indexed_[k].oldIx, newStart: indexed_[k].newIx}
		// Extend while we're seeing changes or short runs of context.
		sameRun := 0
		for k < len(indexed_) {
			op := indexed_[k]
			h.ops = append(h.ops, op.op)
			if op.op.kind == opSame {
				sameRun++
				if sameRun > 2*context {
					// Pulled too far — trim back `context` lines and end hunk.
					h.ops = h.ops[:len(h.ops)-(sameRun-context)]
					k -= (sameRun - context)
					sameRun = 0
					break
				}
			} else {
				sameRun = 0
			}
			k++
		}
		hunks = append(hunks, h)
	}
	return hunks
}
