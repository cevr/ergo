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

// plain is unit's work with no deferred closure (the update workload).
func plain(i int, cell *ref) int {
	cell.mu.Lock()
	cell.v += i
	cell.mu.Unlock()
	return i & 7
}

func benchUpdate(n int) int {
	cell := &ref{}
	s := 0
	for i := 0; i < n; i++ {
		s += plain(i, cell)
	}
	return s + cell.v
}

// res is the owned value of the own workloads (OWNED-LOCAL): its close
// adds its id and its written field to cell.
type res struct {
	id, n int
	cell  *ref
}

func (r res) close() {
	r.cell.mu.Lock()
	r.cell.v += r.id + r.n
	r.cell.mu.Unlock()
}

// ownList closes r with its write on every exit, as a person writes it:
// a deferred closure that reads r when it runs.
func ownList(i int, cell *ref) int {
	r := res{id: i, cell: cell}
	defer func() { r.close() }()
	r.n = i & 3
	return r.id & 7
}

// ownStatic defers the close of r, which it does not write.
func ownStatic(i int, cell *ref) int {
	r := res{id: i, n: i & 3, cell: cell}
	defer r.close()
	return r.id & 7
}

func benchOwn(n int, list bool) int {
	cell := &ref{}
	s := 0
	for i := 0; i < n; i++ {
		if list {
			s += ownList(i, cell)
		} else {
			s += ownStatic(i, cell)
		}
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
	case "update":
		out = benchUpdate(n)
	case "ownlist":
		out = benchOwn(n, true)
	case "ownstatic":
		out = benchOwn(n, false)
	}
	fmt.Println(out)
}
