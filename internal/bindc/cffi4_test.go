package bindc

import (
	"errors"
	"go/ast"
	goparser "go/parser"
	gotoken "go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// TestEmitCGuardHooks verifies that GoPackage emits cGuardIn/cGuardOut hooks,
// SetCGuardHooks, and matching cleanup calls on both statusError and normal
// completion paths for every in_buffer and out_buffer parameter, and that
// GoGuardFile formats cleanly.
func TestEmitCGuardHooks(t *testing.T) {
	for _, h := range headers {
		t.Run(h.dir, func(t *testing.T) {
			m := parseHeader(t, h.dir, h.header)
			b := bindHeader(t, h.dir, h.header, m)
			name, src, err := GoPackage(b)
			if err != nil {
				t.Fatalf("GoPackage: %v", err)
			}
			fset := gotoken.NewFileSet()
			file, err := goparser.ParseFile(fset, name, src, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			if !strings.Contains(string(src), "func SetCGuardHooks(") {
				t.Fatalf("%s: missing SetCGuardHooks export", name)
			}

			typedByName := map[string]*BoundFunc{}
			for _, f := range b.Funcs {
				if f.goTyped() {
					typedByName[f.GoName] = f
				}
			}
			inChecked, outChecked := 0, 0
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Body == nil {
					continue
				}
				bf, ok := typedByName[fn.Name.Name]
				if !ok {
					continue
				}
				wantIn, wantOut := 0, 0
				for _, q := range bf.Positions {
					if q.Class == ClassInBuffer {
						wantIn++
					}
					if q.Class == ClassOutBuffer {
						wantOut++
					}
				}
				if wantIn == 0 && wantOut == 0 {
					continue
				}
				gotIn, gotOut := 0, 0
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					c, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch exprString(c.Fun) {
					case "cGuardIn":
						gotIn++
					case "cGuardOut":
						gotOut++
					}
					return true
				})
				if gotIn != wantIn {
					t.Errorf("%s: got %d cGuardIn calls, want %d", bf.GoName, gotIn, wantIn)
				}
				if gotOut != wantOut {
					t.Errorf("%s: got %d cGuardOut calls, want %d", bf.GoName, gotOut, wantOut)
				}
				inChecked += gotIn
				outChecked += gotOut
			}
			if inChecked == 0 || outChecked == 0 {
				t.Fatalf("%s: expected both in_buffer and out_buffer guard hooks, got in=%d out=%d", name, inChecked, outChecked)
			}

			gName, gSrc, err := GoGuardFile(b.Contract.Go.Package)
			if err != nil {
				t.Fatalf("GoGuardFile: %v", err)
			}
			if !strings.HasSuffix(gName, "_cguard.go") {
				t.Fatalf("GoGuardFile name = %q", gName)
			}
			if !strings.Contains(string(gSrc), "//go:build ergocguard") || !strings.Contains(string(gSrc), "SetCGuardHooks(v1.GuardCReadPtr, v1.GuardCWritePtr)") {
				t.Fatalf("GoGuardFile content unexpected:\n%s", gSrc)
			}
		})
	}
}

const cffi4DriverMain = `package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"ergo.local/bind/fixture"
)

func must(what string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
		os.Exit(2)
	}
}

func main() {
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	mode, lib := os.Args[1], os.Args[2]
	must("Load", fixture.Load(lib))
	ctx, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 8})
	must("ContextCreate", err)
	defer func() { _ = fixture.ContextDestroy(ctx) }()
	node, err := fixture.NodeCreate(ctx, 1)
	must("NodeCreate", err)

	// In C code, a guard-page fault delivers a real SIGSEGV; disable
	// panic-on-fault so Go does not mask anything on Go frames either.
	debug.SetPanicOnFault(false)

	switch mode {
	case "valid":
		want := []byte("hello-guard")
		must("NodeSetName", fixture.NodeSetName(ctx, node, want))
		buf := make([]byte, len(want))
		n, err := fixture.NodeReadName(ctx, node, buf)
		must("NodeReadName", err)
		if int(n) != len(want) || string(buf[:n]) != string(want) {
			fmt.Fprintf(os.Stderr, "mismatch: got %q (%d), want %q\n", buf[:n], n, want)
			os.Exit(3)
		}
		// Run enough guarded calls to exercise ring eviction (> 64).
		for range 80 {
			must("NodeSetName loop", fixture.NodeSetName(ctx, node, want))
			n, err = fixture.NodeReadName(ctx, node, buf)
			must("NodeReadName loop", err)
			if int(n) != len(want) || string(buf[:n]) != string(want) {
				fmt.Fprintf(os.Stderr, "loop mismatch\n")
				os.Exit(3)
			}
		}
		fmt.Println("ok: valid")
	case "over-read", "const-write":
		must("NodeSetName", fixture.NodeSetName(ctx, node, []byte("abcdef")))
	case "post-return-retain":
		must("NodeSetName", fixture.NodeSetName(ctx, node, []byte("abcdef")))
		_, err := fixture.NodeCount(ctx)
		must("NodeCount", err)
	case "out-overflow":
		must("NodeSetName", fixture.NodeSetName(ctx, node, []byte("abcdef")))
		buf := make([]byte, 6)
		_, err := fixture.NodeReadName(ctx, node, buf)
		must("NodeReadName", err)
	default:
		os.Exit(2)
	}
}
`

// TestCGuardPageCatchesViolations builds real libfixture.so and 4 buggy C
// variants (over-read 1 byte, const-write, post-return retention, out-buffer
// overflow), emits fixture.go + fixture_cguard.go with -tags ergocguard, and
// proves all 4 C memory violations deterministically fault with SIGSEGV while
// valid C calls pass cleanly under both default and -tags ergocguard builds.
func TestCGuardPageCatchesViolations(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("mmap/mprotect guard pages require linux or darwin")
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler (cc) on PATH")
	}
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")

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
	if modCache == "" {
		t.Skip("purego@v0.11.3 not cached in GOMODCACHE")
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
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
	goMod := "module ergo.local/bind\n\ngo 1.27\n\nrequire (\n\tergo.local/prototype v0.0.0\n\tgithub.com/ebitengine/purego v0.11.3\n)\n\nreplace ergo.local/prototype => " + repoRoot + "\n"
	write(filepath.Join(mod, "go.mod"), []byte(goMod))
	write(filepath.Join(mod, "go.sum"), readInput(t, "testdata/purego.sum"))

	m := parseHeader(t, "testdata/fixture", "fixture.h")
	b := bindHeader(t, "testdata/fixture", "fixture.h", m)
	goName, goSrc, err := GoPackage(b)
	if err != nil {
		t.Fatalf("GoPackage(fixture): %v", err)
	}
	guardName, guardSrc, err := GoGuardFile("fixture")
	if err != nil {
		t.Fatalf("GoGuardFile(fixture): %v", err)
	}
	write(filepath.Join(mod, "fixture", goName), goSrc)
	write(filepath.Join(mod, "fixture", guardName), guardSrc)
	write(filepath.Join(mod, "driver", "main.go"), []byte(cffi4DriverMain))

	env := append(os.Environ(),
		"CGO_ENABLED=0",
		"GOFLAGS=-mod=readonly -modcacherw",
		"GOMODCACHE="+modCache,
		"GOWORK=off",
		"GOTRACEBACK=none",
	)
	runMust := func(dir, name string, args ...string) string {
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
	cSrc, _ := filepath.Abs("testdata/fixture/fixture.c")
	variants := []struct {
		mode string
		flag string
	}{
		{"valid", ""},
		{"over-read", "-DFIXTURE_BUG_OVERREAD"},
		{"const-write", "-DFIXTURE_BUG_CONST_WRITE"},
		{"post-return-retain", "-DFIXTURE_BUG_RETAIN"},
		{"out-overflow", "-DFIXTURE_BUG_OUT_OVERFLOW"},
	}
	libs := map[string]string{}
	for _, v := range variants {
		lib := filepath.Join(root, "libfixture-"+v.mode+ext)
		args := []string{"-std=c11", "-shared", "-fPIC", "-o", lib}
		if v.flag != "" {
			args = append(args, v.flag)
		}
		args = append(args, cSrc, "-lm", "-lpthread")
		runMust(root, cc, args...)
		libs[v.mode] = lib
	}

	// Build BOTH default (no tag) and guarded (-tags ergocguard) drivers.
	plainBin := filepath.Join(root, "driver-plain")
	guardBin := filepath.Join(root, "driver-guard")
	runMust(mod, goTool, "build", "-o", plainBin, "./driver")
	runMust(mod, goTool, "build", "-tags", "ergocguard", "-o", guardBin, "./driver")

	if out := runMust(root, plainBin, "valid", libs["valid"]); !strings.Contains(out, "ok: valid") {
		t.Fatalf("plain valid run failed: %s", out)
	}
	if out := runMust(root, guardBin, "valid", libs["valid"]); !strings.Contains(out, "ok: valid") {
		t.Fatalf("guarded valid run failed: %s", out)
	}

	for _, bug := range []string{"over-read", "const-write", "post-return-retain", "out-overflow"} {
		t.Run(bug, func(t *testing.T) {
			cmd := exec.Command(guardBin, bug, libs[bug])
			cmd.Dir = root
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%s: guarded C call exited 0, expected SIGSEGV\n%s", bug, out)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("%s: unexpected error %v", bug, err)
			}
			ws, ok := exitErr.Sys().(syscall.WaitStatus)
			if !ok {
				t.Fatalf("%s: missing WaitStatus: %v", bug, err)
			}
			sigged := ws.Signaled() && (ws.Signal() == syscall.SIGSEGV || ws.Signal() == syscall.SIGBUS)
			runtimeFault := strings.Contains(string(out), "SIGSEGV") || strings.Contains(string(out), "SIGBUS") || strings.Contains(string(out), "segmentation violation")
			if !sigged && !runtimeFault {
				t.Fatalf("%s: expected SIGSEGV/SIGBUS, got exit=%d signaled=%v sig=%v\n%s", bug, exitErr.ExitCode(), ws.Signaled(), ws.Signal(), out)
			}
		})
	}
}
