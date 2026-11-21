package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/linked/code"
	"ergo.local/prototype/internal/sketchfront"
)

// TestCodePlanOnTheProduct runs `ergo code plan` and `apply` (SEM4b) on a
// copy of testdata/code/plan, in process: the product Checker answers
// through codeBridge and the plan is written by code.HostFiles.
//
//   - Renaming the operation Greeter.Greet renames the function of the
//     type that provides it and the call through the capability, and is
//     Ready with the proof alpha; renaming that function renames the
//     operation with it.
//   - A parameter (to an exported name: it stays unexported), a local, a
//     variant, a law and a test (whose handles end #law and #test) are
//     renamed with the proof alpha (the Rename rows).
//   - Replacing Greeting with its own text spaced otherwise is Ready with
//     the proof same code and changes no file; replacing it with another
//     body is Ready with the proof checked.
//   - Apply writes the rename, the program then checks, and a second
//     apply of the plan is refused as stale.
//
// Mutants (cmd/ergo/testdata/mutants.json): the Provides edges dropped
// (a rename leaves the provider behind), Code dropped (no same-code
// proof), an extent that ends before its newline (the replace leaves a
// blank line), and the rename family's lookup of a provider function
// named once (internal/linked/code: the provider keeps its name).
func TestCodePlanOnTheProduct(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile("testdata/code/plan/main.ergo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.ergo"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := loadCodeWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(w.sources)
	if err != nil {
		t.Fatal(err)
	}
	sources := string(data)
	plan := func(change string) []string {
		t.Helper()
		out, err := code.Plan(context.Background(), &codeBridge{w: w}, sources, change, false)
		if err != nil || len(out) != 4 {
			t.Fatalf("plan %s: %v %q", change, err, out)
		}
		return out
	}
	op := plan(`rename main.Greeter.Greet to "Hail"`)
	for _, want := range []string{"verdict ready, proof alpha", "+\tHail(name string) string,", "+func Hail(_ Plain, name string) string", "Greeter.Hail(\"you\")"} {
		if !strings.Contains(op[1], want) {
			t.Errorf("the rename of the operation has no %q:\n%s%s", want, op[1], op[2])
		}
	}
	fn := plan(`rename main.Greet to "Hail"`)
	if !strings.Contains(fn[1], "verdict ready, proof alpha") || !strings.Contains(fn[1], "+\tHail(name string) string,") {
		t.Errorf("the rename of the provider's function does not take its operation:\n%s%s", fn[1], fn[2])
	}
	for _, row := range []struct{ change, want string }{
		{`rename main.Area.s to "Shape2"`, "+func Area(Shape2 Shape) int {"},
		{`rename main.Area.rr to "sq"`, "+\t\t3 * sq"},
		{`rename main.Circle to "Round"`, "+type Shape = Round{R int} | Square{Side int}"},
		{`rename main.AreaIsNotNegative#law to "AreaIsPositive"`, "+law AreaIsPositive(side int)"},
		{`rename main.AUnitSquare#test to "OneSquare"`, "+test OneSquare {"},
	} {
		out := plan(row.change)
		if !strings.Contains(out[1], "verdict ready, proof alpha") || !strings.Contains(out[1], row.want) {
			t.Errorf("%s is not an alpha rename that writes %q:\n%s%s", row.change, row.want, out[1], out[2])
		}
	}
	same := plan(`replace main.Greeting "func  Greeting(name string) string {\n    \"dear \" + name\n}"`)
	if !strings.Contains(same[1], "verdict ready, proof same code") || !strings.Contains(same[1], "no file changes") {
		t.Errorf("a respaced replace is not same code with no file changes:\n%s%s", same[1], same[2])
	}
	other := plan(`replace main.Greeting "func Greeting(name string) string {\n    \"dearest \" + name\n}"`)
	if !strings.Contains(other[1], "verdict ready, proof checked") {
		t.Errorf("a replace that changes the code is not proved by a check:\n%s%s", other[1], other[2])
	}
	applied, err := code.Apply(context.Background(), dir, op[3])
	if err != nil || applied[0] != "0" {
		t.Fatalf("apply: %v %q", err, applied)
	}
	a, err := sketchfront.CheckDir(dir)
	if err != nil || len(a.Diagnostics) > 0 {
		t.Fatalf("the applied rename does not check: %v %v", err, a.Diagnostics)
	}
	again, err := code.Apply(context.Background(), dir, op[3])
	if err != nil || again[0] != "1" || !strings.Contains(again[2], "stale") {
		t.Fatalf("a second apply is not stale: %v %q", err, again)
	}
}
