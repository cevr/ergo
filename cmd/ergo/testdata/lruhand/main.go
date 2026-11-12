// Command lruhand is the hand-written Go control of examples/lru's
// `lru bench N`: the same get-or-put loop over the same key generator, on
// an LRU cache whose recency list links generational handles into an
// arena, as the Ergo cache's std arena does. It is the hand Go of the
// verifiers' receipts since the std arena (wf10 s-arena handgen.go, made
// generic over K and V like the Ergo cache), with one guarantee fewer: a
// handle to a released slot whose generation matches is not refused
// (the std arena also checks that the slot is occupied).
// TestLRUCountsAsHandGo (lru_count_linux_test.go) counts both programs.
//
//	lruhand bench N
package main

import (
	"fmt"
	"os"
	"strconv"
)

type ref struct{ index, gen int }

var null = ref{index: -1}

type node[K comparable, V any] struct {
	key        K
	value      V
	prev, next ref
}

type slot[T any] struct {
	value     T
	gen, next int
}

type arena[T any] struct {
	slots      []slot[T]
	free, live int
}

func (a *arena[T]) put(v T) ref {
	a.live++
	if a.free == -1 {
		a.slots = append(a.slots, slot[T]{value: v, gen: 1, next: -1})
		return ref{len(a.slots) - 1, 1}
	}
	at := a.free
	s := &a.slots[at]
	a.free = s.next
	s.value, s.next = v, -1
	return ref{at, s.gen}
}

func (a *arena[T]) at(r ref) *T {
	if r.index < 0 || r.index >= len(a.slots) || a.slots[r.index].gen != r.gen {
		panic("stale ref")
	}
	return &a.slots[r.index].value
}

func (a *arena[T]) release(r ref) T {
	n := *a.at(r)
	s := &a.slots[r.index]
	s.gen, s.next = r.gen+1, a.free
	a.free = r.index
	a.live--
	return n
}

type lru[K comparable, V any] struct {
	capacity   int
	index      map[K]ref
	nodes      arena[node[K, V]]
	head, tail ref
}

func newLRU[K comparable, V any](capacity int) *lru[K, V] {
	return &lru[K, V]{capacity: capacity, index: map[K]ref{}, nodes: arena[node[K, V]]{free: -1}, head: null, tail: null}
}

func (c *lru[K, V]) unlink(n *node[K, V]) {
	if n.prev == null {
		c.head = n.next
	} else {
		c.nodes.at(n.prev).next = n.next
	}
	if n.next == null {
		c.tail = n.prev
	} else {
		c.nodes.at(n.next).prev = n.prev
	}
}

func (c *lru[K, V]) pushed(r ref) {
	if c.head == null {
		c.tail = r
	} else {
		c.nodes.at(c.head).prev = r
	}
	c.head = r
}

func (c *lru[K, V]) touch(r ref) *node[K, V] {
	n := c.nodes.at(r)
	if c.head != r {
		c.unlink(n)
		n.prev, n.next = null, c.head
		c.pushed(r)
	}
	return n
}

func (c *lru[K, V]) Get(k K) (V, bool) {
	r, ok := c.index[k]
	if !ok {
		var zero V
		return zero, false
	}
	return c.touch(r).value, true
}

func (c *lru[K, V]) Put(k K, v V) {
	if r, ok := c.index[k]; ok {
		c.touch(r).value = v
		return
	}
	if len(c.index) == c.capacity {
		old := c.tail
		c.unlink(c.nodes.at(old))
		n := c.nodes.release(old)
		delete(c.index, n.key)
	}
	r := c.nodes.put(node[K, V]{key: k, value: v, prev: null, next: c.head})
	c.index[k] = r
	c.pushed(r)
}

func main() {
	if len(os.Args) != 3 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: lruhand bench N")
		os.Exit(2)
	}
	ops, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	c := newLRU[int, int](1024)
	x, sum := 1, 0
	for i := 0; i < ops; i++ {
		x = (x*1103515245 + 12345) % 2147483648
		k := (x / 65536) % 4096
		if v, ok := c.Get(k); ok {
			sum += v
		} else {
			c.Put(k, i)
		}
	}
	fmt.Printf("%d ops, sum %d\n", ops, sum)
}
