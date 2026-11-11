package lower

import (
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// paysSource calls each function once off a loop and once on a loop path.
// classify is a Go leaf with a boxed result (Tok has five fields, so it
// is an interface) larger than goSizeBudget; tiny is one within it;
// widest is generic (no Go leaf) and larger; first is generic and within
// it; outer is small and calls widest, so widest's site in outer's
// inlined body is on the loop path of outer's site; apply1 substitutes
// its literal, whose body calls widest. step is a machine's step, a boxed
// leaf larger than goSizeBudget, called through the field of its known
// record by once (off a loop) and by std's From (in its loop).
const paysSource = `import machines

type Num { v int }
type Word { s string }
type Op { c string }
type Sep { at int }
type Tok = Num | Word | Op | Sep

func classify(x int) Tok {
	if x < 0 {
		return Sep{at: x}
	}
	if x % 3 == 0 {
		return Num{v: x * 2 + 1}
	}
	if x % 3 == 1 {
		return Word{s: "w\(x)"}
	}
	Op{c: "o\(x)"}
}

func tiny(x int) Tok { Num{v: x} }

func first[T](xs []T, d T) T {
	if len(xs) == 0 {
		return d
	}
	xs[0]
}

func widest[T](xs []T, d T, k int) T {
	if len(xs) == 0 {
		return d
	}
	if k < 0 {
		return xs[0]
	}
	if k >= len(xs) {
		return xs[len(xs) - 1]
	}
	xs[k]
}

func outer(xs []int, k int) int { widest(xs, 0, k) + 1 }

func apply1(f func(x int) int, x int) int { f(x) }

func weight(t Tok) int {
	match t {
	case Num as n:
		n.v
	case Word as w:
		len(w.s)
	case Op as o:
		len(o.c) + 1
	case Sep as s:
		s.at
	}
}

type Count { n int }
type Halt {}
type St = Count | Halt
type Tick { d int }
type Sig = Tick

func step(s St, e Sig) Decision[St, int] {
	match s {
	case Halt:
		Stay[St]{Next: s}
	case Count as c:
		if e.d < 0 {
			return Done[int]{Output: c.n}
		}
		if e.d == 0 {
			return Stay[St]{Next: Count{n: c.n}}
		}
		Stay[St]{Next: Count{n: c.n + e.d * 2 + 1}}
	}
}

type M machine[St, Sig] {
	initial: Count{n: 0},
	step:    step,
}

func once[S, E, O, F, R, W](m Machine[S, E, O, F, R, W], s S, e E) Decision[S, O] fails F needs R {
	let d = try m.Step(s, e)
	d
}

func main() needs Console {
	let xs = []int{4, 5, 6}
	let mut sum = first(xs, 0) + widest(xs, 0, 1) + weight(classify(3)) + weight(tiny(1)) + outer(xs, 2)
	sum = sum + apply1(func(x int) int { widest(xs, 0, x) }, 1)
	for i in 0..3 {
		sum = sum + first(xs, i) + widest(xs, 0, i) + weight(classify(i)) + weight(tiny(i)) + outer(xs, i)
		sum = sum + apply1(func(x int) int { widest(xs, 0, x) }, i)
	}
	match once(M{}, Count{n: 1}, Tick{d: 2}) {
	case Stay[St]:
		sum = sum + 1
	case _:
	}
	let o = machines.Run(M{}, []Sig{Tick{d: 1}, Tick{d: 0}, Tick{d: -1}}) catch {
	case machines.Rejected:
		return
	}
	match o {
	case machines.Finished[St, int] as f:
		sum = sum + f.Output
	case machines.Running[St, int]:
	}
	Console.Println("\(sum)")
}
`

// TestInlinePays holds the size test of the inline rule (inline.go, Size):
// a body larger than Go's inline budget is inlined only on a loop path,
// and a Go leaf with a boxed result only there and only when Go would
// refuse it. It reads the one promised fact, how many Go calls of each
// function the lowered program keeps, under the rule and under the
// reference (inlineCostLowering "budget"), which inlines every site the
// rule before OPT8 admitted.
func TestInlinePays(t *testing.T) {
	if n := inlineSizeOf(t, "classify"); n <= goSizeBudget {
		t.Fatalf("classify has %d nodes, want more than goSizeBudget (%d)", n, goSizeBudget)
	}
	calls := func(code, name string) int {
		return len(regexp.MustCompile(`\b`+name+`(Known)?(_[0-9]+)?(\[[^\]]*\])?\(`).FindAllString(code, -1))
	}
	for _, mode := range []struct {
		rule string
		want map[string]int
	}{
		// Off the loop: widest stays a call twice (main's site, and its
		// site in outer's inlined body) and in the literal's body once;
		// classify and tiny stay calls; on the loop path, only tiny does,
		// since Go inlines it. once's copy calls step; From's inlines it.
		{"", map[string]int{"first": 0, "widest": 3, "classify": 1, "tiny": 2, "outer": 0, "apply1": 0, "onceM:step": 1, "FromM:step": 0}},
		{"budget", map[string]int{"first": 0, "widest": 0, "classify": 0, "tiny": 0, "outer": 0, "apply1": 0, "onceM:step": 0, "FromM:step": 0}},
	} {
		saved := inlineCostLowering
		inlineCostLowering = mode.rule
		o := mustLower(t, "inlinepays"+mode.rule, paysSource)
		inlineCostLowering = saved
		code := o.mainGo()
		for key, want := range mode.want {
			fn, name, ok := strings.Cut(key, ":")
			if !ok {
				fn, name = "main_1", key // Go's main is the entry; Ergo's main is main_1
			}
			body := goFunc(t, code, fn)
			if got := calls(body, name); got != want {
				t.Errorf("rule %q: %s calls %s %d times, want %d\n%s", mode.rule, fn, name, got, want, body)
			}
		}
	}
}

// inlineSizeOf is the inlineSize of the body of paysSource's function name.
func inlineSizeOf(t *testing.T, name string) int {
	t.Helper()
	f, err := syntax.Parse(paysSource)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*syntax.FuncDecl); ok && fd.Name.Name == name && fd.Body != nil {
			return inlineSize(fd.Body)
		}
	}
	t.Fatalf("no function %s", name)
	return 0
}
