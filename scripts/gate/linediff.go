package main

import "strings"

// hunk is one change between two versions of a file: old lines
// [OldLo, OldHi] became new lines [NewLo, NewHi], 1-based and inclusive. A
// pure insertion has OldHi == OldLo-1 (it goes before old line OldLo), a
// pure deletion NewHi == NewLo-1.
type hunk struct {
	OldLo, OldHi, NewLo, NewHi int
}

// maxDiffEdits bounds the line diff's work: past this many edits the two
// versions are one hunk from the first differing line to the last.
const maxDiffEdits = 4000

// lineHunks is the line diff of old and new (Myers' greedy algorithm),
// grouped into hunks of adjacent edits.
func lineHunks(old, new string) []hunk {
	a, b := splitLines(old), splitLines(new)
	// The common prefix and suffix are not edits.
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	a, b = a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	whole := []hunk{{prefix + 1, prefix + len(a), prefix + 1, prefix + len(b)}}
	keepA, keepB, ok := myersKeep(a, b)
	if !ok {
		return whole
	}
	var hunks []hunk
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		if i < len(a) && j < len(b) && keepA[i] && keepB[j] {
			i++
			j++
			continue
		}
		h := hunk{OldLo: prefix + i + 1, NewLo: prefix + j + 1}
		for i < len(a) && !keepA[i] {
			i++
		}
		for j < len(b) && !keepB[j] {
			j++
		}
		h.OldHi, h.NewHi = prefix+i, prefix+j
		hunks = append(hunks, h)
	}
	return hunks
}

// myersKeep marks the lines of a and b that a shortest edit script keeps,
// or reports false past maxDiffEdits.
func myersKeep(a, b []string) (keepA, keepB []bool, ok bool) {
	n, m := len(a), len(b)
	limit := min(n+m, maxDiffEdits)
	offset := limit + 1
	v := make([]int, 2*limit+3)
	var trace [][]int
	found := -1
	for d := 0; d <= limit && found < 0; d++ {
		trace = append(trace, append([]int(nil), v...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || k != d && v[offset+k-1] < v[offset+k+1] {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = d
				break
			}
		}
	}
	if found < 0 {
		return nil, nil, false
	}
	keepA, keepB = make([]bool, n), make([]bool, m)
	x, y := n, m
	for d := found; d > 0; d-- {
		prev := trace[d]
		k := x - y
		var pk int
		if k == -d || k != d && prev[offset+k-1] < prev[offset+k+1] {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := prev[offset+pk]
		py := px - pk
		for x > px && y > py {
			x--
			y--
			keepA[x], keepB[y] = true, true
		}
		x, y = px, py
	}
	for x > 0 && y > 0 {
		x--
		y--
		keepA[x], keepB[y] = true, true
	}
	return keepA, keepB, true
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.SplitAfter(s, "\n")
}
