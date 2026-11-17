package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRFramesMatchSource is the seeded property over bodies that open a
// frame scope (FLIP F7 GP-FRAME, GenerateFrame): exit actions run when the
// body returns, on every path and last registered first, the same under
// both lowerings, and their functions have IR bodies. A failing seed
// prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the frame's End is a deferred call that runs the
// actions; only the compiled program runs it.
//
// Mutant f7g-frame-no-end: the frame's End runs where the frame opens, so
// no action runs. Since STATIC-EXIT the IR's frames hold their actions on
// an Exits and the source's on a Scope, so the property holds the two
// forms equal, a defect in a body (crash) or an action (badExit)
// included: f7g-exits-order (the actions run first registered first) and
// f7g-exits-defect-lost (an action's defect is not joined into the exit).
// The property runs a second time with the IR in the general form too
// (exitLowering "scope"), where f7g-frame-no-end fails it.
func TestIRFramesMatchSource(t *testing.T) {
	f7Property(t, "TestIRFramesMatchSource", GenerateFrame, frameFuncs...)
	defer lower.SetExitLoweringForTest(lower.SetExitLoweringForTest("scope"))
	f7Property(t, "TestIRFramesMatchSource", GenerateFrame, frameFuncs...)
}

// TestIRStaticExits checks the fact STATIC-EXIT promises, in process on
// LowerIR's IR: each body of GenerateFrame registers its actions on a
// v1.Exits (ExitsOnExit), not on a frame scope. That the two forms run the
// same actions in the same order, on a return, a typed failure and a
// defect, is TestIRFramesMatchSource: its source build is the general
// form.
//
// Mutant f7g-exits-never: staticExits holds no frame, so every frame
// keeps the general scope.
func TestIRStaticExits(t *testing.T) {
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateFrame(seed)
		file := filepath.Join(t.TempDir(), "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
		}
		files, _, err := lower.LowerIR(u)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, text)
		}
		var ir struct {
			Funcs []struct {
				Name string `json:"name"`
				Body any    `json:"body"`
			} `json:"funcs"`
		}
		for _, f := range files {
			if f.Path == lower.IRFileName {
				if err := json.Unmarshal(f.Data, &ir); err != nil {
					t.Fatal(err)
				}
			}
		}
		static := map[string]bool{}
		for _, fn := range ir.Funcs {
			kinds := map[string]bool{}
			callKinds(fn.Body, kinds)
			static[fn.Name] = kinds["pcall ExitsOnExit"] && !kinds["pcall OnExit"]
		}
		for _, f := range frameFuncs {
			if !static[f] {
				t.Errorf("seed %d: %s does not register its actions on an Exits\n%s", seed, f, text)
			}
		}
	}
}

// callKinds adds to into "pcall NAME" for each package call under v, the
// IR's JSON.
func callKinds(v any, into map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		if k, _ := x["k"].(string); k == "pcall" {
			if n, ok := x["name"].(string); ok {
				into["pcall "+n] = true
			}
		}
		for _, e := range x {
			callKinds(e, into)
		}
	case []any:
		for _, e := range x {
			callKinds(e, into)
		}
	}
}

// TestIRFramesHaveBodies checks in process, on LowerIR's report, that the
// bodies of GenerateFrame that open a frame scope have IR bodies.
//
// Mutant f7g-frame-gated: irFrame admits only the static form, so a frame
// scope's body keeps its source (the code before GP-FRAME).
func TestIRFramesHaveBodies(t *testing.T) {
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateFrame(seed)
		file := filepath.Join(t.TempDir(), "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
		}
		_, rep, err := lower.LowerIR(u)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, text)
		}
		for _, f := range frameFuncs {
			if !slices.Contains(rep.IR, f) {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v)\n%s", seed, f, rep.IR, text)
			}
		}
	}
}
