package irspike

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestIROwnedMatchSource is the FLIP F7 FORK agreement property for owned
// values in a static frame (GenerateOwn): for each seed the IR path and
// the source path print the same and exit the same, a defect in a Close
// included. Every owN, give and keep gets an IR body, so the lets that own
// a value, the hand-offs to a taking call, a returned value kept until the
// exit, the early returns and the deferred release (last acquired first,
// after a defect too) all run from the IR. A failing seed prints the Ergo
// source, both outcomes and its replay command. Mutant f7f-release-order
// (the release helper releases first acquired first) fails it.
//
// It builds Go for the reason needsBuild gives: the question is what the
// built program releases, in which order, on which exit, which only a run
// shows.
func TestIROwnedMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	inBody := 0
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateOwn(seed)
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIROwnedMatchSource ./internal/experimental/irspike"
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		var want []string
		for _, f := range ownFuncs {
			// A function no owN calls is not lowered: its declaration is
			// the one use of its name.
			// Res's checked Close runs at each release: no call names it.
			if f == "Res.Close" || strings.Count(text, f[strings.LastIndex(f, ".")+1:]+"(") > 1 {
				want = append(want, f)
			}
		}
		for i := 0; strings.Contains(text, fmt.Sprintf("func ow%d(", i)); i++ {
			want = append(want, fmt.Sprintf("ow%d", i))
		}
		var missing []string
		for _, f := range want {
			if !slices.Contains(rep.IR, f) {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed %d: no IR body for %v (IR %v, fallback %v)\n%s\n%s", seed, missing, rep.IR, rep.Fallback, replay, text)
		}
		// A release inside a body calls its slot's helper (ir_own.go
		// slotRelease), named <f>Free.
		if data, err := os.ReadFile(filepath.Join(dir, "ir", "ergoir.json")); err == nil && strings.Contains(string(data), `Free"`) {
			inBody++
		}
		// The frames' release helpers are IR functions (RELEASE-IR): their
		// Go text is a header with no body.
		gofiles, _ := filepath.Glob(filepath.Join(dir, "ir", "*.go"))
		var gotext strings.Builder
		for _, f := range gofiles {
			data, _ := os.ReadFile(f)
			gotext.Write(data)
		}
		if g := gotext.String(); !strings.Contains(g, "its body is IR (FLIP F7, RELEASE-IR)") || strings.Contains(g, "static frame (IR, FLIP F7).") {
			t.Fatalf("seed %d: a static frame's release helper is Go text, not IR\n%s\n%s", seed, replay, text)
		}
	}
	if count >= 6 && inBody == 0 {
		t.Fatalf("no seed of %d..%d releases a value inside a body", first, first+count-1)
	}
}

// TestIROwnedParamsMatchSource runs GenerateOwnedParams's programs by
// both paths (FLIP F7 FORK unit 3): the outputs agree, and every bumpN
// has an IR body under its owned body's name (values.go V5), its own name
// being the wrapper that copies.
func TestIROwnedParamsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateOwnedParams(seed)
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIROwnedParamsMatchSource ./internal/experimental/irspike"
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		for i := 0; strings.Contains(text, fmt.Sprintf("func bump%d(", i)); i++ {
			want := fmt.Sprintf("bump%dOwned", i)
			if !slices.ContainsFunc(rep.IR, func(n string) bool { return strings.HasPrefix(n, want) }) {
				t.Fatalf("seed %d: no IR body %s (IR %v, fallback %v)\n%s\n%s", seed, want, rep.IR, rep.Fallback, replay, text)
			}
		}
		// Each wrapper that copies is an IR function (OWNED-WRAP-IR): its
		// Go text is a header with no body.
		gofiles, _ := filepath.Glob(filepath.Join(dir, "ir", "*.go"))
		var gotext strings.Builder
		for _, f := range gofiles {
			data, _ := os.ReadFile(f)
			gotext.Write(data)
		}
		for i := 0; strings.Contains(text, fmt.Sprintf("func bump%d(", i)); i++ {
			if want := fmt.Sprintf("// bump%d copies its owned arguments into bump%dOwned", i, i); !strings.Contains(gotext.String(), want) {
				t.Fatalf("seed %d: the wrapper bump%d is not an IR function\n%s\n%s", seed, i, replay, text)
			}
		}
	}
}
