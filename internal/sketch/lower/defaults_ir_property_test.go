package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The seeded property "a Map's default the IR states is the emitter's":
// `let mut m T` with no value starts at T's default (types.md, Defaults 3),
// an empty Map for a Map, an int-keyed Map's empty table on the IR. The IR
// builder states it (defaultOf, ir_letpat.go) instead of keeping the
// emitter's body. The same defaultOf fills the fields a record literal
// leaves out, which only a literal the checker admits without every field
// has (a script recorder's empty literal, `recorder[Screen]{}`: source
// literals name every field, Zero values 2); the examples that hold one
// (particles, pipeline) run on both paths in TestIRAgreesOnExamples.
//
// TestIRDefaultsMatchEmitter writes, per seed, a test body that declares
// Maps of several key and value types with no value, writes into each in
// place and checks its length and entries. Each seed runs on the
// evaluator, the text path's test build and `ergo test --native` (the
// IR), which must all pass, and the IR build keeps no body for the
// default. Replay: go test -run '^TestIRDefaultsMatchEmitter$'
// ./internal/sketch/lower -args -defaults.seed=N
//
// Mutant (lower ledger): ir-default-map-nil (a Map's default is Go's nil
// map: the first write through it panics on the IR).

var defaultsSeed = flag.Uint64("defaults.seed", 0, "run TestIRDefaultsMatchEmitter on this seed only")

// defaultsMaps are the Map types a seed declares with no value, a key and
// a value of each.
var defaultsMaps = []struct{ typ, key, val string }{
	{"Map[int, string]", "3", "\"x\""},
	{"Map[string, int]", "\"k\"", "7"},
	{"Map[int64, []int]", "int64(2)", "[]int{1}"},
	{"Map[uint8, bool]", "uint8(5)", "true"},
	{"Map[string, Map[int, int]]", "\"m\"", "Map[int, int]{1: 2}"},
}

// defaultsProgram is seed's package.
func defaultsProgram(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x64656661)) // "defa"
	var body strings.Builder
	n := 2 + r.IntN(4)
	for i := range n {
		m := defaultsMaps[r.IntN(len(defaultsMaps))]
		fmt.Fprintf(&body, "\tlet mut m%d %s\n\tassert len(m%d) == 0\n", i, m.typ, i)
		writes := 1 + r.IntN(2) // a let mut is written (S110)
		for range writes {
			fmt.Fprintf(&body, "\tm%d[%s] = %s\n", i, m.key, m.val)
		}
		want := 0
		if writes > 0 {
			want = 1
		}
		fmt.Fprintf(&body, "\tassert len(m%d) == %d\n", i, want)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "test Defaults {\n%s}\n\n", body.String())
	b.WriteString("func main() needs Console {\n\tConsole.Println(\"x\")\n}\n")
	return b.String()
}

// TestIRDefaultsMatchEmitter is the property above.
//
// It builds Go because the question is what the test builds do at run
// time on the text path and on the IR (a nil map panics only when
// written), which only a run shows.
func TestIRDefaultsMatchEmitter(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{*defaultsSeed}
	if *defaultsSeed == 0 {
		seeds = []uint64{1, 2, 3, 4}
	}
	var builds []*lowered
	srcs := map[uint64]string{}
	for _, seed := range seeds {
		srcs[seed] = defaultsProgram(seed)
		builds = append(builds, lowerTestBuild(t, fmt.Sprintf("defaults%d", seed), srcs[seed]))
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		t.Fatal("the defaults programs do not build")
	}
	for _, seed := range seeds {
		src := srcs[seed]
		replay := fmt.Sprintf("Replay: go test -run '^TestIRDefaultsMatchEmitter$' ./internal/sketch/lower -args -defaults.seed=%d\n%s", seed, src)
		rep, err := evaluate(src)
		if err != nil {
			t.Fatalf("seed %d: the evaluator: %v\n%s", seed, err, replay)
		}
		for _, res := range rep.Results {
			if res.Status != "passed" {
				t.Errorf("seed %d: the evaluator says %s %s: %s\n%s", seed, res.Name, res.Status, res.Reason, replay)
			}
		}
		// The IR keeps no emitter body for a default.
		o := lowerSource("defaults", src, true)
		if o.err != nil || len(o.diags) > 0 || o.refused != nil || o.panicked != nil {
			t.Fatalf("seed %d: lowering: %v %v %v %v\n%s", seed, o.err, o.diags, o.refused, o.panicked, replay)
		}
		u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
		_, irRep, err := LowerIRTests(u)
		if err != nil {
			t.Fatalf("seed %d: LowerIRTests: %v\n%s", seed, err, replay)
		}
		for reason := range irRep.Fallback {
			if strings.Contains(reason, "default that holds a Map") {
				t.Errorf("seed %d: the IR keeps the emitter's body: %s\n%s", seed, reason, replay)
			}
		}
		text, _, _ := runExeArgs(t, exes[fmt.Sprintf("defaults%d", seed)])
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "defaults.ergo"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(ergoCLI(t), "test", dir, "--native", "--count", "1")
		cmd.Dir = dir
		irOut, _ := cmd.CombinedOutput()
		for _, run := range []struct{ name, out string }{{"the text path", text}, {"ergo test --native (the IR)", string(withoutBuildNote(irOut))}} {
			ok := slices.ContainsFunc(lines(run.out), func(l string) bool {
				return (l == "ok Defaults" || strings.HasPrefix(l, "ok Defaults ")) && !strings.Contains(l, "(evaluated)")
			})
			if !ok {
				t.Errorf("seed %d: %s does not pass Defaults natively\n--- output\n%s\n%s", seed, run.name, run.out, replay)
			}
		}
	}
}
