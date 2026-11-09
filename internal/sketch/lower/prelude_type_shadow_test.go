package lower

import "testing"

// TestErgoRunPackageTypeHidesPreludeType (L6; grammar.md, Reserved names
// 2; owner q30): a package's own Option, Map, Range and int run beside the
// prelude's, which std's results and a range literal still use. The
// lowering gives each its own Go name.
func TestErgoRunPackageTypeHidesPreludeType(t *testing.T) {
	needsBuild(t)
	wantRun(t, `import slices

type Option[T] = Present[T]{ V T } | Absent{}
type Map[K, V] { keys []K, vals []V }
// int is the package's type here, so Range's fields are int64.
type Range { lo int64, hi int64 }
type int { n int64 }

func size[K, V](m Map[K, V]) int64 { int64(len(m.keys)) }

func pick(o Option[int]) int64 {
	match o {
	case Present[int]{V: v}:
		v.n
	case Absent:
		0
	}
}

// slices.Max returns the prelude's Option, which this package's Option
// does not hide where std declares it.
func biggest(xs []int64) int64 {
	match slices.Max(xs) {
	case Some[int64]{Value: v}:
		v
	case None:
		-1
	}
}

func main() needs Console {
	let m = Map[string, bool]{keys: []string{"a", "b"}, vals: []bool{true, false}}
	let r = Range{lo: 1, hi: 4}
	let mut s = 0
	for i in 0..3 {
		s += i
	}
	Console.Println("\(m.size()) \(pick(Present[int]{V: int{n: 7}})) \(pick(Absent)) \(r.hi - r.lo) \(biggest([]int64{5, 6})) \(biggest([]int64{})) \(s)")
}
`, "2 7 0 3 6 -1 3\n")
}
