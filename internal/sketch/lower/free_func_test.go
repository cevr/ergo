package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestRuntimeRecordFunctionIsFree (FD1-g2): a function whose first
// parameter is a record whose declaration names a runtime Go type
// (`extern go …/runtime/v1.RPCMalformed type Malformed { Reason string }`,
// as std rpc writes Malformed) lowers as a Go function, never as a method:
// Go takes a method only in its receiver's package, and that is runtime/v1.
// The smallest repro is a trusted module's own binding, so the rule reads
// the declaration and no std name. Mutant: runtime-record-method (the
// GoTypeOf check in methodReceiver dropped; Go refuses "cannot define new
// methods on non-local type").
func TestRuntimeRecordFunctionIsFree(t *testing.T) {
	const src = `extern go ergo.local/prototype/runtime/v1.RPCMalformed type Malformed { Reason string }

func describe(m Malformed) string { "malformed: " + m.Reason }

func main() needs Console {
	Console.Println(Malformed{Reason: "x"}.describe())
}
`
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "/ergo-test/freefn/main.ergo"
	pkg, ds := check.Check("main", []*syntax.File{f}, trustingImporter{})
	if len(ds) != 0 {
		t.Fatalf("checker diagnostics %v\n%s", ds, src)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{f}, Sources: []Source{{Path: f.Name, Text: src}}}
	o := mustLowerUnit(t, "freefn", u, false)
	for _, gf := range o.files {
		if text := string(gf.Data); strings.Contains(text, ") describe(") {
			t.Errorf("%s: describe lowered as a method on the runtime's type:\n%s", gf.Path, text)
		}
	}
	typecheckGo(t, []*lowered{o})
}
