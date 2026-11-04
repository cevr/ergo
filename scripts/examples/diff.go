package main

import (
	"bytes"
	"fmt"
	"strings"
)

// diffContext is the number of unchanged lines a hunk shows around a change.
const diffContext = 3

// maxDiffCells bounds the table of the line diff; past it the diff shows
// both texts whole, one hunk.
const maxDiffCells = 16 << 20

// unifiedDiff is a unified line diff from want to got, with the headers
// "--- want" and "+++ got". A last line without a newline is marked as diff
// marks it, so a missing final newline shows.
func unifiedDiff(want, got []byte) string {
	a, b := splitLines(want), splitLines(got)
	ops := lineOps(a, b)
	var out strings.Builder
	out.WriteString("--- want\n+++ got\n")
	for start := 0; start < len(ops); {
		first := start
		for first < len(ops) && ops[first].kind == ' ' {
			first++
		}
		if first == len(ops) {
			break
		}
		// The hunk runs from the context before the first change to the
		// context after the last change that is no more than two contexts
		// away from the next.
		last := first
		for i := first; i < len(ops); i++ {
			if ops[i].kind != ' ' {
				if i-last > 2*diffContext {
					break
				}
				last = i
			}
		}
		from := max(start, first-diffContext)
		to := min(len(ops), last+diffContext+1)
		writeHunk(&out, ops[from:to])
		start = to
	}
	return out.String()
}

type lineOp struct {
	kind byte // ' ', '-' or '+'
	line string
	a, b int // the line's index in want and in got, before this op
}

func writeHunk(out *strings.Builder, ops []lineOp) {
	aCount, bCount := 0, 0
	for _, op := range ops {
		if op.kind != '+' {
			aCount++
		}
		if op.kind != '-' {
			bCount++
		}
	}
	position := func(start, count int) string {
		if count == 0 {
			return fmt.Sprintf("%d,0", start)
		}
		return fmt.Sprintf("%d,%d", start+1, count)
	}
	fmt.Fprintf(out, "@@ -%s +%s @@\n", position(ops[0].a, aCount), position(ops[0].b, bCount))
	for _, op := range ops {
		out.WriteByte(op.kind)
		out.WriteString(op.line)
		if !strings.HasSuffix(op.line, "\n") {
			out.WriteString("\n\\ No newline at end of file\n")
		}
	}
}

// splitLines splits text after each newline; a last line without one keeps
// none.
func splitLines(text []byte) []string {
	var out []string
	for len(text) > 0 {
		end := bytes.IndexByte(text, '\n') + 1
		if end == 0 {
			end = len(text)
		}
		out = append(out, string(text[:end]))
		text = text[end:]
	}
	return out
}

// lineOps is a shortest edit from a to b by a longest common subsequence of
// lines.
func lineOps(a, b []string) []lineOp {
	var ops []lineOp
	if len(a)*len(b) > maxDiffCells {
		for i, line := range a {
			ops = append(ops, lineOp{'-', line, i, 0})
		}
		for j, line := range b {
			ops = append(ops, lineOp{'+', line, len(a), j})
		}
		return ops
	}
	// common[i][j] is the length of a longest common subsequence of a[i:]
	// and b[j:].
	common := make([][]int32, len(a)+1)
	for i := range common {
		common[i] = make([]int32, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				common[i][j] = common[i+1][j+1] + 1
			} else {
				common[i][j] = max(common[i+1][j], common[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			ops = append(ops, lineOp{' ', a[i], i, j})
			i, j = i+1, j+1
		case j == len(b) || (i < len(a) && common[i+1][j] >= common[i][j+1]):
			ops = append(ops, lineOp{'-', a[i], i, j})
			i++
		default:
			ops = append(ops, lineOp{'+', b[j], i, j})
			j++
		}
	}
	return ops
}
