// Command control is the hand Go twin of benchmarks/mexpr: the product of
// two two-state steps over one signal, folded in a loop, as a person would
// write it in Go for speed; it prints what `mexpr bench KIND N` prints for
// every KIND.
package main

import (
	"fmt"
	"os"
	"strconv"
)

const (
	flip = iota
	hold
)

type state struct {
	on bool
	n  int
}

func left(s state, e uint8) state {
	if e == flip {
		return state{!s.on, s.n + 1}
	}
	return s
}

func right(s state, e uint8) state {
	if e == hold {
		return state{!s.on, s.n + 1}
	}
	return s
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: control bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	xs := make([]uint8, 0, n)
	for i := range n {
		if i%3 == 0 {
			xs = append(xs, hold)
		} else {
			xs = append(xs, flip)
		}
	}
	var l, r state
	for _, e := range xs {
		l, r = left(l, e), right(r, e)
	}
	fmt.Println(l.n, r.n)
}
