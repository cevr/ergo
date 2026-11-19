// Command floor is llmbench's Go-runtime floor (common.md GO-RUNTIME
// FLOOR), run as `floor bench KIND N`. KIND fold appends each word of a
// reply straight to one reused buffer: no delta value, no allocation per
// reply after the first. A stream reader that hands each text span to its
// sink does this work and no less.
package main

import (
	"fmt"
	"os"
	"strconv"
)

const part = 64

var words = []string{"the ", "model ", "said ", "a ", "few ", "short ", "words ", "now "}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: floor bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	t := 0
	var b []byte
	if os.Args[2] == "fold" {
		for r := 0; r < n/part; r++ {
			b = b[:0]
			for i := 0; i < part; i++ {
				b = append(b, words[(r+i)&7]...)
			}
			t += len(b)
		}
	}
	fmt.Println(t)
}
