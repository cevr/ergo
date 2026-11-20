package selfhostcheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/testwait"
)

// TestCompilerInferLawsRun runs the laws of compiler/check/infer (capstone
// S3d) through `ergo test`: InferRecoversBindings, InferRejectsMismatches,
// FramesNest and the counted guard InferStepsPerCallAreConstant.
//
// It builds Go because `ergo test` lowers the laws into a test program and
// runs it: the check-time evaluator runs an operation type's laws, not a
// package's top-level laws, so no in-process check runs these.
func TestCompilerInferLawsRun(t *testing.T) {
	needsBuild(t)
	runCompilerLaws(t, "./check/infer")
}

// TestCompilerInferAgreesWithChecker is S3d's corpus agreement: over every
// package folder of examples/ and std the Go checker checks, the type
// arguments compiler/check/infer infers for a call (cmd/inferdump) print
// as the Go checker's Info.Calls TypeArgs, on every comparable call (see
// goInfer). The types of other packages come from the Go side, as in the
// types agreement. The run logs how many generic calls it compared, and
// fails when it compared none.
//
// It builds Go because inferdump is the Ergo program under test:
// compiler/check/infer is Ergo source, and only its lowered, built form
// runs over a corpus.
func TestCompilerInferAgreesWithChecker(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := inferDump(t, ctx)
	tmp := t.TempDir()
	e, f := filepath.Join(tmp, "ext"), filepath.Join(tmp, "foreign")
	compared, generic, folders := 0, 0, 0
	for _, dir := range corpusDirs(t) {
		pkg := typesPackage(dir)
		if pkg == nil {
			continue
		}
		want, n := goInfer(pkg, "")
		generic += n
		if len(want) == 0 {
			continue
		}
		folders++
		compared += len(want)
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		writeLines(t, e, ext)
		writeLines(t, f, foreign)
		got := comparableOnly(want, runDump(t, ctx, bin, append([]string{"-std", stdDir(), e, f}, ergoFiles(t, dir)...)...))
		if d := setDiff(want, got); d != "" {
			t.Errorf("%s: inferred type arguments differ (go -, ergo +):\n%s", rel(dir), d)
		}
	}
	t.Logf("compared %d of %d calls to a package's own generic functions, in %d folders", compared, generic, folders)
	if compared == 0 {
		t.Fatal("no comparable generic call in the corpus")
	}
}

// TestCompilerInferProperty is S3d's seeded property, the Go checker the
// reference model. Each case is a package of generic functions whose
// parameters take their type parameters in every form unification walks
// (a bare parameter, a slice, an array, a map, a pointer, an instance, a
// function with or without a result and with an error row), and of
// callers that call them with arguments of known types: their own
// parameters, a generic caller's parameter type among them, and literals,
// with written type arguments now and then. inferdump must print the Go
// checker's type arguments for every call. The run fails unless every
// form of inferForms is reached.
//
// It builds Go because inferdump is the program under test.
func TestCompilerInferProperty(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := inferDump(t, ctx)
	base := uint32(1)
	if v := os.Getenv(seedVariable); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			t.Fatalf("%s=%q: %v", seedVariable, v, err)
		}
		base = uint32(n)
	}
	replay := func(seed uint32) string {
		return "replay: " + seedVariable + "=" + strconv.FormatUint(uint64(seed), 10) + " go test -count=1 -run '^TestCompilerInferProperty$' ./internal/selfhostcheck (the first case is the failing one)"
	}
	tmp := t.TempDir()
	file := filepath.Join(tmp, "p.ergo")
	e, f := filepath.Join(tmp, "ext"), filepath.Join(tmp, "foreign")
	forms := inferForms()
	reach := map[string]int{}
	ran, failed := 0, 0
	for seed := base; seed < base+200; seed++ {
		src := genInfer(seed)
		pkg, ds := check.CheckSource(src)
		if pkg == nil || len(ds) > 0 {
			continue
		}
		ran++
		want, _ := goInfer(pkg, "p.ergo")
		for name, re := range forms {
			if re.MatchString(src + "\n" + strings.Join(want, "\n")) {
				reach[name]++
			}
		}
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		writeLines(t, e, ext)
		writeLines(t, f, foreign)
		writeLines(t, file, []string{src})
		got := runDump(t, ctx, bin, e, f, file)
		if d := setDiff(want, got); d != "" {
			failed++
			if failed <= 3 {
				t.Errorf("seed %d: inferdump differs from the Go checker (go -, ergo +):\n%s--- source\n%s\n%s", seed, d, src, replay(seed))
			}
		}
	}
	if ran < 150 {
		t.Errorf("the Go checker refused %d of 200 cases (%s)", 200-ran, replay(base))
	}
	for name := range forms {
		if reach[name] == 0 {
			t.Errorf("the cases reached no %s; the generator lost a form (%s)", name, replay(base))
		}
	}
}

// inferForms are the forms the inference cases must reach, by the source
// or the Go checker's lines ("FILE OFFSET T1 ; T2").
func inferForms() map[string]*regexp.Regexp {
	return map[string]*regexp.Regexp{
		"slice":            regexp.MustCompile(`\(a\d \[\]T`),
		"array":            regexp.MustCompile(`a\d \[3\]T`),
		"map":              regexp.MustCompile(`a\d Map\[string, T\]`),
		"pointer":          regexp.MustCompile(`a\d \*T`),
		"instance":         regexp.MustCompile(`a\d Pair\[T, U\]`),
		"row instance":     regexp.MustCompile(`a\d Job\[T, F\]`),
		"func result":      regexp.MustCompile(`a\d func\(T\) U`),
		"error row":        regexp.MustCompile(`fails F`),
		"joined row":       regexp.MustCompile(`(?m)^p\.ergo \d+ .*main\.E0 \| main\.E1`),
		"no value":         regexp.MustCompile(`(?m)^p\.ergo \d+ .*no value`),
		"caller parameter": regexp.MustCompile(`(?m)^p\.ergo \d+ (.* ; )?X( ;|$)`),
		"literal":          regexp.MustCompile(`(?m)^\tg\d+\(.*(\b1\b|"s"|2\.5|'a')`),
		"written types":    regexp.MustCompile(`(?m)^\tg\d+\[`),
		"written float":    regexp.MustCompile(`(?m)^\tg\d+\[float64.*\b1\b`),
	}
}

// genInfer is one inference case of seed: generic functions g0, g1, …
// and callers c0, c1, … that call them. Each call picks the type
// arguments first and writes each argument as a value of the parameter's
// type with them: a parameter of the caller of that type, or a literal
// whose default type it is.
func genInfer(seed uint32) string {
	r := &rng{s: seed}
	type callee struct {
		name   string
		params []string // the parameters' types, over T, U and F
	}
	forms := []string{"T", "T", "[]T", "[3]T", "Map[string, T]", "*T", "Pair[T, U]", "Box[T]", "func(T) U", "func(T)", "func() T", "func() T fails F", "func() fails F", "Job[T, F]"}
	var gs []callee
	lines := []string{
		"type E0 { Why string }",
		"type E1 { Why string }",
		"type P { X int }",
		"type Box[T] { v T }",
		"type Pair[A, B] { a A, b B }",
		"type Job[T, F] { run func() T fails F }",
		"",
	}
	for i := range 2 + r.n(4) {
		g := callee{name: fmt.Sprintf("g%d", i)}
		for range 1 + r.n(3) {
			g.params = append(g.params, pick(r, forms))
		}
		var tps []string
		all := strings.Join(g.params, " ")
		for _, tp := range []string{"T", "U", "F"} {
			if regexp.MustCompile(`\b` + tp + `\b`).MatchString(all) {
				tps = append(tps, tp)
			}
		}
		ps := make([]string, len(g.params))
		for j, pt := range g.params {
			ps[j] = fmt.Sprintf("a%d %s", j, pt)
		}
		lines = append(lines, fmt.Sprintf("func %s[%s](%s) {}", g.name, strings.Join(tps, ", "), strings.Join(ps, ", ")))
		gs = append(gs, g)
	}
	lines = append(lines, "")
	leaves := []string{"int", "string", "float64", "bool", "P", "Box[int]", "[]string", "Map[string, int]", "rune"}
	rows := []string{"E0", "E1", "E0 | E1"}
	for c := range 1 + r.n(3) {
		generic := r.next() < 0.4
		var params, calls []string
		for k := range 2 + r.n(4) {
			g := pick(r, gs)
			all := strings.Join(g.params, " ")
			bind := map[string]string{}
			for _, tp := range []string{"T", "U"} {
				switch {
				case generic && r.next() < 0.3:
					bind[tp] = "X"
				default:
					bind[tp] = pick(r, leaves)
				}
			}
			bind["F"] = pick(r, rows)
			// A result parameter that only func() T names may bind to no
			// value: the argument returns nothing.
			noValue := strings.Count(all, "T") == 1 && strings.Contains(all, "func() T") && !strings.Contains(all, "func() T fails") && r.next() < 0.5
			// Written type arguments, now and then: then an int literal
			// may stand where T is float64, which only the written type
			// argument decides.
			written := !noValue && r.next() < 0.2
			var args []string
			for j, pt := range g.params {
				lit := ""
				if pt == "T" && !noValue {
					switch bind["T"] {
					case "int":
						lit = "1"
					case "string":
						lit = "\"s\""
					case "float64":
						lit = "2.5"
						if written && r.next() < 0.5 {
							lit = "1"
						}
					case "rune":
						lit = "'a'"
					}
				}
				if lit != "" && r.next() < 0.5 {
					args = append(args, lit)
					continue
				}
				at := pt
				if noValue && pt == "func() T" {
					at = "func()"
				}
				// The row parameter first: its binding names no T or U. An
				// argument's own row may be another of the rows: a later
				// one widens what an earlier one bound.
				fArg := bind["F"]
				if r.next() < 0.5 {
					fArg = pick(r, rows)
				}
				at = regexp.MustCompile(`\bF\b`).ReplaceAllString(at, fArg)
				at = regexp.MustCompile(`\bT\b`).ReplaceAllString(at, bind["T"])
				at = regexp.MustCompile(`\bU\b`).ReplaceAllString(at, bind["U"])
				name := fmt.Sprintf("p%d_%d", k, j)
				params = append(params, name+" "+at)
				args = append(args, name)
			}
			fun := g.name
			if written {
				var ws []string
				// T and U only: a written row argument is S3d2's (a row
				// in a type argument's place resolves as a row there).
				for _, tp := range []string{"T", "U"} {
					if regexp.MustCompile(`\b` + tp + `\b`).MatchString(all) {
						ws = append(ws, bind[tp])
					}
				}
				if len(ws) > 0 {
					fun += "[" + strings.Join(ws[:1+r.n(len(ws))], ", ") + "]"
				}
			}
			calls = append(calls, "\t"+fun+"("+strings.Join(args, ", ")+")")
		}
		tps := ""
		if generic {
			tps = "[X]"
		}
		lines = append(lines, fmt.Sprintf("func c%d%s(%s) {", c, tps, strings.Join(params, ", ")))
		lines = append(lines, calls...)
		lines = append(lines, "}")
	}
	lines = append(lines, "", "func main() {}", "")
	return strings.Join(lines, "\n")
}

// inferDump builds compiler/cmd/inferdump.
func inferDump(t *testing.T, ctx context.Context) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "inferdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/inferdump"); err != nil {
		t.Fatalf("ergo build ./cmd/inferdump: %v\n%s", err, o)
	}
	return bin
}

// comparableOnly is the lines of got whose call ("FILE OFFSET") a line of
// want names: inferdump also prints calls the Go side leaves out.
func comparableOnly(want, got []string) []string {
	keys := map[string]bool{}
	for _, l := range want {
		keys[callKey(l)] = true
	}
	var out []string
	for _, l := range got {
		if keys[callKey(l)] {
			out = append(out, l)
		}
	}
	return out
}

func callKey(l string) string {
	fs := strings.SplitN(l, " ", 3)
	if len(fs) < 2 {
		return l
	}
	return fs[0] + " " + fs[1]
}

// goInfer is the Go checker's type arguments, "FILE OFFSET T1 ; T2 …", for
// each comparable call of pkg, and the count of all its calls by name to
// its own generic functions from a function declaration; a file with no
// name (check.CheckSource's) is unnamed. A call is
// comparable when:
//
//   - its callee is a generic function of pkg with no receiver, called by
//     name, with or without written type arguments, and no trailing block;
//   - it is in the body of a function declaration of pkg with no receiver
//     (inside a function literal there too);
//   - each argument is a basic literal, a signed numeric one, or a
//     parameter of that declaration;
//   - neither signature writes wired[T] of a T that is no bracket
//     parameter (S3t resolves no such signature yet).
func goInfer(pkg *check.Package, unnamed string) ([]string, int) {
	info := pkg.Info
	var out []string
	all := 0
	for _, caller := range pkg.Funcs {
		if caller.Decl == nil || caller.Decl.Recv != nil || caller.Decl.Body == nil || caller.Sig == nil {
			continue
		}
		file := fileName(caller)
		if file == "" {
			file = unnamed
		}
		// The body's parameters are the objects of the parameters'
		// names (Info.Defs), not the signature's Vars.
		params := map[check.Object]bool{}
		if caller.Decl.Type != nil && caller.Decl.Type.Params != nil {
			for _, f := range caller.Decl.Type.Params.List {
				for _, n := range f.Names {
					if o := info.Defs[n]; o != nil {
						params[o] = true
					}
				}
			}
		}
		syntax.Inspect(caller.Decl.Body, func(n syntax.Node) bool {
			x, ok := n.(*syntax.CallExpr)
			if !ok || x.Trailing != nil {
				return true
			}
			fun := unparenX(x.Fun)
			if ix, ok := fun.(*syntax.IndexExpr); ok {
				fun = unparenX(ix.X)
			}
			id, ok := fun.(*syntax.Ident)
			if !ok {
				return true
			}
			c := info.Calls[x]
			if c == nil || c.Func == nil || len(c.TypeArgs) == 0 || c.Func.Pkg != pkg || c.Func.Decl == nil || c.Func.Decl.Recv != nil {
				return true
			}
			all++
			if wiredOfType(caller) || wiredOfType(c.Func) {
				return true
			}
			for _, a := range x.Args {
				a = unparenX(a)
				if u, ok := a.(*syntax.UnaryExpr); ok && (u.Op == syntax.SUB || u.Op == syntax.ADD) {
					if b, ok := unparenX(u.X).(*syntax.BasicLit); ok && (b.Kind == syntax.INT || b.Kind == syntax.FLOAT || b.Kind == syntax.DURATION) {
						continue
					}
					return true
				}
				switch a := a.(type) {
				case *syntax.BasicLit:
					switch a.Kind {
					case syntax.INT, syntax.FLOAT, syntax.DURATION, syntax.STRING, syntax.RUNE:
					default:
						return true
					}
				case *syntax.Ident:
					if v, ok := info.Uses[a].(*check.Var); !ok || !params[v] {
						return true
					}
				default:
					return true
				}
			}
			ts := make([]string, len(c.TypeArgs))
			for i, a := range c.TypeArgs {
				ts[i] = a.String()
			}
			out = append(out, fmt.Sprintf("%s %d %s", file, id.Pos(), strings.Join(ts, " ; ")))
			return true
		})
	}
	return out, all
}

// fileName is the base name of the file that declares f.
func fileName(f *check.Func) string {
	fi := fieldOf(reflect.ValueOf(f), "file")
	if fi.IsNil() {
		return ""
	}
	sf, _ := fieldOf(fi, "syntax").Interface().(*syntax.File)
	if sf == nil || sf.Name == "" {
		return ""
	}
	return filepath.Base(sf.Name)
}
