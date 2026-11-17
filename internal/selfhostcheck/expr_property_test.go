package selfhostcheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/testwait"
)

// TestCompilerExprProperty is S3d's seeded property for expression types:
// each case is a package whose functions' bodies use the forms
// compiler/check/expr types (lets written and inferred, constants in
// typed contexts, records, composites and selectors, slices, ranges,
// conditions, function literals, addresses, as-patterns, string
// interpolation, and generic calls on locals and literals). Each
// expression exprdump types has the Go checker's type, each call it
// infers the Go checker's type arguments, and the run fails unless every
// form in exprForms is typed somewhere. A failing seed prints the source,
// both outputs and the replay command.
//
// It builds Go because exprdump is the Ergo program under test: only its
// lowered, built form runs over the generated cases.
func TestCompilerExprProperty(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := exprDump(t, ctx)
	base := uint32(1)
	if v := os.Getenv(seedVariable); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			t.Fatalf("%s=%q: %v", seedVariable, v, err)
		}
		base = uint32(n)
	}
	replay := func(seed uint32) string {
		return "replay: " + seedVariable + "=" + strconv.FormatUint(uint64(seed), 10) + " go test -count=1 -run '^TestCompilerExprProperty$' ./internal/selfhostcheck (the first case is the failing one)"
	}
	tmp := t.TempDir()
	file := filepath.Join(tmp, "p.ergo")
	e, f := filepath.Join(tmp, "ext"), filepath.Join(tmp, "foreign")
	reached := map[string]int{}
	ran, failed, refused := 0, 0, ""
	for seed := base; seed < base+200; seed++ {
		src := genExpr(seed)
		pkg, ds := check.CheckSource(src)
		if pkg == nil || len(ds) > 0 {
			if refused == "" && len(ds) > 0 {
				refused = fmt.Sprintf("seed %d: %v\n%s", seed, ds[0], src)
			}
			continue
		}
		ran++
		want, forms := goExprTypes(pkg, "p.ergo")
		wantCalls := goCallArgs(pkg, "p.ergo")
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		writeLines(t, e, ext)
		writeLines(t, f, foreign)
		writeLines(t, file, []string{src})
		got := runDump(t, ctx, bin, e, f, file)
		gotCalls := runDump(t, ctx, bin, "-calls", e, f, file)
		d := subsetDiff(want, got, 3) + subsetDiff(wantCalls, gotCalls, 2)
		if d != "" {
			failed++
			if failed <= 3 {
				t.Errorf("seed %d: exprdump differs from the Go checker (go -, ergo +):\n%s--- source\n%s\n%s", seed, d, src, replay(seed))
			}
		}
		for _, l := range got {
			// The syntax form alone (formOf adds what a selector or a
			// call names).
			form, _, _ := strings.Cut(forms[keyOf(l, 3)], " ")
			reached[form]++
		}
		if len(gotCalls) > 0 {
			reached["generic call"]++
		}
	}
	if ran < 150 {
		t.Errorf("the Go checker refused %d of 200 cases (%s); first:\n%s", 200-ran, replay(base), refused)
	}
	for _, form := range exprForms {
		if reached[form] == 0 {
			t.Errorf("no case typed a %s; the generator or the typer lost a form (%s)", form, replay(base))
		}
	}
}

// exprForms are the syntax forms (and inferred generic calls) the cases
// must get typed.
var exprForms = []string{"Ident", "BasicLit", "BinaryExpr", "UnaryExpr", "ParenExpr", "CallExpr", "CompositeLit", "SelectorExpr", "IndexExpr", "InterpolatedString", "FuncLit", "generic call"}

// genExpr is the source of the expression case seed: a few functions whose
// bodies draw statements from a pool of the typer's forms, each local
// folded into an accumulator so every name is used.
func genExpr(seed uint32) string {
	r := &rng{s: seed}
	lines := []string{
		"type R { n int, s string, f float64 }",
		"type Shape = Dot | Sq{W int}",
		"type Dot {}",
		"type Box[T] { v T }",
		"",
		"func id[T](x T) T { x }",
		"func first[T](xs []T, d T) T { if len(xs) > 0 { xs[0] } else { d } }",
		"func boxed[T](x T) Box[T] { Box[T]{v: x} }",
		"func twice(x int) int { x * 2 }",
		"",
	}
	k := func() string { return strconv.Itoa(1 + r.n(9)) }
	for fn := range 1 + r.n(3) {
		var body []string
		add := func(s ...string) { body = append(body, s...) }
		add("\tlet mut acc = " + k())
		v := 0
		local := func() string { v++; return fmt.Sprintf("v%d", v) }
		match := r.n(6)
		for i := range 3 + r.n(8) {
			if i == match {
				s := local()
				add("\tmatch sh {", "\tcase Sq as "+s+":", "\t\tacc = acc + "+s+".W", "\tcase Dot:", "\t\tacc = acc - "+k(), "\t}")
				continue
			}
			x := local()
			switch r.n(18) {
			case 0:
				add("\tlet "+x+" = p + "+k(), "\tacc = acc + "+x)
			case 1:
				add("\tlet "+x+" int64 = "+k(), "\tif "+x+" > "+k()+" {", "\t\tacc = acc + 1", "\t}")
			case 2:
				add("\tlet "+x+" = r.n * "+k(), "\tacc = acc + "+x)
			case 3:
				add("\tlet "+x+" = R{n: "+k()+", s: \"a\", f: 1.5}", "\tacc = acc + "+x+".n + len("+x+".s)")
			case 4:
				add("\tlet "+x+" = xs[0] + xs[len(xs)-1]", "\tacc = acc + "+x)
			case 5:
				e := local()
				add("\tfor "+x+", "+e+" in xs {", "\t\tacc = acc + "+e+" - "+x, "\t}")
			case 6:
				add("\tif p > "+k()+" && q != \"x\" || !(acc < "+k()+") {", "\t\tacc = acc + 1", "\t}")
			case 7:
				add("\tlet mut "+x+"c = r", "\t"+x+"c.n = "+x+"c.n + 1", "\tlet "+x+" = &"+x+"c", "\tacc = acc + "+x+".n")
			case 8:
				add("\tlet "+x+" = "+pick(r, []string{"id(p)", "id(acc)", "id(" + k() + ")", "first(xs, " + k() + ")", "first(xs, p)", "boxed(p).v", "id[int](" + k() + ")"}), "\tacc = acc + "+x)
			case 9:
				add("\tlet "+x+" = "+pick(r, []string{"id(q)", "id(\"s\")", "first([]string{q}, \"d\")", "boxed(q).v"}), "\tacc = acc + len("+x+")")
			case 10:
				add("\tlet "+x+" func(int) int = func(y int) int { y + "+k()+" }", "\tacc = acc + "+x+"(acc)")
			case 11:
				add("\tlet "+x+" = \"n=\\(p) q=\\(q)\"", "\tacc = acc + len("+x+")")
			case 12:
				add("\tlet "+x+" = -"+k(), "\tacc = acc + "+x+" + (p + "+k()+") * 2")
			case 13:
				add("\tlet "+x+" = []string{q, \"z\"}", "\tacc = acc + len("+x+")")
			case 14:
				add("\tlet "+x+" = 1.5 * r.f + 2.0", "\tif "+x+" > 3.0 {", "\t\tacc = acc + 1", "\t}")
			case 15:
				add("\tlet "+x+" = twice(acc) - twice("+k()+")", "\tacc = acc + "+x)
			case 16:
				add("\tlet "+x+" = []int{p, "+k()+", acc}", "\tacc = acc + "+x+"[1]")
			case 17:
				add("\tlet "+x+" = Box[string]{v: q}", "\tacc = acc + len("+x+".v)")
			}
		}
		add("\tacc + " + k())
		lines = append(lines, fmt.Sprintf("func f%d(p int, q string, r R, xs []int, sh Shape) int {", fn))
		lines = append(lines, body...)
		lines = append(lines, "}", "")
	}
	lines = append(lines, "func main() {}", "")
	return strings.Join(lines, "\n")
}
