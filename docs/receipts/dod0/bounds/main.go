// Command bounds is DOD0 row 3: reads through ids that were stored in a
// row and loaded again, so no fact about them is in scope and Go checks
// each index. Built twice from this one source: as is, and with
// -gcflags=-B, which drops every bounds check: the upper bound of what a
// fact can remove. Two loops: a gather (values[ref[i]] for every row, the
// loads independent) and a chase (i = next[i], each load waiting on the
// one before).
//
//	go build -o bounds-checked ./bounds
//	go build -gcflags=-B -o bounds-unchecked ./bounds
//	bounds-checked ROWS ROUNDS
package main

import (
	"fmt"
	"os"
	"strconv"

	"ergo.local/receipts/dod0/internal/report"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: bounds ROWS ROUNDS")
		os.Exit(2)
	}
	n, err1 := strconv.Atoi(os.Args[1])
	rounds, err2 := strconv.Atoi(os.Args[2])
	if err1 != nil || err2 != nil || n < 2 || n > 1<<30 {
		fmt.Fprintln(os.Stderr, "bounds: bad ROWS or ROUNDS")
		os.Exit(2)
	}
	r := report.New(3)
	values := make([]int64, n)
	ref := make([]int32, n)
	next := make([]int32, n)
	for i := range n {
		values[i] = int64(r.IntN(1000))
		ref[i] = int32(r.IntN(n))
	}
	// next is one cycle through every row (Sattolo's shuffle), so the chase
	// visits each row once per lap.
	for i := range next {
		next[i] = int32(i)
	}
	for i := n - 1; i > 0; i-- {
		j := r.IntN(i)
		next[i], next[j] = next[j], next[i]
	}
	var gather, chase int64
	for range rounds {
		for i := range ref {
			gather += values[ref[i]]
		}
		at := int32(0)
		for range n {
			chase += values[at]
			at = next[at]
		}
	}
	fmt.Println(gather, chase)
	report.Done(nil)
}
