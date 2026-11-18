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
		if lower.IRInlineOn() && lower.IRKnownOn() && rep.KnownVariants == 0 {
			t.Errorf("seed %d: no match on an inlined call built its union in place\n%s", seed, replay)
		}
		if lower.IRDropOn() && rep.Dropped == 0 {
			t.Errorf("seed %d: no discarded call named the variant with no result\n%s", seed, replay)
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
