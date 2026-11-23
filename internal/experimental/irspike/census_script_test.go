package irspike

import (
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

const scriptCensusSrc = `import testing

type Probe {
	Get(k string) int,
}

func probed() int {
	let s = testing.Script[Probe]{}
	s.Get.Answer(func(k string) int { len(k) })
	with context { Probe: s } { Probe.Get("abc") }
}

test Probed {
	assert probed() == 3
}
`

// TestScriptLiteralTakesIR: a function that makes a testing.Script with
// its literal, answers an operation and provides the Script in a context
// is an IR body with no reason (IR-FALLBACK-RECORD-LITERAL). Before, the
// literal took the record path, which reads the Script declaration's
// fields and so refused it: "record literal without a field of its
// record" (9 bodies in lane SPL's census at bfabf6f7b).
//
// Mutant: ir-script-lit-refused (the IR builder sends a Script literal
// down the record path again).
func TestScriptLiteralTakesIR(t *testing.T) {
	file := filepath.Join(t.TempDir(), "p.ergo")
	if err := os.WriteFile(file, []byte(scriptCensusSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	c, err := lower.CensusIR(u, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range c.Funcs {
		if f.Name != "probed" {
			continue
		}
		found = true
		if len(f.Reasons) != 0 {
			t.Errorf("probed keeps the text body: %v", f.Reasons)
		}
	}
	if !found {
		t.Errorf("the census holds no function probed: %v", c.Funcs)
	}
}
