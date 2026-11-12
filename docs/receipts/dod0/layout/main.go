// Command layout is DOD0 row 2: a table of records with one string field,
// as one slice of records (side aos) against one slice per field (side
// soa). The work reads the numeric fields of every row and the name of
// every 16th, while the program makes pointer-free garbage, so the
// collector runs with the table live. In aos the collector scans the
// table's every word up to its last pointer; in soa it scans only the
// names column, and the numeric columns sit in no-scan spans.
//
//	go build -ldflags=-X=main.side=soa -o layout-soa ./layout
//	layout-soa ROWS ROUNDS
package main

import (
	"fmt"
	"os"
	"strconv"

	"ergo.local/receipts/dod0/internal/report"
)

// side is the layout, set by the build: "aos" or "soa".
var side = "aos"

type rec struct {
	name    string
	x, y, z int64
	flags   uint32
}

type cols struct {
	names   []string
	x, y, z []int64
	flags   []uint32
}

var sink [][]byte

// The tables stay live to the end, so the final counters see them.
var (
	aos []rec
	soa cols
)

// garbage makes 16 MiB of pointer-free garbage in 1 KiB pieces, keeping the
// last 256 live, so the collector runs.
func garbage(r *report.Rand) {
	if sink == nil {
		sink = make([][]byte, 256)
	}
	for i := range 16 << 10 {
		b := make([]byte, 1024)
		b[r.IntN(1024)] = 1
		sink[i%256] = b
	}
}

func name(i int) string { return "row-" + strconv.Itoa(i) }

func runAoS(n, rounds int) int64 {
	r := report.New(2)
	t := make([]rec, n)
	for i := range t {
		t[i] = rec{name: name(i), x: int64(r.IntN(100)), y: int64(r.IntN(100)), z: int64(r.IntN(100)), flags: uint32(r.IntN(4))}
	}
	aos = t
	var sum int64
	for range rounds {
		for i := range t {
			if t[i].flags&1 != 0 {
				sum += t[i].x + t[i].y - t[i].z
			}
			if i%16 == 0 {
				sum += int64(len(t[i].name))
			}
		}
		garbage(r)
	}
	return sum
}

func runSoA(n, rounds int) int64 {
	r := report.New(2)
	t := cols{names: make([]string, n), x: make([]int64, n), y: make([]int64, n), z: make([]int64, n), flags: make([]uint32, n)}
	for i := range n {
		t.names[i] = name(i)
		t.x[i], t.y[i], t.z[i], t.flags[i] = int64(r.IntN(100)), int64(r.IntN(100)), int64(r.IntN(100)), uint32(r.IntN(4))
	}
	soa = t
	var sum int64
	for range rounds {
		for i := range n {
			if t.flags[i]&1 != 0 {
				sum += t.x[i] + t.y[i] - t.z[i]
			}
			if i%16 == 0 {
				sum += int64(len(t.names[i]))
			}
		}
		garbage(r)
	}
	return sum
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: layout ROWS ROUNDS")
		os.Exit(2)
	}
	n, err1 := strconv.Atoi(os.Args[1])
	rounds, err2 := strconv.Atoi(os.Args[2])
	if err1 != nil || err2 != nil || n < 1 || n > 1<<28 {
		fmt.Fprintln(os.Stderr, "layout: bad ROWS or ROUNDS")
		os.Exit(2)
	}
	var sum int64
	switch side {
	case "aos":
		sum = runAoS(n, rounds)
	case "soa":
		sum = runSoA(n, rounds)
	default:
		fmt.Fprintln(os.Stderr, "layout: side is", side)
		os.Exit(2)
	}
	fmt.Println(sum)
	report.Done(nil)
}
