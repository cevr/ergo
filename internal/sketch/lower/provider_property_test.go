package lower

import (
	"flag"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower/provgen"
	"ergo.local/prototype/internal/sketch/syntax"
)

var provSeed = flag.Uint64("providers.seed", 0, "run TestKnownProvidersAgree on this seed alone")

// adapterParam is a Sink parameter (s, or both's a) of an adapter's type
// in a Go function's parameter list, after the environment.
var adapterParam = regexp.MustCompile(`(\w\(|, )[sa] \*ops[A-Za-z]+AsSink[,)]`)

// TestKnownProvidersAgree is the seeded property of the copies for known
// providers (provider.go): each seed (lower/provgen) writes a program of functions on an
// operation type Sink that call its operations, loop, fail, pass the
// Sink on, and in some keep it an interface (a store in a record, a let,
// a generic argument); main calls them on three providers (by value),
// calls the generic keep at drawn type arguments (a provider's copy is
// its first call's instance; mutant q4-spec-instance-ignored reuses it at
// another), calls a function of two Sinks, and on even
// seeds ends in a defect raised inside a provider's operation. The
// lowering makes exactly the copies the generator's model names
// (knownStats.Providers), the reference (providerLowering "dynamic")
// none, and the two programs print the same and exit alike. A failure
// prints the seed, the Ergo source and both outputs.
// Replay: go test -run '^TestKnownProvidersAgree$' ./internal/sketch/lower -args -providers.seed=N
//
// It builds Go because the question is what the built copies do at run
// time (operation results, failures, the defect and its report); no
// in-process check observes it, and Ergo has no reference interpreter yet
// (test-speed T8).
func TestKnownProvidersAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *provSeed != 0 {
		seeds = []uint64{*provSeed}
	}
	reached := map[string]bool{}
	var programs []*lowered
	sources := map[uint64]string{}
	defects := map[uint64]bool{}
	for _, seed := range seeds {
		src, copies, defect := provgen.Program(seed, reached)
		sources[seed], defects[seed] = src, defect
		for _, mode := range []string{"", "dynamic"} {
			var made int
			saved, savedHook := providerLowering, statsHook
			providerLowering = mode
			statsHook = func(s knownStats) { made = s.Providers }
			p := mustLower(t, fmt.Sprintf("providers%d%s", seed, mode), src)
			providerLowering, statsHook = saved, savedHook
			want := copies
			if mode == "dynamic" {
				want = 0
			}
			if made != want {
				t.Errorf("seed %d, lowering %q: %d copies for known providers, want %d\n%s", seed, mode, made, want, src)
			}
			// The promised form: each copy's Sink parameter is its
			// adapter, a Go pointer, so its operation calls are static.
			var concrete int
			for _, f := range p.files {
				concrete += len(adapterParam.FindAll(f.Data, -1))
			}
			if concrete != want {
				t.Errorf("seed %d, lowering %q: %d Sink parameters of an adapter type, want one per copy (%d)", seed, mode, concrete, want)
			}
			programs = append(programs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the provider programs do not build (seeds %v)", seeds)
	}
	for _, seed := range seeds {
		on, onErr, onCode := runExe(t, exes[fmt.Sprintf("providers%d", seed)])
		off, offErr, offCode := runExe(t, exes[fmt.Sprintf("providers%ddynamic", seed)])
		ended := (onCode != 0) == defects[seed] && strings.Contains(on, "both ")
		if on != off || !sameReport(onErr, offErr) || onCode != offCode || !ended {
			t.Errorf("seed %d: the copies and the reference differ (or the program did not end as written)\nReplay: go test -run '^TestKnownProvidersAgree$' ./internal/sketch/lower -args -providers.seed=%d\n%s\n--- copies (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", seed, seed, sources[seed], onCode, on, onErr, offCode, off, offErr)
		}
	}
	if *provSeed == 0 {
		var missing []string
		for _, k := range []string{"take", "pass on", "pass on: spilled", "provider spilled", "kept: store", "kept: let", "kept: generic", "provider Counter", "provider Log", "provider Fixed", "call kept", "defect", "keep: other instance", "capture: literal", "capture: nested", "capture: std callback", "capture: task", "capture: pass on", "capture: returned", "capture: known callback"} {
			if !reached[k] {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			t.Errorf("the fixed seeds do not reach %v", missing)
		}
	}
}

// TestKnownProviderCopiesTakeTheIR is the in-process half of the copies'
// IR leg (PORT-INSTCOPY; ir_known.go provArg): on the IR path every copy
// the generator's model names (TestKnownProvidersAgree's programs) is an
// IR body, and no body keeps its source body for "argument for a known
// provider's copy", including a call whose provider a later operand's
// statements spilled to a temporary (the shapes "pass on: spilled" and
// "provider spilled"): the temporary holds the adapter's pointer, as the
// emitter's does. TestIRProvidersMatchSource (irspike) runs them.
//
// Mutant (testdata/mutants.json): ir-provider-spilled-arg-refused.
func TestKnownProviderCopiesTakeTheIR(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *provSeed != 0 {
		seeds = []uint64{*provSeed}
	}
	const reason = "argument for a known provider's copy"
	for _, seed := range seeds {
		src, copies, _ := provgen.Program(seed, map[string]bool{})
		replay := fmt.Sprintf("Replay: go test -run '^TestKnownProviderCopiesTakeTheIR$' ./internal/sketch/lower -args -providers.seed=%d", seed)
		o := mustLower(t, fmt.Sprintf("providers%d", seed), src)
		u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
		_, rep, err := LowerIR(u)
		if err != nil {
			t.Fatalf("seed %d: LowerIR: %v\n%s", seed, err, replay)
		}
		if n := rep.Fallback[reason]; n != 0 {
			t.Errorf("seed %d: %d bodies keep their source body for %q\n%s\n%s", seed, n, reason, replay, src)
		}
		built := 0
		for _, f := range rep.IR {
			if strings.HasSuffix(f, "AsSink") {
				built++
			}
		}
		if built != copies {
			t.Errorf("seed %d: %d copies for a known provider are IR bodies, the model names %d (fallbacks %v)\n%s\n%s", seed, built, copies, rep.Fallback, replay, src)
		}
	}
}
