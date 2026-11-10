package lower

import (
	"strings"
	"testing"
)

// Stage D2 (check-v1): one for over a slice, a Map, a Range and a Seq
// (D-D4), `a..b` as a Range[T] value, a Map write, and len (D-D5). The
// refusals are the checker's (internal/sketch/check stage_d2_test.go);
// every form the stage admits runs here through the public CLI.

// TestErgoRunSeq: a for over a Seq[T] built by conversion from a function
// literal runs the body as yield; a break makes yield return false, so a
// lawful Seq stops (the Seq counts its yields); a Seq value is also called
// directly, through its declared function type.
func TestErgoRunSeq(t *testing.T) {
	needsBuild(t)
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

// TestErgoRunDefaultMapIsWritable: the default of a Map is an empty,
// writable Map wherever the lowering produces a default (D-D4, the Map
// rule): a local and a package let with a Map type and no value, a defined
// type over a Map and a tuple's Map. A record has no default (types.md,
// Zero values 2 and 3). Each is written, then counted with len. Go's nil
// map panics on the first write.
func TestErgoRunDefaultMapIsWritable(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type Bag Map[string, int]

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

	let mut g Bag
	g["x"] = 1
	report("defined Map", len(g) == 1)

	let tup (int, Map[string, int])
	let n, tm = tup
	let mut tw = tm
	tw["a"] = n
	report("tuple", len(tw) == 1)
}
`, "local let\npackage let\ndefined Map\ntuple\n")
}

// TestStdOperationThroughDefinedTypeOrPointerRefused: a type defined over a
// std operation type, at any depth, is refused at its definition (the D3a
// rewrite ruling, item 2), once, and its uses add nothing; an operation
// called through a pointer to an operation type is S108 at the call (stage
// D3b, Rule 5). Neither reaches the lowering.
func TestStdOperationThroughDefinedTypeOrPointerRefused(t *testing.T) {
	cases := []struct{ name, src, diag, text string }{
		{"defined", "import queue\n\ntype MyQ queue.Queue[int]\n\nfunc f(q MyQ) {\n\tq.CloseSend()\n}\n", "3:10: S108", "use queue.Queue[int] directly"},
		{"defined-twice", "import queue\n\ntype MyQ queue.Queue[int]\ntype Q2 MyQ\n\nfunc f(q Q2) {\n\tq.CloseSend()\n}\n", "3:10: S108", "use queue.Queue[int] directly"},
		{"pointer", "import queue\n\nfunc f(p *queue.Queue[int]) {\n\tp.CloseSend()\n}\n", "4:2: S108", "an operation type is already a reference; call it on the value"},
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
