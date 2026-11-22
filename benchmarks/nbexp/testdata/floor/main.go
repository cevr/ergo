// Command floor is the hand-optimized Go floor of nb's push kinds: what a
// person writes to grow a slice by one, Go's append with Go's growth
// policy, printing what the Ergo program prints.
package main

import (
	"fmt"
	"os"
	"strconv"
)

func benchAppend(n int) int {
	xs := make([]int, 0)
	for i := range n {
		xs = append(xs, i)
	}
	return len(xs) + xs[n-1]
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: floor bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	out := -1
	switch os.Args[2] {
	case "append", "extern", "plain", "plainint":
		out = benchAppend(n)
	}
	fmt.Println(out)
}
