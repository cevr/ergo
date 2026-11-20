package fix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMutGoRespellsFragments runs the --go respell (RunMut) over a
// generator and a file of code that emits Go, each holding the same piece
// of a signature: the generator's is respelled, the emitter's is Go and
// stays (fragmentFile); a test's closed format is respelled and its open
// piece left. Mutants (fix ledger): fix-fragment-off,
// fix-fragment-test-closes (a test's pieces are closed too),
// fix-result-borrow-ergo (a *T result is read as Ergo), fix-ergo-marker-off
// (a test's Ergo piece is read whole only).
func TestMutGoRespellsFragments(t *testing.T) {
	dir := t.TempDir()
	const body = "package p\n\nvar s = \"func pl%d(r *Rec, k int) int {\\n\"\n"
	gen, emit, test := filepath.Join(dir, "gen_place.go"), filepath.Join(dir, "emit.go"), filepath.Join(dir, "place_test.go")
	for _, f := range []string{gen, emit} {
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A test's format is read whole: the closed one is respelled, the
	// piece (Go that a test expects of the lowering, say) is not.
	const tests = "package p\n\nvar a = \"func pl%d(r *Rec) int {\\n\\t1\\n}\\n\"\nvar b = \"func evens(env *Env, n int) []int {\"\nvar c = \"import sync\\n\\nfunc Bump(b *Board) {\\n\\tlet n = 1\\n\"\n"
	if err := os.WriteFile(test, []byte(tests), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := RunMut(Options{Paths: []string{dir}, Go: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Files[gen]; !strings.Contains(got, `func pl%d(r mut Rec, k int) int {\n`) {
		t.Errorf("the generator's piece is not respelled: %q", got)
	}
	if got := rep.Files[test]; !strings.Contains(got, `func pl%d(r mut Rec) int {`) || !strings.Contains(got, `func evens(env *Env, n int) []int {`) || !strings.Contains(got, `func Bump(b mut Board) {`) {
		t.Errorf("the test's literals: %q, want the closed format and the Ergo piece respelled and the Go piece left", got)
	}
	if got, ok := rep.Files[emit]; ok {
		t.Errorf("the emitter's Go piece is respelled: %q", got)
	}
}

// TestMutFragmentRespellsGeneratorPieces is the smallest repro of the
// borrows the --go respell left in generators (irspike gen_*.go, I-FLIP
// on main 30ccad64e): a fmt format whose verb stops it parsing, and a
// signature whose body the next literal writes. Each is respelled in
// place; a piece that names a Go package's type is Go and stays.
// Mutants (fix ledger): mutfrag-no-close (no closing brace is tried),
// mutfrag-verbs-kept (the verbs stay, so a format does not parse),
// mutfrag-lent-kept (a piece's [T lent] is not deleted, as Mut deletes it).
func TestMutFragmentRespellsGeneratorPieces(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"func pl%d(r *Rec, k int) int {\n\tlet mut loc = k\n", "func pl%d(r mut Rec, k int) int {\n\tlet mut loc = k\n"},
		{"func Area(x *Box) int {\n\tx.B * %d\n}\n\n", "func Area(x mut Box) int {\n\tx.B * %d\n}\n\n"},
		{"func at[T](a *Acc[T], i int) T {\n\ta.items[i %% len(a.items)]\n}\n", "func at[T](a mut Acc[T], i int) T {\n\ta.items[i %% len(a.items)]\n}\n"},
		{"func f(t *testing.T) {\n", "func f(t *testing.T) {\n"},
		{"\tlet v = *p\n", "\tlet v = *p\n"},
		{"func HostAlarms() *AlarmClock { return hostAlarms }", "func HostAlarms() *AlarmClock { return hostAlarms }"},
		{"func lends[T lent](x T) int {\n", "func lends[T](x T) int {\n"},
	} {
		got, _ := MutFragment(c.in, true)
		if got != c.want {
			t.Errorf("MutFragment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
