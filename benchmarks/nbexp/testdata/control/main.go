// Command control is the hand Go twin of nb: append and slices.Repeat as a
// person writes them in Go, printing what the Ergo program prints.
package main

import (
	"fmt"
	"os"
	"slices"
	"strconv"
)

func benchAppend(n int) int {
	xs := make([]int, 0)
	for i := range n {
		xs = append(xs, i)
	}
	return len(xs) + xs[n-1]
}

// push is nb's plain push as a person writes it in Go: the same growth
// policy (room after the length, else a new array of 2n+8), the same cut,
// copy and index write, at the same element type.
func push[T any](xs []T, x T) []T {
	n := len(xs)
	if n < cap(xs) {
		ys := xs[0 : n+1]
		ys[n] = x
		return ys
	}
	ys := make([]T, 2*n+8)[0 : n+1]
	copy(ys, xs)
	ys[n] = x
	return ys
}

func benchPlain(n int) int {
	xs := make([]int, 0)
	for i := range n {
		xs = push(xs, i)
	}
	return len(xs) + xs[n-1]
}

func benchRepeat(n int) int {
	base := []int{1, 2, 3, 4, 5, 6, 7, 8}
	s := 0
	for i := range n / 64 {
		out := slices.Repeat(base, 8)
		s += out[i&63]
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
	out := -1
	switch os.Args[2] {
	case "append", "extern":
		out = benchAppend(n)
	case "plain", "plainint":
		out = benchPlain(n)
	case "repeat", "repeatplain", "repeatappend":
		out = benchRepeat(n)
	}
	fmt.Println(out)
}
