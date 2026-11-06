package sketchfront

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// writeTree writes files (slash paths relative to dir) and returns dir.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// permutedFS lists every directory in the order perm gives, from the
// host's sorted listing, so a loader that keeps the listing order shows it.
type permutedFS struct {
	hostFS
	perm func([]fs.DirEntry) []fs.DirEntry
}

func (p permutedFS) ReadDir(dir string) ([]fs.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	return p.perm(entries), nil
}

func diagnosticLines(a Analysis) string {
	var b strings.Builder
	for _, d := range a.Diagnostics {
		fmt.Fprintf(&b, "%s %s %d:%d %s\n", d.File, d.Code, d.Span.Line, d.Span.Column, d.Message)
	}
	return b.String()
}

// TestLoaderOrderIsTheNames (196b Q2, mutant f): a package's files are its
// .ergo files in byte order of their names, whatever order the host lists
// them in, so the diagnostics, their order and the lowered Go are the same
// bytes for every listing order.
func TestLoaderOrderIsTheNames(t *testing.T) {
	perms := map[string]func([]fs.DirEntry) []fs.DirEntry{
		"sorted":  func(e []fs.DirEntry) []fs.DirEntry { return e },
		"reverse": func(e []fs.DirEntry) []fs.DirEntry { out := slices.Clone(e); slices.Reverse(out); return out },
		"rotate": func(e []fs.DirEntry) []fs.DirEntry {
			if len(e) < 2 {
				return e
			}
			return append(slices.Clone(e[1:]), e[0])
		},
	}
	broken := writeTree(t, map[string]string{
		"ergo.mod":         "module app\n",
		"cmd/app/a.ergo":   "import app/lib\n\nfunc main() needs Console {\n\tConsole.Println(lib.Name() + fromB() + fromC() + missingA)\n}\n",
		"cmd/app/b.ergo":   "func fromB() string {\n\treturn missingB\n}\n",
		"cmd/app/c.ergo":   "func fromC() string {\n\treturn missingC\n}\n",
		"lib/one.ergo":     "func Name() string {\n\treturn two()\n}\n",
		"lib/two.ergo":     "func two() string {\n\treturn \"lib\"\n}\n",
		"lib/.hidden.ergo": "this is not parsed",
		"lib/_skip.ergo":   "nor is this",
	})
	clean := writeTree(t, map[string]string{
		"ergo.mod":       "module app\n",
		"cmd/app/a.ergo": "import app/lib\n\nlet A string = lib.Name() + \"a\"\n\nfunc main() needs Console {\n\tConsole.Println(A + fromB() + fromC())\n}\n",
		"cmd/app/b.ergo": "func fromB() string {\n\treturn \"b\"\n}\n",
		"cmd/app/c.ergo": "func fromC() string {\n\treturn \"c\"\n}\n",
		"lib/one.ergo":   "let First string = two()\n\nfunc Name() string {\n\treturn First\n}\n",
		"lib/two.ergo":   "func two() string {\n\treturn \"lib\"\n}\n",
	})
	var wantDiags, wantGo string
	for _, name := range []string{"sorted", "reverse", "rotate"} {
		fsys := permutedFS{perm: perms[name]}
		a, err := checkDir(fsys, filepath.Join(broken, "cmd", "app"))
		if err != nil {
			t.Fatal(err)
		}
		got := diagnosticLines(a)
		if name == "sorted" {
			wantDiags = got
			if strings.Count(got, "S104") != 3 || !strings.HasPrefix(got, "cmd/app/a.ergo ") || !strings.Contains(got, "\ncmd/app/c.ergo ") {
				t.Fatalf("diagnostics:\n%s\nwant three S104, a.ergo first and c.ergo last", got)
			}
		} else if got != wantDiags {
			t.Fatalf("listing %s: diagnostics\n%s\nwant (sorted listing)\n%s", name, got, wantDiags)
		}

		a, err = checkDir(fsys, filepath.Join(clean, "cmd", "app"))
		if err != nil || len(a.Diagnostics) > 0 {
			t.Fatalf("listing %s: clean program: %v %s", name, err, diagnosticLines(a))
		}
		for _, f := range a.program.root.files {
			if f.syntax.Name != filepath.Base(f.abs) {
				t.Fatalf("listing %s: %s has syntax.File.Name %q, want its name in the package", name, f.rel, f.syntax.Name)
			}
		}
		files, err := lower.Lower(a.program.unit())
		if err != nil {
			t.Fatalf("listing %s: lower: %v", name, err)
		}
		var b strings.Builder
		for _, f := range files {
			fmt.Fprintf(&b, "== %s\n%s", f.Path, f.Data)
		}
		if name == "sorted" {
			wantGo = b.String()
		} else if b.String() != wantGo {
			t.Fatalf("listing %s: the lowered Go differs from the sorted listing's", name)
		}
	}
}

// TestLoaderModuleFile (196b Q1): ergo.mod holds one line, module NAME,
// and NAME is an import path that does not start with a std path.
func TestLoaderModuleFile(t *testing.T) {
	for _, c := range []struct {
		text, module, problem string
	}{
		{"module app\n", "app", ""},
		{"\nmodule my-app/tools\n\n", "my-app/tools", ""},
		{"module example.com/x\n", "example.com/x", ""},
		{"", "", "names no module"},
		{"module log\n", "", "starts with the std package log"},
		{"module strings/x\n", "", "starts with the std package strings"},
		{"module prelude\n", "", "starts with the std package prelude"},
		{"module app\nmodule b\n", "", "one line, module NAME, and nothing else"},
		{"go 1.22\n", "", "one line, module NAME, and nothing else"},
		{"module 1app\n", "", "is not an import path"},
		{"module a//b\n", "", "is not an import path"},
		{"module a/\n", "", "is not an import path"},
	} {
		module, d := parseModule(c.text)
		switch {
		case c.problem == "" && (d != nil || module != c.module):
			t.Errorf("%q: module %q diagnostic %+v, want %q", c.text, module, d, c.module)
		case c.problem != "" && (d == nil || d.Code != "S108" || !strings.Contains(d.Message, c.problem)):
			t.Errorf("%q: diagnostic %+v, want S108 %q", c.text, d, c.problem)
		}
	}
}

// TestLoaderImportReasons: an import the loader cannot serve is S104 with
// its reason: no ergo.mod above the program, no folder in the module, or a
// path outside the module.
func TestLoaderImportReasons(t *testing.T) {
	main := "func main() needs Console {\n\tConsole.Println(x.Name())\n}\n"
	for _, c := range []struct {
		files map[string]string
		dir   string
		want  string
	}{
		{map[string]string{"prog/main.ergo": "import lib/x\n\n" + main}, "prog", `unknown package "lib/x": no ergo.mod above the program's folder declares a module`},
		{map[string]string{"ergo.mod": "module app\n", "main.ergo": "import app/x\n\n" + main}, ".", `unknown package "app/x": no folder x in module app`},
		{map[string]string{"ergo.mod": "module app\n", "main.ergo": "import app/x\n\n" + main, "x/README.md": "no source"}, ".", `unknown package "app/x": the folder x holds no .ergo file`},
		{map[string]string{"ergo.mod": "module app\n", "main.ergo": "import other/x\n\n" + main}, ".", `unknown package "other/x": it is not std and not in module app`},
	} {
		root := writeTree(t, c.files)
		a, err := CheckDir(filepath.Join(root, c.dir))
		if err != nil {
			t.Fatal(err)
		}
		got := diagnosticLines(a)
		if !strings.Contains(got, "S104 1:8 "+c.want) {
			t.Errorf("%v:\n%s\nwant S104 at 1:8 %q", c.files, got, c.want)
		}
	}
}

// TestLoaderBrokenDependency: a package whose dependency has diagnostics
// is not checked, so its diagnostics are the dependency's alone, each in
// its own file.
func TestLoaderBrokenDependency(t *testing.T) {
	root := writeTree(t, map[string]string{
		"ergo.mod":          "module app\n",
		"cmd/app/main.ergo": "import app/lib\n\nfunc main() needs Console {\n\tConsole.Println(lib.Name() + alsoMissing)\n}\n",
		"lib/lib.ergo":      "func Name() string {\n\treturn missing\n}\n",
		"lib/syntax.ergo":   "func (\n",
	})
	a, err := CheckDir(filepath.Join(root, "cmd", "app"))
	if err != nil {
		t.Fatal(err)
	}
	got := diagnosticLines(a)
	if strings.Contains(got, "cmd/app") || !strings.Contains(got, "lib/syntax.ergo S001") || a.pkg != nil {
		t.Fatalf("diagnostics:\n%s\nwant lib's syntax error only, and no package to build", got)
	}
}

// TestUnionVariantFromAnotherPackage (196b Q8): a union's variants are
// records of its package, permanently: S108 with the wrap form, no longer
// the S109 placeholder.
func TestUnionVariantFromAnotherPackage(t *testing.T) {
	a := Check("import timeout\n\ntype Declined {}\n\ntype E = Declined | timeout.Expired\n\nfunc main() {\n}\n")
	got := diagnosticLines(a)
	const want = "S108 5:21 a union's variants are records of its package; wrap timeout.Expired: type E = … | Expired{ Cause timeout.Expired }"
	if !strings.Contains(got, want) {
		t.Fatalf("diagnostics:\n%s\nwant %q", got, want)
	}
}

// TestImportOfAnElementThatIsNotAName (196b Q3): an import path whose last
// element cannot be a name needs an alias, with the keyword rule's form;
// with the alias it is not a needless one.
func TestImportOfAnElementThatIsNotAName(t *testing.T) {
	root := writeTree(t, map[string]string{
		"ergo.mod":           "module app\n",
		"okra-repo/r.ergo":   "func Name() string {\n\treturn \"okra\"\n}\n",
		"cmd/bad/main.ergo":  "import app/okra-repo\n\nfunc main() {\n}\n",
		"cmd/good/main.ergo": "import okra app/okra-repo\n\nfunc main() needs Console {\n\tConsole.Println(okra.Name())\n}\n",
	})
	bad, err := CheckDir(filepath.Join(root, "cmd", "bad"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "cmd/bad/main.ergo S108 1:8 okra-repo is not a name; import the package under a name: import okrarepo app/okra-repo\n"
	if got := diagnosticLines(bad); got != want {
		t.Fatalf("diagnostics:\n%s\nwant\n%s", got, want)
	}
	good, err := CheckDir(filepath.Join(root, "cmd", "good"))
	if err != nil || len(good.Diagnostics) > 0 {
		t.Fatalf("aliased import: %v\n%s", err, diagnosticLines(good))
	}
}
