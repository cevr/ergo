package irspike

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// formGenerators are the generators whose programs the forms properties
// lower: every irspike generator of one program text.
var formGenerators = map[string]func(uint64) string{
	"Generate": Generate, "Adapt": GenerateAdapt, "AdaptTask": GenerateAdaptTask,
	"Closure": GenerateClosure, "Codec": GenerateCodec, "Index": GenerateIndex,
	"LetDefault": GenerateLetDefault, "Update": GenerateUpdate, "Field": GenerateField,
	"Order": GenerateOrder, "Fail": GenerateFail, "Generic": GenerateGeneric,
	"Laws": GenerateLaws, "KnownRecord": GenerateKnownRecord, "Machine": GenerateMachine,
	"Operand": GenerateOperand, "Own": GenerateOwn, "OwnedParams": GenerateOwnedParams,
	"Place": GeneratePlace, "Std": GenerateStd, "Tests": GenerateTests,
	"Tuple": GenerateTuple, "Unary": GenerateUnary, "Union": GenerateUnion,
	"View": GenerateView, "With": GenerateWith,
	"Defect": func(s uint64) string { text, _ := GenerateDefect(s); return text },
	"Equal":  func(s uint64) string { text, _ := GenerateEqual(s); return text },
}

// formCase is one program of the forms properties: an example target, or
// a generator's seed.
type formCase struct {
	example *exampleTarget
	gen     string
	seed    uint64
}

func (c formCase) key() string {
	if c.example != nil {
		return c.example.key()
	}
	return fmt.Sprintf("%s seed %d", c.gen, c.seed)
}

// genCases are seeds first … first+count-1 of every generator.
func genCases() []formCase {
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	var out []formCase
	for _, g := range slices.Sorted(maps.Keys(formGenerators)) {
		for seed := first; seed < first+count; seed++ {
			out = append(out, formCase{gen: g, seed: seed})
		}
	}
	return out
}

// formRun is one program lowered by the IR path with forms and by the
// reference (lower.LowerIRReference), its test build for a test target:
// what differs, the form kinds the bodies held, and a form kind found in
// an IR file.
type formRun struct {
	differs, inFile string
	forms           map[string]int
}

// lowerBoth lowers c both ways. A program that does not check is no case:
// the agreement properties own it.
func lowerBoth(t *testing.T, c formCase) (formRun, bool) {
	t.Helper()
	path, tests := "", false
	if c.example != nil {
		path, tests = c.example.dir, c.example.test
	} else {
		path, tests = filepath.Join(t.TempDir(), "p.ergo"), c.gen == "Tests"
		if err := os.WriteFile(path, []byte(formGenerators[c.gen](c.seed)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var r formRun
	u, err := Load(path)
	if err != nil {
		return r, false
	}
	var got, want []lower.File
	var rep lower.IRReport
	if tests {
		got, rep, err = lower.LowerIRTests(u)
	} else {
		got, rep, err = lower.LowerIR(u)
	}
	if err != nil {
		r.differs = "the IR path with forms fails: " + err.Error()
		return r, true
	}
	want, _, err = lower.LowerIRReference(u, tests)
	if err != nil {
		r.differs = "the reference fails: " + err.Error()
		return r, true
	}
	r.forms = rep.Forms
	if len(got) != len(want) {
		r.differs = fmt.Sprintf("%d files with forms, %d in the reference", len(got), len(want))
	}
	for i := range min(len(got), len(want)) {
		if got[i].Path != want[i].Path || !bytes.Equal(got[i].Data, want[i].Data) {
			r.differs = fmt.Sprintf("%s differs from the reference's %s at byte %d", got[i].Path, want[i].Path, firstDiff(got[i].Data, want[i].Data))
			break
		}
	}
	for _, f := range got {
		if filepath.Base(f.Path) == lower.IRFileName {
			r.inFile = formKindIn(f.Data)
		}
	}
	return r, true
}

// firstDiff is the first byte at which a and b differ.
func firstDiff(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// formKindIn is a form kind an IR file's statements or expressions hold,
// or "".
func formKindIn(data []byte) string {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return "an IR file that does not decode"
	}
	found := ""
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if k, _ := v["k"].(string); k == "match" || k == "variant" {
				found = k
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(v)
	return found
}

// irForms is TestFormsEraseToTheReference, split by program
// (TestFormsEraseToTheReferencePart0 …): the IR forms (IR-FORMS IRF-0,
// lower/ir_forms.go) change nothing once erased. Every example binary and
// test build and four seeds of every irspike generator lower by the IR
// path with forms and by the reference, which builds none
// (lower.LowerIRReference); every file must be equal, byte for byte, and
// no IR file may hold a form. In process (no Go is built); split for its
// length, so the full tier runs it. Mutants: irf-variant-tag-swapped,
// irf-match-default-dropped.
var irForms = newSplit(split[formCase]{
	name:  "TestFormsEraseToTheReference",
	parts: formParts,
	cases: func(t *testing.T) []formCase {
		var out []formCase
		for _, e := range exampleDomain(t) {
			out = append(out, formCase{example: &e})
		}
		return append(out, genCases()...)
	},
	key: formCase.key,
	run: func(t *testing.T, c formCase) {
		r, ok := lowerBoth(t, c)
		if !ok {
			return
		}
		if r.differs != "" {
			t.Errorf("%s: %s", c.key(), r.differs)
		}
		if r.inFile != "" {
			t.Errorf("%s: the IR file holds the form %q", c.key(), r.inFile)
		}
	},
})

// TestIRFileHoldsNoForm: on four seeds of every irspike generator, the
// files with forms equal the reference's (TestFormsEraseToTheReference's
// rule) and no IR file holds a form kind ("match", "variant"): eraseForms leaves none, and
// lowerIRModule refuses a file that would (irNoForms). Over the run the
// bodies held a match form of each layout (a sealed interface, a flat
// union, a scalar-form local) and a variant form of each (an interface, a
// flat union), so the erasure had forms to erase. In process; the fast
// tier's share of TestFormsEraseToTheReference. A failing seed prints its
// replay. Mutants: irf-erase-skips-kind, irf-variant-tag-swapped,
// irf-match-default-dropped.
func TestIRFileHoldsNoForm(t *testing.T) {
	seen := map[string]int{}
	for _, c := range genCases() {
		r, ok := lowerBoth(t, c)
		if !ok {
			continue
		}
		replay := fmt.Sprintf("ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run TestIRFileHoldsNoForm ./internal/experimental/irspike", c.seed)
		if r.inFile != "" {
			t.Errorf("%s: the IR file holds the form %q\nreplay: %s", c.key(), r.inFile, replay)
		}
		if r.differs != "" {
			t.Errorf("%s: %s\nreplay: %s", c.key(), r.differs, replay)
		}
		for k, n := range r.forms {
			seen[k] += n
		}
	}
	for _, k := range []string{"match/iface", "match/flat", "match/scalar", "variant/iface", "variant/flat"} {
		if seen[k] == 0 {
			t.Errorf("the run built no %s form (built %v): the property is vacuous there", k, seen)
		}
	}
}
