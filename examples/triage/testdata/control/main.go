// Command control is the hand Go control of examples/triage for
// scripts/opcount (CONSTRAINTS C4): it reads the log as triage does, sorts
// it with the algorithm of std slices.SortBy (a copy of the input, a
// median-of-three quicksort over an explicit stack of ranges whose pops
// keep the storage, guarded scans, an insertion sort under 13 elements)
// with the compare written for Entry by hand, lazily (the host compare
// runs only on a severity tie), and prints the same lines. It checks a
// stop flag at the function's entry and at the end of every loop
// iteration, as the Ergo program's polls do, and keeps Go's bounds checks.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

var severities = []string{"debug", "info", "warn", "error"}

type entry struct {
	severity int
	host     string
	line     int
}

var stop atomic.Bool

func check() {
	if stop.Load() {
		panic("stopped")
	}
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func compareString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func bySeverity(a, b entry) int {
	if c := compareInt(b.severity, a.severity); c != 0 {
		return c
	}
	if c := compareString(a.host, b.host); c != 0 {
		return c
	}
	return compareInt(a.line, b.line)
}

func byHost(a, b entry) int {
	if c := compareString(a.host, b.host); c != 0 {
		return c
	}
	if c := compareInt(b.severity, a.severity); c != 0 {
		return c
	}
	return compareInt(a.line, b.line)
}

func sortBySeverity(xs []entry) []entry {
	check()
	out := append([]entry(nil), xs...)
	if len(out) < 2 {
		return out
	}
	todo := []int{0, len(out)}
	top := 2
	for top > 0 {
		hi, lo := todo[top-1], todo[top-2]
		top -= 2
		for hi-lo > 12 {
			m := lo + (hi-lo)/2
			last := hi - 1
			if bySeverity(out[m], out[lo]) < 0 {
				out[m], out[lo] = out[lo], out[m]
			}
			if bySeverity(out[last], out[lo]) < 0 {
				out[last], out[lo] = out[lo], out[last]
			}
			if bySeverity(out[last], out[m]) < 0 {
				out[last], out[m] = out[m], out[last]
			}
			p := out[m]
			i, j := lo, last
			for i <= j {
				for i < last && bySeverity(out[i], p) < 0 {
					i++
					check()
				}
				for j > lo && bySeverity(p, out[j]) < 0 {
					j--
					check()
				}
				if i <= j {
					out[i], out[j] = out[j], out[i]
					i++
					j--
				}
				check()
			}
			wlo, whi := lo, j+1
			if j+1-lo < hi-i {
				wlo, whi = i, hi
				hi = j + 1
			} else {
				lo = i
			}
			if top == len(todo) {
				todo = append(todo, wlo, whi)
			} else {
				todo[top], todo[top+1] = wlo, whi
			}
			top += 2
			check()
		}
		for i := lo + 1; i < hi; i++ {
			for j := i; j > lo && bySeverity(out[j], out[j-1]) < 0; j-- {
				out[j], out[j-1] = out[j-1], out[j]
				check()
			}
			check()
		}
		check()
	}
	return out
}

// sortByHost is sortBySeverity with byHost; only `-by severity` is measured.
func sortByHost(xs []entry) []entry {
	out := append([]entry(nil), xs...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && byHost(out[j], out[j-1]) < 0; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func main() {
	by := flag.String("by", "severity", "sort by `order`")
	flag.Parse()
	var entries []entry
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		text := sc.Text()
		if text == "" {
			continue
		}
		fields := strings.Fields(text)
		severity := -1
		if len(fields) == 2 {
			for i, s := range severities {
				if s == fields[0] {
					severity = i
					break
				}
			}
		}
		if severity < 0 {
			fmt.Fprintf(os.Stderr, "triage: line %d: want SEVERITY HOST, got %q\n", n, text)
			os.Exit(1)
		}
		entries = append(entries, entry{severity, fields[1], n})
	}
	var sorted []entry
	if *by == "host" {
		sorted = sortByHost(entries)
	} else {
		sorted = sortBySeverity(entries)
	}
	var b strings.Builder
	for _, e := range sorted {
		b.WriteString(severities[e.severity] + " " + e.host + " " + strconv.Itoa(e.line) + "\n")
	}
	w := bufio.NewWriter(os.Stdout)
	w.WriteString(b.String())
	w.Flush()
}
