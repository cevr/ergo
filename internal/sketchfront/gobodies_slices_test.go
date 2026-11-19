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
