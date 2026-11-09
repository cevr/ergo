package lower

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The dispatcher (test-speed plan T2): buildAll links the programs of one
// call into one binary, so a test pays one link, not one per program. Each
// program becomes a package with an exported entry; the binary's main
// picks the entry by the name it was started under (bin/NAME is a symbolic
// link to it, as busybox does), so each program still runs as its own
// process, with the os.Args its own binary had. ERGO_TEST_SEPARATE_BINARIES=1
// selects the old path, one vetted binary per program, as the reference.

// separateBinaries selects the reference path of buildAll.
var separateBinaries = os.Getenv("ERGO_TEST_SEPARATE_BINARIES") == "1"

const (
	// dispatchEntry is the exported name of a program's main.
	dispatchEntry = "ErgoTestMain"
	// dispatchRegister is the renamed init that registers the program's
	// source map: the entry calls it first, so each process registers only
	// its own program's tables (v1.RegisterCalls keeps one set).
	dispatchRegister = "ergoTestRegisterCalls"
)

// asDispatchPackage rewrites one lowered main.go into a package the
// dispatcher imports: the package clause names a library package, main is
// exported as dispatchEntry, and the init that calls RegisterCalls becomes
// dispatchRegister, called first by the entry. Every edit stays on its
// line, so the //line directives and the Go lines below them are as before.
func asDispatchPackage(src string) (string, error) {
	if strings.Contains(src, dispatchEntry) || strings.Contains(src, dispatchRegister) {
		return "", fmt.Errorf("the program already names %s or %s", dispatchEntry, dispatchRegister)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, parser.SkipObjectResolution)
	if err != nil {
		return "", err
	}
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	type edit struct {
		from, to int
		text     string
	}
	edits := []edit{{off(f.Name.Pos()), off(f.Name.End()), "ergoprogram"}}
	var entry *ast.FuncDecl
	register := false
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Body == nil {
			continue
		}
		switch {
		case fd.Name.Name == "main":
			entry = fd
		case fd.Name.Name == "init" && registersCalls(fd):
			if register {
				return "", errors.New("two inits register the source map")
			}
			register = true
			edits = append(edits, edit{off(fd.Name.Pos()), off(fd.Name.End()), dispatchRegister})
		}
	}
	if entry == nil {
		return "", errors.New("no func main")
	}
	edits = append(edits, edit{off(entry.Name.Pos()), off(entry.Name.End()), dispatchEntry})
	if register {
		at := off(entry.Body.Lbrace) + 1
		edits = append(edits, edit{at, at, " " + dispatchRegister + "();"})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].from > edits[j].from })
	for _, e := range edits {
		src = src[:e.from] + e.text + src[e.to:]
	}
	return src, nil
}

// registersCalls reports whether fd's body is the one call X.RegisterCalls(…)
// that callTable (calls.go) writes.
func registersCalls(fd *ast.FuncDecl) bool {
	if len(fd.Body.List) != 1 {
		return false
	}
	es, ok := fd.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "RegisterCalls"
}

// dispatcherMain is the dispatcher's main package: it runs the entry of
// the program named by the base of os.Args[0].
func dispatcherMain(names []string) string {
	var b strings.Builder
	b.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"path/filepath\"\n\t\"strings\"\n\n")
	for i, n := range names {
		fmt.Fprintf(&b, "\tp%d %s\n", i, strconv.Quote("ergo.local/program/p/"+n))
	}
	b.WriteString(")\n\nvar programs = map[string]func(){\n")
	for i, n := range names {
		fmt.Fprintf(&b, "\t%s: p%d.%s,\n", strconv.Quote(n), i, dispatchEntry)
	}
	b.WriteString("}\n\nfunc main() {\n")
	b.WriteString("\tname := strings.TrimSuffix(filepath.Base(os.Args[0]), \".exe\")\n")
	b.WriteString("\tentry, ok := programs[name]\n")
	b.WriteString("\tif !ok {\n\t\tfmt.Fprintf(os.Stderr, \"dispatch: no program %q\\n\", name)\n\t\tos.Exit(125)\n\t}\n")
	b.WriteString("\tentry()\n}\n")
	return b.String()
}

// dispatchSeed replays one seed of TestDispatchAgreesWithSeparate.
var dispatchSeed = flag.Uint64("dispatch.seed", 0, "replay one seed of TestDispatchAgreesWithSeparate")

// dispatchSeeds are the seeds the full tier runs.
var dispatchSeeds = []uint64{1, 2}

// dispatchDefects are programs whose defect reports name Ergo frames from
// the program's own source map: if each process did not register its own
// tables, a report would name another program's declarations, or Go frames.
var dispatchDefects = []string{
	`func pick(xs []int) int { xs[4] }

func a(xs []int) int { pick(xs) + 1 }

func main() needs Console {
	let v = a([]int{1})
	if v > 0 { Console.Println("never") }
}
`,
	`func down(xs []int, n int) int {
	if n == 0 { xs[n + 5] } else { down(xs, n - 1) }
}

func main() needs Console {
	Console.Println("before")
	let v = down([]int{1}, 3)
	if v > 0 { Console.Println("never") }
}
`,
	`func helperOne(xs []int) int { xs[2] }

func helperTwo(xs []int) int { helperOne(xs) }

func helperThree(xs []int) int { helperTwo(xs) }

func main() needs Console {
	let v = helperThree([]int{7})
	if v > 0 { Console.Println("never") }
}
`,
}

// TestDispatchAgreesWithSeparate is the property of the dispatcher: for
// each program of a seeded sample (corpus programs that lower with an
// entry, and the programs of dispatchDefects), the dispatcher's run (its
// stdout, stderr and exit code) equals the run of the program's own
// binary, the reference path. The runs go in a seeded order, all from one
// dispatcher, so state that one program's package initialization leaks
// into another's run shows. A program whose own binary gives two different
// runs is not compared (it reads the clock or the scheduler). A failing
// seed prints the program; -dispatch.seed=N replays it.
func TestDispatchAgreesWithSeparate(t *testing.T) {
	needsBuild(t)
	seeds := dispatchSeeds
	if *dispatchSeed != 0 {
		seeds = []uint64{*dispatchSeed}
	}
	sources, err := corpusDomain()
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x6469737061746368))
		var programs []*lowered
		for i, src := range dispatchDefects {
			programs = append(programs, mustLower(t, fmt.Sprintf("d%d_%d", seed, i), src))
		}
		for _, i := range r.Perm(len(sources)) {
			if len(programs) == len(dispatchDefects)+24 {
				break
			}
			o := lowerSource(fmt.Sprintf("c%d_%d", seed, i), sources[i], true)
			if o.file != nil && len(o.diags) == 0 && o.panicked == nil && o.err == nil && len(o.refused) == 0 && strings.Contains(o.src, "func main(") {
				programs = append(programs, o)
			}
		}
		r.Shuffle(len(programs), func(i, j int) { programs[i], programs[j] = programs[j], programs[i] })
		dispatched := buildDispatch(t, programs)
		separate := buildSeparate(t, programs)
		if dispatched == nil || separate == nil {
			t.Fatalf("seed %d: the sample does not build", seed)
		}
		compared, unstable := 0, 0
		for _, p := range programs {
			want := dispatchRun(t, separate[p.name])
			got := dispatchRun(t, dispatched[p.name])
			if got == want {
				compared++
				continue
			}
			if again := dispatchRun(t, separate[p.name]); again != want {
				unstable++
				continue
			}
			t.Errorf("seed %d (-dispatch.seed=%d): %s runs differently in the dispatcher\ndispatcher: %s\nown binary: %s\nsource:\n%s", seed, seed, p.name, got, want, p.src)
		}
		if compared < len(dispatchDefects)+8 {
			t.Errorf("seed %d: only %d of %d programs compared (%d unstable): the sample lost its domain?", seed, compared, len(programs), unstable)
		}
		t.Logf("seed %d: %d programs compared, %d unstable", seed, compared, unstable)
	}
}

// dispatchRun runs exe with no input and a deadline and returns its exit
// code, stdout and stderr as one comparable string.
func dispatchRun(t *testing.T, exe string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Stdin = bytes.NewReader(nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runProgram(t, cmd)
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("exit %d\nstdout %q\nstderr %q", code, stdout.String(), stderr.String())
}

// TestAsDispatchPackage: the rewrite exports main, turns the registering
// init into the entry's first call and leaves every line where it was.
func TestAsDispatchPackage(t *testing.T) {
	src := "package main\n\nimport v1 \"rt\"\n\nfunc main() {\n\tv1.Main()\n}\n\nfunc init() {\n\tv1.RegisterCalls(a[:], b[:])\n}\n\nfunc init() { other() }\n"
	got, err := asDispatchPackage(src)
	if err != nil {
		t.Fatal(err)
	}
	want := "package ergoprogram\n\nimport v1 \"rt\"\n\nfunc ErgoTestMain() { ergoTestRegisterCalls();\n\tv1.Main()\n}\n\nfunc ergoTestRegisterCalls() {\n\tv1.RegisterCalls(a[:], b[:])\n}\n\nfunc init() { other() }\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Count(got, "\n") != strings.Count(src, "\n") {
		t.Fatal("the rewrite moved lines")
	}
	if _, err := asDispatchPackage("package main\n\nfunc init() {}\n"); err == nil {
		t.Fatal("a program with no main rewrote")
	}
	if _, err := asDispatchPackage("package main\n\nfunc ErgoTestMain() {}\n\nfunc main() {}\n"); err == nil {
		t.Fatal("a program that names the entry rewrote")
	}
}

// TestDispatcherMain: the dispatcher picks the program by the base name it
// was started under, and refuses a name it does not hold.
func TestDispatcherMain(t *testing.T) {
	got := dispatcherMain([]string{"a", "b_1"})
	for _, want := range []string{
		"\tp0 \"ergo.local/program/p/a\"\n",
		"\t\"b_1\": p1.ErgoTestMain,\n",
		"filepath.Base(os.Args[0])",
		"os.Exit(125)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dispatcher lacks %q:\n%s", want, got)
		}
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "main.go", got, 0); err != nil {
		t.Fatalf("the dispatcher does not parse: %v\n%s", err, got)
	}
}

// symlinkProgram makes bin/NAME start the dispatcher under NAME.
func symlinkProgram(t *testing.T, dispatcher, exe string) {
	t.Helper()
	if err := os.Symlink(dispatcher, exe); err != nil {
		t.Fatal(err)
	}
}

// dispatchRoot is where buildDispatch puts the dispatcher, beside bin.
func dispatchRoot(root string) string { return filepath.Join(root, "dispatcher") }
