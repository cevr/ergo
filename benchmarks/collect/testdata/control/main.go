// Command control is collectbench's hand Go twin
// (benchmarks/collect/cmd/collectbench): the same parts of the same
// source, as a person optimizing Go writes them, run as
// `control bench KIND N`. KIND collect appends each part at once into a
// slice made to its size; fold collects each part that way, then folds it
// (hand Go collects before it folds: it allocates); foldfloor folds each
// part in place, with no slice (the floor).
package main

import (
	"fmt"
	"os"
	"strconv"
)

const part = 64

func source(m int) []int {
	xs := make([]int, 0, m)
	for i := 0; i < m; i++ {
		xs = append(xs, i*7+1)
	}
	return xs
}

func collectPart(src []int, lo, hi int) int {
	ys := make([]int, 0, hi-lo)
	ys = append(ys, src[lo:hi]...)
	return ys[(hi-lo)/2]
}

func foldPart(src []int, lo, hi int) int {
	ys := make([]int, 0, hi-lo)
	ys = append(ys, src[lo:hi]...)
	h := 0
	for _, y := range ys {
		h += y
	}
	return h
}

func foldFloor(src []int, lo, hi int) int {
	h := 0
	for _, y := range src[lo:hi] {
		h += y
	}
	return h
}

func bench(kind string, n int) int {
	src := source(4096)
	s := 0
	for r := 0; r < n/part; r++ {
		lo := (r & 63) * part
		switch kind {
		case "collect":
			s += collectPart(src, lo, lo+part)
		case "fold":
			s += foldPart(src, lo, lo+part)
		default:
			s += foldFloor(src, lo, lo+part)
		}
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
	fmt.Println(bench(os.Args[2], n))
}
