package residue_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runtimeWaitsAllow is the allow list of TestRuntimeWaitsPark: a row is
// `path<TAB>function<TAB>reason`.
const runtimeWaitsAllow = "testdata/runtime-waits-allow.txt"

// TestRuntimeWaitsPark is the guard of wf10 MK1 (runtime.md, Machines and
// actors 11.16): a blocking wait in runtime/v1 that Ergo code reaches is a
// wait a clock's domain sees. Before MK1 a work's wait for its children
// was a raw channel receive, so a time.Manual's Advance waited for ever
// for a work that waited for a timer that only the Advance could fire.
//
// The rule is syntactic: in every non-test Go file of runtime/v1, a
// function (its literals included) that blocks (a channel receive or send
// outside a select, a select with no default, or a call of a method Wait
// with no arguments: a sync.WaitGroup's, a sync.Cond's, a process's)
// also parks its task (calls rt.Park), so the wait is the clock's. A wait
// that needs no park is a row of the allow list with its reason: one that
// ends when a task that runs reaches its next wait, so no clock and no
// caller holds it (a handshake); one on the host (a process, an input),
// which the clock cannot see (rt idle.go); a send that never waits; and a
// call that parks inside runtime/rt. A row that admits nothing fails too.
// A range over a channel is not seen (it needs types); runtime/v1 has
// none.
func TestRuntimeWaitsPark(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "runtime/v1/*.go")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	allow := readAllow(t, runtimeWaitsAllow)
	used := map[string]bool{}
	files := 0
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.HasSuffix(name, "_test.go") || strings.Contains(name, "/testdata/") || filepath.Dir(name) != "runtime/v1" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		files++
		for _, w := range unparkedWaits(t, name, src) {
			key := name + "\t" + w.function
			if _, ok := allow[key]; ok {
				used[key] = true
				continue
			}
			t.Errorf("%s: %s %s and parks no task, so a manual clock's Advance waits for it (runtime.md 11.16; wf10 MK1). Park the waiter (rt.Park, woken through rt.Wake), or add `%s<TAB>%s<TAB>reason` to %s", w.at, w.function, w.what, name, w.function, runtimeWaitsAllow)
		}
	}
	if files < 20 {
		t.Fatalf("read %d runtime/v1 files; the listing is broken", files)
	}
	for _, key := range staleRows(allow, used) {
		t.Errorf("%s: the row %q admits no unparked wait; remove it", runtimeWaitsAllow, strings.ReplaceAll(key, "\t", " "))
	}
}

// unparkedWait is one blocking wait in a function that parks no task.
type unparkedWait struct {
	at, function, what string
}

// unparkedWaits returns the first blocking wait of each function of src
// that calls no rt.Park.
func unparkedWaits(t *testing.T, name string, src []byte) []unparkedWait {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var out []unparkedWait
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var first ast.Node
		var what string
		parks := false
		inComm := map[ast.Node]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectStmt:
				def := false
				for _, c := range n.Body.List {
					cc := c.(*ast.CommClause)
					if cc.Comm == nil {
						def = true
					} else {
						inComm[cc.Comm] = true
					}
				}
				if !def && first == nil {
					first, what = n, "selects with no default"
				}
			case *ast.ExprStmt, *ast.AssignStmt, *ast.SendStmt:
				if inComm[n] {
					// A select's case: the select is the wait.
					return false
				}
				if s, ok := n.(*ast.SendStmt); ok && first == nil {
					first, what = s, "sends on a channel"
				}
			case *ast.UnaryExpr:
				if n.Op == token.ARROW && first == nil {
					first, what = n, "receives from a channel"
				}
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					break
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "rt" && sel.Sel.Name == "Park" {
					parks = true
				}
				if sel.Sel.Name == "Wait" && len(n.Args) == 0 && first == nil {
					first, what = n, "calls Wait"
				}
			}
			return true
		})
		if first != nil && !parks {
			out = append(out, unparkedWait{at: fset.Position(first.Pos()).String(), function: funcName(fn), what: what})
		}
	}
	return out
}

// funcName is fn's name, with its receiver's type for a method.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if s, ok := typ.(*ast.StarExpr); ok {
		typ = s.X
	}
	switch x := typ.(type) {
	case *ast.IndexExpr:
		typ = x.X
	case *ast.IndexListExpr:
		typ = x.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// TestRuntimeWaitRule holds that the rule reads the function literals of a
// function: a wait in a literal is the function's. It is the one form that
// TestRuntimeWaitsPark (with its stale rows) does not pin: the mutant
// mk1-residue-skips-literals (unparkedWaits does not enter a FuncLit)
// passes it, since no admitted function of runtime/v1 waits only in a
// literal.
func TestRuntimeWaitRule(t *testing.T) {
	src := "package p\n\nfunc f() {\n\tgo func() { <-done }()\n}\n"
	if got := unparkedWaits(t, "p.go", []byte(src)); len(got) != 1 {
		t.Errorf("a wait in a function literal: %d functions flagged, want 1", len(got))
	}
}
