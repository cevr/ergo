package lower

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// rowArgCallees are generic functions with a value parameter of an error
// row parameter's type (types.md, Generics 1): F alone, F in a row with a
// concrete error type, and F on a function on a type (called x.f(…)).
// Every call writes its type arguments: the checker does not infer F from
// a value argument when the call is a catch's operand (ABSENCE
// r/infer-f.md, with CN).
const rowArgCallees = `type NotFound { Key int }
type Gone {}
type Busy {}
type Box { N int }

func orFail[T, F](o Option[T], f F) T fails F {
	match o {
	case Some[T] as s:
		s.Value
	case None:
		f
	}
}

func busyOr[F](f F, n int) int fails F | Busy {
	if n > 9 {
		return Busy
	}
	f
}

func firstOr[T, F](xs []T, f F) T fails F | Busy {
	if len(xs) > 1 {
		return Busy
	}
	if len(xs) > 0 {
		return xs[0]
	}
	f
}

func Raise[F](b Box, f F) int fails F {
	if b.N > 0 {
		return b.N
	}
	f
}
`

// rowArgCase is one generated call and the line it prints.
type rowArgCase struct{ src, want string }

// rowArgGen writes call i of the program: a callee, an argument (a record
// with a field, a bare record, or a local) and a hit, a miss or a Busy.
// A miss of NotFound{Key: k} prints 1000+k, of Gone -1, a Busy -2.
func rowArgGen(r *rand.Rand, i int) rowArgCase {
	k := r.IntN(50)
	type arg struct {
		pre, expr, typ, arm string
		miss                int
	}
	args := []arg{
		{"", fmt.Sprintf("NotFound{Key: %d}", k), "NotFound", "\tcase NotFound as e:\n\t\t1000 + e.Key\n", 1000 + k},
		{"", "Gone", "Gone", "\tcase Gone:\n\t\t-1\n", -1},
		{fmt.Sprintf("\tlet m%d = NotFound{Key: %d}\n", i, k), fmt.Sprintf("m%d", i), "NotFound", "\tcase NotFound as e:\n\t\t1000 + e.Key\n", 1000 + k},
	}
	a := args[r.IntN(len(args))]
	arms := a.arm
	busy := "\tcase Busy:\n\t\t-2\n"
	var call string
	want := a.miss
	switch r.IntN(4) {
	case 0:
		o := "None"
		if r.IntN(2) == 0 {
			o, want = fmt.Sprintf("Some{Value: %d}", k), k
		}
		call = fmt.Sprintf("orFail[int, %s](%s, %s)", a.typ, o, a.expr)
	case 1:
		n := r.IntN(2) * (10 + k)
		if n > 9 {
			want = -2
		}
		call = fmt.Sprintf("busyOr[%s](%s, %d)", a.typ, a.expr, n)
		arms += busy
	case 2:
		xs := []string{"[]int{}", fmt.Sprintf("[]int{%d}", k), fmt.Sprintf("[]int{%d, 1}", k)}[r.IntN(3)]
		switch strings.Count(xs, ",") {
		case 1:
			want = -2
		default:
			if xs != "[]int{}" {
				want = k
			}
		}
		call = fmt.Sprintf("firstOr[int, %s](%s, %s)", a.typ, xs, a.expr)
		arms += busy
	default:
		n := r.IntN(2) * (k + 1)
		if n > 0 {
			want = n
		}
		call = fmt.Sprintf("Box{N: %d}.Raise[%s](%s)", n, a.typ, a.expr)
	}
	src := a.pre + fmt.Sprintf("\tlet t%d = %s catch {\n%s\t}\n\tConsole.Println(\"\\(t%d)\")\n", i, call, arms, i)
	return rowArgCase{src: src, want: strconv.Itoa(want)}
}

// rowArgProgram is the seeded program and the lines it must print.
func rowArgProgram(seed uint64) (string, string) {
	r := rand.New(rand.NewPCG(seed, 23))
	var b, want strings.Builder
	b.WriteString(rowArgCallees + "\nfunc main() needs Console {\n")
	for i := range 24 {
		c := rowArgGen(r, i)
		b.WriteString(c.src)
		want.WriteString(c.want + "\n")
	}
	b.WriteString("}\n")
	return b.String(), want.String()
}

// TestRowArgumentsBuildOnBothPaths: a generic function's value parameter
// of an error row parameter's type, given an error type's value (a record
// with a field, a bare record, a local), builds on the IR path and prints
// what the source path prints and the model says (ir_expr.go argIntoRow).
// Seeds 1 to 4, or ERGO_SEED; a failure prints the program and the seed.
//
// It builds Go because the question is the patched compiler's check of
// the IR's call (toolchain/ir, assignable), which no in-process check
// reads: ergo run builds the IR path, buildAll the source path.
func TestRowArgumentsBuildOnBothPaths(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4}
	if s := os.Getenv("ERGO_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED=%q: %v", s, err)
		}
		seeds = []uint64{v}
	}
	for _, seed := range seeds {
		src, want := rowArgProgram(seed)
		o := lowerSource(fmt.Sprintf("rowarg%d", seed), src, true)
		if o.err != nil || len(o.diags) > 0 || o.refused != nil || o.panicked != nil {
			t.Fatalf("seed %d: lowering failed: %v %v %v %v\n%s", seed, o.err, o.diags, o.refused, o.panicked, src)
		}
		exes := buildAll(t, []*lowered{o}, true)
		stdout, stderr, code := runExe(t, exes[o.name])
		if code != 0 || stdout != want {
			t.Fatalf("seed %d: the source path printed (exit %d)\n%s\nwant\n%s\n%s\n%s", seed, code, stdout, want, stderr, src)
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "main.ergo")
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(ergoCLI(t), "run", path)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if got := string(withoutBuildNote(out)); err != nil || got != want {
			t.Fatalf("seed %d (ERGO_SEED=%d): the IR path (ergo run): %v\n%s\nwant\n%s\n%s", seed, seed, err, got, want, src)
		}
	}
}
