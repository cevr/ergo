package check

import (
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// budgetProgram imports one std package, calls its externs and binds a
// provider of a std operation type, so its check reaches the sealed std
// set (SealedStd) and the runtime body facts (v1.BodyOf).
const budgetProgram = `import time

let Manual = context { Scheduler: time.NewManual(0) }

func main() needs Console {
	let m = time.NewManual(0)
	Console.Println("\(m.Now().UnixSeconds())")
	Console.Println(with Manual { "bound" })
}
`

// TestCheckWorkGrowsWithImports is the counted check budget (CHK1): one
// check of a program that imports time, with an importer of its own, parses
// the files of time's std import closure and no other std file, checks
// those packages and no other, and reads no runtime/v1 Go source. It counts
// work, never time. It catches a check whose cost grows with the std, not
// with the program: the importer checking a package the program does not
// reach, or BodyOf reading the runtime's parsed sources instead of its
// generated table (mutant bodies-from-source of the ledger).
func TestCheckWorkGrowsWithImports(t *testing.T) {
	f, err := syntax.Parse(budgetProgram)
	if err != nil {
		t.Fatal(err)
	}
	// A std path always resolves through Std (importSpec), so the check
	// gets a fresh Std. The test is not parallel: no other test of the
	// package runs while it holds the swap.
	imp := newStdImporter()
	saved := Std
	Std = imp
	defer func() { Std = saved }()
	reads := v1.SourceReads()
	if _, diags := Check("main", []*syntax.File{f}, nil); len(diags) > 0 {
		t.Fatalf("the budget program does not check: %v", diags)
	}
	closure := stdClosure(t, "time")
	files := int64(0)
	for _, p := range closure {
		srcs, _ := std.Package(p)
		files += int64(len(srcs))
	}
	want := Work{StdFilesParsed: files, StdPackagesChecked: int64(len(closure))}
	if got := imp.done(); got != want {
		t.Errorf("one check of a program importing time did %+v; want %+v, the std closure %v alone", got, want, closure)
	}
	if n := v1.SourceReads() - reads; n != 0 {
		t.Errorf("one check read runtime/v1's parsed Go sources %d times; want 0 (the body facts are a generated table)", n)
	}
}

// stdClosure is the std import closure of the std package root, sorted,
// read from the import declarations of its sources.
func stdClosure(t *testing.T, root string) []string {
	var out []string
	var visit func(p string)
	visit = func(p string) {
		if slices.Contains(out, p) {
			return
		}
		srcs, ok := std.Package(p)
		if !ok {
			return // a Go import
		}
		out = append(out, p)
		for _, src := range srcs {
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatalf("std package %s does not parse: %v", p, err)
			}
			for _, d := range f.Decls {
				if id, ok := d.(*syntax.ImportDecl); ok {
					for _, s := range id.Specs {
						visit(strings.Trim(s.Path.Value, "\"`"))
					}
				}
			}
		}
	}
	visit(root)
	slices.Sort(out)
	return out
}
