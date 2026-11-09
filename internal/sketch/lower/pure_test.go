package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"
)

// pureModel is one program and the model's verdict: the functions it
// emits with no environment, those that keep one, and its stdout.
type pureModel struct {
	name, src, stdout string
	pure, impure      []string // "f" for a function, "T.f" for a Go method on T
}

// envParams lists each function and Go method of the emitted file with
// whether its first parameter is the environment (*Env): "f" for a
// function, "T.f" for a method on T.
func envParams(t *testing.T, code string) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", code, 0)
	if err != nil {
		t.Fatalf("parse the emitted Go: %v", err)
	}
	out := map[string]bool{}
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		key := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) == 1 {
			rt := fn.Recv.List[0].Type
			if s, ok := rt.(*ast.StarExpr); ok {
				rt = s.X
			}
			if id, ok := rt.(*ast.Ident); ok {
				key = id.Name + "." + key
			}
		}
		env := false
		if ps := fn.Type.Params.List; len(ps) > 0 {
			if s, ok := ps[0].Type.(*ast.StarExpr); ok {
				if id, ok := s.X.(*ast.Ident); ok && id.Name == "Env" {
					env = true
				}
			}
		}
		out[key] = env
	}
	return out
}

// checkEnvParams compares the emitted functions' environment parameters
// with the model.
func checkEnvParams(t *testing.T, m pureModel, code string) {
	t.Helper()
	got := envParams(t, code)
	check := func(names []string, want bool) {
		for _, n := range names {
			env, ok := got[n]
			if n == "main" {
				// The Ergo main is the Go function the generated Go main runs.
				env, ok = got["main_1"]
				if !ok {
					env, ok = got["ergoMain"]
				}
				if !ok {
					continue
				}
			}
			switch {
			case !ok:
				var have []string
				for k := range got {
					have = append(have, k)
				}
				sort.Strings(have)
				t.Errorf("%s: no Go function %s (have %v)", m.name, n, have)
			case env != want:
				t.Errorf("%s: %s takes the environment %v, want %v", m.name, n, env, want)
			}
		}
	}
	check(m.pure, false)
	check(m.impure, true)
}
