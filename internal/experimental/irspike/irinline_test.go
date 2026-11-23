package irspike

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRKnowsLRUGet lowers examples/lru/cmd/lru on the IR, in process, and
// requires that a match on an inlined call build no union. Churn's
// `match c.Get(k)` is the one site of the shape: Get is an inlined body
// with an environment parameter (its arena operations), so its call is
// hoisted into a temporary the match's subject then reads, and only
// coalesce joins the two, after which the known-variant pass sees the
// copy write the subject (ir_forms_inline.go). GenerateInline's helpers
// take no environment, and a helper given one by a `needs` row or a
// function-value call is not inlined, so no generated case holds this
// shape yet (IR-GETMATCH's ordering guard).
func TestIRKnowsLRUGet(t *testing.T) {
	if !lower.IRInlineOn() || !lower.IRKnownOn() {
		t.Skip("the inliner or the known-variant pass is off")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	u, err := Load(filepath.Join(root, "examples", "lru", "cmd", "lru"))
	if err != nil {
		t.Fatal(err)
	}
	_, rep, err := lower.LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if rep.KnownVariants == 0 {
		t.Fatalf("examples/lru: no match on an inlined call built its union in place (inlined %d calls; fallback %v)", rep.Inlined, rep.Fallback)
	}
}

// TestIRInlineMatchesSource is RED17's property over the IR inliner
// (GenerateInline): each program prints the same and exits the same under
// the source lowering (whose OPT4a inlines the same calls) and the IR,
// and a defect's report, the Ergo frames above Go's own traceback, is the
// same line for line: an inlined body's frames come from the inline
// table. With the inliner on, the IR copies calls in every seed, and a
// match on an inlined call that returns an Option builds no Option
// (IR-GETMATCH, ir_forms_variant.go), and a discarded call of a body left as
// a call names its variant with no result (IR-DROP, ir_forms_drop.go). A
// failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because a copy that binds, renames or returns wrong, and a
// position the report reads wrong, show only in the compiled program.
func TestIRInlineMatchesSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateInline(uint64(seed))
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRInlineMatchesSource$' ./internal/experimental/irspike"
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
		a, b := runReport(t, sb), runReport(t, ib)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		if seed%2 == 1 && a.exit == 0 {
			t.Errorf("seed %d: the defect seed exits 0\n%s", seed, replay)
		}
		if lower.IRInlineOn() && rep.Inlined == 0 {
			t.Errorf("seed %d: the IR inlined no call (fallback %v)\n%s", seed, rep.Fallback, replay)
		}
		// The generator's three matches on an inlined Option helper build
		// no Option: two on look, and one on peek, whose tail match on a
		// Map's Get is lru's Cache.Get shape (TestIRKnowsLRUGet holds the
		// hoisted call that needs coalesce first).
		if lower.IRInlineOn() && lower.IRKnownOn() && rep.KnownVariants < 3 {
			t.Errorf("seed %d: only %d matches on an inlined call built their union in place\n%s", seed, rep.KnownVariants, replay)
		}
		// FAIL-INLINE: fetch's and the leaf parity's failures jump to
		// their arms; parity is copied only for that jump.
		if lower.IRInlineOn() && lower.IRFailJumpOn() && rep.FailJumps < 2 {
			t.Errorf("seed %d: only %d inlined failures jumped to their catch arm\n%s", seed, rep.FailJumps, replay)
		}
		if lower.IRInlineOn() && lower.IRFailJumpOn() && rep.FailLeaves == 0 {
			t.Errorf("seed %d: the fallible leaf parity was not copied for its failure's jump\n%s", seed, replay)
		}
		if lower.IRDropOn() && rep.Dropped == 0 {
			t.Errorf("seed %d: no discarded call named the variant with no result\n%s", seed, replay)
		}
		// The arena helpers (DROPPED-IR): the copies of Free and Put hold
		// sites in std bodies, take's value is zeroed only once Free's copy
		// wrote it, the Pair put in a freed slot is written field by field,
		// and the IR builds the variant with no result.
		for _, c := range []struct {
			on   bool
			n    int
			what string
		}{
			{lower.IRInlineOn(), rep.StdInlined, "call in a library body was inlined"},
			{lower.IRInlineOn() && lower.IRDeadZeroOn(), rep.DeadZeros, "dead zero value set was dropped"},
			{lower.IRInlineOn() && lower.IRStoreOn(), rep.StoreFields, "stored record literal was written field by field"},
			{lower.IRDropOn() && lower.IRDropBodyOn(), rep.DropBodies, "variant with no result was the IR's"},
		} {
			if c.on && c.n == 0 {
				t.Errorf("seed %d: no %s\n%s", seed, c.what, replay)
			}
		}
	}
}

// runReport is run with the whole Ergo part of stderr: the lines before
// Go's own traceback (its first "goroutine " line).
func runReport(t *testing.T, bin string) outcome {
	t.Helper()
	cmd := exec.Command(bin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	_, err := MeasureBounded(cmd, programMemory)
	report, _, _ := strings.Cut(errb.String(), "\ngoroutine ")
	o := outcome{stdout: out.String(), stderr: report}
	if ee, ok := err.(*exec.ExitError); ok {
		o.exit = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s: %v", bin, err)
	}
	return o
}
