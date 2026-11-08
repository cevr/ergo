package sketchfront

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestPackageTypeHidesPreludeTypeOnlyInItsPackage (L6; grammar.md, Reserved
// names 2; owner q30): a package that declares Option and Map hides the
// prelude's only inside itself. A package that imports it still means the
// prelude's Option and the map type by the bare names, and reaches the
// other package's types qualified.
func TestPackageTypeHidesPreludeTypeOnlyInItsPackage(t *testing.T) {
	root := writeTree(t, map[string]string{
		"ergo.mod": "module app\n",
		"own/own.ergo": "type Option { V int }\ntype Map[K, V] { keys []K, vals []V }\n\n" +
			"func Make() Option { Option{V: 1} }\n\nfunc Pairs() Map[string, int] { Map[string, int]{keys: []string{\"a\"}, vals: []int{1}} }\n\n" +
			"func Count[K, V](m Map[K, V]) int { len(m.keys) }\n",
		"cmd/good/main.ergo": "import app/own\n\n" +
			"func pick(o Option[int]) int {\n\tmatch o {\n\tcase Some[int]{Value: v}:\n\t\tv\n\tcase None:\n\t\t0\n\t}\n}\n\n" +
			"func main() needs Console {\n\tlet m Map[string, int] = Map[string, int]{\"b\": 2}\n\tlet o own.Option = own.Make()\n\tlet p own.Map[string, int] = own.Pairs()\n" +
			"\tConsole.Println(\"\\(pick(Some[int]{Value: 3})) \\(len(m)) \\(o.V) \\(p.Count())\")\n}\n",
		// The importer's bare Option is the prelude's, so the other
		// package's Option is no Option[int].
		"cmd/bad/main.ergo": "import app/own\n\nfunc f() Option[int] { own.Make() }\n\nfunc main() {\n}\n",
	})
	good, err := CheckDir(filepath.Join(root, "cmd", "good"))
	if err != nil || len(good.Diagnostics) > 0 {
		t.Fatalf("an importer of a package that hides prelude types: %v\n%s", err, diagnosticLines(good))
	}
	bad, err := CheckDir(filepath.Join(root, "cmd", "bad"))
	if err != nil {
		t.Fatal(err)
	}
	if got := diagnosticLines(bad); !strings.Contains(got, "S108 3:24 cannot use own.Option as Option[int]") {
		t.Fatalf("own.Option where the prelude's Option[int] is expected: diagnostics\n%s\nwant S108 at 3:24", got)
	}
}
