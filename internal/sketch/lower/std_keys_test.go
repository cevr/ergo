package lower

import (
	"errors"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// stdFixture checks src as the std package at path (check.StdFixture).
func stdFixture(t *testing.T, path, src string) *check.Package {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	pkg, ds := check.StdFixture(path, []*syntax.File{f})
	if len(ds) != 0 {
		t.Fatalf("std fixture %s: %v", path, ds)
	}
	return pkg
}

// TestStdRecordsKeyedByPackage (std-align S3b): two std packages each
// declare a record NotFound, and one program fails with each and catches
// each in its own arm. A std record binds a runtime struct by its package
// and its name (v1.StdRecord), so the two records are two Go types and
// each arm takes its own failure. Before, the lowering bound a std record
// by its bare name, so both lowered to v1.NotFound, the fs record's struct,
// and the catch could not tell them apart.
func TestStdRecordsKeyedByPackage(t *testing.T) {
	const fixture = `type NotFound { Path string }

func Fail(path string) fails NotFound {
	return NotFound{Path: path}
}
`
	imp := pkgImporter{
		"fixturea": stdFixture(t, "fixturea", fixture),
		"fixtureb": stdFixture(t, "fixtureb", fixture),
	}
	const src = `import fixturea
import fixtureb
import fs

func run(n int) fails fixturea.NotFound | fixtureb.NotFound | fs.NotFound {
	if n == 0 {
		try fixturea.Fail("a.txt")
	} else if n == 1 {
		try fixtureb.Fail("b.txt")
	} else {
		return fs.NotFound{Path: "fs.txt"}
	}
}

func which(n int) string {
	run(n) catch {
	case fixturea.NotFound as a:
		return "fixturea " + a.Path
	case fixtureb.NotFound as b:
		return "fixtureb " + b.Path
	case fs.NotFound as f:
		return "fs " + f.Path
	}
	"none"
}

func main() needs Console {
	Console.Println(which(0))
	Console.Println(which(1))
	Console.Println(which(2))
}
`
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	out := &lowered{name: "stdkeys", src: src, file: file}
	out.pkg, out.diags = check.Check("main", []*syntax.File{file}, imp)
	if len(out.diags) != 0 {
		t.Fatalf("diagnostics: %v", out.diags)
	}
	code, err := lowerPackage(Unit{Package: out.pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}, false)
	var refused *Refused
	if errors.As(err, &refused) || err != nil {
		t.Fatalf("lowering: %v", err)
	}
	out.files = []File{{Path: "go.mod", Data: []byte(goMod)}, {Path: "main.go", Data: code}}
	// fs.NotFound keeps its row: it is v1.NotFound, the value FSReadFile
	// returns; neither fixture record has a row, so each is a type of the
	// program.
	if !strings.Contains(string(code), "ergo.NotFound{") {
		t.Errorf("fs.NotFound does not lower to the runtime's NotFound:\n%s", code)
	}
	exes := buildAll(t, []*lowered{out}, true)
	if exes == nil {
		return
	}
	stdout, stderr, exit := runExe(t, exes["stdkeys"])
	if want := "fixturea a.txt\nfixtureb b.txt\nfs fs.txt\n"; stdout != want || exit != 0 {
		t.Errorf("exit %d, stdout %q, want %q\nstderr %s", exit, stdout, want, firstLines(stderr, 12))
	}
}
