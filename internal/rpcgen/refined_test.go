package rpcgen

import (
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketchfront"
)

// TestRefinedIntConvertsThroughItsBase is the smallest repro of FD1-g1
// (fk-domains, FD1a): the int walker of a refined int wrote int64(v),
// which the checker refuses ("cannot convert domains.Offset to int64 in
// one step"), so a contract with a refined int parameter generated a
// package that did not check. It catches rpcgen-refined-one-step, which
// TestRpcLoopbackAgrees cannot: its contracts draw no refined type. It
// runs in process: the question is whether the generated code checks.
func TestRefinedIntConvertsThroughItsBase(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ergo.mod"), "module mod\n")
	writeFile(t, filepath.Join(dir, "api", "api.ergo"), "type Digit int where 0 <= it && it <= 9\ntype API { Next(d Digit) int }\n")
	a, err := sketchfront.CheckDir(filepath.Join(dir, "api"))
	if err != nil || a.Root().Package == nil {
		t.Fatalf("the contract does not check: %v %v", err, a.Diagnostics)
	}
	out, err := Generate(a.Root().Package, "mod/api", "API")
	if err != nil {
		t.Fatal(err)
	}
	for name, src := range out.Files {
		writeFile(t, filepath.Join(dir, "api", out.Dir, name), src)
	}
	g, err := sketchfront.CheckDir(filepath.Join(dir, "api", out.Dir))
	if err != nil || len(g.Diagnostics) > 0 {
		t.Fatalf("the generated package does not check: %v %v\n%s", err, g.Diagnostics, out.Files["wire.ergo"])
	}
}
