package lower

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestFilepathFootprintsPure: every filepath body is marked pure, so a
// function that calls each export, Split's record result and Rel's failure
// included, has an empty footprint, as one that calls nothing has
// (stdlib.md, Pure bodies 4).
func TestFilepathFootprintsPure(t *testing.T) {
	const src = `import filepath

func all(p string, q string) string fails filepath.NotRelative {
	let s = filepath.Split(p)
	let r = try filepath.Rel(p, q)
	if filepath.IsAbs(p) {
		return r
	}
	filepath.Join([]string{filepath.Clean(s.Dir), filepath.Base(p), filepath.Dir(q), filepath.Ext(q)})
}
`
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{f}, check.Std)
	if len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	for _, fn := range pkg.Funcs {
		if fn.Name != "all" {
			continue
		}
		if fp := pkg.Info.Footprints[fn.Decl]; !fp.Empty() || fp.EntryPoll {
			t.Fatalf("all: footprint %+v, want empty with no entry poll", fp)
		}
		return
	}
	t.Fatal("no func all")
}

// TestFilepathRuns: each function of std filepath (stdlib.md, filepath)
// calls its runtime/v1 body (Split's returns v1.Parts) and prints one
// line. Rel fails filepath.NotRelative: a catch arm takes it with both
// paths, and main's uncaught NotRelative is written with its text and
// status 1. A package-level let may call it. Its subtest
// effect.path.join-posix is the evidence of that conformance case
// (Effect's DenoPath.test.ts "runs the demo program").
func TestFilepathRuns(t *testing.T) {
	t.Run("effect.path.join-posix", func(t *testing.T) {
		runBehaviors(t, []behavior{{name: "joinPosix", src: `import filepath

func main() needs Console {
	Console.Println(filepath.Join([]string{"tmp", "file.txt"}))
}
`, stdout: "tmp/file.txt\n"}})
	})
	runBehaviors(t, []behavior{
		{name: "filepath", src: `import filepath

let cleaned = filepath.Clean("a//b/./../c")

func rel(base string, target string) string {
	let r = filepath.Rel(base, target) catch {
	case filepath.NotRelative as n:
		return "not relative \(n.Base) \(n.Target)"
	}
	r
}

func main() needs Console {
	Console.Println(filepath.Join([]string{"tmp", "file.txt"}))
	Console.Println(filepath.Join([]string{"a", "b/../c", "", "d.txt"}))
	Console.Println("[\(filepath.Join([]string{"", ""}))]")
	Console.Println(filepath.Base("/usr/lib/"))
	Console.Println(filepath.Dir("/usr/lib/x.go"))
	Console.Println(filepath.Ext("archive.tar.gz"))
	Console.Println("[\(filepath.Ext("Makefile"))]")
	Console.Println(cleaned)
	Console.Println(filepath.Clean(""))
	Console.Println("\(filepath.IsAbs("/a")) \(filepath.IsAbs("a/b"))")
	let parts = filepath.Split("a/b/c.txt")
	Console.Println("<\(parts.Dir)|\(parts.File)>")
	let bare = filepath.Split("c.txt")
	Console.Println("<\(bare.Dir)|\(bare.File)>")
	Console.Println(rel("/a/b", "/a/c/d"))
	Console.Println(rel("a", "a"))
	Console.Println(rel("/a", "b"))
}
`, stdout: "tmp/file.txt\na/c/d.txt\n[]\nlib\n/usr/lib\n.gz\n[]\na/c\n.\ntrue false\n<a/b/|c.txt>\n<|c.txt>\n../c/d\n.\nnot relative /a b\n"},
		{name: "relUncaught", src: `import filepath

func main() fails filepath.NotRelative needs Console {
	Console.Println(try filepath.Rel("../x", "y"))
}
`, exit: 1, stderr: []string{"error: filepath.NotRelative{Base:../x Target:y}\n"}},
	})
}
