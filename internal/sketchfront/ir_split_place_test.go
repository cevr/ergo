package sketchfront

import (
	"encoding/json"
	"path"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRSplitAddressesPlaces is the smallest repro of the 20e3b4fbe
// fallout (TAB-IR r2 in the LAND FIRST sweep): the IR split gave an IR
// body a variable of another package that ergoInit sets (declared with
// no value) through a getter that returned its value, so &tabExplainK[i],
// the table read of lzpack's lz.Explain in main, took the address of a
// call's result, and the patched compile refused it ("ergoir: address of
// a non-place"; about 25 examples). The getter returns the variable's
// address and the body reads *get(), a place. Every "addr" of every IR
// body of the split reaches its root through indexes and fields, and the
// root is no call; and lzpack reads another package's variable through
// a getter under an "addr" (the check is not vacuous). Mutant:
// tab-ir-split-getter-value. It checks the lowered files in process: the
// in-process LowerIR of irspike's properties does not split.
func TestIRSplitAddressesPlaces(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples", "lzpack", "cmd", "lzpack"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := CheckDir(dir)
	if err != nil || len(a.Diagnostics) > 0 {
		t.Fatalf("lzpack does not check: %v %v", err, a.Diagnostics)
	}
	unit, err := analysisUnit(a, filepath.Join(dir, "main.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	files, _, rep, err := lower.LowerIRSplit(unit, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reason != "" {
		t.Fatalf("lzpack: the IR split refuses: %s", rep.Reason)
	}
	throughGetter := 0
	for _, f := range files {
		if path.Base(f.Path) != lower.IRFileName {
			continue
		}
		var v any
		if err := json.Unmarshal(f.Data, &v); err != nil {
			t.Fatalf("%s: %v", f.Path, err)
		}
		irAddrs(v, func(x map[string]any) {
			root := x["x"]
			for {
				m, ok := root.(map[string]any)
				if !ok {
					return
				}
				switch m["k"] {
				case "index", "field":
					root = m["x"]
					continue
				case "call", "mcall", "pcall":
					t.Errorf("%s: the address of a call's result (%v), which is no place", f.Path, m["name"])
				case "deref":
					if c, ok := m["x"].(map[string]any); ok && c["k"] == "call" {
						throughGetter++
					}
				}
				return
			}
		})
	}
	if throughGetter == 0 {
		t.Fatal("lzpack: no IR body takes the address of another package's variable through its getter: the check is vacuous")
	}
}

// irAddrs calls f on every "addr" expression of a decoded IR file.
func irAddrs(v any, f func(map[string]any)) {
	switch v := v.(type) {
	case map[string]any:
		if v["k"] == "addr" {
			f(v)
		}
		for _, e := range v {
			irAddrs(e, f)
		}
	case []any:
		for _, e := range v {
			irAddrs(e, f)
		}
	}
}
