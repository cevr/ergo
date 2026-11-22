package main

import (
	"slices"
	"strings"
	"testing"
)

// The guard packages run whole in the fast tier: no size cut, no
// selection, a note that says why (kills guards-step-skipped-in-fast for
// internal/residue, granted the checkout, and marker-ignored for
// internal/sketch/stdlib, which declares //gate:whole); -guards keeps
// exactly their steps and what they need.
func TestGuardPackagesRunWhole(t *testing.T) {
	root := checkoutRoot(t)
	byPackage := fastPackageSteps(t, root)
	for _, dir := range []string{"internal/residue", "internal/sketch/stdlib"} {
		steps := byPackage[dir]
		if len(steps) == 0 {
			t.Errorf("%s has no fast step", dir)
		}
		for _, step := range steps {
			if slices.Contains(step.Argv, "-skip") || slices.Contains(step.Argv, "-run") || !strings.HasPrefix(step.Note, "whole: ") {
				t.Errorf("%s runs %q (note %q); want every test, whole", step.Name, step.Argv, step.Note)
			}
		}
	}
	steps, err := stepsFor(root, guardsTier)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		needed := slices.ContainsFunc(steps, func(other *Step) bool { return slices.Contains(other.Needs, step.Name) })
		if !strings.HasPrefix(step.Note, "whole: ") && !needed {
			t.Errorf("-guards runs %s, which is no guard and no guard needs", step.Name)
		}
	}
}

// A //gate:whole declaration gives its reason and sits in a package that
// builds no Go (kills marker-on-building-package-accepted) and has at most
// wholeTestLimit tests.
func TestWholeMarkerHoldsItsBounds(t *testing.T) {
	root := t.TempDir()
	p := testPackage{dir: "a"}
	write(t, root, "a/a_test.go", "package a\n\n//gate:whole the rows of a\n\nimport \"testing\"\n\nfunc TestRows(t *testing.T) {}\n")
	if reason, err := wholeOf(root, p); err != nil || reason != "the rows of a" {
		t.Errorf("a declared guard: %q, %v; want its reason", reason, err)
	}
	write(t, root, "a/b_test.go", "package a\n\nimport \"testing\"\n\nfunc TestBuild(t *testing.T) {\n\tneedsBuild(t)\n}\n")
	if _, err := wholeOf(root, p); err == nil || !strings.Contains(err.Error(), "TestBuild builds Go") {
		t.Errorf("a declared guard with a test that builds Go: %v; want it refused", err)
	}
	write(t, root, "a/b_test.go", "package a\n\n//gate:whole\n")
	if _, err := wholeOf(root, p); err == nil || !strings.Contains(err.Error(), "no reason") {
		t.Errorf("a declaration with no reason: %v; want it refused", err)
	}
	var many strings.Builder
	many.WriteString("package a\n\nimport \"testing\"\n\n")
	for i := range wholeTestLimit {
		many.WriteString("func TestMany" + strings.Repeat("x", i+1) + "(t *testing.T) {}\n")
	}
	write(t, root, "a/b_test.go", many.String())
	if _, err := wholeOf(root, p); err == nil || !strings.Contains(err.Error(), "over wholeTestLimit") {
		t.Errorf("a declared guard of %d tests: %v; want it refused", wholeTestLimit+1, err)
	}
}
