package checkwork_test

import (
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// budgetProgram imports one std package, calls its externs and binds a
// provider of a std operation type, so its check reaches the sealed std
// set and the runtime body facts.
const budgetProgram = `import time

let Manual = context { Scheduler: time.NewManual(0) }

func main() needs Console {
	let m = time.NewManual(0)
	Console.Println("\(m.Now().UnixSeconds())")
	Console.Println(with Manual { "bound" })
}
`

// TestCheckWorkGrowsWithImports is the counted check budget (CHK1): in a
// fresh process, one check of a program that imports time parses the
// files of time's std import closure and no other std file, checks those
// packages and no other, and never parses runtime/v1's Go sources. It
// catches a check that parses all of std (a per-process table built from
// source, such as the sealed std set) or the runtime's sources (BodyOf
// reading files instead of its generated table): mutants
// sealed-from-source and bodies-from-source of the ledgers.
func TestCheckWorkGrowsWithImports(t *testing.T) {
	if before := check.WorkDone(); before != (check.Work{}) || v1.SourceParses() != 0 {
		t.Fatalf("the process did std work before the check: %+v, runtime/v1 parses %d", before, v1.SourceParses())
	}
	if _, diags := check.CheckSource(budgetProgram); len(diags) > 0 {
		t.Fatalf("the budget program does not check: %v", diags)
	}
	closure := stdClosure(t, "time")
	files := int64(0)
	for _, p := range closure {
		srcs, _ := std.Package(p)
		files += int64(len(srcs))
	}
	want := check.Work{StdFilesParsed: files, StdPackagesChecked: int64(len(closure))}
	if got := check.WorkDone(); got != want {
		t.Errorf("one check of a program importing time did %+v; want %+v, the std closure %v alone", got, want, closure)
	}
	if n := v1.SourceParses(); n != 0 {
		t.Errorf("one check parsed runtime/v1's Go sources %d times; want 0 (the body facts are a generated table)", n)
	}
}

// stdClosure is the std import closure of the std package root, sorted,
// read from the import declarations of its sources.
func stdClosure(t *testing.T, root string) []string {
	seen := map[string]bool{}
	var visit func(p string)
	visit = func(p string) {
		if seen[p] {
			return
		}
		srcs, ok := std.Package(p)
		if !ok {
			return // a Go import or the prelude
		}
		seen[p] = true
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
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
