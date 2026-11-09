package lower

import (
	"errors"
	"strconv"
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

// TestStdRecordsBindTheRowsGoName (std-align S3b round 2, M1): a std
// record lowers to the struct its row names, not to a struct of its own
// name. Two fixture packages each declare NotFound { Path string }, and a
// test table binds them through each permutation of the two runtime/v1
// structs of that shape, NotFound and Denied: the identity, where only the
// package tells them apart, and the swap, where neither record's Go name is
// its own name. Each time the emitted Go names each row's struct, has no
// struct of the program for either record, and each catch arm takes its
// own failure. A lowering that emits the record's name in place of the row's
// lowers both to one Go type, and the arms collide.
func TestStdRecordsBindTheRowsGoName(t *testing.T) {
	needsBuild(t)
	const fixture = `type NotFound { Path string }

func Fail(path string) fails NotFound {
	return NotFound{Path: path}
}
`
	const src = `import fixturea
import fixtureb

func run(n int) fails fixturea.NotFound | fixtureb.NotFound {
	if n == 0 {
		try fixturea.Fail("a.txt")
	} else {
		try fixtureb.Fail("b.txt")
	}
}

func which(n int) string {
	run(n) catch {
	case fixturea.NotFound as a:
		return "fixturea " + a.Path
	case fixtureb.NotFound as b:
		return "fixtureb " + b.Path
	}
	"none"
}

func main() needs Console {
	Console.Println(which(0))
	Console.Println(which(1))
}
`
	imp := pkgImporter{
		"fixturea": stdFixture(t, "fixturea", fixture),
		"fixtureb": stdFixture(t, "fixtureb", fixture),
	}
	real := stdRecord
	t.Cleanup(func() { stdRecord = real })
	var built []*lowered
	for i, rows := range [][2]string{{"NotFound", "Denied"}, {"Denied", "NotFound"}} {
		table := map[string]string{"fixturea.NotFound": rows[0], "fixtureb.NotFound": rows[1]}
		stdRecord = func(pkg, name string) (string, bool) {
			if goName, ok := table[pkg+"."+name]; ok {
				return goName, true
			}
			return real(pkg, name)
		}
		file, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		out := &lowered{name: "rowname" + strconv.Itoa(i), src: src, file: file}
		out.pkg, out.diags = check.Check("main", []*syntax.File{file}, imp)
		if len(out.diags) != 0 {
			t.Fatalf("diagnostics: %v", out.diags)
		}
		code, err := lowerPackage(Unit{Package: out.pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}, false)
		if err != nil {
			t.Fatalf("rows %v: lowering: %v", rows, err)
		}
		for _, goName := range rows {
			if !strings.Contains(string(code), "ergo."+goName+"{Path:") {
				t.Errorf("rows %v: the Go does not build v1.%s:\n%s", rows, goName, code)
			}
		}
		if strings.Contains(string(code), "type NotFound struct") {
			t.Errorf("rows %v: the Go declares a struct of the program for a record with a row:\n%s", rows, code)
		}
		out.files = []File{{Path: "go.mod", Data: []byte(goMod)}, {Path: "main.go", Data: code}}
		built = append(built, out)
	}
	stdRecord = real
	exes := buildAll(t, built, true)
	if exes == nil {
		return
	}
	for _, out := range built {
		stdout, stderr, exit := runExe(t, exes[out.name])
		if want := "fixturea a.txt\nfixtureb b.txt\n"; stdout != want || exit != 0 {
			t.Errorf("%s: exit %d, stdout %q, want %q\nstderr %s", out.name, exit, stdout, want, firstLines(stderr, 12))
		}
	}
}

// TestStdRecordsKeyedByPackage (std-align S3b): two std packages each
// declare a record NotFound, and one program fails with each and catches
// each in its own arm. A std record binds a runtime struct by its package
// and its name (v1.StdRecord), so the two records are two Go types and
// each arm takes its own failure. Before, the lowering bound a std record
// by its bare name, so both lowered to v1.NotFound, the fs record's struct,
// and the catch could not tell them apart.
func TestStdRecordsKeyedByPackage(t *testing.T) {
	needsBuild(t)
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
