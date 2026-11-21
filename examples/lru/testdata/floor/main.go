// Command floor is the Go-runtime floor of examples/lru's `lru bench N`
// (common.md GO-RUNTIME FLOOR): the same get-or-put loop over the same
// key generator, on an LRU cache at the least work the Go runtime allows
// for the job. The job is a least-recently-used cache of 1024 entries over
// integer keys, any int: so the index is still a hash table, not an array
// indexed by the key. Against cmd/ergo/testdata/lruhand (the hand Go
// control: a Go map into a generational arena) it drops what the job does
// not need:
//
//   - the index is open addressing over a power-of-two table of int32 node
//     numbers, twice the capacity, linear probing, deletion by backward
//     shift: no Go map, no hashing through the runtime's map code, no
//     tombstones;
//
//   - the nodes are one packed array, fixed at the capacity, links int32
//     node numbers: no generations, no stale check, no free list (a full
//     cache reuses the evicted tail's node in place);
//
//   - no allocation after the start.
//
//     floor bench N
package main

import (
	"fmt"
	"os"
	"strconv"
)

const none = -1

type node struct {
	key, value int
	prev, next int32
}

type lru struct {
	nodes      []node
	slots      []int32 // node number + 1; 0 is empty
	mask       uint64
	shift      uint
	head, tail int32
}

func newLRU(capacity int) *lru {
	size, shift := 1, uint(64)
	for size < 2*capacity {
		size, shift = size*2, shift-1
	}
	return &lru{nodes: make([]node, 0, capacity), slots: make([]int32, size), mask: uint64(size - 1), shift: shift, head: none, tail: none}
}

func (c *lru) home(k int) uint64 { return (uint64(k) * 0x9e3779b97f4a7c15) >> c.shift }

// find is the slot of k, and whether k is there (else the empty slot that ends its probe).
func (c *lru) find(k int) (uint64, bool) {
	for i := c.home(k); ; i = (i + 1) & c.mask {
		s := c.slots[i]
		if s == 0 {
			return i, false
		}
		if c.nodes[s-1].key == k {
			return i, true
		}
	}
}

// remove empties slot i and shifts back the entries after it that probed past it.
func (c *lru) remove(i uint64) {
	for j := (i + 1) & c.mask; ; j = (j + 1) & c.mask {
		s := c.slots[j]
		if s == 0 {
			break
		}
		h := c.home(c.nodes[s-1].key)
		if (j-h)&c.mask >= (j-i)&c.mask {
			c.slots[i] = s
			i = j
		}
	}
	c.slots[i] = 0
}

func (c *lru) unlink(n int32) {
	p, x := c.nodes[n].prev, c.nodes[n].next
	if p == none {
		c.head = x
	} else {
		c.nodes[p].next = x
	}
	if x == none {
		c.tail = p
	} else {
		c.nodes[x].prev = p
	}
}

func (c *lru) push(n int32) {
	c.nodes[n].prev, c.nodes[n].next = none, c.head
	if c.head == none {
		c.tail = n
	} else {
		c.nodes[c.head].prev = n
	}
	c.head = n
}

// getOrPut is the value of k (made most recent), or puts v at k and reports false.
func (c *lru) getOrPut(k, v int) (int, bool) {
	i, ok := c.find(k)
	if ok {
		n := c.slots[i] - 1
		if c.head != n {
			c.unlink(n)
			c.push(n)
		}
		return c.nodes[n].value, true
	}
	var n int32
	if len(c.nodes) < cap(c.nodes) {
		n = int32(len(c.nodes))
		c.nodes = c.nodes[:n+1]
	} else {
		n = c.tail
		c.unlink(n)
		j, _ := c.find(c.nodes[n].key)
		c.remove(j)
		i, _ = c.find(k) // the shift may have moved k's empty slot
	}
	c.nodes[n].key, c.nodes[n].value = k, v
	c.slots[i] = n + 1
	c.push(n)
	return 0, false
}

func main() {
	if len(os.Args) != 3 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: floor bench N")
		os.Exit(2)
	}
	ops, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	c := newLRU(1024)
	x, sum := 1, 0
	for i := 0; i < ops; i++ {
		x = (x*1103515245 + 12345) % 2147483648
		k := (x / 65536) % 4096
		if v, ok := c.getOrPut(k, i); ok {
			sum += v
		}
	}
	fmt.Printf("%d ops, sum %d\n", ops, sum)
}
