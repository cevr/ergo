// Command idwidth is DOD0 row 1: a tree in one slice of nodes linked by
// ids, walked depth first, with 4-byte ids (side 32, int32; side u32,
// uint32) against 8-byte ids (side 64). The node is three ids and one
// int32 value: 16 bytes with 4-byte ids, 32 with 8-byte ids. Parents are random, so the walk reads
// nodes in no order: a cache miss per node once the tree outgrows the
// cache, which is where the width shows.
//
//	go build -ldflags=-X=main.side=32 -o idwidth32 ./idwidth
//	idwidth32 NODES WALKS
package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"unsafe"

	"ergo.local/receipts/dod0/internal/report"
)

// side is the id width, set by the build: "32" or "64".
var side = "64"

type id interface{ ~int32 | ~int64 | ~uint32 }

// none is the id of no node: all bits set (-1 for a signed id).
func none[I id]() I { return ^I(0) }

type node[I id] struct {
	first, next, parent I
	val                 int32
}

// build links node i under a random earlier node, for i in 1..n-1.
func build[I id](n int, r *report.Rand) []node[I] {
	ns := make([]node[I], n)
	for i := range ns {
		ns[i] = node[I]{first: none[I](), next: none[I](), parent: none[I](), val: int32(r.IntN(1000))}
	}
	for i := 1; i < n; i++ {
		p := r.IntN(i)
		ns[i].parent = I(p)
		ns[i].next = ns[p].first
		ns[p].first = I(i)
	}
	return ns
}

// walk sums the values depth first from the root, and the depth of each
// node by its parent chain from every 64th node.
func walk[I id](ns []node[I], stack []I) (int64, []I) {
	var sum int64
	stack = append(stack[:0], 0)
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		sum += int64(ns[i].val)
		for c := ns[i].first; c != none[I](); c = ns[c].next {
			stack = append(stack, c)
		}
	}
	for i := 0; i < len(ns); i += 64 {
		for p := ns[i].parent; p != none[I](); p = ns[p].parent {
			sum++
		}
	}
	return sum, stack
}

func run[I id](n, walks int) {
	r := report.New(1)
	ns := build[I](n, r)
	var total int64
	var stack []I
	for range walks {
		var s int64
		s, stack = walk(ns, stack)
		total += s
	}
	fmt.Println(total)
	var z node[I]
	report.Done(map[string]float64{"node-bytes": float64(unsafe.Sizeof(z)), "tree-bytes": float64(uintptr(n) * unsafe.Sizeof(z))})
	runtime.KeepAlive(ns)
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: idwidth NODES WALKS")
		os.Exit(2)
	}
	n, err1 := strconv.Atoi(os.Args[1])
	walks, err2 := strconv.Atoi(os.Args[2])
	if err1 != nil || err2 != nil || n < 1 || n > 1<<30 {
		fmt.Fprintln(os.Stderr, "idwidth: bad NODES or WALKS")
		os.Exit(2)
	}
	switch side {
	case "32":
		run[int32](n, walks)
	case "64":
		run[int64](n, walks)
	case "u32":
		run[uint32](n, walks)
	default:
		fmt.Fprintln(os.Stderr, "idwidth: side is", side)
		os.Exit(2)
	}
}
