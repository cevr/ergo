package build

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// TestDirectCompilesInParallel: the direct path starts the compiles of a
// module's packages together as far as the import graph allows (compile-
// speed CS10). With a compile of each of the width leaf packages, which
// import nothing of each other, held until all width are in flight, a
// cold build of the module completes; a path that compiles one package at
// a time never gets there, and the test's deadline releases the held
// compiles and names the count. It builds Go because the held processes
// are the direct path's real compiles; no in-process check starts them.
// Mutant: one compile at a time (cs-direct-serial-compiles).
func TestDirectCompilesInParallel(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	const width = 3
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(width + 1))
	dir := t.TempDir()
	files := map[string]string{"go.mod": "module example.local/par\n\ngo " + hostLang() + "\n"}
	var imports, calls []string
	leaves := map[string]bool{}
	for i := range width {
		files[fmt.Sprintf("l%d/l.go", i)] = fmt.Sprintf("package l%d\n\n//go:noinline\nfunc F(n int) int { return n + %d }\n", i, i)
		imports = append(imports, fmt.Sprintf("\t\"example.local/par/l%d\"", i))
		calls = append(calls, fmt.Sprintf("l%d.F(1)", i))
		leaves[fmt.Sprintf("example.local/par/l%d", i)] = true
	}
	files["main.go"] = "package main\n\nimport (\n\t\"os\"\n\t\"strconv\"\n\n" + strings.Join(imports, "\n") + "\n)\n\nfunc main() { os.Stdout.WriteString(strconv.Itoa(" + strings.Join(calls, " + ") + ")) }\n"
	for name, text := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	held, high := 0, 0
	all := make(chan struct{})
	timeout := testwait.After(t)
	stop := func() {
		select {
		case <-all:
		default:
			close(all)
		}
	}
	defer func(run func(*exec.Cmd, string) ([]byte, error)) { runCompile = run }(runCompile)
	runCompile = func(cmd *exec.Cmd, path string) ([]byte, error) {
		if leaves[path] {
			mu.Lock()
			held++
			high = max(high, held)
			if held == width {
				stop()
			}
			mu.Unlock()
			select {
			case <-all:
			case <-timeout:
				mu.Lock()
				t.Errorf("hung: %d of the %d leaf compiles were in flight at once", high, width)
				stop()
				mu.Unlock()
			}
		}
		return cmd.CombinedOutput()
	}
	var stderr bytes.Buffer
	report, err := Toolchain{Go: "go"}.Build(Cache{Root: t.TempDir()}, Module{Dir: dir}, filepath.Join(t.TempDir(), "par"), &stderr)
	if err != nil || !report.Direct {
		t.Fatalf("build: %v (%s)\n%s", err, report, stderr.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if high != width {
		t.Fatalf("at most %d leaf compiles were in flight at once, want the graph's width %d", high, width)
	}
}

// hostLang is the language version of the Go that runs the test, the go
// line the direct path accepts.
func hostLang() string { return "1." + fmt.Sprint(hostMinor()) }
