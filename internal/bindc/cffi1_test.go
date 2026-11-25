package bindc

import (
	"fmt"
	"go/ast"
	goparser "go/parser"
	gotoken "go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestWordDirectPinBeforeUintptr statically verifies CFFI-1 invariants over
// the freshly generated Go AST for both fixture and opentui bindings:
//
//  1. isWordDirect rejects every function with float32/float64 parameters or
//     results, by-value structs, or >15 parameters, and accepts pure word
//     functions.
//  2. Every uintptr(...) conversion of a Go pointer (unsafe.Pointer(...),
//     sliceData(...), or a _v<n> pointer local) is strictly preceded by a
//     matching _pin.Pin(...) call in the function's statement order.
//  3. Every wordPool.Get() call precedes any uintptr(...) conversion of a Go
//     pointer.
//  4. Every function that declares _pin calls _pin.Unpin() on both its
//     statusError early-return branch (when it checks status) and its normal
//     completion path.
func TestWordDirectPinBeforeUintptr(t *testing.T) {
	for _, h := range headers {
		t.Run(h.dir, func(t *testing.T) {
			m := parseHeader(t, h.dir, h.header)
			b := bindHeader(t, h.dir, h.header, m)

			// 1. Check isWordDirect eligibility against every bound function.
			for _, f := range b.Funcs {
				wd := b.isWordDirect(f.C)
				hasFloatOrStruct := b.k.isByValueStruct(f.C.Result)
				if sc, ok := b.k.scalarOf(f.C.Result); ok && (sc == Float32 || sc == Float64) {
					hasFloatOrStruct = true
				}
				for _, p := range f.C.Params {
					if b.k.isByValueStruct(p.Type) {
						hasFloatOrStruct = true
					}
					if sc, ok := b.k.scalarOf(p.Type); ok && (sc == Float32 || sc == Float64) {
						hasFloatOrStruct = true
					}
				}
				if wd && (hasFloatOrStruct || len(f.C.Params) > puregoSyscallMaxArgs) {
					t.Fatalf("%s: isWordDirect=true for non-word function (float/struct=%v, params=%d)", f.C.Name, hasFloatOrStruct, len(f.C.Params))
				}
			}
			if h.dir == "testdata/fixture" {
				wantNonWord := map[string]bool{
					"fx_point_length":        true,
					"fx_context_age":         true,
					"fx_node_measure":        true,
					"fx_context_measure_all": true,
				}
				for _, f := range b.Funcs {
					if !f.goTyped() {
						continue
					}
					got := b.isWordDirect(f.C)
					if got == wantNonWord[f.C.Name] {
						t.Fatalf("fixture %s: isWordDirect=%v, want %v", f.C.Name, got, !wantNonWord[f.C.Name])
					}
				}
			}

			// 2. Parse freshly emitted Go package and inspect every typed function.
			name, src, err := GoPackage(b)
			if err != nil {
				t.Fatalf("GoPackage: %v", err)
			}
			fset := gotoken.NewFileSet()
			file, err := goparser.ParseFile(fset, name, src, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}

			typedByName := map[string]*BoundFunc{}
			for _, f := range b.Funcs {
				if f.goTyped() {
					typedByName[f.GoName] = f
				}
			}

			checked := 0
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Body == nil {
					continue
				}
				bf, ok := typedByName[fn.Name.Name]
				if !ok {
					continue
				}
				checked++
				verifyFuncPinOrder(t, fset, bf, fn)
			}
			if checked != len(typedByName) {
				t.Fatalf("checked %d typed funcs, want %d", checked, len(typedByName))
			}
		})
	}
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.UnaryExpr:
		return x.Op.String() + exprString(x.X)
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		var args []string
		for _, a := range x.Args {
			args = append(args, exprString(a))
		}
		return exprString(x.Fun) + "(" + strings.Join(args, ", ") + ")"
	default:
		return fmt.Sprintf("%T", e)
	}
}

func isCallNamed(c *ast.CallExpr, want string) bool {
	return exprString(c.Fun) == want
}

// pointerKey extracts a canonical target key for a Go pointer expression passed
// to _pin.Pin(...) or uintptr(...), or "" when the expression is not a Go pointer.
func pointerKey(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.CallExpr:
		fn := exprString(x.Fun)
		if fn == "unsafe.Pointer" && len(x.Args) == 1 {
			return exprString(x.Args[0])
		}
		if fn == "sliceData" && len(x.Args) == 1 {
			return "sliceData(" + exprString(x.Args[0]) + ")"
		}
	case *ast.Ident:
		if strings.HasPrefix(x.Name, "_v") || strings.HasPrefix(x.Name, "_o") || strings.HasPrefix(x.Name, "_s") {
			return x.Name
		}
	case *ast.UnaryExpr:
		if x.Op == gotoken.AND {
			return "&" + exprString(x.X)
		}
	}
	return ""
}

func verifyFuncPinOrder(t *testing.T, fset *gotoken.FileSet, bf *BoundFunc, fn *ast.FuncDecl) {
	t.Helper()
	// Expected pointer args from the binding's Positions.
	needPin := false
	for _, q := range bf.Positions {
		switch q.Class {
		case ClassOut, ClassOutCount, ClassValue, ClassOptional, ClassInBuffer, ClassOutBuffer:
			needPin = true
		}
	}

	pinned := map[string]gotoken.Pos{}
	var firstUintptrPos gotoken.Pos
	var poolGetPos gotoken.Pos
	hasPinDecl := false
	statusErrHasUnpin := false
	hasStatusErrBranch := false
	topLevelUnpins := 0

	// Walk statements in source order.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ValueSpec:
			for _, id := range x.Names {
				if id.Name == "_pin" {
					hasPinDecl = true
				}
			}
		case *ast.CallExpr:
			if isCallNamed(x, "_pin.Pin") && len(x.Args) == 1 {
				key := pointerKey(x.Args[0])
				if key == "" {
					key = exprString(x.Args[0])
				}
				if _, exists := pinned[key]; !exists {
					pinned[key] = x.Pos()
				}
			}
			if isCallNamed(x, "wordPool.Get") {
				poolGetPos = x.Pos()
			}
			if isCallNamed(x, "uintptr") && len(x.Args) == 1 {
				if key := pointerKey(x.Args[0]); key != "" {
					if firstUintptrPos == gotoken.NoPos || x.Pos() < firstUintptrPos {
						firstUintptrPos = x.Pos()
					}
					pinPos, ok := pinned[key]
					if !ok {
						t.Errorf("%s (%s): uintptr(%s) has no preceding _pin.Pin(%s)",
							bf.GoName, fset.Position(x.Pos()), exprString(x.Args[0]), key)
					} else if pinPos >= x.Pos() {
						t.Errorf("%s (%s): _pin.Pin(%s) at %s does not precede uintptr conversion",
							bf.GoName, fset.Position(x.Pos()), key, fset.Position(pinPos))
					}
				}
			}
		}
		return true
	})

	if poolGetPos != gotoken.NoPos && firstUintptrPos != gotoken.NoPos && poolGetPos >= firstUintptrPos {
		t.Errorf("%s: wordPool.Get() at %s must precede uintptr conversion at %s",
			bf.GoName, fset.Position(poolGetPos), fset.Position(firstUintptrPos))
	}

	// Check _pin.Unpin() on statusError branch and top-level completion path.
	for _, stmt := range fn.Body.List {
		switch st := stmt.(type) {
		case *ast.ExprStmt:
			if c, ok := st.X.(*ast.CallExpr); ok && isCallNamed(c, "_pin.Unpin") {
				topLevelUnpins++
			}
		case *ast.IfStmt:
			if assign, ok := st.Init.(*ast.AssignStmt); ok && len(assign.Rhs) == 1 {
				if c, ok := assign.Rhs[0].(*ast.CallExpr); ok && isCallNamed(c, "statusError") {
					hasStatusErrBranch = true
					ast.Inspect(st.Body, func(n ast.Node) bool {
						if call, ok := n.(*ast.CallExpr); ok && isCallNamed(call, "_pin.Unpin") {
							statusErrHasUnpin = true
						}
						return true
					})
				}
			}
		}
	}

	if needPin {
		if !hasPinDecl {
			t.Errorf("%s: passes Go pointers to C but declares no runtime.Pinner (_pin)", bf.GoName)
		}
		if len(pinned) == 0 {
			t.Errorf("%s: declares no _pin.Pin calls despite Go pointer arguments", bf.GoName)
		}
		if topLevelUnpins == 0 {
			t.Errorf("%s: missing _pin.Unpin() on normal completion path", bf.GoName)
		}
		if hasStatusErrBranch && !statusErrHasUnpin {
			t.Errorf("%s: missing _pin.Unpin() on statusError early-return branch", bf.GoName)
		}
	}
}

const cffi1OracleExport = `package fixture

// SetForceRegisterFunc toggles word-direct SyscallN vs purego.RegisterFunc
// for differential oracle testing.
func SetForceRegisterFunc(v bool) { forceRegisterFunc = v }
`

// runCFFI1Driver compiles the freshly emitted fixture Go binding (via GoPackage,
// not static golden) against real purego v0.11.3 and libfixture.so, then runs
// testdata/live/cffi1/main.go in the given mode.
func runCFFI1Driver(t *testing.T, mode string) string {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler (cc) on PATH")
	}
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")

	// Find a GOMODCACHE that already has purego v0.11.3 (instant, offline), or
	// download into a temp cache when ERGO_BINDC_LIVE=1.
	modCache := ""
	var candidates []string
	if envCache := os.Getenv("GOMODCACHE"); envCache != "" {
		candidates = append(candidates, envCache)
	}
	if out, err := exec.Command(goTool, "env", "GOMODCACHE").Output(); err == nil {
		if c := strings.TrimSpace(string(out)); c != "" {
			candidates = append(candidates, c)
		}
	}
	for _, c := range candidates {
		if st, err := os.Stat(filepath.Join(c, "github.com", "ebitengine", "purego@v0.11.3")); err == nil && st.IsDir() {
			modCache = c
			break
		}
	}
	root := t.TempDir()
	if modCache == "" {
		if os.Getenv("ERGO_BINDC_LIVE") != "1" {
			t.Skip("purego@v0.11.3 not cached in GOMODCACHE; warm it or set ERGO_BINDC_LIVE=1")
		}
		modCache = filepath.Join(root, "modcache")
	}

	mod := filepath.Join(root, "mod")
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(mod, "go.mod"), []byte("module ergo.local/bind\n\ngo 1.25.0\n\nrequire github.com/ebitengine/purego v0.11.3\n"))
	write(filepath.Join(mod, "go.sum"), readInput(t, "testdata/purego.sum"))

	// Generate fixture Go package LIVE from the current emitter so mutants are
	// tested directly against real C libfixture.so.
	m := parseHeader(t, "testdata/fixture", "fixture.h")
	b := bindHeader(t, "testdata/fixture", "fixture.h", m)
	goName, goSrc, err := GoPackage(b)
	if err != nil {
		t.Fatalf("GoPackage(fixture): %v", err)
	}
	write(filepath.Join(mod, "fixture", goName), goSrc)
	write(filepath.Join(mod, "fixture", "oracle_export.go"), []byte(cffi1OracleExport))
	write(filepath.Join(mod, "driver", "main.go"), readInput(t, "testdata/live/cffi1/main.go"))

	env := append(os.Environ(),
		"CGO_ENABLED=0",
		"GOFLAGS=-mod=readonly -modcacherw",
		"GOMODCACHE="+modCache,
		"GOWORK=off",
	)
	run := func(dir, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	libPath := filepath.Join(root, "libfixture"+ext)
	cSrc, _ := filepath.Abs("testdata/fixture/fixture.c")
	run(root, cc, "-std=c11", "-shared", "-fPIC", "-o", libPath, cSrc, "-lm", "-lpthread")

	driverBin := filepath.Join(root, "cffi1-driver")
	run(mod, goTool, "build", "-o", driverBin, "./driver")
	return run(root, driverBin, "-mode", mode, "-lib", libPath)
}

func TestWordDirectOracle(t *testing.T) {
	out := runCFFI1Driver(t, "oracle")
	if !strings.Contains(out, "ok: oracle matched across all 23 typed functions") {
		t.Fatalf("unexpected oracle output:\n%s", out)
	}
}

func TestPinUnderStackGrowth(t *testing.T) {
	out := runCFFI1Driver(t, "stackgrowth")
	if !strings.Contains(out, "ok: stack growth and GC preserved all pinned buffers") {
		t.Fatalf("unexpected stackgrowth output:\n%s", out)
	}
}

func TestPinBalanceOnErrorAndPanic(t *testing.T) {
	out := runCFFI1Driver(t, "pinbalance")
	if !strings.Contains(out, "ok: pin balance verified across 1000 errors and 1000 panics") {
		t.Fatalf("unexpected pinbalance output:\n%s", out)
	}
}

func TestWordDirectZeroAlloc(t *testing.T) {
	out := runCFFI1Driver(t, "zeroalloc")
	t.Log("\n" + strings.TrimSpace(out))
	if !strings.Contains(out, "ok: zero allocs/op verified") {
		t.Fatalf("unexpected zeroalloc output:\n%s", out)
	}
}
