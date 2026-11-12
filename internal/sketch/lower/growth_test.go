package lower

import (
	"regexp"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestInlineGrowth holds which sites the growth rule (growth.go) inlines,
// counted in the lowered Go of each function, under the rule and under its
// reference (growthLowering "function", 1600 nodes per Go function, which
// inlines every site here). mid's body has more than a fifth and at most a
// quarter of a path's allowance, so four sites fill a path and a fifth is
// refused; the test checks that premise. Mutants: growth-paths-shared,
// growth-prefix-dropped, growth-cold-ignored, growth-unbounded,
// growth-inlined-path-reset (lower/testdata/mutants.json).
func TestInlineGrowth(t *testing.T) {
	const src = `type Box[T] { items []T }

type Oops { n int }

func mid[T](b *Box[T], i int) T {
	if i < 0 || i >= len(b.items) {
		return b.items[0]
	}
	let a = b.items[i]
	let c = b.items[len(b.items) - 1 - i]
	if i == 1 {
		return c
	}
	if i == 2 {
		return a
	}
	if i == 3 {
		return b.items[i - 1]
	}
	if i == 4 {
		return b.items[i - 2]
	}
	if len(b.items) > 9 {
		return b.items[9]
	}
	a
}

func wrap[T](b *Box[T], i int) T { mid(b, i) }

func arms(b *Box[int], k int) int fails Oops {
	if k == 0 {
		return mid(b, 0) + mid(b, 1) + mid(b, 2) + mid(b, 0) + mid(b, 1)
	}
	if k == 1 {
		return mid(b, 1)
	}
	if k == 2 {
		return Oops{n: mid(b, 2)}
	}
	0
}

func prefix(b *Box[int]) int {
	let s = mid(b, 0) + mid(b, 1) + mid(b, 2) + mid(b, 0)
	if s > 0 {
		return s + mid(b, 1)
	}
	s
}

func nested(b *Box[int]) int {
	let s = mid(b, 0) + mid(b, 1) + mid(b, 2) + mid(b, 0)
	s + wrap(b, 1)
}

func main() needs Console {
	let mut b = Box[int]{items: []int{1, 2, 3}}
	b.items[0] = 4
	let v = arms(&b, 0) catch {
	case Oops as o:
		o.n
	}
	Console.Println("\(v) \(prefix(&b)) \(nested(&b))")
}
`
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	sizes := map[string]int{}
	for _, d := range file.Decls {
		if fd, ok := d.(*syntax.FuncDecl); ok && fd.Body != nil {
			sizes[fd.Name.Name] = inlineSize(fd.Body)
		}
	}
	if m, w := sizes["mid"], sizes["wrap"]; !(5*m > inlineGrowth && 4*m+w <= inlineGrowth && m <= inlineBudget) {
		t.Fatalf("premise: mid has %d nodes and wrap %d; want 4 mids and a wrap within %d, 5 mids past it, mid within %d", m, w, inlineGrowth, inlineBudget)
	}
	calls := func(body, name string) int {
		return len(regexp.MustCompile(`\b`+name+`(Known)?(_[0-9]+)?(\[[^\]]*\])?\(`).FindAllStringIndex(body, -1))
	}
	for _, ref := range []bool{false, true} {
		// The size rule (inline.go, Size; TestInlinePays) is held at its
		// reference, so every site here is decided by growth alone: mid is
		// larger than goSizeBudget, and these sites are on no loop path.
		saved, savedCost := growthLowering, inlineCostLowering
		growthLowering, inlineCostLowering = "", "budget"
		if ref {
			growthLowering = "function"
		}
		o := mustLower(t, "inlinegrowth", src)
		growthLowering, inlineCostLowering = saved, savedCost
		code := o.mainGo()
		for _, row := range []struct {
			fn, callee string
			want, ref  int
			why        string
		}{
			{"arms", "mid", 2, 0, "the fifth site of arm 0 is past its path's allowance, and the site of the cold arm 2 is a call; arm 1 keeps its allowance"},
			{"prefix", "mid", 1, 0, "the if block starts from the growth of the list around it"},
			{"nested", "wrap", 0, 0, "wrap fits the path"},
			{"nested", "mid", 1, 0, "the inlined wrap's site of mid spends the path of wrap's site"},
		} {
			want := row.want
			if ref {
				want = row.ref
			}
			if got := calls(goFunc(t, code, row.fn), row.callee); got != want {
				t.Errorf("reference %v: %s makes %d Go calls of %s, want %d (%s)\n%s", ref, row.fn, got, row.callee, want, row.why, goFunc(t, code, row.fn))
			}
		}
	}
}
