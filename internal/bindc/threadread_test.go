package bindc

import (
	"bytes"
	"go/ast"
	goparser "go/parser"
	gotoken "go/token"
	"regexp"
	"slices"
	"sort"
	"testing"
)

// The thread id (cbThread) names the thread the goroutine runs on at the
// read. Go has no per-thread storage, and an unlocked goroutine can move
// to another thread right after the read, so the id is valid only while the
// goroutine is held on its thread: in a trampoline (C calls back on a
// goroutine that the runtime holds on its thread for the callback), or
// under runtime.LockOSThread.

// trampolineName is a trampoline of a generated package.
var trampolineName = regexp.MustCompile(`^cbTramp[0-9]+$`)

// unlockedThreadReads returns the functions of the Go source src that read
// cbThread where the goroutine may move: outside the trampolines and the
// functions that only trampolines call, and not after a
// runtime.LockOSThread at the top level of the function body that no
// runtime.UnlockOSThread at that level has ended (a deferred unlock ends it
// at the return). Methods are named Type.Method.
func unlockedThreadReads(t *testing.T, src []byte) []string {
	t.Helper()
	file, err := goparser.ParseFile(gotoken.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
			name := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) == 1 {
				rt := fd.Recv.List[0].Type
				if s, ok := rt.(*ast.StarExpr); ok {
					rt = s.X
				}
				if id, ok := rt.(*ast.Ident); ok {
					name = id.Name + "." + name
				}
			}
			funcs[name] = fd
		}
	}
	// callers of each package function: the functions whose bodies call it
	// (a call, a defer or a go statement).
	callers := map[string][]string{}
	for name, fd := range funcs {
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && funcs[id.Name] != nil {
					callers[id.Name] = append(callers[id.Name], name)
				}
			}
			return true
		})
	}
	// The trampoline context: the trampolines, then each function that has
	// callers and is called only from the trampoline context.
	inTramp := map[string]bool{}
	for name := range funcs {
		inTramp[name] = trampolineName.MatchString(name)
	}
	for changed := true; changed; {
		changed = false
		for name := range funcs {
			if inTramp[name] || len(callers[name]) == 0 {
				continue
			}
			all := true
			for _, c := range callers[name] {
				all = all && inTramp[c]
			}
			if all {
				inTramp[name], changed = true, true
			}
		}
	}
	runtimeCall := func(s ast.Stmt, fn string) bool {
		es, ok := s.(*ast.ExprStmt)
		if !ok {
			return false
		}
		c, ok := es.X.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		x, ok := sel.X.(*ast.Ident)
		return ok && x.Name == "runtime" && sel.Sel.Name == fn
	}
	readsThread := func(n ast.Node) bool {
		found := false
		ast.Inspect(n, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "cbThread" {
					found = true
				}
			}
			return !found
		})
		return found
	}
	var bad []string
	for name, fd := range funcs {
		if inTramp[name] || name == "cbThread" {
			continue
		}
		locks := 0
		for _, s := range fd.Body.List {
			if readsThread(s) && locks < 1 {
				bad = append(bad, name)
				break
			}
			switch {
			case runtimeCall(s, "LockOSThread"):
				locks++
			case runtimeCall(s, "UnlockOSThread"):
				locks--
			}
		}
	}
	sort.Strings(bad)
	return bad
}

// TestThreadReadsLocked (bind-c-2 r2, 2c M1): in the Go packages that the
// emitter makes for both golden headers (TestGolden holds the goldens equal
// to them), every read of the thread id outside a trampoline is under a
// lock of the goroutine to its thread. The controls remove the lock of
// cbTakeHere (the r2 head's take, which let a goroutine take another
// goroutine's pending panic) and the lock of ownerHere; each must be found.
func TestThreadReadsLocked(t *testing.T) {
	for _, h := range headers {
		m := parseHeader(t, h.dir, h.header)
		b := bindHeader(t, h.dir, h.header, m)
		name, src, err := GoPackage(b)
		if err != nil {
			t.Fatalf("%s: GoPackage: %v", h.header, err)
		}
		if bad := unlockedThreadReads(t, src); len(bad) > 0 {
			t.Errorf("%s/go/%s reads cbThread with the goroutine free to move in %v", h.dir, name, bad)
		}
		if !bytes.Contains(src, []byte("func cbThread()")) {
			continue
		}
		for _, c := range []struct{ fn, from, to string }{
			{"cbTakeHere", "func cbTakeHere() *CallbackPanic {\n\truntime.LockOSThread()\n", "func cbTakeHere() *CallbackPanic {\n"},
			{"ownerHere", "func ownerHere(o *Owner) bool {\n\truntime.LockOSThread()\n", "func ownerHere(o *Owner) bool {\n"},
		} {
			mut := bytes.Replace(src, []byte(c.from), []byte(c.to), 1)
			if bytes.Equal(mut, src) {
				t.Fatalf("%s/go/%s: the control finds no lock in %s", h.dir, name, c.fn)
			}
			if bad := unlockedThreadReads(t, mut); !slices.Contains(bad, c.fn) {
				t.Errorf("%s/go/%s: without its lock, %s is not found (found %v)", h.dir, name, c.fn, bad)
			}
		}
	}
}
