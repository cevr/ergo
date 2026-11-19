package sketchfront

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
)

// importedTapeSource is package tape of module m: a cell writer whose law
// RawJoins the build rewrites by, and Show, which encodes any wire value
// on it. It has no main, so no ergo test of its own writes a ledger.
const importedTapeSource = `type Tape {
	..Writer,
	mut Written() string,
	mut raw(text string),

	law RawJoins(a string, b string) { raw(a); raw(b) } == { raw(a + b) },
}

type tape { out string } implements Tape

func raw(t mut tape, s string) { t.out = t.out + s }
func Written(t mut tape) string { t.out }
func Null(t mut tape) { t.raw("n") }
func Number(t mut tape, text string) { t.raw(text) }
func Bool(t mut tape, b bool) { t.raw("b") }
func Int(t mut tape, n int64) { t.raw("i") }
func Uint(t mut tape, n uint64) { t.raw("u") }
func Float(t mut tape, f float64) { t.raw("f") }
func String(t mut tape, s string) { t.raw(s) }
func StringBytes(t mut tape, b []byte) { t.raw("s") }
func BeginList(t mut tape, n int) { t.raw("[") }
func EndList(t mut tape) { t.raw("]") }
func BeginObject(t mut tape, n int) { t.raw("{") }
func Tag(t mut tape, name string) { t.raw(name) }
func Key(t mut tape, k string) { t.raw(k) }
func EndObject(t mut tape) { t.raw("}") }

func Show[T wire](v T) string needs wired[T] {
	let t Tape = tape{out: ""}
	encode(v, t)
	t.Written()
}
`

// TestLedgerChecksAnImportedProvider: ergo test of a binary writes the
// ledger entries of a law its build rewrites by when the provider is
// declared by another package of the module (a module's own codec), and
// the build then rewrites with them and names no entry missing (runtime.md,
// Tests 10, Writer; NOPRIV-JSON). Before, the binary's check build ran
// only its own package's laws and a library writes no ledger, so such a
// site kept the reference lowering (the gap NP2 left).
//
// It builds Go because a ledger verdict is the output of the law check
// build, which runs the imported package's law: the in-process fake
// (fakeLawChecks) reports every law it is asked for, so it cannot tell a
// law the check build runs from one it never had.
func TestLedgerChecksAnImportedProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds the law check")
	}
	saved := compilerIdentity
	compilerIdentity = func() (string, error) { return "test", nil }
	defer func() { compilerIdentity = saved }()
	dir := writeTree(t, map[string]string{
		"ergo.mod":        "module m\n",
		"tape/tape.ergo":  importedTapeSource,
		"cmd/a/main.ergo": "import m/tape\n\ntype Pet { Name string, Age int }\n\nfunc main() needs Console { Console.Println(tape.Show(Pet{Name: \"Rex\", Age: 3})) }\n",
	})
	a, err := CheckDir(filepath.Join(dir, "cmd", "a"))
	if err != nil || len(a.Diagnostics) > 0 {
		t.Fatalf("cmd/a: %v %v", err, a.Diagnostics)
	}
	path := filepath.Join(t.TempDir(), lower.LedgerFile)
	req := BuildRequest{Mode: build.ModeBuild, Source: "/ergo-test/ledger.ergo", Ledger: path, Cache: build.Cache{Root: t.TempDir()}, Toolchain: build.HostToolchain()}
	if _, err := UpdateLedger(a, req); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(path)
	if !strings.Contains(string(text), "law m/tape.Tape.RawJoins for m/tape.tape proved") {
		t.Fatalf("ergo test of cmd/a wrote no proved entry of tape's RawJoins:\n%s", text)
	}
	program, _, notes, err := lowered(t, a, path, true)
	if err != nil || len(notes) > 0 {
		t.Fatalf("the locked build after ergo test: %v %q", err, notes)
	}
	none, _, _, err := lowered(t, a, filepath.Join(t.TempDir(), lower.LedgerFile), false)
	if err != nil {
		t.Fatal(err)
	}
	if program == none {
		t.Fatalf("the build with the entries is the build with none: tape's windows are not rewritten\n%s", program)
	}
}
