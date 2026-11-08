package residue_test

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wallClockFloor is the shortest constant duration a test may compare a
// measured duration with. A gate host under load stretches a step of a few
// milliseconds into seconds, so a shorter bound is a verdict of the host's
// load, not of the code (fu-gate-flakes, batch 11: three gates in a row
// failed on three tests of that kind).
const wallClockFloor = 10 * time.Second

// wallClockAllow is the allow list: a row is `path<TAB>function<TAB>reason`.
const wallClockAllow = "testdata/wallclock-allow.txt"

// TestTestsCompareNoWallClock is the guard of fu-gate-flakes (c): no test
// outside a benchmark compares a measured duration (time.Since, a
// one-argument Sub, and what is computed from them in the same function)
// with another measured duration (a ratio of two timings) or with a
// constant duration under wallClockFloor. Such a comparison passes on a
// quiet host and fails on a loaded one. A row of the allow list admits one
// function of one file and says why; a row that admits nothing fails too.
func TestTestsCompareNoWallClock(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "*_test.go")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	allow := readWallClockAllow(t)
	used := map[string]bool{}
	files := 0
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.Contains("/"+name, "/testdata/") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		files++
		for _, c := range wallClockComparisons(t, name, src) {
			key := name + "\t" + c.function
			if _, ok := allow[key]; ok {
				used[key] = true
				continue
			}
			t.Errorf("%s: %s compares %s; a loaded host decides it (fu-gate-flakes). Bound the wait by an event, or by %v or more, or add `%s<TAB>%s<TAB>reason` to %s", c.at, c.function, c.what, wallClockFloor, name, c.function, wallClockAllow)
		}
	}
	if files < 100 {
		t.Fatalf("read %d tracked test files; the listing is broken", files)
	}
	for _, key := range staleRows(allow, used) {
		t.Errorf("%s: the row %q admits no comparison; remove it", wallClockAllow, strings.ReplaceAll(key, "\t", " "))
	}
}

// staleRows returns the rows of allow that admitted no comparison.
func staleRows(allow map[string]string, used map[string]bool) []string {
	var stale []string
	for key := range allow {
		if !used[key] {
			stale = append(stale, key)
		}
	}
	return stale
}

// TestWallClockStaleRow: a row whose comparison is gone fails the guard.
func TestWallClockStaleRow(t *testing.T) {
	allow := map[string]string{"a_test.go\tTestA": "r", "b_test.go\tTestB": "r"}
	if got := staleRows(allow, map[string]bool{"a_test.go\tTestA": true}); len(got) != 1 || got[0] != "b_test.go\tTestB" {
		t.Errorf("stale rows %q, want the unused row of TestB", got)
	}
}

// readWallClockAllow reads the allow list; each row must give a reason.
func readWallClockAllow(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(wallClockAllow)
	if err != nil {
		t.Fatal(err)
	}
	allow := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "\t")
		if len(fields) != 3 || fields[0] == "" || fields[1] == "" || strings.TrimSpace(fields[2]) == "" {
			t.Errorf("%s:%d: want `path<TAB>function<TAB>reason`, got %q", wallClockAllow, line, text)
			continue
		}
		allow[fields[0]+"\t"+fields[1]] = fields[2]
	}
	return allow
}

// wallClockComparison is one comparison the rule refuses.
type wallClockComparison struct {
	at, function, what string
}

// wallClockComparisons returns each comparison in src, outside a
// Benchmark function, of a measured duration with a measured duration or
// with a constant duration under wallClockFloor (zero left out: a sign
// test is no timing).
func wallClockComparisons(t *testing.T, name string, src []byte) []wallClockComparison {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	consts := fileConstants(file)
	var out []wallClockComparison
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || strings.HasPrefix(fn.Name.Name, "Benchmark") {
			continue
		}
		m := measuredIn(fn.Body, consts)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			b, ok := n.(*ast.BinaryExpr)
			if !ok {
				return true
			}
			switch b.Op {
			case token.LSS, token.GTR, token.LEQ, token.GEQ:
			default:
				return true
			}
			left, right := m.measured(b.X), m.measured(b.Y)
			var what string
			switch {
			case left && right:
				what = "two measured durations"
			case left || right:
				other := b.Y
				if right {
					other = b.X
				}
				d, ok := m.duration(other)
				if !ok || d == 0 || d >= wallClockFloor {
					return true
				}
				what = fmt.Sprintf("a measured duration with %v", d)
			default:
				return true
			}
			out = append(out, wallClockComparison{at: fset.Position(b.Pos()).String(), function: fn.Name.Name, what: what})
			return true
		})
	}
	return out
}

// fileConstants returns the constant expressions declared in the file, by
// name, at its top level and in its functions.
func fileConstants(file *ast.File) map[string]ast.Expr {
	consts := map[string]ast.Expr{}
	ast.Inspect(file, func(n ast.Node) bool {
		g, ok := n.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			return true
		}
		for _, spec := range g.Specs {
			v := spec.(*ast.ValueSpec)
			for i, name := range v.Names {
				if i < len(v.Values) {
					consts[name.Name] = v.Values[i]
				}
			}
		}
		return true
	})
	return consts
}

// measure knows, in one function, which names hold a measured duration and
// which name a function literal that returns one.
type measure struct {
	consts map[string]ast.Expr
	names  map[string]bool
	funcs  map[string]bool
}

// measuredIn marks the names of body that are assigned a measured value,
// until no more change: `elapsed := time.Since(start)`, `best = elapsed`,
// and `run := func() time.Duration { return time.Since(start) }`.
func measuredIn(body *ast.BlockStmt, consts map[string]ast.Expr) *measure {
	m := &measure{consts: consts, names: map[string]bool{}, funcs: map[string]bool{}}
	for changed := true; changed; {
		changed = false
		mark := func(set map[string]bool, name string) {
			if name != "_" && !set[name] {
				set[name] = true
				changed = true
			}
		}
		ast.Inspect(body, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range s.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok {
						continue
					}
					var rhs ast.Expr
					switch {
					case len(s.Rhs) == len(s.Lhs):
						rhs = s.Rhs[i]
					case len(s.Rhs) == 1:
						rhs = s.Rhs[0]
					default:
						continue
					}
					if lit, ok := rhs.(*ast.FuncLit); ok && m.returnsMeasured(lit) {
						mark(m.funcs, id.Name)
					} else if m.measured(rhs) {
						mark(m.names, id.Name)
					}
				}
			case *ast.ValueSpec:
				for i, id := range s.Names {
					if i < len(s.Values) && m.measured(s.Values[i]) {
						mark(m.names, id.Name)
					}
				}
			}
			return true
		})
	}
	return m
}

// returnsMeasured reports whether a return of lit (its own, not a nested
// literal's) gives a measured value.
func (m *measure) returnsMeasured(lit *ast.FuncLit) bool {
	found := false
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			for _, r := range s.Results {
				if m.measured(r) {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// measured reports whether e is a measured duration: time.Since(x), a
// one-argument Sub call, a name assigned one, a call of a literal that
// returns one, or arithmetic, a conversion or a Round or Truncate of one.
func (m *measure) measured(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.ParenExpr:
		return m.measured(e.X)
	case *ast.Ident:
		return m.names[e.Name]
	case *ast.BinaryExpr:
		switch e.Op {
		case token.ADD, token.SUB, token.MUL, token.QUO:
			return m.measured(e.X) || m.measured(e.Y)
		}
	case *ast.CallExpr:
		switch fun := e.Fun.(type) {
		case *ast.Ident:
			return m.funcs[fun.Name] || len(e.Args) == 1 && (fun.Name == "float64" || fun.Name == "int64") && m.measured(e.Args[0])
		case *ast.SelectorExpr:
			if pkg, ok := fun.X.(*ast.Ident); ok && pkg.Name == "time" && fun.Sel.Name == "Since" {
				return true
			}
			switch fun.Sel.Name {
			case "Sub":
				return len(e.Args) == 1
			case "Round", "Truncate":
				return m.measured(fun.X)
			}
		}
	}
	return false
}

// duration returns the value of e when it is a constant duration: an
// integer literal (nanoseconds), time.Nanosecond to time.Hour, a constant
// of the file, and products, sums and quotients of these.
func (m *measure) duration(e ast.Expr) (time.Duration, bool) {
	v, ok := m.value(e, 0)
	if !ok {
		return 0, false
	}
	n, exact := constant.Int64Val(constant.ToInt(v))
	return time.Duration(n), exact
}

func (m *measure) value(e ast.Expr, depth int) (constant.Value, bool) {
	if depth > 8 {
		return nil, false
	}
	units := map[string]time.Duration{"Nanosecond": time.Nanosecond, "Microsecond": time.Microsecond, "Millisecond": time.Millisecond, "Second": time.Second, "Minute": time.Minute, "Hour": time.Hour}
	switch e := e.(type) {
	case *ast.ParenExpr:
		return m.value(e.X, depth+1)
	case *ast.BasicLit:
		if e.Kind == token.INT || e.Kind == token.FLOAT {
			return constant.MakeFromLiteral(e.Value, e.Kind, 0), true
		}
	case *ast.SelectorExpr:
		if pkg, ok := e.X.(*ast.Ident); ok && pkg.Name == "time" {
			if u, ok := units[e.Sel.Name]; ok {
				return constant.MakeInt64(int64(u)), true
			}
		}
	case *ast.Ident:
		if c, ok := m.consts[e.Name]; ok {
			return m.value(c, depth+1)
		}
	case *ast.CallExpr:
		// time.Duration(n) and the like: a conversion of a constant.
		if len(e.Args) == 1 {
			if sel, ok := e.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Duration" {
				return m.value(e.Args[0], depth+1)
			}
		}
	case *ast.BinaryExpr:
		x, ok := m.value(e.X, depth+1)
		if !ok {
			return nil, false
		}
		y, ok := m.value(e.Y, depth+1)
		if !ok {
			return nil, false
		}
		switch e.Op {
		case token.MUL, token.ADD, token.SUB:
			return constant.BinaryOp(x, e.Op, y), true
		case token.QUO:
			if constant.Sign(y) == 0 {
				return nil, false
			}
			return constant.BinaryOp(x, e.Op, y), true
		}
	}
	return nil, false
}

// TestWallClockRule pins the rule on fixed sources.
func TestWallClockRule(t *testing.T) {
	for _, c := range []struct {
		body string
		bad  int
		name string
	}{
		{`if time.Since(start) > 2*time.Second { t.Fatal() }`, 1, "a measured duration against 2 s"},
		{`elapsed := time.Since(start); if elapsed > 5*time.Second { t.Fatal() }`, 1, "a name assigned a measurement"},
		{`if took := time.Since(began); took > time.Minute { t.Fatal() }`, 0, "a bound of a minute"},
		{`if time.Since(start) > 10*time.Second { t.Fatal() }`, 0, "a bound at the floor"},
		{`if time.Since(start) >= 9*time.Second + 999*time.Millisecond { t.Fatal() }`, 1, "a sum under the floor"},
		{`const bound = 1500 * time.Millisecond; if time.Since(start) > bound { t.Fatal() }`, 1, "a constant of the function"},
		{`if time.Since(start) > top { t.Fatal() }`, 0, "a constant of the file at 20 s"},
		{`if time.Since(start) > low { t.Fatal() }`, 1, "a constant of the file at 3 s"},
		{`d := end.Sub(start); if d < 300*time.Millisecond { t.Fatal() }`, 1, "a one-argument Sub"},
		{`if time.Since(start).Round(time.Millisecond) > time.Second { t.Fatal() }`, 1, "a rounded measurement"},
		{`run := func() time.Duration { s := time.Now(); work(); return time.Since(s) }; a, b := run(), run(); if a > 4*b { t.Fatal() }`, 1, "a ratio of two measurements by a literal"},
		{`var best time.Duration; for range 3 { d := time.Since(s); if best == 0 || d < best { best = d } }; if best > limit { t.Fatal() }`, 1, "a measurement against a measurement (d < best)"},
		{`left := time.Until(end); if left < 0 { left = 0 }`, 0, "time.Until against zero"},
		{`if time.Since(start) > 0 { t.Fatal() }`, 0, "a sign test"},
		{`if time.Since(start) > limit { t.Fatal() }`, 0, "an unknown bound (a known gap)"},
		{`t.Logf("took %v", time.Since(start))`, 0, "a log"},
		{`if x.Sub(a, b) > 3 { t.Fatal() }`, 0, "a Sub with two arguments"},
		{`if n < 2*time.Second { t.Fatal() }`, 0, "no measurement"},
	} {
		src := "package p\n\nconst top = 20 * time.Second\nconst low = 3 * time.Second\n\nfunc TestX(t *testing.T) {\n\t" + c.body + "\n}\n"
		if got := wallClockComparisons(t, "p_test.go", []byte(src)); len(got) != c.bad {
			t.Errorf("%s: %s: %d comparisons flagged (%v), want %d", c.name, c.body, len(got), got, c.bad)
		}
	}
	bench := "package p\n\nfunc BenchmarkX(b *testing.B) {\n\tif time.Since(start) > time.Second { b.Fatal() }\n}\n"
	if got := wallClockComparisons(t, "p_test.go", []byte(bench)); len(got) != 0 {
		t.Errorf("a benchmark: %d comparisons flagged, want 0", len(got))
	}
}
