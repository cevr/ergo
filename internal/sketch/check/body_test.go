package check_test

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
)

// TestBodyFactsFollowTheBody holds the body contract to its rule (GLOSSARY
// Body contract; STD-NOPRIV NP4b): an extern's facts follow the Go body its
// declaration binds, never the package that declares it. For every extern
// of the prelude and of each std package, a user package's extern of the
// same runtime/v1 body gets the same check.BodyOf facts; for every
// operation of a std type that is a runtime/v1 method, a user type of the
// same name in a package that binds the runtime gets the same
// check.OpBodyOf facts. The user package is refused (it binds the runtime,
// NP4e's door), which does not change what its declarations bind. A
// prelude type name is the language's, so a user package cannot declare
// it; those operations are left out.
//
// Mutant body-fact-std-only (check.BodyOf reads no fact for an extern of a
// program package) and opbody-fact-std-only (the same in check.OpBodyOf):
// no other test checks a non-std package that binds a runtime body.
func TestBodyFactsFollowTheBody(t *testing.T) {
	pkgs := stdPackages(t)
	stdFacts := map[string]check.Body{}
	type op struct{ typ, name string }
	stdOps := map[op]check.OpBody{}
	for _, p := range pkgs {
		for _, f := range p.Funcs {
			if b := check.BodyOf(f); b.Runtime != "" {
				stdFacts[b.Runtime] = b
			}
		}
		for _, tn := range p.Types {
			n, ok := tn.Type.(*check.Named)
			if !ok || check.IsPredeclared(tn.Name) {
				continue
			}
			it, ok := n.Underlying().(*check.Interface)
			if !ok {
				continue
			}
			for _, m := range it.Methods {
				if b := check.OpBodyOf(n, m.Name); b.Runtime != "" {
					stdOps[op{tn.Name, m.Name}] = b
				}
			}
		}
	}
	if len(stdFacts) == 0 || len(stdOps) == 0 {
		t.Fatalf("std binds %d runtime bodies and %d runtime operations: the walk found no std package", len(stdFacts), len(stdOps))
	}
	names := make([]string, 0, len(stdFacts))
	for name := range stdFacts {
		names = append(names, name)
	}
	sort.Strings(names)
	var src strings.Builder
	for i, name := range names {
		fmt.Fprintf(&src, "extern go ergo.local/prototype/runtime/v1.%s func body%d()\n", name, i)
	}
	opsOf := map[string][]string{}
	for k := range stdOps {
		opsOf[k.typ] = append(opsOf[k.typ], k.name)
	}
	for typ, ops := range opsOf {
		sort.Strings(ops)
		fmt.Fprintf(&src, "type %s { %s() }\n", typ, strings.Join(ops, "(), "))
	}
	user, ds := check.CheckSource(src.String())
	if user == nil {
		t.Fatalf("the user package does not parse: %v\n%s", ds, src.String())
	}
	seen := 0
	for _, f := range user.Funcs {
		var i int
		if _, err := fmt.Sscanf(f.Name, "body%d", &i); err != nil || i >= len(names) {
			continue
		}
		seen++
		if got, want := check.BodyOf(f), stdFacts[names[i]]; !reflect.DeepEqual(got, want) {
			t.Errorf("a user extern of runtime/v1.%s gets the facts\n\t%+v\nstd's extern of the same body gets\n\t%+v", names[i], got, want)
		}
	}
	if seen != len(names) {
		t.Fatalf("the user package declares %d of the %d externs", seen, len(names))
	}
	for k, want := range stdOps {
		var n *check.Named
		for _, tn := range user.Types {
			if tn.Name == k.typ {
				n, _ = tn.Type.(*check.Named)
			}
		}
		if n == nil {
			t.Fatalf("the user package declares no type %s", k.typ)
		}
		if got := check.OpBodyOf(n, k.name); got != want {
			t.Errorf("the operation %s.%s of a user type gets the facts %+v; std's gets %+v", k.typ, k.name, got, want)
		}
	}
}

// stdPackages are the prelude and every std package.
func stdPackages(t *testing.T) []*check.Package {
	t.Helper()
	outside, ds := check.CheckSource("func uses(_ LiveScheduler) {}\n")
	if len(ds) != 0 {
		t.Fatalf("outside: %v", ds)
	}
	var out []*check.Package
	for id, obj := range outside.Info.Uses {
		if tn, ok := obj.(*check.TypeName); ok && id.Name == "LiveScheduler" {
			out = append(out, tn.Pkg)
		}
	}
	if len(out) != 1 {
		t.Fatal("no prelude package behind LiveScheduler")
	}
	for _, path := range std.Packages() {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		out = append(out, pkg)
	}
	return out
}
