package lower

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestNamingWorkIsLinear: a counted bound on the lowering's work, not a
// time. For a function of n calls, each of which needs a temporary of one
// base, the namer tests at most two candidate names per name it gives, so
// its work grows linearly in n; every function body is lowered exactly
// once and go/format runs once, so naming lowers no body (findings M2).
func TestNamingWorkIsLinear(t *testing.T) {
	for _, n := range []int{500, 4000} {
		var src strings.Builder
		src.WriteString("func g(k int) int { return k }\n\nfunc main() needs Console {\n\tlet mut acc = 0\n")
		for k := 0; k < n; k++ {
			fmt.Fprintf(&src, "\tacc = acc + g(%d)\n", k%10)
		}
		src.WriteString("\tif acc > 0 {\n\t\tConsole.Println(\"ok\")\n\t}\n}\n")
		file, err := syntax.Parse(src.String())
		if err != nil {
			t.Fatal(err)
		}
		pkg, diags := check.Check("main", []*syntax.File{file}, nil)
		if len(diags) > 0 {
			t.Fatalf("n=%d: %v", n, diags)
		}
		l := newLowerer(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src.String()}}})
		if _, err := l.program(); err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		w := l.names.work
		t.Logf("n=%d: %d names given, %d candidates tested, %d bodies lowered, %d go/format runs", n, w.allocs, w.probes, l.bodies, l.formats)
		if w.allocs < n {
			t.Errorf("n=%d: only %d names given; the program needs a temporary per call", n, w.allocs)
		}
		if w.probes > 2*w.allocs {
			t.Errorf("n=%d: %d candidates tested for %d names, more than two per name", n, w.probes, w.allocs)
		}
		if l.bodies != 2 || l.formats != 1 {
			t.Errorf("n=%d: %d bodies lowered and %d go/format runs, want 2 and 1", n, l.bodies, l.formats)
		}
	}
}

// TestDefaultWorkIsLinear: a counted bound on the work and the output of
// defaults, not a time (stage D2 round 2 review M2). A chain of n defined
// tuples, each holding the one before and the first a Map, and a diamond,
// each holding the one before twice (a record has no default: types.md,
// Zero values 3), get one holdsMap visit decision per
// type and one generated default function per type: the holdsMap calls and
// the generated Go bytes stay under a fixed multiple of the type count. A
// walk that restarts per default is quadratic on the chain; inlining each
// default's literal is exponential on the diamond (2^20 copies of R0).
func TestDefaultWorkIsLinear(t *testing.T) {
	chain := func(n int) string {
		var src strings.Builder
		src.WriteString("type R0 (int, Map[string, int])\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&src, "type R%d (int, R%d)\n", i, i-1)
		}
		fmt.Fprintf(&src, "func main() {\n\tlet r R%d\n\t_ = r\n}\n", n-1)
		return src.String()
	}
	diamond := func(n int) string {
		var src strings.Builder
		src.WriteString("type R0 (int, Map[string, int])\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&src, "type R%d (R%d, R%d)\n", i, i-1, i-1)
		}
		fmt.Fprintf(&src, "func main() {\n\tlet r R%d\n\t_ = r\n}\n", n-1)
		return src.String()
	}
	cases := []struct {
		name  string
		types int
		src   string
	}{
		{"chain", 100, chain(100)},
		{"chain", 200, chain(200)},
		{"chain", 400, chain(400)},
		{"diamond", 21, diamond(21)},
	}
	for _, c := range cases {
		file, err := syntax.Parse(c.src)
		if err != nil {
			t.Fatal(err)
		}
		pkg, diags := check.Check("main", []*syntax.File{file}, nil)
		if len(diags) > 0 {
			t.Fatalf("%s %d: %v", c.name, c.types, diags)
		}
		l := newLowerer(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: c.src}}})
		code, err := l.program()
		if err != nil {
			t.Fatalf("%s %d: %v", c.name, c.types, err)
		}
		t.Logf("%s of %d types: %d holdsMap calls, %d Go bytes", c.name, c.types, l.holdsVisits, len(code))
		if l.holdsVisits > 8*c.types {
			t.Errorf("%s of %d types: %d holdsMap calls, more than 8 per type", c.name, c.types, l.holdsVisits)
		}
		if len(code) > 4000+300*c.types {
			t.Errorf("%s of %d types: %d Go bytes, more than 4000 + 300 per type", c.name, c.types, len(code))
		}
		if got := strings.Count(string(code), "\nfunc ergoDefault"); got != c.types {
			t.Errorf("%s of %d types: %d generated default functions, want one per type", c.name, c.types, got)
		}
	}
}
