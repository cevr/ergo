// Command control is collectbench's hand Go twin
// (benchmarks/collect/cmd/collectbench): the same parts of the same
// source, as a person optimizing Go writes them, run as
// `control bench KIND N`. KIND collect appends each part at once into a
// slice made to its size; fold collects each part that way, then folds it
// (hand Go collects before it folds: it allocates); foldfloor folds each
// part in place, with no slice (the floor); reslice copies a part of a
// fresh line into its own slice and grows it; reslicefloor takes the
// part itself (the floor).
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

// line is a fresh source of 80 elements for each part, made to its size.
func line(r int) []int {
	xs := make([]int, 0, 80)
	for i := 0; i < 80; i++ {
		xs = append(xs, i*7+r)
	}
	return xs
}

// reslicePart copies 64 elements of a fresh line into their own slice
// (what Go must write when it cannot prove the line dead), then grows it.
func reslicePart(r int) int {
	src := line(r)
	ys := make([]int, 0, 65)
	ys = append(ys, src[8:72]...)
	ys = append(ys, r)
	return ys[r&63] + len(ys)
}

// resliceFloor takes the line's part itself, as a person who knows the
// line is dead writes it.
func resliceFloor(r int) int {
	src := line(r)
	ys := src[8:72]
	ys = append(ys, r)
	return ys[r&63] + len(ys)
}

func bench(kind string, n int) int {
	if kind == "reslice" || kind == "reslicefloor" {
		t := 0
		for r := 0; r < n/part; r++ {
			if kind == "reslice" {
				t += reslicePart(r)
			} else {
				t += resliceFloor(r)
			}
		}
		return t
	}
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
