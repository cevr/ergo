// Command control is the hand Go twin of benchmarks/frame: the exit action
// as a person writes it in Go, a deferred closure, with the same guarantee
// the Ergo action has (it runs on every exit of unit, a panic's included)
// and a mutex-guarded cell as sync.Ref is (GP-FRAME; opcount builds it as
// the control).
package main

import (
	"fmt"
	"os"
	"strconv"
	"sync"
)

type ref struct {
	mu sync.Mutex
	v  int
}

func unit(i int, cell *ref) int {
	defer func() {
		cell.mu.Lock()
		cell.v += i
		cell.mu.Unlock()
	}()
	return i & 7
}

func benchExit(n int) int {
	cell := &ref{}
	s := 0
	for i := 0; i < n; i++ {
		s += unit(i, cell)
	}
	return s + cell.v
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
	case "exit":
		out = benchExit(n)
	}
	fmt.Println(out)
}
