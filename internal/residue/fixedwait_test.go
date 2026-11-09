package residue_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fixedWaitAllow is the allow list of TestTestsWaitNoFixedTime: a row is
// `path<TAB>function<TAB>reason`.
const fixedWaitAllow = "testdata/fixedwait-allow.txt"

// TestTestsWaitNoFixedTime is the guard of fu-time-limits (Test rule: "No
// fixed time limits (wait for events)"). No test outside a benchmark waits
// a constant time: time.After, time.Sleep, time.NewTimer and time.AfterFunc
// of a constant, context.WithTimeout of one, a deadline of time.Now() plus
// one (context.WithDeadline, a connection's Set*Deadline, or a name that is
// later compared with the clock), a constant exec.Cmd.WaitDelay, and a
// constant -timeout or -test.timeout flag of a child process. A loaded gate
// host stretches any such time, so the wait turns into a verdict of the
// load. A wait ends at its event; one that needs a bound takes it from the
// test's deadline (internal/testwait, whose own constants are its poll
// interval and report share). A row of the allow list admits one function
// of one file and says why (a window that load only weakens, a fixture's
// lifetime, a named follow-up); a row that admits nothing fails too.
func TestTestsWaitNoFixedTime(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "*.go")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	type source struct {
		name string
		file *ast.File
	}
	var tests []source
	consts := map[string]map[string]ast.Expr{} // by directory
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.Contains("/"+name, "/testdata/") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		dir := path.Dir(name)
		if consts[dir] == nil {
			consts[dir] = map[string]ast.Expr{}
		}
		for k, v := range packageDurations(file) {
			consts[dir][k] = v
		}
		if strings.HasSuffix(name, "_test.go") {
			tests = append(tests, source{name, file})
		}
	}
	if len(tests) < 100 {
		t.Fatalf("read %d tracked test files; the listing is broken", len(tests))
	}
	allow := readAllow(t, fixedWaitAllow)
	used := map[string]bool{}
	for _, s := range tests {
		src, err := os.ReadFile(filepath.Join(repo, s.name))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range fixedWaits(t, s.name, src, consts[path.Dir(s.name)]) {
			key := s.name + "\t" + w.function
			if _, ok := allow[key]; ok {
				used[key] = true
				continue
			}
			t.Errorf("%s: %s waits %s; a loaded host decides it (fu-time-limits). Wait for the event, or bound the wait by the test's deadline (internal/testwait), or add `%s<TAB>%s<TAB>reason` to %s", w.at, w.function, w.what, s.name, w.function, fixedWaitAllow)
		}
	}
	for _, key := range staleRows(allow, used) {
		t.Errorf("%s: the row %q admits no fixed wait; remove it", fixedWaitAllow, strings.ReplaceAll(key, "\t", " "))
	}
}

// packageDurations returns the top-level constants of file, and its
// top-level variables with a value, by name: what a test of its package
// may name as a duration.
func packageDurations(file *ast.File) map[string]ast.Expr {
	out := map[string]ast.Expr{}
	for _, decl := range file.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST && g.Tok != token.VAR {
			continue
		}
		for _, spec := range g.Specs {
			v := spec.(*ast.ValueSpec)
			for i, name := range v.Names {
				if i < len(v.Values) {
					out[name.Name] = v.Values[i]
				}
			}
		}
	}
	return out
}

// fixedWait is one fixed wait the rule refuses.
type fixedWait struct {
	at, function, what string
}

// timeoutFlag is a child's -timeout or -test.timeout flag with its value.
var timeoutFlag = regexp.MustCompile(`^-(?:test\.)?timeout=(.+)$`)

// fixedWaits returns each fixed wait in src, outside a Benchmark function.
// consts holds the durations its package declares; the file's own
// constants, function constants included, and a function's names that are
// only ever given a constant duration count too.
func fixedWaits(t *testing.T, name string, src []byte, consts map[string]ast.Expr) []fixedWait {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	all := map[string]ast.Expr{}
	for k, v := range consts {
		all[k] = v
	}
	for k, v := range fileConstants(file) {
		all[k] = v
	}
	var out []fixedWait
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || strings.HasPrefix(fn.Name.Name, "Benchmark") {
			continue
		}
		m := &measure{consts: localDurations(fn, all)}
		add := func(n ast.Node, what string) {
			out = append(out, fixedWait{at: fset.Position(n.Pos()).String(), function: fn.Name.Name, what: what})
		}
		fixed := func(e ast.Expr) (time.Duration, bool) {
			d, ok := m.duration(e)
			return d, ok && d > 0
		}
		// nowPlus reports the constant of time.Now().Add(constant).
		nowPlus := func(e ast.Expr) (time.Duration, bool) {
			call, ok := e.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return 0, false
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Add" || !isCall(sel.X, "time", "Now") {
				return 0, false
			}
			return fixed(call.Args[0])
		}
		deadlines := map[string]ast.Node{} // names given time.Now().Add(constant)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					if i >= len(n.Rhs) {
						break
					}
					if id, ok := lhs.(*ast.Ident); ok {
						if _, ok := nowPlus(n.Rhs[i]); ok {
							deadlines[id.Name] = n
						}
					}
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "WaitDelay" {
						if d, ok := fixed(n.Rhs[i]); ok {
							add(n, fmt.Sprintf("a WaitDelay of %v", d))
						}
					}
				}
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					if s, err := strconv.Unquote(n.Value); err == nil {
						if f := timeoutFlag.FindStringSubmatch(s); f != nil {
							if d, err := time.ParseDuration(f[1]); err == nil && d > 0 {
								add(n, fmt.Sprintf("the flag %s", s))
							}
						}
					}
				}
			case *ast.CompositeLit:
				flagPairs(n.Elts, add)
			case *ast.CallExpr:
				flagPairs(n.Args, add)
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, _ := sel.X.(*ast.Ident)
				switch {
				case pkg != nil && pkg.Name == "time" && len(n.Args) >= 1 && (sel.Sel.Name == "After" || sel.Sel.Name == "Sleep" || sel.Sel.Name == "NewTimer" || sel.Sel.Name == "AfterFunc"):
					if d, ok := fixed(n.Args[0]); ok {
						add(n, fmt.Sprintf("time.%s(%v)", sel.Sel.Name, d))
					}
				case pkg != nil && pkg.Name == "context" && sel.Sel.Name == "WithTimeout" && len(n.Args) == 2:
					if d, ok := fixed(n.Args[1]); ok {
						add(n, fmt.Sprintf("context.WithTimeout of %v", d))
					}
				case sel.Sel.Name == "WithDeadline" || strings.HasPrefix(sel.Sel.Name, "Set") && strings.HasSuffix(sel.Sel.Name, "Deadline"):
					for _, arg := range n.Args {
						if d, ok := nowPlus(arg); ok {
							add(n, fmt.Sprintf("a deadline %v from now (%s)", d, sel.Sel.Name))
						}
					}
				}
			}
			return true
		})
		// A name given time.Now().Add(constant) is a fixed deadline when it
		// is compared with the clock: time.Until(name), time.Now().Before
		// or After(name).
		compared := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			id, ok := call.Args[0].(*ast.Ident)
			if !ok || deadlines[id.Name] == nil {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && (isCall(n.(*ast.CallExpr), "time", "Until") || (sel.Sel.Name == "Before" || sel.Sel.Name == "After") && isCall(sel.X, "time", "Now")) {
				compared[id.Name] = true
			}
			return true
		})
		for name, at := range deadlines {
			if compared[name] {
				d, _ := nowPlus(at.(*ast.AssignStmt).Rhs[indexOf(at.(*ast.AssignStmt), name)])
				add(at, fmt.Sprintf("until a deadline %v from now (%s)", d, name))
			}
		}
	}
	return out
}

// flagPairs adds each `"-timeout", "<constant>"` pair of list (a call's
// arguments, a slice's elements): a child's -timeout flag with its value.
func flagPairs(list []ast.Expr, add func(ast.Node, string)) {
	for i := 0; i+1 < len(list); i++ {
		if !isString(list[i], "-timeout") && !isString(list[i], "-test.timeout") {
			continue
		}
		if lit, ok := list[i+1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				if d, err := time.ParseDuration(s); err == nil && d > 0 {
					add(list[i], "the flag -timeout "+s)
				}
			}
		}
	}
}

// indexOf is the position of name among the left sides of a.
func indexOf(a *ast.AssignStmt, name string) int {
	for i, lhs := range a.Lhs {
		if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
			return i
		}
	}
	return 0
}

// isCall reports whether e is a call of pkg.fn.
func isCall(e ast.Expr, pkg, fn string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != fn {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

// localDurations adds to consts the names of fn that are given only
// constant durations (`limit := 5 * time.Second`); a name of fn that is
// also given anything else, or is a parameter, hides a constant of the
// same name.
func localDurations(fn *ast.FuncDecl, consts map[string]ast.Expr) map[string]ast.Expr {
	out := map[string]ast.Expr{}
	for k, v := range consts {
		out[k] = v
	}
	m := &measure{consts: consts}
	given, other := map[string]ast.Expr{}, map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Field:
			for _, id := range n.Names {
				other[id.Name] = true
			}
		case *ast.RangeStmt:
			for _, e := range []ast.Expr{n.Key, n.Value} {
				if id, ok := e.(*ast.Ident); ok {
					other[id.Name] = true
				}
			}
		case *ast.ValueSpec:
			for i, id := range n.Names {
				if i < len(n.Values) && len(n.Names) == len(n.Values) {
					if _, ok := m.duration(n.Values[i]); ok {
						given[id.Name] = n.Values[i]
						continue
					}
				}
				other[id.Name] = true
			}
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if len(n.Lhs) == len(n.Rhs) && n.Tok != token.ADD_ASSIGN && n.Tok != token.SUB_ASSIGN && n.Tok != token.MUL_ASSIGN && n.Tok != token.QUO_ASSIGN {
					if _, ok := m.duration(n.Rhs[i]); ok {
						given[id.Name] = n.Rhs[i]
						continue
					}
				}
				other[id.Name] = true
			}
		}
		return true
	})
	for name := range other {
		delete(out, name)
	}
	for name, e := range given {
		if !other[name] {
			out[name] = e
		}
	}
	return out
}

// readAllow reads an allow list of rows `path<TAB>function<TAB>reason`;
// each row must give a reason.
func readAllow(t *testing.T, file string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	allow := map[string]string{}
	for i, text := range strings.Split(string(data), "\n") {
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "\t")
		if len(fields) != 3 || fields[0] == "" || fields[1] == "" || strings.TrimSpace(fields[2]) == "" {
			t.Errorf("%s:%d: want `path<TAB>function<TAB>reason`, got %q", file, i+1, text)
			continue
		}
		allow[fields[0]+"\t"+fields[1]] = fields[2]
	}
	return allow
}

// TestFixedWaitRule pins the rule on fixed sources.
func TestFixedWaitRule(t *testing.T) {
	for _, c := range []struct {
		body string
		bad  int
		name string
	}{
		{`select { case <-done: case <-time.After(3 * time.Second): t.Fatal() }`, 1, "a 3 s bound"},
		{`time.Sleep(10 * time.Millisecond)`, 1, "an ordering sleep"},
		{`<-time.After(hold)`, 1, "a constant of the package"},
		{`limit := 2 * time.Minute; <-time.After(limit)`, 1, "a name given only a constant"},
		{`limit := 2 * time.Minute; if long { limit = testwait.Left(t) }; <-time.After(limit)`, 0, "a name also given a derived bound"},
		{`ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute); defer cancel()`, 1, "a context of 4 min"},
		{`ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute)); defer cancel()`, 1, "a context deadline from now"},
		{`_ = conn.SetDeadline(time.Now().Add(3 * time.Second))`, 1, "a connection deadline"},
		{`deadline := time.Now().Add(10 * time.Second); for time.Now().Before(deadline) { poll() }`, 1, "a poll until a deadline"},
		{`old := time.Now().Add(time.Hour); walk(old)`, 0, "a time as data"},
		{`cmd.WaitDelay = 5 * time.Second`, 1, "a WaitDelay"},
		{`exec.Command("go", "test", "-timeout=3m", ".")`, 1, "a go test flag"},
		{`f.run("-timeout", "20s", "p")`, 1, "a runner flag"},
		{`rows := [][]string{{"-timeout", "2s", "p"}}; use(rows)`, 1, "a runner flag in a slice"},
		{`<-time.After(testwait.Left(t) / 2)`, 0, "a bound from the deadline"},
		{`<-testwait.After(t)`, 0, "the helper"},
		{`time.Sleep(time.Duration(rng.IntN(3000)) * time.Microsecond)`, 0, "a random delay"},
		{`time.Sleep(0)`, 0, "a yield"},
	} {
		src := "package p\n\nconst hold = 300 * time.Millisecond\n\nfunc TestX(t *testing.T) {\n\t" + c.body + "\n}\n"
		if got := fixedWaits(t, "p_test.go", []byte(src), nil); len(got) != c.bad {
			t.Errorf("%s: %s: %d fixed waits flagged (%v), want %d", c.name, c.body, len(got), got, c.bad)
		}
	}
	bench := "package p\n\nfunc BenchmarkX(b *testing.B) {\n\ttime.Sleep(time.Second)\n}\n"
	if got := fixedWaits(t, "p_test.go", []byte(bench), nil); len(got) != 0 {
		t.Errorf("a benchmark: %d fixed waits flagged, want 0", len(got))
	}
	pkg := map[string]ast.Expr{"loadBound": &ast.BinaryExpr{X: &ast.BasicLit{Kind: token.INT, Value: "30"}, Op: token.MUL, Y: &ast.SelectorExpr{X: ast.NewIdent("time"), Sel: ast.NewIdent("Second")}}}
	other := "package p\n\nfunc TestY(t *testing.T) {\n\t<-time.After(loadBound)\n}\n"
	if got := fixedWaits(t, "p_test.go", []byte(other), pkg); len(got) != 1 {
		t.Errorf("a constant of another file of the package: %d fixed waits flagged, want 1", len(got))
	}
}
