package check

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

func TestRuntime199CallKindSentinel(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "info.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, decl := range f.Decls {
		group, ok := decl.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		var names []string
		hasSentinel := false
		for _, spec := range group.Specs {
			for _, name := range spec.(*ast.ValueSpec).Names {
				names = append(names, name.Name)
				if name.Name == "CallKindCount" {
					hasSentinel = true
					found++
				}
			}
		}
		if hasSentinel && names[len(names)-1] != "CallKindCount" {
			t.Errorf("CallKindCount must be the last name in its const block; got %v", names)
		}
	}
	if found != 1 {
		t.Errorf("found %d CallKindCount sentinels, want one", found)
	}
}

// An unknown kind is a compiler bug, never silent purity. Probe both sides
// of the valid range and the sentinel itself, which is not a call kind.
func TestRuntime199UnknownCallKind(t *testing.T) {
	for _, kind := range []CallKind{-1, CallKindCount, CallKindCount + 1} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			defer func() {
				want := fmt.Sprintf("check: unknown call kind %d in footprint", kind)
				if got := recover(); got != want {
					t.Errorf("unknown call kind: got %v, want ICE %q", got, want)
				}
			}()
			fp := &footprinter{}
			fp.call(&fpNode{}, &syntax.CallExpr{}, &Call{Kind: kind})
		})
	}
}
