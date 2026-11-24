package irspike

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRBulkAppendMatchesSource checks a user binding of Go's bulk
// append on live and owned slices, in a generic body and its copies on
// integers and records with a slice field, and with a dropped result.
//
// It builds Go because only the patched decoder selects Go's variadic
// append and runs the IR bodies, rather than the text bodies, at runtime.
func TestIRBulkAppendMatchesSource(t *testing.T) {
	f7Run(t, "TestIRBulkAppendMatchesSource", bulkAppendProgram, func(r lower.IRReport) string {
		var copies int
		for _, name := range r.IR {
			if strings.HasPrefix(name, "mergeMono_i") {
				copies++
			}
		}
		if copies < 2 {
			return fmt.Sprintf("missing IR monomorphic bodies for int and Row (IR bodies: %v)", r.IR)
		}
		return ""
	}, true, "merge", "owned", "live", "records", "dropped")
}

func bulkAppendProgram(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, seed^0x96a135fadeecab3b))
	n := func() int { return r.IntN(30) + 1 }
	return fmt.Sprintf(`
import slices

extern go append func spread[T](xs []T, ys []T) []T

type Row { Data []int }
type Trace { Seen int }

func merge[T](xs []T, ys []T) []T { spread(xs, ys) }

func owned(x int, ys []int) []int {
	let mut out = slices.WithCapacity[int](8)
	out = out.Append(x)
	out = spread(out, ys)
	out
}

func live(n int, ys []int) int {
	let mut xs = slices.WithCapacity[int](n * 3)
	for i in 0..n {
		xs = xs.Append(i + 1)
	}
	let left = spread(xs, ys)
	let right = spread(xs, xs)
	let empty = spread(xs, []int{})
	left[n] * 10000 + right[n] * 100 + empty[n - 1]
}

func records(a int, b int, c int) int {
	let xs = []Row{Row{Data: []int{a}}}
	let mut ys = []Row{Row{Data: []int{b}}, Row{Data: []int{c}}}
	let joined = merge(xs, ys)
	ys[0].Data[0] = 0
	joined[0].Data[0] * 10000 + joined[1].Data[0] * 100 + joined[2].Data[0] + ys[0].Data[0]
}

func mark(t mut Trace, x int) []int {
	t.Seen = t.Seen * 100 + x
	[]int{x}
}

func dropped(x int) int {
	let mut t = Trace{Seen: 0}
	let _ = spread(mark(&t, x), mark(&t, x + 1))
	t.Seen
}

func main() needs Console {
	let xs = []int{%d}
	let ys = []int{%d, %d}
	let a = merge(xs, ys)
	let b = owned(xs[0], ys)
	Console.Println("\(a[0]) \(a[1]) \(a[2]) \(b[0]) \(b[1]) \(b[2]) \(live(3, ys)) \(records(%d, %d, %d)) \(dropped(xs[0]))")
}
`, n(), n()+1, n(), n(), n(), n())
}
