package selfhostcheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
// form in exprForms is typed somewhere. exprdump reports no type error in
// a case the Go checker accepts; and in the case's bad twin (one statement
// of badStmts added, which the Go checker rejects) it reports one, each
// statement of the pool somewhere (S3d5). A failing seed prints the
// source, both outputs and the replay command.
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
	// The prelude is every case's import: its builtin.ergo declares the
	// operations the cases call on slices, strings, []byte and runes.
	group := preludeGroup(t, tmp)
	reached := map[string]int{}
	caught := map[int]int{}
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
		got, diags, lets := splitDiags(runDump(t, ctx, bin, append([]string{e, f, file}, group...)...))
		gotCalls := runDump(t, ctx, bin, append([]string{"-calls", e, f, file}, group...)...)
		if len(diags) > 0 {
			failed++
			if failed <= 3 {
				t.Errorf("seed %d: the Go checker accepts the case, exprdump reports type errors %v:\n--- source\n%s\n%s", seed, diags, src, replay(seed))
			}
		}
		// The bad twin, with the case's tables: its declarations are the
		// case's.
		bad, which := genBad(seed)
		if _, bds := check.CheckSource(bad); len(bds) > 0 {
			writeLines(t, file, []string{bad})
			_, badDiags, _ := splitDiags(runDump(t, ctx, bin, append([]string{e, f, file}, group...)...))
			if len(badDiags) == 0 {
				failed++
				if failed <= 3 {
					t.Errorf("seed %d: the Go checker rejects the bad twin (%v), exprdump reports no type error:\n--- source\n%s\n%s", seed, bds[0], bad, replay(seed))
				}
			} else {
				caught[which]++
			}
		}
		// Each value whose inputs the typer types has the Go checker's
		// type (CHECK-VALUE-TYPES); a generated case's are all typed.
		vd, excluded := valueDiff(goValues(pkg, "p.ergo"), got, lets, nil, forms)
		if len(excluded) > 0 {
			vd += fmt.Sprintf("excluded values %v\n", excluded)
		}
		d := subsetDiff(want, got, 3) + subsetDiff(wantCalls, gotCalls, 2) + vd
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
			if op := operationOf(src, l); op != "" {
				reached[op]++
			}
		}
		if len(gotCalls) > 0 {
			reached["generic call"]++
		}
		reached["package let"] += len(lets)
	}
	if ran < 150 {
		t.Errorf("the Go checker refused %d of 200 cases (%s); first:\n%s", 200-ran, replay(base), refused)
	}
	for _, form := range exprForms {
		if reached[form] == 0 {
			t.Errorf("no case typed a %s; the generator or the typer lost a form (%s)", form, replay(base))
		}
	}
	for i, st := range badStmts {
		if caught[i] == 0 {
			t.Errorf("no bad twin with %q was caught by both checkers; the generator or the typer lost a diagnostic (%s)", st, replay(base))
		}
	}
}

// splitDiags separates exprdump's "D …" lines (type errors) and "L …"
// lines (package lets' types) from its expression lines.
func splitDiags(lines []string) ([]string, []string, []string) {
	var types, diags, lets []string
	for _, l := range lines {
		if d, ok := strings.CutPrefix(l, "D "); ok {
			diags = append(diags, d)
		} else if d, ok := strings.CutPrefix(l, "L "); ok {
			lets = append(lets, d)
		} else {
			types = append(types, l)
		}
	}
	return types, diags, lets
}

// badStmts are statements with one error, each one the typer reports: a
// type error between basic types (an operand, an assignment, a condition,
// an argument, a written let type, an index, a constant of another kind),
// and a binding or call rule's.
var badStmts = []string{
	"\tacc = acc + q",
	"\tacc = acc + \"s\"",
	"\tacc = q",
	"\tif q {\n\t\tacc = acc + 1\n\t}",
	"\tacc = acc + twice(q)",
	"\tlet bad int = q\n\tacc = acc + bad",
	"\tacc = acc + xs[q]",
	"\tacc = acc + xs[1.5]",
	"\tacc = acc + r.f",
	"\tif p > 1.5 {\n\t\tacc = acc + 1\n\t}",
	// A name nothing declares, an assignment rooted at a binder no let mut
	// declares, and a call of another count of arguments (S3d6).
	"\tacc = acc + nope",
	"\tp = acc",
	"\tlet fixed = acc\n\tfixed = fixed + 1\n\tacc = acc + fixed",
	"\tr.n = acc",
	"\txs[0] = acc",
	"\tfor e in xs {\n\t\te = 1\n\t}",
	"\tacc = acc + twice(acc, 1)",
	"\tacc = acc + id(acc, 1)",
	"\tacc = acc + twice()",
}

// genBad is the case seed with one statement of badStmts, drawn by the
// seed, before the first function's final value; and the statement's
// index.
func genBad(seed uint32) (string, int) {
	src := genExpr(seed)
	which := int(seed % uint32(len(badStmts)))
	at := strings.Index(src, "\n\tacc + ")
	return src[:at] + "\n" + badStmts[which] + src[at:], which
}

// exprForms are the syntax forms (and inferred generic calls) the cases
// must get typed.
var exprForms = []string{"Ident", "BasicLit", "BinaryExpr", "UnaryExpr", "ParenExpr", "CallExpr", "CompositeLit", "SelectorExpr", "IndexExpr", "InterpolatedString", "FuncLit", "generic call", "slice operation", "string operation", "[]byte operation", "rune operation", "variant argument", "tuple let", "tuple pattern", "generic variant argument", "plain variant of a generic union", "bare record variant", "IfStmt", "SwitchStmt", "TypeSwitchStmt", "CatchExpr", "package let", "inherited law operation"}

// operationCall is a call of the language's operations the generator
// writes (case 30), and the receiver it reaches the operation from.
var operationCall = regexp.MustCompile(`^[\w\[\]{}, ]+\.(Append|Bytes|Text|RuneText)\(([\w{}: ]*)\)$`)

// tupleName is a name a tuple let of the generator binds (case 30): by
// names (vNta) or by a tuple pattern (vNpa).
var tupleName = regexp.MustCompile(`^v[0-9]+([tp])[ab]$`)

// operationOf is the receiver class of the typed call at line l ("FILE
// START END TYPE") of src, when it calls one of the language's operations
// the generator writes: a slice's Append, a string's Bytes, a []byte's
// Text or a rune's RuneText, and an Append of a variant ([]Shape's
// Append(Sq{…})) as "variant argument", one on []Option[int] as "generic
// variant argument" (Some) or "plain variant of a generic union" (None),
// []Shape's Append(Dot) as "bare record variant",
// and a use of a name a tuple let
// binds by names as "tuple let" and by a pattern as "tuple pattern"; the law's
// Prior(n) call is an inherited operation. Otherwise it returns "".
func operationOf(src, l string) string {
	f := strings.Fields(l)
	if len(f) < 3 {
		return ""
	}
	start, err1 := strconv.Atoi(f[1])
	end, err2 := strconv.Atoi(f[2])
	if err1 != nil || err2 != nil || start < 0 || end > len(src) || start >= end {
		return ""
	}
	if src[start:end] == "Prior(n)" {
		return "inherited law operation"
	}
	if m := tupleName.FindStringSubmatch(src[start:end]); m != nil {
		return map[string]string{"t": "tuple let", "p": "tuple pattern"}[m[1]]
	}
	m := operationCall.FindStringSubmatch(src[start:end])
	if m == nil {
		return ""
	}
	if m[1] == "Append" && strings.HasPrefix(src[start:end], "[]Option") && m[2] == "None" {
		return "plain variant of a generic union"
	}
	if m[1] == "Append" && strings.HasPrefix(src[start:end], "[]Option") {
		return "generic variant argument"
	}
	if m[1] == "Append" && m[2] == "Dot" {
		return "bare record variant"
	}
	if m[1] == "Append" && strings.ContainsRune(m[2], '{') {
		return "variant argument"
	}
	return map[string]string{"Append": "slice operation", "Bytes": "string operation", "Text": "[]byte operation", "RuneText": "rune operation"}[m[1]]
}

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
		"type Mark = Tick{N int} | Tock{N int}",
		"",
		"func id[T](x T) T { x }",
		"func first[T](xs []T, d T) T { if len(xs) > 0 { xs[0] } else { d } }",
		"func boxed[T](x T) Box[T] { Box[T]{v: x} }",
		"func twice(x int) int { x * 2 }",
		"func split(x int) (int, string) { (x * 2, \"s\") }",
		"",
		"type Oops {}",
		"",
		"func get(n int) int fails Oops {",
		"\tif n > 3 {",
		"\t\treturn Oops",
		"\t}",
		"\tn",
		"}",
		"",
		"func put(n int) fails Oops {",
		"\tif n > 3 {",
		"\t\treturn Oops",
		"\t}",
		"}",
		"",
		"let base = 3",
		"let label string = \"x\"",
		"let made = twice(2)",
		"let marked = if base > 2 { Tick{N: base} } else { Tock{N: 1} }",
		"",
		// A refined type derives its parse function; an operation type's
		// law calls its operations by their bare names.
		"type Pos int where it > 0",
		"",
		"type History[T] { Prior(n T) Option[T] }",
		"",
		"type Counter {",
		"\t..History[int],",
		"\tNext(n int) int,",
		"",
		"\tlaw Grows(n int) {",
		"\t\tlet m = if n > 0 { Next(n) } else { 0 }",
		"\t\tm >= 0 || n < 0",
		"\t},",
		"",
		"\tlaw PriorIsNonnegative(n int) {",
		"\t\tmatch Prior(n) {",
		"\t\tcase Some{Value: x}:",
		"\t\t\tx >= 0",
		"\t\tcase None:",
		"\t\t\ttrue",
		"\t\t}",
		"\t},",
		"}",
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
			switch r.n(31) {
			case 30:
				// The language's operations on a slice, a string, a
				// []byte and a rune (builtin.ergo, D-D8).
				switch r.n(6) {
				case 0:
					add("\tlet "+x+" = xs.Append(acc)", "\tacc = acc + len("+x+")")
				case 1:
					add("\tlet "+x+" = q.Bytes()", "\tacc = acc + len("+x+")")
				case 2:
					add("\tlet "+x+" = []byte{1, 2}.Text()", "\tacc = acc + len("+x+")")
				case 3:
					add("\tlet "+x+"r rune = "+k(), "\tlet "+x+" = "+x+"r.RuneText()", "\tacc = acc + len("+x+")")
				case 4:
					// A variant argument where T is bound to its union
					// (INFER-VARIANT-ARG): the receiver binds T first.
					// A generic union's variants too: Option[int]'s Some[int] and
					// None (INFER-VARIANT-GENERIC).
					call := []string{"[]Shape{sh}.Append(Dot)", "[]Shape{sh}.Append(Sq{W: acc})", "[]Option[int]{}.Append(Some{Value: acc})", "[]Option[int]{}.Append(None)"}[r.n(4)]
					add("\tlet "+x+" = "+call, "\tacc = acc + len("+x+")")
				case 5:
					// A let of a tuple's parts, by names or by a tuple pattern.
					if r.n(2) == 0 {
						add("\tlet "+x+"ta, "+x+"tb = split(acc)", "\tacc = acc + "+x+"ta + len("+x+"tb)")
					} else {
						add("\tlet ("+x+"pa, "+x+"pb) = split(acc)", "\tacc = acc + "+x+"pa + len("+x+"pb)")
					}
				}
			case 0:
				add("\tlet "+x+" = p + "+k(), "\tacc = acc + "+x)
			case 1:
				add("\tlet "+x+" int64 = "+k(), "\tif "+x+" > "+k()+" {", "\t\tacc = acc + xs["+x+"]", "\t}")
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
				add("\tlet mut "+x+"c = r", "\t"+x+"c.n = "+x+"c.n + 1", "\tlet "+x+" = &"+x+"c", "\t"+x+".n = acc", "\tacc = acc + "+x+".n")
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
			// The values CHECK-VALUE-TYPES types: a value if (one type, a
			// leaving arm, an Unreachable arm, a union join), a value
			// match, a value catch and a catch of no value, and the
			// package's lets.
			case 18:
				add("\tlet "+x+" = if p > "+k()+" { p } else { "+k()+" }", "\tacc = acc + "+x)
			case 19:
				add("\tlet "+x+" = if p > "+k()+" { "+k()+" } else { return acc }", "\tacc = acc + "+x)
			case 20:
				add("\tlet "+x+" = if q == \"a\" { acc } else { Unreachable(\"no\") }", "\tacc = acc + "+x)
			case 21:
				b := local()
				add("\tlet "+x+" = match sh {", "\tcase Sq as "+b+":", "\t\t"+b+".W", "\tcase Dot:", "\t\t"+k(), "\t}", "\tacc = acc + "+x)
			case 27:
				// A variant after its union keeps the union.
				b := local()
				add("\tlet "+x+" = if p > "+k()+" { marked } else { Tick{N: p} }", "\tmatch "+x+" {", "\tcase Tick as "+b+":", "\t\tacc = acc + "+b+".N", "\tcase Tock:", "\t}")
			// The calls and names CHECK-CALL-COVERAGE types: a refined
			// type's derived parse function, and a fieldless record's
			// name as a value.
			case 28:
				add("\tlet "+x+" = ParsePos(p) catch {", "\tcase Unsatisfied:", "\t\tPos(1)", "\t}", "\tacc = acc + int("+x+")")
			case 29:
				b := local()
				add("\tlet "+x+" = if p > "+k()+" { Dot } else { Sq{W: p} }", "\tmatch "+x+" {", "\tcase Sq as "+b+":", "\t\tacc = acc + "+b+".W", "\tcase Dot:", "\t}")
			case 26:
				add("\tput(p) catch {", "\tcase Oops:", "\t\tacc = acc + 1", "\t}")
			case 25:
				add("\tlet "+x+" = match p {", "\tcase "+k()+":", "\t\t"+k(), "\tcase _:", "\t\tp", "\t}", "\tacc = acc + "+x)
			case 22:
				add("\tlet "+x+" = get(p) catch {", "\tcase Oops:", "\t\t"+k(), "\t}", "\tacc = acc + "+x)
			case 23:
				b := local()
				add("\tlet "+x+" = if p > "+k()+" { Tick{N: p} } else { Tock{N: "+k()+"} }", "\tmatch "+x+" {", "\tcase Tick as "+b+":", "\t\tacc = acc + "+b+".N", "\tcase Tock:", "\t}")
			case 24:
				b := local()
				add("\tacc = acc + base + made + len(label)", "\tmatch marked {", "\tcase Tick as "+b+":", "\t\tacc = acc + "+b+".N", "\tcase Tock:", "\t}")
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
