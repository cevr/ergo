package lower

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// goExportLibrary is the Ergo library a Go host calls in TestGoCallsErgo.
const goExportLibrary = `import go fmt
import go io
import go strconv
import scope

func greeting() string { return "hello " }

let Greeting = greeting()

func numbers() []int { return []int{1, 2, 3} }

let Numbers = numbers()

// Nums returns a package-level let: Go gets a copy it may write.
func Nums() []int { return Numbers }

type Counter { N int }

// Write makes a Counter an io.Writer: Go calls it through the adapter.
func Write(c Counter, p []byte) int fails go.Error needs go.Foreign {
	return len(p)
}

type Pair { A int }

func Hello(name string) string { return Greeting + name }

func Sum(xs []int) int {
	let mut n = 0
	for x in xs {
		n += x
	}
	return n
}

func Atoi(s string) int fails go.Error { return strconv.Atoi(s) }

// Count passes an Ergo value where Go takes an io.Writer.
func Count(s string) int needs go.Foreign {
	return io.WriteString(Counter{N: 0}, s) catch {
	case go.Error: -1
	}
}

// Probe calls back into Go on the goroutine of the Go call.
func Probe(p fmt.Stringer) string needs go.Foreign { return p.String() }

// Wait tells Go it is about to sleep, then sleeps until Go cancels it.
func Wait(ready fmt.Stringer) needs Console | Ambient | go.Foreign {
	scope.OnExit() {
		Console.Println("released")
	}
	let _ = ready.String()
	with context { Scheduler: NewLiveScheduler() } {
		Scheduler.Sleep(1h)
	}
	Console.Println("woke")
}

func Unwrap(p Pair) int { return p.A }
`

// goExportFailing is a second library, whose package-level let has a
// defect: every call fails with it, the first and the later ones.
const goExportFailing = `func zero() int { return 0 }

let Bad = 10 / zero()

func Get() int { return Bad + 1 }
`

// goExportHost is the Go program that calls the library.
const goExportHost = `package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"example.com/bad"
	"example.com/greet"
)

func init() { runtime.LockOSThread() }

// goid is the calling goroutine's id, from its stack header.
func goid() string {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	return strings.Fields(string(buf))[1]
}

type probe struct{}

func (probe) String() string { return goid() }

// canceller cancels the call's context when the Ergo body says it is
// about to sleep: the Wait call runs on main, with no goroutine of the
// host's own.
type canceller context.CancelFunc

func (c canceller) String() string { c(); return "" }

func main() {
	ctx := context.Background()
	h, err := greet.Hello(ctx, "go")
	fmt.Println(h, err)
	xs := []int{1, 2, 3}
	n, err := greet.Sum(ctx, xs)
	fmt.Println(n, err)
	_, err = greet.Atoi(ctx, "x")
	fmt.Println(err != nil && strings.Contains(err.Error(), "invalid syntax"))
	ns, _ := greet.Nums(ctx)
	ns[0] = 99
	ns, _ = greet.Nums(ctx)
	fmt.Println("nums", ns[0])
	c, err := greet.Count(ctx, "hello")
	fmt.Println(c, err)
	id, err := greet.Probe(ctx, probe{})
	fmt.Println("same goroutine", id == goid(), err)
	// A context that can be cancelled and is not during the call: no
	// goroutine starts or ends around the call, so the count waits for
	// nothing; a watcher that Call left behind would still be blocked.
	lctx, lcancel := context.WithCancel(ctx)
	before := runtime.NumGoroutine()
	_, _ = greet.Hello(lctx, "go")
	fmt.Println("leaked", runtime.NumGoroutine()-before)
	lcancel()
	cctx, cancel := context.WithCancel(ctx)
	err = greet.Wait(cctx, canceller(cancel))
	fmt.Println(errors.Is(err, context.Canceled))
	for range 2 {
		v, err := bad.Get(ctx)
		fmt.Println("bad", v, err != nil && strings.Contains(err.Error(), "divide by zero"))
	}
}
`

// TestGoCallsErgo is IG11's acceptance (docs/spec/interop.md, Go calls
// Ergo; #202's inbound control): a Go program calls an Ergo library that
// LowerLibrary emitted. Each exported function whose signature Go can
// spell is a Go function of the same name taking a context first: a pure
// one, one over a slice, one that fails with a go.Error, one that passes
// an Ergo value where Go takes an io.Writer (the adapter), one that calls
// a Go interface back on the goroutine of the Go call (main-thread
// affinity: the host locked its OS thread, and the body runs on it), and
// one blocked in Scheduler.Sleep that the Go caller cancels: its release
// runs and the call returns context.Canceled; a call with a context that
// is not cancelled leaves no goroutine, counted where none starts or ends
// (no timing: batch-16a r4 on base). A function over an Ergo record is
// not exported, with its reason.
//
// Mutants it alone catches: Call ignores ctx (Wait never returns: the
// test's deadline names it); Call returns the interruption instead of
// ctx.Err(); the body runs on a new goroutine (the goroutine ids differ);
// the adapter passes the Ergo value as is (go build fails); the package
// lets never run (Hello prints "go"); a slice result not copied (nums 99);
// the once forgets that the lets failed (the second bad.Get returns 1,
// nil: batch-15 IG11 M1); Call watches ctx in a goroutine it never stops
// (leaked 1: mutant ig11-call-watcher-leaks); Call's watcher never ends
// after a cancel (Call waits for it, so Wait never returns and the
// deadline names it: mutant 16a-r5-cancel-watcher-stuck).
//
// It builds Go because the subject is a Go program calling emitted Go: only
// go build and a run show the exported functions, the adapter and the
// cancellation.
func TestGoCallsErgo(t *testing.T) {
	needsBuild(t)
	lower := func(src, name string) []File {
		file, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		pkg, diags := check.Check("main", []*syntax.File{file}, nil)
		if len(diags) > 0 {
			t.Fatalf("check: %v", diags)
		}
		u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
		files, err := LowerLibrary(u, "example.com/"+name, name)
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	files := lower(goExportLibrary, "greet")
	root := t.TempDir()
	write := func(rel string, data []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var lib string
	for _, f := range append(files, Runtime()...) {
		write("greet/"+f.Path, f.Data)
		if f.Path == "greet.go" {
			lib = string(f.Data)
		}
	}
	if !strings.Contains(lib, "// Not exported to Go: Unwrap: parameter 1 has the type main.Pair.") {
		t.Errorf("the library does not list Unwrap as not exported:\n%s", lib)
	}
	for _, f := range lower(goExportFailing, "bad") {
		write("bad/"+f.Path, f.Data)
	}
	write("host/go.mod", []byte("module host\n\ngo "+goVersion+"\n\nrequire example.com/greet v0.0.0\n\nrequire example.com/bad v0.0.0\n\nrequire "+runtimeModule+" v0.0.0\n\nreplace example.com/greet => ../greet\n\nreplace example.com/bad => ../bad\n\nreplace "+runtimeModule+" => ../greet/"+runtimeDir+"\n"))
	write("host/main.go", []byte(goExportHost))
	exe := filepath.Join(root, "host.exe")
	build := exec.Command("go", "build", "-o", exe, ".")
	build.Dir = filepath.Join(root, "host")
	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s\nlibrary:\n%s", err, out, lib)
	}
	cmd := exec.Command(exe)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := runProgram(t, cmd); err != nil {
		t.Fatalf("host: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	want := "hello go <nil>\n6 <nil>\ntrue\nnums 1\n5 <nil>\nsame goroutine true <nil>\nleaked 0\nreleased\ntrue\nbad 0 true\nbad 0 true\n"
	if got := stdout.String(); got != want {
		t.Errorf("host output:\n%s\nwant:\n%s\nstderr:\n%s", got, want, stderr.String())
	}
}
