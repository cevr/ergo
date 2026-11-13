package fix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestFixHiddenPreludeType holds the text of the S102 whose clause names a
// prelude type the package hides (types.md, Scope 2; L6, owner q30): a
// package type that takes a prelude name hides the prelude's type there,
// so no clause the package writes can name it. The checker then asks for
// the rename, with no clause, and ergo fix writes nothing, so it never
// writes a clause that names the package's type instead. One row per kind
// of hidden name: the map type (the syntax's Map), a universe basic type,
// and a prelude generic type.
//
// Mutants it kills (ledger: rel5-hide-*): each hiding check of spell
// dropped (fix would write the package's type), and the clause attached
// to the S102 anyway.
func TestFixHiddenPreludeType(t *testing.T) {
	for _, c := range []struct{ name, arg, decl, want string }{
		{"map", "Map[string, int]", "type Map[K, V] { k K }", "Map"},
		{"basic", "[]string", "type string { b []byte }", "string"},
		{"prelude generic", "Option[int]", "type Option { v int }", "Option"},
	} {
		dir := t.TempDir()
		files := map[string]string{
			"ergo.mod":         "module m\n",
			"other/other.ergo": "type Tab[K] { Put(k K) }\n\nfunc Need() needs Tab[" + c.arg + "] {}\n",
			"main.ergo":        "import m/other\n\n" + c.decl + "\n\nfunc G() {\n\tother.Need()\n}\n\nfunc main() {}\n",
		}
		texts := map[string]string{}
		for name, text := range files {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			if name != "ergo.mod" {
				texts[path] = text
			}
		}
		key := filepath.Join(dir, "main.ergo")
		out, err := Dir(dir)(texts, check.Options{})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var got []string
		for _, d := range out.Diagnostics {
			s := filepath.Base(d.File) + " " + d.Code + " " + d.Message
			if d.Fix != nil {
				s += " [with a clause]"
			}
			got = append(got, s)
		}
		want := "main.ergo S102 this call requires other.Tab[" + c.arg + "], which is not in the capability row of function G and no with provides it; the clause cannot be written here, since this package's type " + c.want + " hides the prelude's (types.md, Scope 2); rename the package's type"
		if strings.Join(got, "\n") != want {
			t.Errorf("%s:\ngot:\n%s\nwant:\n%s", c.name, strings.Join(got, "\n"), want)
		}
		res, err := Run(texts, Dir(dir))
		if err != nil {
			t.Fatalf("%s: fix: %v", c.name, err)
		}
		if res.Texts[key] != texts[key] {
			t.Errorf("%s: fix wrote main.ergo:\n%s", c.name, res.Texts[key])
		}
	}
}
