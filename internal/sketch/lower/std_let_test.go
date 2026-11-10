package lower

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestStdPackageLet is the smallest repro of wf10 defects D3: a top-level
// let of a std package, read by its own function and by the program,
// stopped the lowering with S109 "the let fixturelet.Default has no Go
// name": the naming pass named the lets of the program's own packages
// only. The lowered Go type-checks in process (typecheckGo).
//
// The fixture also has a let that only its law reads, through a function
// (wf10 defects D6: std flag's lawText ran in every program that imports
// flag). A std let is lowered where a body of the build reaches it, so
// the build lowers the two lets the program reaches and not the third: a
// count of the std let declarations lowered (stdLetInits), not a time.
//
// In the full tier it also runs the program: it builds Go because the
// other half of the defect is when the let gets its value: a std let must
// be set before a program let that reads it, and Go's types do not show
// the order of initFunc's statements, only a run does.
func TestStdPackageLet(t *testing.T) {
	const fixture = `type Framing { Sep string }

let lines = Framing{Sep: "+"}

let Default = Framing{Sep: lines.Sep + lines.Sep}

func Join(a string, b string) string { a + lines.Sep + b }

let lawFraming = Framing{Sep: Join("x", "y")}

func lawSep() string { lawFraming.Sep }

law SepIsKept(s string) { lawSep() == "x+y" || s == "" }
`
	imp := pkgImporter{"fixturelet": stdFixture(t, "fixturelet", fixture)}
	const src = `import fixturelet

let wide = fixturelet.Default.Sep + "|"

func main() needs Console {
	Console.Println(fixturelet.Join("a", "b") + wide)
}
`
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	out := &lowered{name: "stdlet", src: src, file: file}
	out.pkg, out.diags = check.Check("main", []*syntax.File{file}, imp)
	if len(out.diags) != 0 {
		t.Fatalf("diagnostics: %v", out.diags)
	}
	l := newLowerer(Unit{Package: out.pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}})
	code, err := l.program()
	if err != nil {
		t.Fatalf("lowering: %v", err)
	}
	if l.stdLetInits != 2 {
		t.Errorf("%d std let declarations lowered, want 2 (lines and Default; lawFraming only a law reads)", l.stdLetInits)
	}
	out.files = []File{{Path: "go.mod", Data: []byte(goMod)}, {Path: "main.go", Data: code}}
	typecheckGo(t, []*lowered{out})
	if testing.Short() {
		return
	}
	exes := buildAll(t, []*lowered{out}, true)
	if exes == nil {
		return
	}
	stdout, stderr, status := runExe(t, exes["stdlet"])
	if status != 0 || stdout != "a+b++|\n" {
		t.Fatalf("got %q, exit %d, stderr %s; want %q", stdout, status, firstLines(stderr, 8), "a+b++|\n")
	}
}
