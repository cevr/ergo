package sketchfront

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// scanKernel is the shape of lane SIMD's scan kernels (LAWFFI LF2m): a
// module body over a []byte that returns an int, IndexByte-like.
const scanKernel = `package gohost

//ergo:wait none
func IndexByte(s []byte, c byte) int {
	for i, b := range s {
		if b == c {
			return i
		}
	}
	return -1
}
`

// TestModuleBodyEffects (LAWFFI LF2m r2; docs/spec/interop.md, Trusted Go
// 3) pins the refusals of a module body that is not effect-free and whose
// extern declares no go.Foreign: they name the effect and the row to
// write (a body that writes a package variable; a body written in
// assembly, which has no Go body to read; a body that calls a function
// of its package that writes one). TestModuleGoBodies binds such
// bodies with the row. Mutants (sketchfront mutants.json):
// lf2m-effectful-unforeign-bound, lf2m-asm-body-effect-free,
// lf2m-helper-unread, lf2m-archsimd-impure. It binds a vector kernel
// that imports simd/archsimd with no row (LF2m r3).
func TestModuleBodyEffects(t *testing.T) {
	// A vector kernel (lane SIMD's U1): archsimd is a pure import, so the
	// body is effect-free and binds with no row (LF2m r3). The checker
	// reads the source only, so no GOEXPERIMENT is needed here.
	const simdSrc = `package simd

import "simd/archsimd"

func Sum(s []int64) int64 {
	var acc archsimd.Int64x4
	for len(s) >= 4 {
		acc = acc.Add(archsimd.LoadInt64x4Slice(s))
		s = s[4:]
	}
	var out [4]int64
	acc.Store(&out)
	t := out[0] + out[1] + out[2] + out[3]
	for _, v := range s {
		t += v
	}
	return t
}
`
	const goSrc = `package gohost

var total int

//ergo:wait none
func Bump(n int) int {
	total += n
	return total
}

//ergo:wait none
func Count(s []byte, c byte) int

//ergo:wait none
func Via(n int) int { return Bump(n) + 1 }
`
	for _, c := range []struct{ mod, extern, law, want string }{
		{"module app\n", "extern go example.com/app/gohost.Bump func bump(n int) int\n", "",
			"main.ergo S108 1:1 extern go example.com/app/gohost.Bump: its Go body is not effect-free (its body names total, a top-level function or variable of the package); declare needs go.Foreign"},
		{"module app\n", "extern go example.com/app/gohost.Count func count(s []byte, c byte) int\n", "",
			"main.ergo S108 1:1 extern go example.com/app/gohost.Count: its Go body is not effect-free (it has no Go body to read (assembly)); declare needs go.Foreign"},
		{"module app\n", "extern go example.com/app/simd.Sum func sum(s []int64) int64\n", "", ""},
		{"module app\n", "extern go example.com/app/gohost.Via func via(n int) int\n", "",
			"main.ergo S108 1:1 extern go example.com/app/gohost.Via: its Go body is not effect-free (it calls Bump, and its body names total, a top-level function or variable of the package); declare needs go.Foreign"},
	} {
		files := map[string]string{
			"go.mod":           "module example.com/app\n\ngo 1.27\n",
			"ergo.mod":         c.mod,
			"gohost/gohost.go": goSrc,
			"simd/simd.go":     simdSrc,
			"main.ergo":        c.extern + "\n" + c.law + "\nfunc main() {}\n",
		}
		a, err := CheckDir(writeTree(t, files))
		if err != nil {
			t.Fatal(err)
		}
		if got := diagnosticLines(a); c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("ergo.mod %q, %s:\n%s\nwant %q", c.mod, c.extern, got, c.want)
		}
	}
}

// TestModuleBodyTakesSlices (LAWFFI LF2m; docs/spec/interop.md, Trusted
// Go 3): a module's own body, with no trust line, takes a slice of a basic type, and until
// a law makes it pure the call passes it a copy, since the module's Go may
// write or keep it. Mutants (sketchfront mutants.json):
// lf2m-slice-param-refused, lf2m-slice-not-copied, lf2m-own-go-needs-trust.
func TestModuleBodyTakesSlices(t *testing.T) {
	files := map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27\n",
		"ergo.mod":         "module app\n", // no trust line: the module's own Go needs none
		"gohost/gohost.go": scanKernel,
		"main.ergo": "import strconv\n\n" +
			"extern go example.com/app/gohost.IndexByte func indexByte(s []byte, c byte) int\n\n" +
			"func main() needs Console {\n\tlet s = []byte{1, 2, 3}\n\tConsole.Println(strconv.Itoa(indexByte(s, 2)))\n}\n",
	}
	a, err := CheckDir(writeTree(t, files))
	if err != nil {
		t.Fatal(err)
	}
	if got := diagnosticLines(a); got != "" {
		t.Fatalf("a body over []byte does not check:\n%s", got)
	}
	out, err := lower.Lower(a.program.unit())
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, f := range out {
		text.Write(f.Data)
	}
	i := strings.Index(text.String(), "gohost.IndexByte(")
	if i < 0 {
		t.Fatalf("the lowered Go calls no gohost.IndexByte:\n%s", text.String())
	}
	if rest := text.String()[i+len("gohost.IndexByte("):]; !strings.HasPrefix(rest, "slices.Clone(") {
		t.Errorf("IndexByte, not yet pure, gets its slice uncopied: %.80s", rest)
	}
}
