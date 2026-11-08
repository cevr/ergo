package lower

import "testing"

// TestStdSlicesMoreRuns (std-slices-maps): slices.Clone, Concat, Repeat
// and Slice, Ascending's Len and At, and maps.Clone, GetOr and Update
// lower and run. An Append to a Slice of a slice does not write the
// slice; an Append to a slice with room does not change a Concat made
// from it; a write to a Clone does not reach its source. Update upserts
// through a callback that sees the old value, Some or None, changes the
// Map it is given and returns it; a failing callback ends Update with its
// failure, through Update's row, and leaves the Map as it was.
func TestStdSlicesMoreRuns(t *testing.T) {
	runBehaviors(t, []behavior{
		{name: "slicesmore", src: stdSlicesMoreProgram, stdout: stdSlicesMoreOutput},
	})
}

const stdSlicesMoreProgram = `import slices
import maps
import strings

type TooBig { N int }

func ints(xs []int) string {
	strings.Join(slices.Transform(xs, func(x int) string { "\(x)" }), ",")
}

func count(old Option[int]) int {
	match old {
	case Some[int] as s:
		s.Value + 1
	case None:
		1
	}
}

func capped(old Option[int]) int fails TooBig {
	let n = count(old)
	if n > 2 {
		return TooBig{N: n}
	}
	n
}

func main() needs Console {
	let xs = []int{1, 2, 3, 4}
	let head = slices.Slice(xs, 0, 2)
	let grown = head.Append(9)
	Console.Println(ints(head) + " " + ints(grown) + " " + ints(xs))

	let mut roomy = slices.WithCapacity[int](8)
	roomy = roomy.Append(1)
	roomy = roomy.Append(2)
	let joined = slices.Concat(roomy, []int{3, 4})
	let more = roomy.Append(7)
	Console.Println(ints(joined) + " " + ints(more))

	let mut copy = slices.Clone(xs)
	copy[0] = 100
	Console.Println(ints(copy) + " " + ints(xs))

	Console.Println(ints(slices.Repeat([]int{1, 2}, 3)) + " [" + ints(slices.Repeat([]int{5}, 0)) + "] " + ints(slices.Repeat([]int{7}, 2)))

	let a = slices.Ascend([]int{5, 3, 9, 1})
	Console.Println("\(a.Len()) \(a.At(0)) \(slices.At(a, 3)) \(slices.Len(a))")

	let words = []string{"a", "b", "a", "c", "a"}
	let mut tally = Map[string, int]{}
	for w in words {
		tally = maps.Update(tally, w, count)
	}
	Console.Println("\(maps.GetOr(tally, "a", 0)) \(maps.GetOr(tally, "b", 0)) \(maps.GetOr(tally, "z", -1)) \(len(tally))")

	let snapshot = maps.Clone(tally)
	let mut changed = maps.Clone(tally)
	changed["a"] = 0
	changed["new"] = 1
	Console.Println("\(maps.GetOr(tally, "a", 0)) \(maps.GetOr(snapshot, "a", 0)) \(maps.GetOr(changed, "a", 9)) \(len(tally)) \(len(changed))")

	let mut limited = Map[string, int]{}
	let mut failed = ""
	for w in words {
		limited = maps.Update(limited, w, capped) catch {
		case TooBig as e:
			failed = failed + "\(w)=\(e.N) "
			limited
		}
	}
	Console.Println(failed + "\(maps.GetOr(limited, "a", 0)) \(maps.GetOr(limited, "b", 0))")
}
`

const stdSlicesMoreOutput = "1,2 1,2,9 1,2,3,4\n" +
	"1,2,3,4 1,2,7\n" +
	"100,2,3,4 1,2,3,4\n" +
	"1,2,1,2,1,2 [] 7,7\n" +
	"4 1 9 4\n" +
	"3 1 -1 3\n" +
	"3 3 0 3 4\n" +
	"a=3 2 1\n"
