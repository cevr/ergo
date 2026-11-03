package lower

import (
	"go/ast"
	"go/token"
	"go/types"
	"regexp"
	"strconv"
	"strings"
	"testing"

	v1 "ergo.local/prototype/runtime/v1"
)

// The running task leaves an environment only through taskOf, and a Go
// member name only through memberName. Resolved through go/types, every
// owner that reads either is classified here, so an emitter that builds a
// runtime binding's call itself (bypassing runtimeCall, with any env) fails
// until it is named. No literal spells the task field.
func TestRuntime199TaskReaders(t *testing.T) {
	src := typedPackage(t, ".")
	scope := src.pkg.Scope()
	for _, c := range []struct {
		obj        string
		classified map[string]string
	}{
		{"envTask", map[string]string{
			"taskOf":                "the only reader",
			"lowerer.envType":       "declares the field",
			"funcCtx.operations":    "an operation table's Env literal for the caller's task",
			"funcCtx.witnessMethod": "a witness adapter's Env literal for the caller's task",
			"lowerer.entry":         "the root Env literal in main",
			"lowerer.testEntry":     "the root Env literal in the test build's main (196l)",
		}},
		{"taskOf", map[string]string{
			"funcCtx.hiddenArgs":    "runtimeCall's hidden CallerTask argument",
			"funcCtx.programOpCall": "a program-declared operation; it refuses every runtime type",
			"funcCtx.callText":      "a capability operation through the Env's provider field",
			"funcCtx.openFrame":     "the frame scope's parent task (NewScope)",
			"funcCtx.poll":          "the interruption point (Check)",
			"funcCtx.envClosure":    "a task callback's Env copy",
		}},
		{"memberName", map[string]string{
			"funcCtx.runtimeCall":    "the runtime binding emitter",
			"funcCtx.programOpCall":  "a program-declared operation's method",
			"funcCtx.callText":       "a capability operation's method",
			"funcCtx.operations":     "an operation table's method values",
			"funcCtx.witnessCall":    "a call through a witness field",
			"funcCtx.witnessMethod":  "a witness adapter's method and the field it calls",
			"funcCtx.witnessOp":      "a witness field that passes on the caller's witness",
			"funcCtx.witnessValue":   "a witness literal's fields",
			"typeCtx.witnessType":    "a witness type's fields",
			"lowerer.emitType":       "a declared type's fields and methods",
			"lowerer.envField":       "a capability's Env field",
			"lowerer.funcDecl":       "a declared function's Go name",
			"lowerer.intrinsic":      "an intrinsic's declared target",
			"lowerer.methodReceiver": "a declared method's receiver",
			"lowerer.typeDecl":       "a declared type's members",
			"lowerer.entry":          "the host Console's Println",
		}},
	} {
		obj := scope.Lookup(c.obj)
		if obj == nil {
			t.Fatalf("lower has no %s", c.obj)
		}
		users := src.users(obj)
		for _, owner := range sortedKeys(users) {
			if c.classified[owner] == "" {
				t.Errorf("%s reads %s and is not classified; runtime binding calls go through runtimeCall", owner, c.obj)
			}
		}
		for owner := range c.classified {
			if !users[owner] {
				t.Errorf("%s is classified as a reader of %s but no longer reads it", owner, c.obj)
			}
		}
	}
	field := regexp.MustCompile(`\.` + envTask + `\b`)
	src.owners(func(owner string, decl ast.Decl) {
		for _, s := range stringLits(t, decl) {
			if field.MatchString(s) {
				t.Errorf("%s spells the task field in %q; use taskOf", owner, s)
			}
		}
	})
}

// A noWait catalog entry runs without a waiting task, so its runtime body
// may not park: it takes no hidden task unless it is a release route or a
// context read (takesCaller, which runtimeCall checks against the body), and
// neither its runtime/v1 function nor any runtime/v1 function it calls holds
// a select, a channel receive or a channel send. A body that starts to park
// fails here until the catalog states its waiting policy. Waits inside rt
// (a drain's join) are the drain owner's (drainWait), outside this scan.
func TestRuntime199NoWaitBindings(t *testing.T) {
	src := typedPackage(t, "../../../runtime/v1")
	decls := map[types.Object]*ast.FuncDecl{}
	src.owners(func(_ string, decl ast.Decl) {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			decls[src.info.Defs[fn.Name]] = fn
		}
	})
	lookup := func(name string) types.Object {
		if recv, method, ok := strings.Cut(name, "."); ok {
			return src.method(t, recv, method)
		}
		obj := src.pkg.Scope().Lookup(name)
		if obj == nil {
			t.Fatalf("runtime/v1 has no function %s", name)
		}
		return obj
	}
	checked := 0
	for _, name := range sortedKeys(func() map[string]bool {
		m := map[string]bool{}
		for name, b := range runtimeBindings {
			if b.wait == noWait {
				m[name] = true
			}
		}
		return m
	}()) {
		b := runtimeBindings[name]
		body, err := v1.BodyOf(name)
		if err != nil {
			t.Fatal(err)
		}
		if hasHidden(body.Hidden, v1.CallerTask) && b.release != callerRelease && !b.reads {
			t.Errorf("%s takes a hidden task and declares no wait", name)
		}
		seen := map[types.Object]bool{}
		var visit func(obj types.Object, path string)
		visit = func(obj types.Object, path string) {
			obj = originOf(obj)
			fn := decls[obj]
			if fn == nil || seen[obj] {
				return
			}
			seen[obj] = true
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectStmt:
					t.Errorf("%s waits: %s selects", name, path)
				case *ast.SendStmt:
					t.Errorf("%s waits: %s sends on a channel", name, path)
				case *ast.UnaryExpr:
					if n.Op == token.ARROW {
						t.Errorf("%s waits: %s receives from a channel", name, path)
					}
				case *ast.RangeStmt:
					if tv, ok := src.info.Types[n.X]; ok {
						if _, ok := tv.Type.Underlying().(*types.Chan); ok {
							t.Errorf("%s waits: %s ranges over a channel", name, path)
						}
					}
				case *ast.GoStmt:
					return false // the goroutine it starts waits, not the caller
				case *ast.Ident:
					if f, ok := src.info.Uses[n].(*types.Func); ok && f.Pkg() == src.pkg {
						visit(f, path+" -> "+f.Name())
					}
				}
				return true
			})
		}
		visit(lookup(name), name)
		checked++
	}
	if checked == 0 {
		t.Fatal("the catalog has no noWait bindings")
	}
}

// originOf is the generic declaration of an instantiated function or method.
func originOf(obj types.Object) types.Object {
	if f, ok := obj.(*types.Func); ok {
		return f.Origin()
	}
	return obj
}

// Binding emission is a separate seam from runtime cancellation. New raw
// runtime calls must use runtimeCall, even when no fixture reaches them.
// The exceptions are formatting, collection intrinsics and frame plumbing;
// none is a user-visible blocking binding.
func TestRuntime199BlockingOwner(t *testing.T) {
	helpers := map[string]bool{
		"Append": true, "Delete": true, "NewScope": true, "Site": true,
		"RegisterCalls": true, "TextBool": true, "TextDuration": true,
		"TextInt": true, "TextUint": true, "TextFloat": true, "TextList": true,
		"Main": true, "Println": true, "End": true,
	}
	call := regexp.MustCompile(`\.([A-Za-z][A-Za-z0-9_]*)\(`)
	methods := map[string]bool{}
	for name, binding := range runtimeBindings {
		if binding.wait != noWait {
			parts := strings.Split(name, ".")
			methods[parts[len(parts)-1]] = true
		}
	}
	i1LowerSource(t, func(owner string, node ast.Node) {
		if owner == "runtimeCall" {
			return
		}
		usesRuntime := false
		var literals []string
		ast.Inspect(node, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if s, ok := n.Fun.(*ast.SelectorExpr); ok {
					usesRuntime = usesRuntime || s.Sel.Name == "rt"
					if s.Sel.Name == "hiddenArgs" {
						t.Errorf("%s emits hidden binding arguments outside runtimeCall", owner)
					}
				}
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					value, err := strconv.Unquote(n.Value)
					if err != nil {
						t.Fatal(err)
					}
					literals = append(literals, value)
				}
			}
			return true
		})
		for _, text := range literals {
			for _, m := range call.FindAllStringSubmatch(text, -1) {
				if methods[m[1]] || usesRuntime && !helpers[m[1]] {
					t.Errorf("%s emits runtime binding %s outside runtimeCall", owner, m[1])
				}
			}
		}
	})
}
