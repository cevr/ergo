// Command floor is the floor of examples/tn1-lint's workload: a lower
// bound on the work the Go runtime allows for a TypeScript feature record,
// not a record. It does only what any record of the input must: one pass
// over the bytes through a 256-entry class table, one hash per token, the
// shape counts of each run of three tokens as bit-sliced counters (a
// carry-save adder over 64-bit words, about 8 word operations per run),
// and 16 hex digits per line into one preallocated buffer. It keeps no
// tokens, no statements, no comments and no node, so it costs less than
// any correct record; the receipt's ratio to it is the distance to the
// floor (common.md, GO-RUNTIME FLOOR). It prints a digest line count, so
// its output is not the record and opcount cannot use it as a control;
// measure it with perf stat at the workload's two sizes (the slope).
//
//	floor FILE
package main

import (
	"bufio"
	"fmt"
	"os"
)

const (
	space = iota
	word
	punct
	newline
)

var class [256]uint8

func init() {
	for i := range class {
		class[i] = punct
	}
	for _, c := range []byte(" \t\r") {
		class[c] = space
	}
	class['\n'] = newline
	for c := 'a'; c <= 'z'; c++ {
		class[c] = word
	}
	for c := 'A'; c <= 'Z'; c++ {
		class[c] = word
	}
	for c := '0'; c <= '9'; c++ {
		class[c] = word
	}
	class['_'], class['$'] = word, word
	for c := 0x80; c < 0x100; c++ {
		class[c] = word
	}
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: floor FILE")
		os.Exit(2)
	}
	src, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "floor:", err)
		os.Exit(1)
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 0, len(src)/2+64)
	var h1, h2, line uint64
	var c0, c1, c2 uint64 // bit-sliced counts of the runs' bits, mod 8
	lines := 0
	for i := 0; i < len(src); {
		k := class[src[i]]
		switch k {
		case space:
			i++
		case newline:
			for s := 60; s >= 0; s -= 4 {
				out = append(out, hex[(line>>uint(s))&15])
			}
			out = append(out, '\n')
			line = 0
			lines++
			i++
		default:
			h := uint64(14695981039346656037)
			j := i
			if k == word {
				for j < len(src) && class[src[j]] == word {
					h = (h ^ uint64(src[j])) * 1099511628211
					j++
				}
			} else {
				h = (h ^ uint64(src[j])) * 1099511628211
				j++
			}
			run := h ^ (h1 << 1) ^ (h2 << 2)
			carry := c0 & run
			c0 ^= run
			carry2 := c1 & carry
			c1 ^= carry
			c2 ^= carry2
			line ^= h
			h2, h1 = h1, h
			i = j
		}
	}
	w := bufio.NewWriter(os.Stdout)
	w.Write(out)
	fmt.Fprintf(w, "lines %d shape %x\n", lines, c0^c1^c2)
	if err := w.Flush(); err != nil {
		os.Exit(1)
	}
}
