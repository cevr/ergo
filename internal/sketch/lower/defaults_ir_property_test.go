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

// The seeded property "a default the IR states is the emitter's": a field
// a record literal leaves out, and a `let mut m Map[…]` with no value,
// start at their default (types.md, Defaults): Go's zero value, or an
// empty Map (an int-keyed Map's empty table), field by field through a
// nested record. The IR builder states each default in the literal
// (defaultOf, ir_letpat.go) instead of keeping the emitter's body.
//
// TestIRDefaultsMatchEmitter writes, per seed, records whose fields mix
// scalars, slices, string- and int-keyed Maps and a nested record holding
// a Map; one test body builds literals that leave out a random subset of
// fields, and declares Maps with no value; it then writes into each
// defaulted Map in place and checks every length and value. Each seed
// runs on the evaluator, the text path's test build and `ergo test
// --native` (the IR), which must all pass, and the IR build keeps no
// body for either census reason. Replay: go test -run
// '^TestIRDefaultsMatchEmitter$' ./internal/sketch/lower -args -defaults.seed=N
//
// Mutant (lower ledger): ir-default-map-nil (an omitted Map field is
// Go's nil map: the first write through it panics on the IR).

var defaultsSeed = flag.Uint64("defaults.seed", 0, "run TestIRDefaultsMatchEmitter on this seed only")

// defaultsField is one field kind: its Ergo type, a write into a value v
// of the field (in place), and the facts that hold after it.
type defaultsField struct {
	typ, write, fact string
}

var defaultsKinds = []defaultsField{
	{"int", "", "%s == 0"},
	{"string", "", "%s == \"\""},
	{"[]int", "", "len(%s) == 0"},
	{"Map[string, int]", "%s[\"k\"] = 7", "len(%s) == 1 && %s[\"k\"] == 7"},
	{"Map[int, int]", "%s[3] = 9", "len(%s) == 1 && %s[3] == 9"},
	{"Inner", "%s.M[1] = true", "len(%s.M) == 1"},
}

// defaultsProgram is seed's package.
func defaultsProgram(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x64656661)) // "defa"
	var b strings.Builder
	b.WriteString("type Inner { M Map[int, bool], N int }\n\n")
	var body strings.Builder
	for ri := 0; ri < 2; ri++ {
		n := 2 + r.IntN(4)
		var fields []string
		kinds := make([]defaultsField, n)
		for i := range n {
			kinds[i] = defaultsKinds[r.IntN(len(defaultsKinds))]
			fields = append(fields, fmt.Sprintf("F%d %s", i, kinds[i].typ))
		}
		fmt.Fprintf(&b, "type R%d { %s }\n\n", ri, strings.Join(fields, ", "))
		// The literal gives some fields and leaves the rest out; at
		// least one is left out.
		var given []string
		var omitted []int
		for i := range n {
			if r.IntN(3) == 0 && len(omitted) < n-1 || i == n-1 && len(omitted) == 0 {
				omitted = append(omitted, i)
				continue
			}
			given = append(given, fmt.Sprintf("F%d: %s", i, defaultsValue(kinds[i].typ)))
		}
		v := fmt.Sprintf("r%d", ri)
		fmt.Fprintf(&body, "\tlet mut %s = R%d{%s}\n", v, ri, strings.Join(given, ", "))
		for _, i := range omitted {
			f := fmt.Sprintf("%s.F%d", v, i)
			k := kinds[i]
			if k.write != "" {
				fmt.Fprintf(&body, "\t"+k.write+"\n", f)
			}
			fmt.Fprintf(&body, "\tassert "+k.fact+"\n", repeatArg(k.fact, f)...)
		}
	}
	// A Map declared with no value, written in place.
	fmt.Fprintf(&body, "\tlet mut m Map[int, string]\n\tm[%d] = \"x\"\n\tassert len(m) == 1\n", r.IntN(9))
	fmt.Fprintf(&body, "\tlet mut s Map[string, int]\n\ts[\"a\"] = %d\n\tassert len(s) == 1\n", r.IntN(9))
	fmt.Fprintf(&b, "test Defaults {\n%s}\n\n", body.String())
	b.WriteString("func main() needs Console {\n\tConsole.Println(\"x\")\n}\n")
	return b.String()
}

// defaultsValue is a value of the field type typ that is not its
// default.
func defaultsValue(typ string) string {
	switch typ {
	case "int":
		return "4"
	case "string":
		return "\"s\""
	case "[]int":
		return "[]int{1}"
	case "Map[string, int]":
		return "Map[string, int]{\"a\": 1}"
	case "Map[int, int]":
		return "Map[int, int]{1: 2}"
	}
	return "Inner{M: Map[int, bool]{5: true}, N: 1}"
}

// repeatArg is f once for each %s of format.
func repeatArg(format, f string) []any {
	var out []any
	for range strings.Count(format, "%s") {
		out = append(out, f)
	}
	return out
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
			if strings.Contains(reason, "without every field") || strings.Contains(reason, "default that holds a Map") {
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
