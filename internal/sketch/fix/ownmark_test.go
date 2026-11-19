package fix_test

import (
	"os"
	"path/filepath"
	"testing"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/fix"
)

// TestFixWritesOwnMarks is the smallest program of the own mark's
// migration (types.md, Generics 9; MODES-5): a resource bound to an
// unmarked type parameter is S108, and ergo fix writes [T own] where the
// type parameter is declared, rounds until none is left to write (pass's
// mark requires keep's, the cmd/ergo loop), splits a group so the mark
// stays on its name ([A, B] → [A any, B own]), and the result checks and
// formats as written.
//
// Mutants it kills that no other test does: the fix writes no own mark
// (modes5-fix-own-unwritten); the last name of a group marked with the
// group's other names (modes5-fix-own-group-joined).
func TestFixWritesOwnMarks(t *testing.T) {
	const old = `type Res { id int } implements Closer

func Close(r Res) {}

type Pair[A, B] { a A, b B }

func keep[T](x own T) T { x }

func pass[T](x own T) T { keep(x) }

func main() {
	let p = Pair{a: 1, b: Res{id: 1}}
	Close(p.b)
	let r = pass(Res{id: 2})
	Close(r)
}
`
	const want = `type Res { id int } implements Closer

func Close(r Res) {}

type Pair[A any, B own] { a A, b B }

func keep[T own](x own T) T { x }

func pass[T own](x own T) T { keep(x) }

func main() {
	let p = Pair{a: 1, b: Res{id: 1}}
	Close(p.b)
	let r = pass(Res{id: 2})
	Close(r)
}
`
	dir := t.TempDir()
	file := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(file, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, diags := check.CheckSource(old); len(diags) == 0 {
		t.Fatal("the old program checks: the unmarked T admits a resource")
	}
	rounds := 0
	for ; rounds < 4; rounds++ {
		rep, err := fix.Run(fix.Options{Paths: []string{dir}, Write: true})
		if err != nil {
			t.Fatalf("fix: %v", err)
		}
		if len(rep.Unwritten) > 0 {
			t.Fatalf("fix left marks unwritten: %v", rep.Unwritten)
		}
		if !rep.WroteOwn() {
			break
		}
	}
	got, _ := os.ReadFile(file)
	if string(got) != want {
		t.Fatalf("after %d rounds the fix wrote:\n%s\nwant:\n%s", rounds, got, want)
	}
	if _, diags := check.CheckSource(want); len(diags) != 0 {
		t.Errorf("the fixed program does not check: %v", diags)
	}
	if out, err := format.Source(want); err != nil || out != want {
		t.Errorf("format changes the fixed program (%v):\n%s", err, out)
	}
}
