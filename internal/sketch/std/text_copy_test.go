package std_test

import (
	"fmt"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestStdCopyAgrees is STD-NOPRIV for std text (stdlib.md, Text spans):
// std gets nothing a module cannot write. The package's sources, checked
// as a user package of another module, check with no diagnostic and give
// the same exported names and the same laws as std's own check. So text
// binds no runtime body and leans on no compiler rule keyed on its name;
// the laws that hold of std's copy hold of a module's, since they are the
// same source.
func TestStdCopyAgrees(t *testing.T) {
	sources, ok := std.Package("text")
	if !ok {
		t.Fatal("no std package text")
	}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	slices.Sort(names)
	var files []*syntax.File
	for _, name := range names {
		f, err := syntax.Parse(sources[name])
		if err != nil {
			t.Fatalf("text/%s: %v", name, err)
		}
		files = append(files, f)
	}
	copied, ds := check.Check("example.com/spans/text", files, nil)
	if len(ds) != 0 || copied == nil {
		t.Fatalf("std text copied into a module does not check: %v", ds)
	}
	own, err := check.Std.Import("text")
	if err != nil {
		t.Fatal(err)
	}
	exported := func(p *check.Package) []string {
		var out []string
		for _, o := range p.Exported() {
			switch o := o.(type) {
			case *check.TypeName:
				out = append(out, "type "+o.Name)
			case *check.Func:
				out = append(out, "func "+o.Name)
			default:
				out = append(out, fmt.Sprintf("%T", o))
			}
		}
		slices.Sort(out)
		return out
	}
	laws := func(p *check.Package) []string {
		var out []string
		for _, l := range p.Laws {
			out = append(out, l.Name)
		}
		slices.Sort(out)
		return out
	}
	if a, b := exported(copied), exported(own); !slices.Equal(a, b) {
		t.Errorf("the copy exports %v, std text %v", a, b)
	}
	if a, b := laws(copied), laws(own); len(b) == 0 || !slices.Equal(a, b) {
		t.Errorf("the copy has laws %v, std text %v", a, b)
	}
}
