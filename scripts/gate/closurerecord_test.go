package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestMergeTierRecordsForTheSweep: in the merge tier, a go test step of
// the module's packages records its runs, and the closure sweep of such a
// package waits for it and reads the records; a step whose runs a sweep
// could not reuse (a tier variable, build tags, the build of every test
// binary) records nothing, its package's sweep stays as it was, and the
// inner tier's steps are unchanged. Mutants: a step with build tags
// records (heavy-cost2-gate-tags-recorded), a sweep does not wait for its
// package's runs (heavy-cost2-gate-sweep-unordered), the inner tier's step
// is changed in place (heavy-cost2-gate-inner-changed), a recording step
// runs beside a writer of the checkout (heavy-cost2-gate-record-beside-writer).
func TestMergeTierRecordsForTheSweep(t *testing.T) {
	host := sweepOS
	t.Cleanup(func() { sweepOS = host })
	sweepOS = "linux"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module m\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sweepOf := func(dir string) *Step {
		s := sweepStep(dir, 1)
		s.Name = "closure sweep ./" + dir
		s.Needs = []string{"go test build"}
		return s
	}
	steps := []*Step{
		{Name: fullBuildStep, Argv: []string{"go", "test", testTimeout, "-run", "^$", "./..."}},
		{Name: "go test ./a [1/1]", Needs: []string{"go test build"}, Argv: []string{"go", "test", testTimeout, "-run", "^(TestA)$", "m/a"}},
		{Name: "go test (other packages)", Needs: []string{"go test build"}, Argv: []string{"go", "test", testTimeout, "m/b", "m/c"}},
		{Name: "go test ./lower [1/1]", Argv: []string{"go", "test", "m/lower"}, Env: cliTestEnvironment},
		{Name: "explored", Argv: []string{"go", "test", "-tags", "ergoexplore", "./rt"}},
		sweepOf("a"), sweepOf("b"), sweepOf("lower"), sweepOf("rt"),
	}
	inner := make([]Step, len(steps))
	for i, s := range steps {
		inner[i] = *s
	}
	got := recordForSweep(root, steps)
	record := closureRecordVariable + "=" + closureRecordDir()
	reuse := closureReuseVariable + "=" + closureRecordDir()
	byName := map[string]*Step{}
	for _, s := range got {
		byName[s.Name] = s
	}
	for _, name := range []string{"go test ./a [1/1]", "go test (other packages)"} {
		if !slices.Contains(byName[name].Env, record) {
			t.Errorf("%s: env %q; want %s", name, byName[name].Env, record)
		}
	}
	for _, name := range []string{fullBuildStep, "go test ./lower [1/1]", "explored", "closure sweep ./lower", "closure sweep ./rt"} {
		if !slices.Equal(byName[name].Env, inner[slices.IndexFunc(steps, func(s *Step) bool { return s.Name == name })].Env) ||
			!slices.Equal(byName[name].Needs, inner[slices.IndexFunc(steps, func(s *Step) bool { return s.Name == name })].Needs) {
			t.Errorf("%s: env %q, needs %q; want them unchanged", name, byName[name].Env, byName[name].Needs)
		}
	}
	for sweep, run := range map[string]string{"closure sweep ./a": "go test ./a [1/1]", "closure sweep ./b": "go test (other packages)"} {
		if !slices.Contains(byName[sweep].Needs, run) || !slices.Contains(byName[sweep].Env, reuse) {
			t.Errorf("%s: needs %q, env %q; want %q needed and %s", sweep, byName[sweep].Needs, byName[sweep].Env, run, reuse)
		}
	}
	// A recording step starts after the writers of the checkout, as the
	// sweep it stands for does.
	writer := &Step{Name: buildCLIStep, Argv: []string{"go", "build", "-o", "bin/ergo", "./cmd/ergo"}, Writes: gateOutputs}
	ordered := append(slices.Clone(got), writer)
	index := map[string]int{}
	for i, s := range ordered {
		index[s.Name] = i
	}
	after, err := sweepsAfterWriters(ordered, index)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go test ./a [1/1]", "closure sweep ./a"} {
		if !slices.Contains(after[index[name]], index[buildCLIStep]) {
			t.Errorf("%s starts after %v; want after %s, which writes the checkout", name, after[index[name]], buildCLIStep)
		}
	}
	for i, s := range steps {
		if !slices.Equal(s.Env, inner[i].Env) || !slices.Equal(s.Needs, inner[i].Needs) {
			t.Errorf("the inner tier's %s changed: env %q, needs %q", s.Name, s.Env, s.Needs)
		}
	}
}
