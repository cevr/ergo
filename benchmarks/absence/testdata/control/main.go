// Command control is absencebench's hand Go twin
// (benchmarks/absence/cmd/absencebench), run as `control bench KIND N`:
// the floor of each kind, Go's comma-ok read, with the error built on the
// miss only (orfail, orfailhand) or the default given (the combinator
// kinds and their hand twins: map, mapor, then, orelse, filter).
package main

import (
	"fmt"
	"os"
	"strconv"
)

type miss struct{ Key int }

func (m miss) Error() string { return "miss" }

func table() map[int]int {
	m := map[int]int{}
	for i := 0; i < 1024; i++ {
		m[i*2] = i
	}
	return m
}

func look(m map[int]int, k int) (int, error) {
	v, ok := m[k]
	if !ok {
		return 0, miss{Key: k}
	}
	return v, nil
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "control: N is not a count")
		os.Exit(2)
	}
	m := table()
	t := 0
	switch os.Args[2] {
	case "orfail", "orfailhand", "userorfail", "orfaillet", "userorfaillet":
		for i := 0; i < n; i++ {
			v, err := look(m, i&2047)
			if err != nil {
				v = err.(miss).Key & 1
			}
			t += v
		}
	case "map", "maphand", "mapor":
		for i := 0; i < n; i++ {
			if v, ok := m[i&2047]; ok {
				t += v*2 + 1
			}
		}
	case "then", "thenhand":
		for i := 0; i < n; i++ {
			if v, ok := m[i&2047]; ok {
				if w, ok := m[v]; ok {
					t += w
				}
			}
		}
	case "orelse", "orelsehand":
		for i := 0; i < n; i++ {
			k := i & 2047
			v, ok := m[k]
			if !ok {
				v = k & 1
			}
			t += v
		}
	case "filter", "filterhand":
		for i := 0; i < n; i++ {
			if v, ok := m[i&2047]; ok && v&1 == 0 {
				t += v
			}
		}
	default:
		fmt.Fprintln(os.Stderr, "control: unknown KIND")
		os.Exit(2)
	}
	fmt.Println(t)
}
