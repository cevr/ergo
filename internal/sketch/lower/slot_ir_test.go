package lower

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestSlotSitesTakeTheIR is the in-process half of the IR leg of the slot
// variant (UNI2): a simple match on a call of a function whose union
// result it alone reads calls the callee's slot variant, as the text
// path's caller does, and each slot variant the IR caller asks for is an
// IR body: every return writes its slot and returns the tag (a variant's
// value, a union value taken apart, another slot variant passed on, plain
// and fallible). The variants hold a string, a Map, a slice, a record of
// several fields and nothing, the payload shapes of the census's slot
// variants. The fixture runs on both paths in
// TestIRReprosMatchSource/slotmatch (irspike).
//
// Mutant (testdata/mutants.json): ir-slot-site-refused.
func TestSlotSitesTakeTheIR(t *testing.T) {
	src, err := os.ReadFile("../../experimental/irspike/testdata/slotmatch.ergo")
	if err != nil {
		t.Fatal(err)
	}
	o := mustLower(t, "slotmatch", string(src))
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: string(src)}}}
	files, rep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Fallback) != 0 {
		t.Errorf("bodies keep their source body: %v", rep.Fallback)
	}
	for _, name := range []string{"describe", "attempt", "ergo__leafSlot", "ergo__pickSlot", "ergo__tryPickSlot"} {
		if !slices.Contains(rep.IR, name) {
			t.Errorf("%s has no IR body (IR %v)", name, rep.IR)
		}
	}
	// The callers call the slot variants: no box is made for the match.
	calls := map[string]string{"describe": "ergo__pickSlot", "attempt": "ergo__tryPickSlot", "ergo__pickSlot": "ergo__leafSlot", "ergo__tryPickSlot": "ergo__tryPickSlot"}
	for _, f := range files {
		if f.Path != IRFileName {
			continue
		}
		var ir irFile
		if err := json.Unmarshal(f.Data, &ir); err != nil {
			t.Fatal(err)
		}
		// A fallible caller's body may be its flat split's (ir_forms_flat.go).
		for _, fn := range ir.Funcs {
			caller := strings.TrimSuffix(fn.Name, "Flat")
			want, ok := calls[caller]
			if !ok {
				continue
			}
			b, _ := json.Marshal(fn.Body)
			if strings.Contains(string(b), `"name":"`+want+`"`) {
				delete(calls, caller)
			}
		}
	}
	for caller, want := range calls {
		t.Errorf("%s does not call %s", caller, want)
	}
}
