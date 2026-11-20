// Command control is the hand Go twin of benchmarks/level: each probe as
// a person would write it in Go for speed, with Ergo's guarantees (a
// float product rounds, written float64(a*b)), printing what the Ergo
// probe prints (U3 CPU-LEVEL; opcount builds it as the control).
package main

import (
	"fmt"
	"math"
	"math/bits"
	"os"
	"strconv"
)

func words() []uint64 {
	out := make([]uint64, 0, 1024)
	x := uint64(88172645463325252)
	for range 1024 {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		out = append(out, x)
	}
	return out
}

func benchPopcount(n int) int {
	w := words()
	s := 0
	for i := 0; i < n; i++ {
		s += bits.OnesCount64(w[i&1023])
	}
	return s
}

func benchDot(n int) int {
	a := make([]float64, 1024)
	b := make([]float64, 1024)
	for i := range 1024 {
		a[i] = float64(i&7) + 0.1
		b[i] = 0.5 + float64(float64(i&3)*0.3)
	}
	s := 0.0
	for i := 0; i < n; i++ {
		s += float64(a[i&1023] * b[i&1023])
	}
	// Ergo's int(x) saturates (types.md, Conversions 4); the sum is in range.
	return int(math.Trunc(float64(s * 1000.0)))
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
	case "popcount":
		out = benchPopcount(n)
	case "dot":
		out = benchDot(n)
	}
	fmt.Println(out)
}
