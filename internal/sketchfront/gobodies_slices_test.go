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

// TestModuleBodyEffects (LAWFFI LF2m; docs/spec/interop.md, Trusted Go 3;
// types.md, Laws 14 rule 4) pins the refusals of a module body that is not
// effect-free: with no trust line its binding is refused, naming the
// effect and the line to write (a body that writes a package variable; a body written in
// assembly, which has no Go body to read); with the trust line it binds,
// and a law over it is no rewrite. Mutants (sketchfront mutants.json):
// lf2m-effectful-untrusted-bound, lf2m-asm-body-effect-free,
// lf2m-effectful-law-in-scope.
func TestModuleBodyEffects(t *testing.T) {
	const goSrc = `package gohost

var total int

//ergo:wait none
func Bump(n int) int {
	total += n
	return total
}

//ergo:wait none
func Count(s []byte, c byte) int
`
	const law = "func bumpRef(n int) int { n }\n\nlaw BumpIsRef(n int) { bumpRef(n) } == { bump(n) }\n"
	for _, c := range []struct{ mod, extern, law, want string }{
		{"module app\n", "extern go example.com/app/gohost.Bump func bump(n int) int\n", "",
			"main.ergo S108 1:1 extern go example.com/app/gohost.Bump: its Go body is not effect-free (its body names total, a top-level function or variable of the package)"},
		{"module app\n", "extern go example.com/app/gohost.Count func count(s []byte, c byte) int\n", "",
			"main.ergo S108 1:1 extern go example.com/app/gohost.Count: its Go body is not effect-free (it has no Go body to read (assembly)), and a package binds such a body of its module's own Go only when the root module trusts its Go; write the line `trust go app`"},
		{"module app\ntrust go app\n", "extern go example.com/app/gohost.Bump func bump(n int) int\n", law,
			"law BumpIsRef is no rewrite: bump is a Go body of the module that is not effect-free (its body names total"},
	} {
		files := map[string]string{
			"go.mod":           "module example.com/app\n\ngo 1.27\n",
			"ergo.mod":         c.mod,
			"gohost/gohost.go": goSrc,
			"main.ergo":        c.extern + "\n" + c.law + "\nfunc main() {}\n",
		}
		a, err := CheckDir(writeTree(t, files))
		if err != nil {
			t.Fatal(err)
		}
		if got := diagnosticLines(a); !strings.Contains(got, c.want) {
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
