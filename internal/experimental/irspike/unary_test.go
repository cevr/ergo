package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestIRUnaryOperandsMatchSource is the seeded property over unary
// operators with binary operands (MISCOMPILE-1, GenerateUnary): each
// program prints the same under both lowerings. A failing seed prints
// the Ergo source, both outcomes and its replay command. Mutant M7 (the
// derivative's un case without the parenthesized operand) fails it.
//
// It builds Go because the defect is in the patched compile's writer
// (staticBool reads the operand's syntax), after the IR text is checked:
// only a compiled and run program shows the branch it dropped.
func TestIRUnaryOperandsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		text := GenerateUnary(seed)
		checkBoth(t, src, ir, text, func() string {
			return "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRUnaryOperandsMatchSource ./internal/experimental/irspike"
		})
	}
}

// TestIRReprosMatchSource runs the smallest repros of fixed miscompiles
// under both lowerings, each with the function it needs an IR body of:
//   - notand (MISCOMPILE-1): `if !(p && false)` was dropped as never
//     taken (derivative mutant M7);
//   - rowslot (FLIP OPC, downloads' actor.ExitOf): a Slot row's Go type
//     argument was dropped at the call, and a caught error stored in a
//     field of the row's Go type parameter was not asserted to it, so the
//     IR build did not compile (mutants opc-goarg-slot-row-dropped,
//     opc-intoslot-unasserted);
//   - byteindex (red #31, NP4f-15's http query.ergo): the decoder refused
//     a string or slice indexed by a byte, and a slice expression's byte
//     bound (two derivative edits of the fork patch, which the irspike
//     ledger's note names: red #31);
//   - gocalls (IR-GOCALLS): Go calls over go.Any, with a go.Ref result
//     and with two results, and go.New, go.Zero, go.Is and go.As kept
//     their source bodies; the IR's form of each must print what the
//     source body prints (mutant irgc-any-elems-unconverted, whose IR the
//     decoder refuses).
//     IR-GOCALLS-2 adds kept (a Go result copied, mutant
//     irgc2-result-copy-dropped) and eof (Go package variables).
//   - gonil (IR-GOCALLS-4): a go.Ref result Go returns as nil (a user's
//     extern go of flag.Lookup) is the same GoNilDefect on both paths
//     (mutant irgc-nil-check-dropped).
//   - sleeper (PORT-CALLS-2): a std body's hidden scheduler, the
//     runtime's v1.Sleeper, from the program's Scheduler interface
//     (timeout.PopWithin; mutant pc2-scheduler-unasserted);
//   - substr (PORT-SLICE): a substring of a string and of a named type over
//     one, its bounds checked (mutant ps-string-slice-refused);
//   - update (PORT-UPDATE): a record update of a computed base, the base
//     run once before the field values (mutant pu-computed-base-refused);
//   - appender (SB-FLOOR): a []byte appender called on its receiver, its
//     operand clipped unless the call writes it in place (the value rule,
//     V3; mutant sb-std-value-rule-dropped).
//   - mapkey (FLIP F7 PORT-TEMP, pipeline's failing): a Map key or a
//     slice index whose calls run statements runs them after the map or
//     slice and before the value's, and the element is fixed before the
//     value's (mutants port-temp-key-after-value, port-temp-elem-unbound);
//   - fused-arm-scopes (jobq): a fused match inside range-over-function
//     keeps its scopes in source order (mutant fused-arm-ends-at-match);
//   - owningwith (IR-FALLBACK-REMAINDER r3): withs that own resources
//     release what they built once, last built first, at the block's end
//     or on a failure that leaves it; the output is the release order
//     (mutant ir-owning-with-refused);
//   - slotmatch (IR-FALLBACK-REMAINDER r4): a match on a call takes the
//     callee's slot variant, whose returns write the slots, for each
//     payload shape (mutant ir-slot-site-refused);
//   - subunionmatch (IR-FALLBACK-REMAINDER r5): a match whose arm tests
//     a declared sub-union expands its variants in the IR decision tree,
//     for flat and interface unions, overlapping arms, and tuples
//     (mutant ir-subunion-match-refused);
//   - valuecapture (IR-FALLBACK-REMAINDER r5): escaping function literals
//     and trailing blocks snapshot captured let mut locals by value on
//     the IR (mutant ir-value-capture-refused);
//   - opswitness (IR-FALLBACK-REMAINDER r5): operations conversions whose
//     table functions take witnesses populate the adapter's witness fields
//     on the IR (mutant ir-ops-conv-witness-refused);
//   - wiretuple (IR-FALLBACK-REMAINDER r6): tuple codec writers lower on
//     the IR for both the reference Writer and cell writers (mutant
//     ir-wire-tuple-writer-refused);
//   - wireownwrite (IR-FALLBACK-REMAINDER r6): codec writers for types
//     with WireWrite hooks dispatch through the adapter type switch on
//     the IR reference Writer (mutant ir-wire-own-write-refused);
//   - wiregeneric (IR-FALLBACK-REMAINDER r6): generic codec writers
//     ([]T, Option[T], Map[string, T], (string, T), []Option[T]) pass and
//     call codec witnesses on the IR (mutant ir-wire-generic-writer-refused);
//   - taskwait: a task with no success value still returns a Go pair.
//     A closed-row wait discards both results; try and a tail return
//     preserve the typed failure (mutant ir-bound-unit-result-pair-lost).
//
// It builds Go because only the compiler writer marks the scopes, and
// only executing both binaries reveals a miscompile.
func TestIRReprosMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	for _, r := range []struct {
		name       string
		fns        []string
		wantStdout string // when nonempty, require a successful run with this output
	}{
		{name: "notand", fns: []string{"pick"}},
		{name: "rowslot", fns: []string{"held", "run"}},
		{name: "byteindex", fns: []string{"hex", "pick", "tail"}},
		{name: "gocalls", fns: []string{"show", "pattern", "host", "stored", "split", "zone", "classify", "kept", "eof", "params", "cookie", "limited"}},
		{name: "gonil", fns: []string{"name"}},
		{name: "sleeper", fns: []string{"pop"}},
		{name: "substr", fns: []string{"tail", "cut", "head"}},
		{name: "update", fns: []string{"bump", "Store.moved"}},
		{name: "appender", fns: []string{"shared"}},
		{name: "mapkey", fns: mapKeyIRBodies()},
		{name: "fused-arm-scopes", fns: []string{"work"}},
		{name: "owningwith", fns: []string{"inline", "midway", "lent", "nested", "valued", "tail"}, wantStdout: "inline log db\nclose db\nclose log\nend inline\nmidway log db\nclose db\nclose log\nmidway failed\nlent db plain\nend lent\nclose db\nnested log db\nclose log\nafter inner\nclose db\nend nested\nvalued db\nclose db\nvalued 7\nvalued db\nclose db\nvalued -1\ntail log db\nclose db\nclose log\ntail failed\n"},
		{name: "slotmatch", fns: []string{"describe", "attempt", "ergo__leafSlot", "ergo__pickSlot", "ergo__tryPickSlot"}},
		{name: "subunionmatch", fns: []string{"settle", "noBind", "firstWins", "nodeLeaf", "nodeBranch", "ergo__nodeBranchSlot", "describeNode"}},
		{name: "valuecapture", fns: []string{"makeAdder", "makeLookup", "nestedCapture", "trailingCapture"}},
		{name: "opswitness", fns: []string{"makeScaled", "makeDynScaled", "makeNested", "withScaled"}},
		{name: "wiretuple", fns: []string{"writePair", "writeNested"}},
		{name: "wireownwrite", fns: []string{"writeRaw", "writeAttr"}},
		{name: "wiregeneric", fns: []string{"writeSlice", "writeOption", "writeMap", "writePair", "writeNested"}},
		{name: "stdclose", fns: []string{"size", "chunks", "io__File"}, wantStdout: "read true same true missing -2\n"},
		{name: "taskwait", fns: []string{"wait", "tryWait", "tailWait"}, wantStdout: "try success: true; try rejected: true; tail rejected: true\n"},
	} {
		t.Run(r.name, func(t *testing.T) {
			text, err := os.ReadFile(filepath.Join("testdata", r.name+".ergo"))
			if err != nil {
				t.Fatal(err)
			}
			rep, got := compareBoth(t, src, ir, string(text), func() string {
				return "replay: go test -run 'TestIRReprosMatchSource/" + r.name + "' ./internal/experimental/irspike"
			})
			if want := (outcome{stdout: r.wantStdout}); r.wantStdout != "" && got != want {
				t.Fatalf("%s: both paths gave %+v; want %+v", r.name, got, want)
			}
			for _, fn := range r.fns {
				if !slices.Contains(rep, fn) {
					t.Fatalf("%s: %s has no IR body (IR bodies: %v)", r.name, fn, rep)
				}
			}
		})
	}
}

// checkBoth builds the program text by both paths, runs both and fails
// when their outcomes differ; it returns the functions with IR bodies.
func checkBoth(t *testing.T, src, ir Toolchain, text string, replay func() string) []string {
	t.Helper()
	bodies, _ := compareBoth(t, src, ir, text, replay)
	return bodies
}

func compareBoth(t *testing.T, src, ir Toolchain, text string, replay func() string) ([]string, outcome) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatalf("the program does not check: %v\n%s", err, text)
	}
	sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
	a, b := run(t, sb), run(t, ib)
	if a != b {
		t.Fatalf("the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", a, b, replay(), text)
	}
	return rep.IR, a
}
