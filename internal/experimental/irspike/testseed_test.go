package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRHarnessSeedMatchesSource catches a missing seed lowering, or a
// lowering that silently uses the default seed. The draw is the integer
// draw of sorted-insert/ordered's InsertKeepsOrder; a false assertion
// makes the harness report the seed it actually received. It builds Go
// because the two executables must read the seed from their arguments.
func TestIRHarnessSeedMatchesSource(t *testing.T) {
	const text = `import gen

test InsertKeepsOrder {
	let x = gen.Named("x", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(-5, 100) })
	assert x < -5
}
`
	const seed = "424242"
	dir := t.TempDir()
	file := filepath.Join(dir, "ordered.ergo")
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	source, err := lower.LowerTests(u)
	if err != nil {
		t.Fatalf("source lowering: %v\n%s", err, text)
	}
	irFiles, rep, err := lower.LowerIRTests(u)
	if err != nil {
		t.Fatalf("IR lowering: %v\n%s", err, text)
	}
	if !slices.Contains(rep.IR, "ergoTestInsertKeepsOrder") {
		t.Fatalf("the harnessed test has no IR body; lowered: %v; fallback: %v\n%s", rep.IR, rep.Fallback, text)
	}

	src, ir := needsBuild(t)
	var outcomes [2]outcome
	for i, p := range []Path{Source, IR} {
		files, tc := source, src
		if p == IR {
			files, tc = irFiles, ir
		}
		mod := filepath.Join(dir, p.String())
		if err := writeModule(mod, files); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(dir, p.String()+".test")
		if _, err := Build(tc, p, mod, bin, BuildOptions{}); err != nil {
			t.Fatalf("%s build: %v\n%s", p, err, text)
		}
		outcomes[i] = runCase(t, bin, caseRun{name: "harness", args: []string{"--source", dir, "--seed", seed}})
	}
	if outcomes[0] != outcomes[1] {
		t.Fatalf("harness reports differ\nsource: %+v\nIR: %+v\nreplay: go test -run '^TestIRHarnessSeedMatchesSource$' ./internal/experimental/irspike\n%s", outcomes[0], outcomes[1], text)
	}
	if got := outcomes[0]; got.exit == 0 || !strings.Contains(got.stdout+got.stderr, "--seed "+seed) {
		t.Fatalf("want a refutation that replays seed %s, got %+v\n%s", seed, got, text)
	}
	t.Logf("source and IR both report the refutation with seed %s", seed)
}
