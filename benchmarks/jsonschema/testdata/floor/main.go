// Command floor is schemabench's Go-runtime floor (common.md GO-RUNTIME
// FLOOR), run as `floor bench conforms N`. For a schema generated from a
// type, a value decoded into that type has every property the schema
// states but one the Go type cannot: its union's tag is one of the
// variants'. The check is that one compare; no check of a value does less.
package main

import (
	"fmt"
	"os"
	"strconv"
)

type size struct {
	tag    string
	inches int
}

type box struct {
	label string
	count int
	size  size
	tags  []string
}

//go:noinline
func fits(b *box) bool { return b.size.tag == "Small" || b.size.tag == "Large" }

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" || os.Args[2] != "conforms" {
		fmt.Fprintln(os.Stderr, "usage: floor bench conforms N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	b := &box{label: "crate", count: 12, size: size{tag: "Large", inches: 30}, tags: []string{"wood", "heavy"}}
	fit := 0
	for i := 0; i < n; i++ {
		if fits(b) {
			fit++
		}
	}
	fmt.Println(fit)
}
