package irspike

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

const codecCensusSrc = `import json

type Ev { N int, Name string }

func load(text string) string {
	let r = json.Open(text, 0, 0) catch {
	case _:
		return "malformed"
	}
	let e = decode[Ev](r) catch {
	case _:
		return "wrong"
	}
	e.Name
}

func main() needs Console {
	Console.Println(load("{\"n\": 1, \"name\": \"x\"}"))
}
`

// TestCensusCountsCodecText: a codec function that keeps its text body is
// a census body with the reason "codec reader: <form>" (PORT-WIRE-READERS
// step 0), owned by a unit, and the census counts it once as a function
// however many targets reach it, and once per target in CodecReached. The
// program is lowered as two targets of different domains.
//
// Mutants: census-codec-uncounted (codecText adds no census body: the
// decoders are invisible debt again), census-codec-per-target (the
// function's key holds the domain: one function counted twice).
func TestCensusCountsCodecText(t *testing.T) {
	file := filepath.Join(t.TempDir(), "p.ergo")
	if err := os.WriteFile(file, []byte(codecCensusSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	b := &censusBuilder{c: &Census{Targets: map[Domain]int{}}, byKey: map[string]*CensusFunc{}}
	for _, d := range []Domain{Examples, Tests} {
		c, err := lower.CensusIR(u, d == Tests)
		if err != nil {
			t.Fatal(err)
		}
		b.add(d, c.Funcs)
	}
	const reason = "codec reader: record"
	funcs := 0
	for _, f := range b.c.Funcs {
		if strings.HasPrefix(f.Key, "codec ") && contains(f.Reasons, reason) {
			funcs++
		}
	}
	if funcs != 1 {
		t.Errorf("the census holds %d codec functions with %q; want Ev's decoder once", funcs, reason)
	}
	if got := b.c.CodecReached[reason]; got[Examples] != 1 || got[Tests] != 1 {
		t.Errorf("CodecReached[%q] = %v; want one target in examples and one in tests", reason, got)
	}
	if u := UnitOf(reason); u != "PORT-WIRE-READERS" {
		t.Errorf("UnitOf(%q) = %q; want PORT-WIRE-READERS", reason, u)
	}
	var w strings.Builder
	b.c.WriteTable(&w)
	if !strings.Contains(w.String(), "| "+reason+" | 1 | 1 | 1 |") {
		t.Errorf("the table does not print %q as one function reached by one example and one test target:\n%s", reason, w.String())
	}
}
