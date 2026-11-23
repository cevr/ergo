package check

import (
	"strings"
	"testing"
)

// TestOwnMarkRows holds the text and position of the own mark's
// declaration rules (types.md, Generics 9; MODES-5) that the generated
// property (TestTakesInferenceModel) does not draw: a generic union's mark
// read from its declaration and asked of nothing else (GEN9-GENERAL), a
// mark on a row, and an extern's mark against
// runtime/v1's reviewed table (a program's extern is refused a type
// parameter as well, so that row checks its own diagnostic among others;
// std's reviewed marks are the admitted side).
//
// Mutants it kills that no other test does: an unmarked generic type's
// instance admitted (gen9-unmarked-type-admitted); own admitted on a row
// (modes5-own-row-admitted); an extern's mark admitted unreviewed
// (modes5-own-extern-unreviewed).
func TestOwnMarkRows(t *testing.T) {
	type row struct {
		name, src string
		line, col int
		text      string // "" for none
		others    bool   // other diagnostics may stand beside it
	}
	rows := []row{
		{"type mark asks nothing of its functions", `type Box[T own] { v T }

func Peek[T](b Box[T]) int { 0 }

func main() {}
`, 0, 0, "", false},
		{"marked user union admits a resource", `type File { fd int } implements Closer

func Close(f File) {}

type MySome[T own] { Value T }

type MyNone {}

type MyOpt[T own] = MySome[T] | MyNone

func pass[T own](o own MyOpt[T]) MyOpt[T] { o }

func take(o own MyOpt[File]) Option[File] {
	match o {
	case MySome{Value: v}:
		Some{Value: v}
	case MyNone:
		None
	}
}

func lendOpt(o Option[File]) {}

func main() {
	let o MyOpt[File] = MySome[File]{Value: File{fd: 1}}
	let r = take(pass(o))
	lendOpt(r)
}
`, 0, 0, "", false},
		{"unmarked user union refuses a resource", `type File { fd int } implements Closer

func Close(f File) {}

type MySome[T] { Value T }

type MyNone {}

type MyOpt[T] = MySome[T] | MyNone

func lendOpt(o MyOpt[File]) {}

func main() {
	let o MyOpt[File] = MyNone
	lendOpt(o)
}
`, 14, 8, "MyOpt cannot take File as its T: T is not marked own", true},
		{"own row", `type Bad {}

func run[E own](f func() int fails E) int fails E { try f() }

func main() {}
`, 3, 12, "own marks a type parameter that a resource may bind, and E is a row", false},
		{"extern unreviewed", `extern go slices.Clone func clone[T own](s []T) []T

func main() {}
`, 1, 37, "clone's Go body is not reviewed as moving its values only (runtime/v1's MovesOnly table), so its type parameters cannot be marked own", true},
	}
	for _, r := range rows {
		_, diags := checkFor(r.src)
		if r.text == "" {
			if len(diags) != 0 {
				t.Errorf("%s: got %+v, want none", r.name, diags)
			}
			continue
		}
		found := 0
		for _, d := range diags {
			if d.Code == CodeType && d.Line == r.line && d.Column == r.col && strings.Contains(d.Message, r.text) {
				found++
			}
		}
		if found != 1 || !r.others && len(diags) != 1 {
			t.Errorf("%s: got %+v, want one S108 at %d:%d containing %q", r.name, diags, r.line, r.col, r.text)
		}
	}
}
