package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// TestIRStaticExits checks the facts STATIC-EXIT and STATIC-EXIT2
// promise, in process on LowerIR's IR: each body of GenerateFrame
// registers its actions on a v1.Exits, not on a frame scope, in the form
// exitForms gives (the slots for a loop's registrations, else a Go defer
// of Run, or of Quiet for a block with an empty footprint). That the forms
// run the same actions in the same order, on a return, a typed failure
// and a defect, is TestIRFramesMatchSource: its source build is the
// general form.
//
// Mutants: f7g-exits-never (staticExits holds no frame, so every frame
// keeps the general scope), f7g-exits-no-defer (deferExits keeps the
// slots), f7g-exits-never-quiet (no block is quiet).
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
		forms := map[string][]string{}
		for _, fn := range ir.Funcs {
			kinds := map[string]bool{}
			callKinds(fn.Body, kinds)
			if kinds["pcall OnExit"] {
				forms[fn.Name] = []string{"OnExit"}
				continue
			}
			for _, k := range []string{"ExitsOnExit", "Run", "Quiet"} {
				if kinds["pcall "+k] || kinds["defer "+k] {
					forms[fn.Name] = append(forms[fn.Name], k)
				}
			}
		}
		for _, f := range frameFuncs {
			if got, want := strings.Join(forms[f], " "), strings.Join(exitForms()[f], " "); got != want {
				t.Errorf("seed %d: %s registers its actions with %q, want %q\n%s", seed, f, got, want, text)
			}
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
