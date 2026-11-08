package sketchfront

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestNamedRowOfAnotherPackage (L2; types.md, Error types 4): a union of
// another package names a row when each of its variants is an error type
// there, and is S104 otherwise; a catch arm of the imported union binds it.
func TestNamedRowOfAnotherPackage(t *testing.T) {
	root := writeTree(t, map[string]string{
		"ergo.mod":       "module app\n",
		"errs/errs.ergo": "type Bad { At int }\ntype Oops {}\ntype Fail = Bad | Oops\n\nfunc Run(n int) int fails Fail {\n\tif n == 1 {\n\t\treturn Oops\n\t}\n\tn\n}\n\nfunc Describe(f Fail) string {\n\tmatch f {\n\tcase Bad:\n\t\t\"bad\"\n\tcase Oops:\n\t\t\"oops\"\n\t}\n}\n",
		"vals/vals.ergo": "type Lone {}\ntype Two {}\ntype Pair = Lone | Two\n\nfunc First() Pair {\n\tLone\n}\n",
		"cmd/good/main.ergo": "import app/errs\n\nfunc twice(n int) int fails errs.Fail {\n\tlet r = try errs.Run(n)\n\t2 * r\n}\n\n" +
			"func main() needs Console {\n\tlet s = twice(1) catch {\n\tcase errs.Fail as f:\n\t\tConsole.Println(errs.Describe(f))\n\t\treturn\n\t}\n\tConsole.Println(\"\\(s)\")\n}\n",
		"cmd/bad/main.ergo": "import app/vals\n\nfunc f() fails vals.Pair {\n}\n\nfunc main() {\n}\n",
	})
	good, err := CheckDir(filepath.Join(root, "cmd", "good"))
	if err != nil || len(good.Diagnostics) > 0 {
		t.Fatalf("a union of error types of another package: %v\n%s", err, diagnosticLines(good))
	}
	bad, err := CheckDir(filepath.Join(root, "cmd", "bad"))
	if err != nil {
		t.Fatal(err)
	}
	if got := diagnosticLines(bad); !strings.Contains(got, "S104") || !strings.Contains(got, "vals.Lone is not") {
		t.Fatalf("a union of plain records of another package: diagnostics\n%s\nwant S104 naming vals.Lone", got)
	}
}
