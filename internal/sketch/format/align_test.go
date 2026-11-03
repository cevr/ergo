package format

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// tapers are width sequences that start long and approach short cells, or
// the reverse, slowly enough that each step passes go/printer's neighbour
// test (review r3 M1). Each is followed by a long run of short cells.
func tapers(first int) map[string][]int {
	geometric := func(step float64) []int {
		var ws []int
		for w := float64(first); int(w) > 8; w *= step {
			ws = append(ws, int(w))
		}
		return ws
	}
	// The reviewer's family: each width is half the geometric mean of the
	// widths before it, never below 40.
	var halfMean []int
	lnsum := 0.0
	for w := first; ; {
		halfMean = append(halfMean, w)
		lnsum += math.Log(float64(w))
		if w == 40 {
			break
		}
		w = max(40, int(math.Ceil(math.Exp(lnsum/float64(len(halfMean)))/2)))
	}
	var linear []int
	for w := first; w > 8; w -= max(1, first/64) {
		linear = append(linear, w)
	}
	rising := geometric(0.7)
	for i, j := 0, len(rising)-1; i < j; i, j = i+1, j-1 {
		rising[i], rising[j] = rising[j], rising[i]
	}
	return map[string][]int{
		"half the mean": halfMean,
		"factor 0.7":    geometric(0.7),
		"factor 0.45":   geometric(0.45),
		"linear":        linear,
		"rising":        rising,
	}
}

// columnKinds render one row whose aligned cell is size wide, for every
// column the formatter aligns.
var columnKinds = []struct {
	name, open, close string
	row               func(i, size int) string
}{
	{"record names", "type T {\n", "}\n", func(i, size int) string { return cell("f", i, size) + " int,\n" }},
	{"record types", "type T {\n", "}\n", func(i, size int) string { return fmt.Sprintf("f%d %s,\n", i, cell("T", i, size)) }},
	{"record types before comments", "type T {\n", "}\n", func(i, size int) string { return fmt.Sprintf("f%d %s, // c\n", i, cell("T", i, size)) }},
	{"record names before comments", "type T {\n", "}\n", func(i, size int) string { return cell("f", i, size) + " int, // c\n" }},
	{"let values before comments", "", "", func(i, size int) string { return fmt.Sprintf("let v%d = %s // c\n", i, cell("x", i, size)) }},
	{"map keys", "let m = Map[string, int]{\n", "}\n", func(i, size int) string { return fmt.Sprintf("%q: 1,\n", cell("k", i, max(1, size-2))) }},
	{"context keys", "let W = context {\n", "}\n", func(i, size int) string { return cell("K", i, size) + ": T{},\n" }},
}

// cell is a unique name of at least size characters.
func cell(prefix string, i, size int) string {
	name := fmt.Sprintf("%s%d", prefix, i)
	return name + strings.Repeat("a", max(0, size-len(name)))
}

// TestAlignmentIsLinear: for every aligned column and every taper, the
// formatted text is at most 2.5 times the input plus 45 bytes for each
// aligned column of each line (one tab, and padding of at most 41 columns
// or 1.5 times the cell's own width; printer.go, the section bound).
func TestAlignmentIsLinear(t *testing.T) {
	for _, kind := range columnKinds {
		for _, first := range []int{256, 4096} {
			for taper, widths := range tapers(first) {
				var b strings.Builder
				b.WriteString("" + kind.open)
				for i, w := range widths {
					b.WriteString(kind.row(i, w))
				}
				for i := range 4 * first {
					b.WriteString(kind.row(len(widths)+i, 4))
				}
				b.WriteString(kind.close)
				src := b.String()
				out, err := Source(src)
				if err != nil {
					t.Fatalf("%s, %s from %d: %v", kind.name, taper, first, err)
				}
				lines := strings.Count(src, "\n")
				if bound := 5*len(src)/2 + 2*45*lines; len(out) > bound {
					t.Errorf("%s, %s from %d: %d input bytes formatted to %d, over the bound %d", kind.name, taper, first, len(src), len(out), bound)
				}
				if first == 256 {
					if msg := contracts(src, out); msg != "" {
						t.Errorf("%s, %s: %s", kind.name, taper, msg)
					}
				}
			}
		}
	}
}
