package lower

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std/gostd"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/toolchain"
	v1 "ergo.local/prototype/runtime/v1"
)

// readsFixGo is a Go package whose Bump writes its slice. readsFixBinding
// edits the binding ergo bind writes for it to say Bump only reads the
// slice: a planted false fact.
const readsFixGo = `package readsfix

// Bump adds one to xs[0] and returns it.
func Bump(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	xs[0]++
	return xs[0]
}

// Rev writes src reversed into dst and returns dst's last element: it
// reads src and writes dst.
func Rev(src, dst []int) int {
	n := min(len(src), len(dst))
	for i := 0; i < n; i++ {
		dst[i] = src[len(src)-1-i]
	}
	if len(dst) == 0 {
		return 0
	}
	return dst[len(dst)-1]
}

// Poke writes b[0] and returns len(b) (TestViewFaultIsDefect).
func Poke(b []byte) int {
	if len(b) > 0 {
		b[0] = 'z'
	}
	return len(b)
}
`

// readsFixProgram passes live slices to readsfix. The value model: Ergo's
// slices never change, so bump gives 2 + 1; rev gives 1, the last of
// [3, 2, 1], with one slice as both operands; rev2 gives 1, and src and
// dst keep 1 and 0.
const readsFixProgram = `import go readsfix

func bump(xs []int) int {
	readsfix.Bump(xs) + xs[0]
}

func rev() int {
	let xs = []int{1, 2, 3}
	readsfix.Rev(xs, xs)
}

func rev2() string {
	let src = []int{1, 2, 3}
	let dst = []int{0, 0, 0}
	let r = readsfix.Rev(src, dst)
	"\(r)|\(src[0])|\(dst[0])"
}

func main() needs Console | go.Foreign {
	Console.Println("\(bump([]int{1, 2}))")
	Console.Println("\(rev())")
	Console.Println(rev2())
}

test BumpLeavesItsArgument {
	assert bump([]int{1, 2}) == 3
}
`

// readsfix is in the pure tier in this test binary only, so a test may
// call Bump (a test never gets go.Foreign; interop.md, Host calls) and the
// test build's compare runs where `ergo test` runs. Its pure row acts only
// on a current validated guard line (LAWFFI LF2d, check.PureActs), so the
// checker's ledger gets one, planted as the row is.
func init() {
	v1.PureImports = append(v1.PureImports, "readsfix")
	line := gofacts.Entry{Key: gofacts.PureKey("readsfix"), Verdict: "validated", By: "planted", With: toolchain.Version, Digest: gofacts.PureDigest("readsfix", nil, toolchain.Version)}.Line()
	shipped := check.FactLedger
	check.FactLedger = func() string { return shipped() + "\n" + line + "\n" }
}

// plantReadsFixPure makes readsfix's pure row act for the rest of t: since
// LF2d a package of v1.PureImports is pure only on its guard line in the
// std ledger, current and validated (check.PureActs), so t plants that
// line beside the shipped ones, as it plants the reads lines (LF-TESTFOREIGN).
func plantReadsFixPure(t *testing.T) {
	t.Helper()
	saved := check.FactLedger
	t.Cleanup(func() { check.FactLedger = saved })
	line := "fact " + gofacts.PureKey("readsfix") + " validated by guard with " + toolchain.Version + " " + gofacts.PureDigest("readsfix", v1.PureImportsExcept["readsfix"], toolchain.Version) + "\n"
	ledger := saved() + line
	check.FactLedger = func() string { return ledger }
	if !check.PureActs("readsfix") {
		t.Fatalf("readsfix's planted pure line does not act: %q", line)
	}
}

// readsFixImporter gives readsfix's binding, with the planted reads line,
// beside Go's standard library.
func readsFixImporter(t *testing.T) hresImporter {
	t.Helper()
	plantReadsFixPure(t)
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "readsfix.go", readsFixGo, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{}).Check("readsfix", fs, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := gobind.File([]*types.Package{pkg}, []gobind.Platform{{GOOS: "linux", GOARCH: "amd64"}}, "go1.27")
	if err != nil {
		t.Fatal(err)
	}
	planted := binding
	for fn, line := range map[string]string{"Bump": "// ergo:reads xs", "Rev": "// ergo:reads src", "Poke": "// ergo:reads b (vouched: planted)"} {
		next := strings.Replace(planted, "extern go readsfix."+fn+" ", line+"\nextern go readsfix."+fn+" ", 1)
		if next == planted {
			t.Fatalf("no extern of readsfix.%s in its binding:\n%s", fn, binding)
		}
		planted = next
	}
	return hresImporter{check.NewGoBindings(func(path string) (string, bool) {
		if path == "readsfix" {
			return planted, true
		}
		return gostd.Binding(path)
	})}
}

// readsFixFiles are readsfix's module files beside a program.
func readsFixFiles() []File {
	return []File{
		{Path: "go.mod", Data: []byte(goMod + "\nrequire readsfix v0.0.0\n\nreplace readsfix => ./readsfix\n")},
		{Path: "readsfix/go.mod", Data: []byte("module readsfix\n\ngo " + goVersion + "\n")},
		{Path: "readsfix/readsfix.go", Data: []byte(readsFixGo)},
	}
}

// hostReadsFalseFact is TestHostCopyElisionAgrees's planted false fact:
// the run build passes the live slice to Bump, which writes it, so the
// value Ergo holds changes (the class of a false reads fact: a wrong value
// in memory-safe Go); the test build gives Bump a copy and its compare
// after the call is the named defect. It builds Go because the subject is
// what Go's write does to the running program's values and the defect the
// test binary raises; no in-process check runs the Go function.
func hostReadsFalseFact(t *testing.T) {
	t.Helper()
	file, err := syntax.Parse(readsFixProgram)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, readsFixImporter(t))
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: readsFixProgram}}}
	run := mustLowerUnit(t, "reads_false_run", u, false)
	test := mustLowerUnit(t, "reads_false_test", u, true)
	run.extra, test.extra = readsFixFiles(), readsFixFiles()
	exes := buildAll(t, []*lowered{run, test}, true)
	if exes == nil {
		return
	}
	// 4: Bump wrote Ergo's slice, as the false fact let it; the true
	// facts change nothing: Rev's dst is copied whatever src's fact says.
	if stdout, stderr, code := runExe(t, exes[run.name]); code != 0 || stdout != "4\n1\n1|1|0\n" {
		t.Errorf("the run build: exit %d, stdout %q, want \"4\\n1\\n1|1|0\\n\"; stderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
	const defect = "Go wrote xs, which its binding says readsfix.Bump only reads, at main.ergo:4:2"
	if stdout, stderr, code := runExe(t, exes[test.name]); code == 0 || !strings.Contains(stdout+stderr, defect) {
		t.Errorf("the test build of the false fact: exit %d, want the defect %q; stdout:\n%s\nstderr:\n%s", code, defect, stdout, firstLines(stderr, 12))
	}
}

// joinsProgram passes a live []string, a parameter read after the calls,
// to strings.Join and path/filepath.Join (a variadic binding).
const joinsProgram = `import go strings
import go path/filepath

func both(parts []string) int {
	len(strings.Join(parts, ",")) + len(filepath.Join(parts)) + len(parts)
}

func main() needs Console {
	Console.Println("\(both([]string{"a", "b"}))")
}
`

// TestJoinsTakeNoCopy: strings.Join and path/filepath.Join only read
// their []string (gofacts' vouched reads rows, validated in std's ledger),
// so the run build passes a live slice with no copy and the test build
// compares each after its call (JOIN, from NP4f-4: the copy cost the
// import go forms 1.659x of the runtime body). The lowered text is the
// check because the absence of the copy is the promise.
//
// Mutants it alone catches: join-reads-strings-dropped and
// join-reads-filepath-dropped (a binding without its reads line).
func TestJoinsTakeNoCopy(t *testing.T) {
	file, err := syntax.Parse(joinsProgram)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: joinsProgram}}}
	text := func(o *lowered) string {
		out := ""
		for _, f := range o.files {
			if strings.HasSuffix(f.Path, ".go") {
				out += string(f.Data)
			}
		}
		return out
	}
	run := mustLowerUnit(t, "joins_run", u, false)
	if s := text(run); strings.Contains(s, "Clone(") {
		t.Errorf("the run build copies a Join operand:\n%s", s)
	}
	test := mustLowerUnit(t, "joins_test", u, true)
	if n := strings.Count(text(test), ".GoReadsCheck("); n != 2 {
		t.Errorf("the test build compares %d Join operands after the call, want 2:\n%s", n, text(test))
	}
	typecheckGo(t, []*lowered{run, test})
}

// readsProgram calls Go functions whose bindings carry a `reads` fact
// (sort.SearchInts a, sort.SearchStrings a) on live slices: a parameter,
// a field and a local read after the call.
const readsProgram = `import go sort

type Index { keys []int }

func find(xs []int, n int) int needs go.Foreign {
	sort.SearchInts(xs, n) + len(xs)
}

func field(ix Index, n int) int needs go.Foreign {
	sort.SearchInts(ix.keys, n) + len(ix.keys)
}

func local(n int) string needs go.Foreign {
	let ss = []string{"a", "\(n)"}
	let i = sort.SearchStrings(ss, "b")
	"\(i)" + ss[0]
}

func main() needs Console | go.Foreign {
	Console.Println("\(find([]int{1, 3, 5}, 3)) \(field(Index{keys: []int{2, 4}}, 4)) \(local(7))")
}
`

// TestReadsCallTakesNoCopy is LF1's promised form (LAWFFI; interop.md,
// Foreign facts 6): a live slice that a Go function only reads (a checked
// `reads` fact) goes to Go with no copy in the run build, as a parameter,
// a field or a local read after the call; the test build copies each and
// compares it after the call, one v1.GoReadsCheck per site. The lowered
// text is the check because the absence of the copy is the promise.
//
// Mutants it alone catches: ffi-reads-off (the run build copies a reads
// operand) and ffi-reads-guard-off (the test build copies and compares
// nothing).
func TestReadsCallTakesNoCopy(t *testing.T) {
	file, err := syntax.Parse(readsProgram)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: readsProgram}}}
	count := func(o *lowered, s string) (int, string) {
		n, text := 0, ""
		for _, f := range o.files {
			if strings.HasSuffix(f.Path, ".go") {
				n += bytes.Count(f.Data, []byte(s))
				text += string(f.Data)
			}
		}
		return n, text
	}
	run := mustLowerUnit(t, "reads_run", u, false)
	if n, text := count(run, "Clone("); n != 0 {
		t.Errorf("the run build copies %d reads operands:\n%s", n, text)
	}
	test := mustLowerUnit(t, "reads_test", u, true)
	if n, text := count(test, ".GoReadsCheck("); n != 3 {
		t.Errorf("the test build compares %d reads operands after the call, want 3:\n%s", n, text)
	}
	typecheckGo(t, []*lowered{run, test})
}
