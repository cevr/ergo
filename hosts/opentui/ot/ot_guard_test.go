//go:build (linux || darwin) && (amd64 || arm64)

package ot

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"testing"

	v1 "ergo.local/prototype/runtime/v1"
)

// TestOpenTUIPinBeforeUintptr verifies that the regenerated opentui.go uses
// CFFI-1's pin-safe zero-alloc SyscallN fast path (cCall/cCall0 + runtime.Pinner
// before every uintptr pointer conversion) and that raw.go pins its Go pointer
// arguments before calling into C.
func TestOpenTUIPinBeforeUintptr(t *testing.T) {
	fset := token.NewFileSet()
	genFile, err := parser.ParseFile(fset, "opentui.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	wordCalls := 0
	pinnedFuncs := 0
	guardHooks := 0
	for _, decl := range genFile.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || !ast.IsExported(fn.Name.Name) {
			continue
		}
		pins := map[string]token.Pos{}
		hasUnpin := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var sb strings.Builder
			_ = printer.Fprint(&sb, fset, c.Fun)
			name := sb.String()
			switch {
			case name == "cCall" || name == "cCall0":
				wordCalls++
			case name == "cGuardIn" || name == "cGuardOut":
				guardHooks++
			case name == "_pin.Pin" && len(c.Args) == 1:
				var arg strings.Builder
				_ = printer.Fprint(&arg, fset, c.Args[0])
				if _, exists := pins[arg.String()]; !exists {
					pins[arg.String()] = c.Pos()
				}
			case name == "_pin.Unpin":
				hasUnpin = true
			case name == "uintptr" && len(c.Args) == 1:
				var arg strings.Builder
				_ = printer.Fprint(&arg, fset, c.Args[0])
				key := arg.String()
				if strings.HasPrefix(key, "unsafe.Pointer(") {
					key = strings.TrimSuffix(strings.TrimPrefix(key, "unsafe.Pointer("), ")")
				}
				if strings.HasPrefix(key, "_v") || strings.HasPrefix(key, "_o") || strings.HasPrefix(key, "_s") || strings.HasPrefix(key, "&") {
					pinPos, ok := pins[key]
					if !ok || pinPos >= c.Pos() {
						t.Errorf("%s (%s): uintptr(%s) not preceded by _pin.Pin(%s)", fn.Name.Name, fset.Position(c.Pos()), key, key)
					}
				}
			}
			return true
		})
		if len(pins) > 0 {
			pinnedFuncs++
			if !hasUnpin {
				t.Errorf("%s: missing _pin.Unpin()", fn.Name.Name)
			}
		}
	}
	if wordCalls < 200 {
		t.Fatalf("expected >=200 word-direct SyscallN call sites in opentui.go, got %d", wordCalls)
	}
	if pinnedFuncs < 100 {
		t.Fatalf("expected >=100 pinned typed functions in opentui.go, got %d", pinnedFuncs)
	}
	if guardHooks == 0 {
		t.Fatalf("expected cGuardIn/cGuardOut hook sites in opentui.go, got 0")
	}

	rawFile, err := parser.ParseFile(fset, "raw.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	rawPinned := 0
	for _, decl := range rawFile.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || !ast.IsExported(fn.Name.Name) {
			continue
		}
		hasPin, hasUnpin := false, false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var sb strings.Builder
			_ = printer.Fprint(&sb, fset, c.Fun)
			switch sb.String() {
			case "pin.Pin":
				hasPin = true
			case "pin.Unpin":
				hasUnpin = true
			}
			return true
		})
		if !hasPin || !hasUnpin {
			t.Errorf("raw.go %s: expected pin.Pin and pin.Unpin", fn.Name.Name)
		}
		rawPinned++
	}
	if rawPinned < 3 {
		t.Fatalf("expected 3 pinned functions in raw.go, got %d", rawPinned)
	}
}

// TestOpenTUIGuardHooksWiring verifies that SetCGuardHooks wires and clears
// runtime/v1 guard-page hooks cleanly and exercises GuardCReadPtr/GuardCWritePtr.
func TestOpenTUIGuardHooksWiring(t *testing.T) {
	prevIn, prevOut := cGuardIn, cGuardOut
	defer SetCGuardHooks(prevIn, prevOut)

	SetCGuardHooks(v1.GuardCReadPtr, v1.GuardCWritePtr)
	if cGuardIn == nil || cGuardOut == nil {
		t.Fatal("SetCGuardHooks did not install hooks")
	}

	src := []byte("opentui-guard")
	pIn, doneIn := cGuardIn(sliceData(src), uintptr(len(src)), "ot_scene_set_text", "text")
	if pIn == nil || doneIn == nil {
		t.Fatal("cGuardIn returned nil pointer or done func")
	}
	doneIn()

	dst := make([]byte, 8)
	pOut, doneOut := cGuardOut(sliceData(dst), uintptr(len(dst)), "ot_session_read_output", "buf")
	if pOut == nil || doneOut == nil {
		t.Fatal("cGuardOut returned nil pointer or done func")
	}
	doneOut()

	SetCGuardHooks(nil, nil)
	if cGuardIn != nil || cGuardOut != nil {
		t.Fatal("SetCGuardHooks(nil, nil) did not clear hooks")
	}
}
