package irspike

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRTestsMatchSource is IRG6's agreement property for test bodies:
// for generated programs of tests and asserts (GenerateTests), the test
// builds of the IR path and the source path report the same passes and
// failures, with the same texts, and exit the same. Every test gets an IR
// body in every seed. A failing seed prints the Ergo source, both
// outcomes and its replay command. It builds Go for the reason
// needsBuild gives.
func TestIRTestsMatchSource(t *testing.T) {
	testBuildsAgree(t, "TestIRTestsMatchSource", GenerateTests, "ergoTest", testBodies, "FAIL ", "ok ")
}

// TestIRLawsMatchSource is IRG6's agreement property for law bodies: for
// generated programs of laws (GenerateLaws), the test builds of both
// paths report the same held and refuted laws, with the same sites, and
// exit the same. Every law gets an IR body in every seed, a generic
// operation type's copies too (mutant g6-generic-law-refused). It builds
// Go for the reason needsBuild gives.
func TestIRLawsMatchSource(t *testing.T) {
	testBuildsAgree(t, "TestIRLawsMatchSource", GenerateLaws, "ergoLaw", lawBodies, "FAIL law", "ok law")
}

// testBuildsAgree runs the seeds of gen: each program's test build by
// both paths, every body whose Go name starts with prefix (want of them)
// an IR body, the runs equal; over a whole run, both a failing (fail in
// the output) and a passing (pass) report.
func testBuildsAgree(t *testing.T, name string, gen func(uint64) string, prefix string, want int, fail, pass string) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	failed, passed := false, false
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := gen(seed)
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run " + name + " ./internal/experimental/irspike"
		if _, rep, err := lower.LowerIRTests(u); err != nil {
			t.Fatal(err)
		} else if n := countPrefix(rep.IR, prefix); n != want {
			t.Fatalf("seed %d: %d of %d %s bodies are IR bodies (IR %v, fallback %v)\n%s\n%s", seed, n, want, prefix, rep.IR, rep.Fallback, replay, text)
		}
		var bins [2]string
		for i, p := range []Path{Source, IR} {
			mod := filepath.Join(dir, p.String())
			if err := emitTests(u, p, mod); err != nil {
				t.Fatalf("seed %d: test build (%s): %v", seed, p, err)
			}
			tc := src
			if p == IR {
				tc = ir
			}
			bins[i] = filepath.Join(dir, p.String()+".test")
			if _, err := Build(tc, p, mod, bins[i], BuildOptions{}); err != nil {
				t.Fatalf("seed %d: %s test build: %v\n%s\n%s", seed, p, err, replay, text)
			}
		}
		run := caseRun{name: "test", args: []string{"--source", dir}}
		a, b := runCase(t, bins[0], run), runCase(t, bins[1], run)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		failed = failed || strings.Contains(a.stdout, fail)
		passed = passed || strings.Contains(a.stdout, pass)
	}
	if count >= 6 && !(failed && passed) {
		t.Fatalf("the run did not reach both a passing and a failing report (passed %v, failed %v)", passed, failed)
	}
}

// countPrefix counts the names that start with prefix.
func countPrefix(names []string, prefix string) int {
	n := 0
	for _, name := range names {
		if strings.HasPrefix(name, prefix) {
			n++
		}
	}
	return n
}
