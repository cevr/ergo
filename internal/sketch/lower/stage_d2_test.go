package lower

import (
	"strings"
	"testing"
)

// Stage D2 (check-v1): one for over a slice, a Map, a Range and a Seq
// (D-D4), `a..b` as a Range[T] value, a Map write, and len (D-D5). The
// refusals are the checker's (internal/sketch/check stage_d2_test.go);
// every form the stage admits runs here through the public CLI.

// TestErgoRunForDomains: a for over a slice binds the element, or the
// index and the element; a for over a Map binds the value, or the key and
// the value (summed, so the unspecified order does not show); a Range
// counts from Start up to End, End excluded, over int and int64, from a
// literal `a..b` and from a Range value; an empty Range runs no body; `_`
// ignores a binding; break and continue reach the for-in, through a match
// too; a return leaves the function from inside the loop; a function
// literal captures one iteration's binding; a binding may be a record
// pattern; and the domain is evaluated once.
func TestErgoRunForDomains(t *testing.T) {
	wantRun(t, `type Pair { A int, B int }

func report(name string, ok bool) needs Console {
	if ok {
		Console.Println(name)
	} else {
		Console.Println("FAIL " + name)
	}
}

func sumSlice(xs []int) int {
	let mut s = 0
	for x in xs {
		s += x
	}
	s
}

func weighted(xs []int) int {
	let mut s = 0
	for i, x in xs {
		s += i * x
	}
	s
}

func mapValues(m Map[string, int]) int {
	let mut s = 0
	for v in m {
		s += v
	}
	s
}

func mapKeys(m Map[string, int]) int {
	let mut s = 0
	for k, v in m {
		s += len(k) * v
	}
	s
}

func count(n int64) int64 {
	let mut s int64 = 0
	for i in 0..n {
		s += i
	}
	s
}

func fromValue(r Range[int]) int {
	let mut s = 0
	for i in r {
		s = s * 10 + i
	}
	s
}

func ignored(xs []string) int {
	let mut n = 0
	for _ in xs {
		n += 1
	}
	for _, _ in xs {
		n += 1
	}
	for i, _ in xs {
		n += i
	}
	n
}

func firstOver(xs []int, limit int) int {
	for x in xs {
		if x > limit {
			return x
		}
	}
	-1
}

func skipAndStop(xs []int) int {
	let mut s = 0
	for x in xs {
		if x == 2 {
			continue
		}
		match x {
			case 5: break
			case _: s += x
		}
	}
	s
}

func captured() int {
	let mut fs = []func() int{func() int { 0 }, func() int { 0 }, func() int { 0 }}
	for i in 1..4 {
		fs[i - 1] = func() int { i }
	}
	let mut s = 0
	for f in fs {
		s = s * 10 + f()
	}
	s
}

func pairs(ps []Pair) int {
	let mut s = 0
	for Pair{A: a, B: b} in ps {
		s += a * b
	}
	s
}

func calls(n int) int needs Console {
	Console.Println("domain")
	n
}

func once() int needs Console {
	let mut s = 0
	for i in 0..calls(3) {
		s += i
	}
	s
}

func main() needs Console {
	report("slice", sumSlice([]int{1, 2, 3}) == 6)
	report("slice index", weighted([]int{5, 6, 7}) == 20)
	report("map values", mapValues(Map[string, int]{"a": 1, "bb": 2, "ccc": 3}) == 6)
	report("map keys", mapKeys(Map[string, int]{"a": 1, "bb": 2, "ccc": 3}) == 14)
	report("range int64", count(5) == 10)
	report("range value", fromValue(1..4) == 123)
	report("empty range", fromValue(3..3) == 0 && fromValue(4..2) == 0)
	report("blank", ignored([]string{"x", "y", "z"}) == 9)
	report("return", firstOver([]int{1, 4, 9}, 3) == 4 && firstOver([]int{1}, 3) == -1)
	report("break continue", skipAndStop([]int{1, 2, 3, 5, 7}) == 4)
	report("captured", captured() == 123)
	report("pattern", pairs([]Pair{Pair{A: 2, B: 3}, Pair{A: 4, B: 5}}) == 26)
	report("once", once() == 3)
}
`, "slice\nslice index\nmap values\nmap keys\nrange int64\nrange value\nempty range\nblank\nreturn\nbreak continue\ncaptured\npattern\ndomain\nonce\n")
}

// TestErgoRunSeq: a for over a Seq[T] built by conversion from a function
// literal runs the body as yield; a break makes yield return false, so a
// lawful Seq stops (the Seq counts its yields); a Seq value is also called
// directly, through its declared function type.
func TestErgoRunSeq(t *testing.T) {
	wantRun(t, `func upTo(n int) Seq[int] {
	Seq[int](func(yield func(v int) bool) {
		let mut i = 0
		for i < n {
			if !yield(i) {
				return
			}
			i += 1
		}
	})
}

func report(name string, ok bool) needs Console {
	if ok {
		Console.Println(name)
	} else {
		Console.Println("FAIL " + name)
	}
}

func main() needs Console {
	let mut s = 0
	for v in upTo(4) {
		s = s * 10 + v
	}
	report("seq", s == 123)

	let mut yields = 0
	let counted = Seq[string](func(yield func(v string) bool) {
		for w in []string{"a", "b", "c", "d"} {
			yields += 1
			if !yield(w) {
				return
			}
		}
	})
	let mut seen = ""
	for w in counted {
		if w == "c" {
			break
		}
		seen = seen + w
	}
	report("seq break", seen == "ab" && yields == 3)

	let mut n = 0
	for _ in upTo(3) {
		n += 1
	}
	report("seq blank", n == 3)

	let mut direct = 0
	upTo(5)(func(v int) bool {
		direct += v
		v < 2
	})
	report("seq call", direct == 3)
}
`, "seq\nseq break\nseq blank\nseq call\n")
}

// TestErgoRunMapWriteAndLen: a Map write inserts a new entry and replaces
// an existing one; len counts a slice, a Map and a string's bytes.
func TestErgoRunMapWriteAndLen(t *testing.T) {
	wantRun(t, `func main() needs Console {
	let mut m = Map[string, int]{"a": 1}
	m["b"] = 2
	m["a"] = 10
	let mut s = 0
	for k, v in m {
		s += len(k) * v
	}
	if s == 12 && len(m) == 2 {
		Console.Println("map write")
	}
	if len([]int{1, 2, 3}) == 3 && len("héllo") == 6 && len(Map[int, int]{}) == 0 {
		Console.Println("len")
	}
}
`, "map write\nlen\n")
}

// TestErgoRunDefaultMapIsWritable: the default of a Map is an empty,
// writable Map wherever the lowering produces a default (D-D4, the Map
// rule): a local and a package let with a Map type and no value, a field a
// record literal omits, a Map inside a defaulted record at any depth, an
// elided record in a literal, a generic record's Map field, a defined type
// over a Map, a tuple's Map and a record that holds itself through a Map.
// Each is written, then counted with len. Go's nil map panics on the first
// write.
func TestErgoRunDefaultMapIsWritable(t *testing.T) {
	wantRun(t, `type R { N int, M Map[string, int] }
type O { I R, K int }
type Box[T] { V T, M Map[string, T] }
type Bag Map[string, int]
type L { Next Map[string, L], Kids []L, N int }

let pm Map[string, int]

func report(name string, ok bool) needs Console {
	if ok {
		Console.Println(name)
	}
}

func main() needs Console {
	let mut m Map[string, int]
	m["a"] = 1
	report("local let", len(m) == 1)

	let mut w = pm
	w["a"] = 1
	report("package let", len(w) == 1)

	let mut r = R{N: 1}
	r.M["a"] = 1
	report("omitted field", len(r.M) == 1 && r.N == 1)

	let mut o = O{K: 2}
	o.I.M["k"] = 1
	report("nested omitted field", len(o.I.M) == 1)

	let mut d O
	d.I.M["k"] = 1
	d.I.M["j"] = 2
	report("let of a record", len(d.I.M) == 2)

	let mut e = []O{{I: R{N: 3}}}
	e[0].I.M["k"] = 1
	report("elided record", len(e[0].I.M) == 1 && e[0].I.N == 3)

	let mut b Box[int]
	b.M["a"] = b.V + 1
	report("generic record", len(b.M) == 1)

	let mut g Bag
	g["x"] = 1
	report("defined Map", len(g) == 1)

	let tup (int, Map[string, int])
	let n, tm = tup
	let mut tw = tm
	tw["a"] = n
	report("tuple", len(tw) == 1)

	let mut l L
	l.Next["x"] = L{N: 1}
	report("recursive record", len(l.Next) == 1)
}
`, "local let\npackage let\nomitted field\nnested omitted field\nlet of a record\nelided record\ngeneric record\ndefined Map\ntuple\nrecursive record\n")
}

// TestErgoRunNamedTupleDefaultMapIsWritable: a defined type over a tuple,
// and an instance of a generic one, defaults like its tuple, keeping its
// named Go type (stage D2 round 2 review M1): a direct let, a package let,
// an omitted record field and a generic instance, each destructured, then
// its Map written and counted. A default inside a generic function, whose
// type mentions the function's type parameter, is built by a generic
// default function. The first program is the review's reproduction.
func TestErgoRunNamedTupleDefaultMapIsWritable(t *testing.T) {
	wantRun(t, `type T (int, Map[string, int])
func main() needs Console {
    let tup T
    let (n, m) = tup
    let mut w = m
    w["a"] = n
    if len(w) == 1 { Console.Println("ok") }
}
`, "ok\n")
	wantRun(t, `type T (int, Map[string, int])
type P[A] (A, Map[string, A])
type H { N int, U T }
type G { Q P[string], K int }
type Box[A] { V []A, M Map[string, A] }

let pt T

func report(name string, ok bool) needs Console {
	if ok {
		Console.Println(name)
	}
}

func inGeneric[A](x A) int {
	let mut b Box[A]
	b.M["a"] = x
	len(b.M)
}

func main() needs Console {
	let tup T
	let (n, m) = tup
	let mut w = m
	w["a"] = n
	report("named tuple let", len(w) == 1)

	let (pn, pm) = pt
	let mut pw = pm
	pw["a"] = pn
	report("named tuple package let", len(pw) == 1)

	let h = H{N: 1}
	let (hn, hm) = h.U
	let mut hw = hm
	hw["a"] = hn + h.N
	report("named tuple omitted field", len(hw) == 1)

	let gi P[string]
	let (gs, gm) = gi
	let mut gw = gm
	gw["k"] = gs
	report("generic named tuple", len(gw) == 1)

	let g = G{K: 2}
	let (qs, qm) = g.Q
	let mut qw = qm
	qw["k"] = qs
	report("generic named tuple field", len(qw) == 1 && g.K == 2)

	report("generic function", inGeneric(5) == 1 && inGeneric("s") == 1)
}
`, "named tuple let\nnamed tuple package let\nnamed tuple omitted field\ngeneric named tuple\ngeneric named tuple field\ngeneric function\n")
}

// TestErgoRunDefaultsShareNoMap: every default builds new Maps (stage D2
// round 2 review M2): two lets of one record type, the two fields of one
// type in a record, a package let and a local, and two omitted fields of a
// literal each get their own Map, though one generated function builds
// them all.
func TestErgoRunDefaultsShareNoMap(t *testing.T) {
	wantRun(t, `type R { M Map[string, int] }
type O { A R, B R }

let pr R

func main() needs Console {
	let mut a R
	let b R
	a.M["x"] = 1
	let mut o O
	o.A.M["y"] = 1
	let mut p = O{}
	p.B.M["z"] = 1
	if len(a.M) == 1 && len(b.M) == 0 && len(o.B.M) == 0 && len(p.A.M) == 0 && len(pr.M) == 0 {
		Console.Println("independent")
	}
}
`, "independent\n")
}

// TestErgoRunMapWriteTargets: a Map write fixes its Map, left to right,
// before a key whose evaluation writes statements (spec D4): when the key
// reassigns the Map variable, the write still reaches the Map the variable
// held, not the new one. held is a copy taken before the write (value-rule:
// a Map is a value), so it keeps its one entry. A Map in a record field and
// in a slice element is a write target too.
func TestErgoRunMapWriteTargets(t *testing.T) {
	wantRun(t, `type Failed { Code int }
type R { M Map[string, int] }

func key(ok bool) string fails Failed needs Console {
	Console.Println("key")
	if ok {
		return "k"
	}
	Failed{Code: 1}
}

func main() needs Console {
	let mut m = Map[string, int]{"x": 0}
	let held = m
	m[key(false) catch {
	case Failed:
		m = Map[string, int]{}
		"z"
	}] = 1
	if len(held) == 1 && len(m) == 0 {
		Console.Println("map fixed first")
	}
	let mut r = R{}
	r.M[key(true) catch { case Failed: "z" }] = 2
	let mut rs = []R{R{}, R{}}
	rs[1].M["a"] = 3
	if len(r.M) == 1 && len(rs[0].M) == 0 && len(rs[1].M) == 1 {
		Console.Println("field and element")
	}
}
`, "key\nmap fixed first\nkey\nfield and element\n")
}

// TestErgoRunQueueValues: queue.Values() is the runtime's Values with the
// caller's task, its Go iter.Seq adapted to the Ergo Seq shape at the call
// (stage D3b, Rule 3; D2 r1 review m1). A for over it ends when the queue
// is closed and empty, and a break leaves the values not yet popped in the
// queue.
func TestErgoRunQueueValues(t *testing.T) {
	wantRun(t, `import queue

func fill(q queue.Queue[int], n int) fails queue.Closed {
	for i in 0..n {
		try q.Push(i)
	}
	q.Close()
}

func sum(q queue.Queue[int]) int {
	let mut s = 0
	for v in q.Values() {
		s += v
	}
	s
}

func main() fails queue.Closed needs Console {
	let q = queue.New[int](8)
	try fill(q, 5)
	if sum(q) == 10 {
		Console.Println("values")
	}
	let r = queue.New[int](8)
	try fill(r, 5)
	for v in r.Values() {
		if v == 1 {
			break
		}
	}
	let left = try r.Pop()
	if left == 2 {
		Console.Println("break keeps the rest")
	}
}
`, "values\nbreak keeps the rest\n")
}

// TestStdOperationThroughDefinedTypeOrPointerRefused: a type defined over a
// std operation type, at any depth, is refused at its definition (the D3a
// rewrite ruling, item 2), once, and its uses add nothing; an operation
// called through a pointer to an operation type is S108 at the call (stage
// D3b, Rule 5). Neither reaches the lowering.
func TestStdOperationThroughDefinedTypeOrPointerRefused(t *testing.T) {
	cases := []struct{ name, src, diag, text string }{
		{"defined", "import queue\n\ntype MyQ queue.Queue[int]\n\nfunc f(q MyQ) {\n\tq.Close()\n}\n", "3:10: S108", "use queue.Queue[int] directly"},
		{"defined-twice", "import queue\n\ntype MyQ queue.Queue[int]\ntype Q2 MyQ\n\nfunc f(q Q2) {\n\tq.Close()\n}\n", "3:10: S108", "use queue.Queue[int] directly"},
		{"pointer", "import queue\n\nfunc f(p *queue.Queue[int]) {\n\tp.Close()\n}\n", "4:2: S108", "an operation type is already a reference; call it on the value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := lowerSource("r", tc.src, false)
			if len(o.diags) != 1 || !strings.HasPrefix(o.diags[0].String(), tc.diag) || !strings.Contains(o.diags[0].Message, tc.text) {
				t.Fatalf("want one diagnostic %s %q; got %v", tc.diag, tc.text, o.diags)
			}
		})
	}
}

// TestRangeLowersWithoutRefusal:no for-in, `a..b` or Map write is refused
// by the lowering any more (the stage D refusals are gone).
func TestRangeLowersWithoutRefusal(t *testing.T) {
	o := lowerSource("r", "func f(xs []int, m Map[int, int]) int {\n\tlet mut s = 0\n\tlet mut w = m\n\tfor i, x in xs {\n\t\tw[i] = x\n\t}\n\tfor v in w {\n\t\ts += v\n\t}\n\tlet r = 0..len(xs)\n\tfor i in r {\n\t\ts += i\n\t}\n\ts\n}\n", false)
	if len(o.diags) != 0 || len(o.refused) != 0 || o.panicked != nil {
		t.Fatalf("want a clean lowering; got diags %v, refusals %v, panic %v", o.diags, o.refused, o.panicked)
	}
	if !strings.Contains(o.mainGo(), "range") {
		t.Fatalf("want a Go range loop in:\n%s", o.mainGo())
	}
}
