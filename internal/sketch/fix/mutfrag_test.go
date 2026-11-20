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
// stays (fragmentFile). Mutant (fix ledger): fix-fragment-off.
func TestMutGoRespellsFragments(t *testing.T) {
	dir := t.TempDir()
	const body = "package p\n\nvar s = \"func pl%d(r *Rec, k int) int {\\n\"\n"
	gen, emit := filepath.Join(dir, "gen_place.go"), filepath.Join(dir, "emit.go")
	for _, f := range []string{gen, emit} {
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := RunMut(Options{Paths: []string{dir}, Go: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Files[gen]; !strings.Contains(got, `func pl%d(r mut Rec, k int) int {\n`) {
		t.Errorf("the generator's piece is not respelled: %q", got)
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
// mutfrag-verbs-kept (the verbs stay, so a format does not parse).
func TestMutFragmentRespellsGeneratorPieces(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"func pl%d(r *Rec, k int) int {\n\tlet mut loc = k\n", "func pl%d(r mut Rec, k int) int {\n\tlet mut loc = k\n"},
		{"func Area(x *Box) int {\n\tx.B * %d\n}\n\n", "func Area(x mut Box) int {\n\tx.B * %d\n}\n\n"},
		{"func at[T](a *Acc[T], i int) T {\n\ta.items[i %% len(a.items)]\n}\n", "func at[T](a mut Acc[T], i int) T {\n\ta.items[i %% len(a.items)]\n}\n"},
		{"func f(t *testing.T) {\n", "func f(t *testing.T) {\n"},
		{"\tlet v = *p\n", "\tlet v = *p\n"},
	} {
		got, _ := MutFragment(c.in)
		if got != c.want {
			t.Errorf("MutFragment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
