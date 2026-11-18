package lower

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/toolchain"
)

// viewFalseProgram passes the bytes of a string to readsfix.Poke, which
// writes them, under a planted row that vouches it only reads them.
const viewFalseProgram = `import go readsfix
import strings

func poke(s string) int {
	readsfix.Poke(strings.Bytes(s))
}

func main() needs Console | go.Foreign {
	Console.Println("\(poke("abc"))")
}

test Poke {
	assert poke("abc") == 3
}
`

// TestViewFaultIsDefect is LF2b's acceptance (4): under a planted false
// row that allows a view (readsfix.Poke writes its operand), the run build
// passes a view and the test build a guarded copy, whose write faults and
// is the named defect. It builds Go because the subject is a page fault of
// the running program and the defect the program reports; no in-process
// check runs the Go function under the guard's page protection.
func TestViewFaultIsDefect(t *testing.T) {
	needsBuild(t)
	row := gofacts.Row{Target: "readsfix.Poke", Fact: "reads", Param: "b", Reason: "planted", View: true}
	savedRows, savedLedger := gofacts.Rows, shippedLedger
	t.Cleanup(func() { gofacts.Rows, shippedLedger = savedRows, savedLedger })
	gofacts.Rows = append(append([]gofacts.Row{}, savedRows...), row)
	entries := map[string]LedgerEntry{}
	e := LedgerEntry{Fact: row.Key(), Verdict: "validated", By: "guard", With: toolchain.Version, Digest: row.Digest(toolchain.Version)}
	entries[e.Key()] = e
	shippedLedger = func() string { return FormatLedger(entries) }
	file, err := syntax.Parse(viewFalseProgram)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, readsFixImporter(t))
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: viewFalseProgram}}}
	test := mustLowerUnit(t, "view_false_test", u, true)
	test.extra = readsFixFiles()
	exes := buildAll(t, []*lowered{test}, true)
	if exes == nil {
		return
	}
	const defect = "readsfix.Poke wrote to its argument b, which its row vouches it only reads (main.ergo:5:2); remove the row"
	if stdout, stderr, code := runExe(t, exes[test.name]); code == 0 || !strings.Contains(stdout+stderr, defect) {
		t.Errorf("the test build of the false row: exit %d, want the defect %q; stdout:\n%s\nstderr:\n%s", code, defect, stdout, firstLines(stderr, 12))
	}
}

// viewCase is one site of TestHostViewAgrees's program: its line and
// whether the model admits a view there.
func viewCase(r *rand.Rand, k int, ieeeCurrent bool) (string, bool) {
	// The string operand: a parameter, or an expression over it.
	str := []string{"s", `s + "x"`, `strings.ToUpper(s)`}[r.IntN(3)]
	bytesOf := "strings.Bytes(" + str + ")"
	kind := r.IntN(6)
	if k == 0 {
		// Every program has a site of the row whose line the seed may
		// make stale.
		kind = 0
	}
	switch kind {
	case 0:
		// A view row (vouched, View): admitted when its line is current.
		return fmt.Sprintf("\tlet v%d = crc32.ChecksumIEEE(%s)\n", k, bytesOf), ieeeCurrent
	case 1:
		// The same, through a binding: the operand is no copying call.
		return fmt.Sprintf("\tlet b%d = %s\n\tlet v%d = crc32.ChecksumIEEE(b%d)\n", k, bytesOf, k, k), false
	case 2:
		// A view row of another function, always current.
		return fmt.Sprintf("\tlet v%d = sha256.Sum256(%s)[0]\n", k, bytesOf), true
	case 3:
		// A derived (checked) fact: Go drops the copy itself; no view.
		return fmt.Sprintf("\tlet v%d = utf8.Valid(%s)\n", k, bytesOf), false
	case 4:
		// A vouched fact whose row allows no view.
		return fmt.Sprintf("\tlet v%d = bytes.IndexByte(%s, byte(97))\n", k, bytesOf), false
	}
	// A copy of the bytes of a byte slice, not a string: no view form.
	return fmt.Sprintf("\tlet v%d = crc32.ChecksumIEEE(slices.Clone(%s))\n", k, bytesOf), false
}

// TestHostViewAgrees is host-view's agreement property (LAWFFI LF2b;
// interop.md, Foreign facts 8), on the lowered Go: on seeded programs of
// Go calls on a string's bytes, the run build takes a view exactly at the
// sites the model admits (a copying call whose body has a view form, as
// the whole operand, at a parameter whose vouched reads fact allows a view
// and whose guard line is current); the test build takes a guarded copy
// at the same sites and no view; a -views=off build and the reference
// hostViewLowering "copy" take neither. A failing seed prints the program and
// the replay command.
//
// Mutants it alone catches: view-on-assumed (a stale guard line acts),
// view-on-unmarked-row (a vouched row with no View takes one),
// view-in-test-build, view-switch-ignored and view-not-whole-operand (a
// call with no view form takes one).
func TestHostViewAgrees(t *testing.T) {
	var ieee gofacts.Row
	for _, r := range gofacts.Of("hash/crc32.ChecksumIEEE") {
		ieee = r
	}
	saved := shippedLedger
	t.Cleanup(func() { shippedLedger = saved })
	count := func(o *lowered, s string) int {
		n := 0
		for _, f := range o.files {
			if strings.HasSuffix(f.Path, ".go") {
				n += bytes.Count(f.Data, []byte(s))
			}
		}
		return n
	}
	for _, seed := range importGoSeeds(1, 2, 3, 4) {
		r := rand.New(rand.NewPCG(seed, 0x71e3))
		ieeeCurrent := seed%2 == 0 // half the seeds make ChecksumIEEE's line stale
		entries, err := ParseLedger(saved())
		if err != nil {
			t.Fatal(err)
		}
		if !ieeeCurrent {
			e := entries["fact "+ieee.Key()]
			e.Digest = ieee.Digest("go1.0")
			entries[e.Key()] = e
		}
		shippedLedger = func() string { return FormatLedger(entries) }
		var body, result strings.Builder
		want := 0
		for k := range 8 {
			line, view := viewCase(r, k, ieeeCurrent)
			body.WriteString(line)
			fmt.Fprintf(&result, `\(v%d)`, k)
			if view {
				want++
			}
		}
		var imports strings.Builder
		for _, p := range []string{"go bytes", "go crypto/sha256", "go hash/crc32", "go unicode/utf8", "slices", "strings"} {
			if name := p[strings.LastIndexAny(p, " /")+1:]; strings.Contains(body.String(), name+".") {
				imports.WriteString("import " + p + "\n")
			}
		}
		src := imports.String() + "\nfunc f(s string) string needs go.Foreign {\n" + body.String() + "\t\"" + result.String() + "\"\n}\n\nfunc main() needs Console | go.Foreign {\n\tConsole.Println(f(\"ab\"))\n}\n"
		replay := fmt.Sprintf("seed %d (go test -run TestHostViewAgrees -importgo.seed %d ./internal/sketch/lower); ChecksumIEEE's line current: %v\n%s", seed, seed, ieeeCurrent, src)
		file, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("%v\n%s", err, replay)
		}
		pkg, diags := check.Check("main", []*syntax.File{file}, nil)
		if len(diags) > 0 {
			t.Fatalf("%v\n%s", diags, replay)
		}
		u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
		run := mustLowerUnit(t, "view_run", u, false)
		test := mustLowerUnit(t, "view_test", u, true)
		if n := count(run, ".BytesView("); n != want {
			t.Errorf("the run build takes %d views, the model %d\n%s", n, want, replay)
		}
		if n, v := count(test, ".GuardBytes("), count(test, ".BytesView("); n != want || v != 0 {
			t.Errorf("the test build takes %d guarded copies and %d views; the model %d and 0\n%s", n, v, want, replay)
		}
		u.NoViews = true
		off := mustLowerUnit(t, "view_off", u, false)
		offTest := mustLowerUnit(t, "view_off_test", u, true)
		if n := count(off, ".BytesView(") + count(offTest, ".GuardBytes("); n != 0 {
			t.Errorf("-views=off takes %d views or guarded copies\n%s", n, replay)
		}
		u.NoViews = false
		hostViewLowering = "copy"
		ref := mustLowerUnit(t, "view_ref", u, false)
		hostViewLowering = ""
		if n := count(ref, ".BytesView("); n != 0 {
			t.Errorf("the reference takes %d views\n%s", n, replay)
		}
		typecheckGo(t, []*lowered{run, test})
	}
}
