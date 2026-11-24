package lower

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// inforceIRSeeds are TestInForceContextsAgree's seeds, or its one replayed
// seed.
func inforceIRSeeds() []uint64 {
	if *inforceSeed != 0 {
		return []uint64{*inforceSeed}
	}
	return []uint64{1, 2, 3, 4, 5, 6}
}

// TestInForceTakesTheIR is the in-process half of InForce's IR leg
// (IR-INFORCE; rows.md, Context values, InForce): on the IR path every
// function of TestInForceContextsAgree's programs that calls InForce, the
// generic hold and holdC of a written row, has an IR body, and that body
// writes the held context in place (its HeldLive check), as the emitter
// does, rather than calling the prelude's InForce, whose Go body is a
// panic (decls.go intrinsic). Before IR-INFORCE the IR called it, and every
// InForce caller panicked when it ran: the text leg alone built the
// property's programs.
//
// Ordinary generic calls pass the same copier as a Go type argument:
// holdVia forwards the caller's row, holdMore joins it with a concrete
// capability, and holdBoth joins two caller rows. None keeps a text body.
//
// Mutants (testdata/mutants.json): ir-inforce-ordinary-call,
// ir-held-row-call-refused.
func TestInForceTakesTheIR(t *testing.T) {
	for _, seed := range inforceIRSeeds() {
		src, _, _ := inforceProgram(seed)
		replay := fmt.Sprintf("Replay: go test -run '^TestInForceTakesTheIR$' ./internal/sketch/lower -args -inforce.seed=%d", seed)
		o := mustLower(t, fmt.Sprintf("inforce%d", seed), src)
		u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
		files, rep, err := LowerIR(u)
		if err != nil {
			t.Fatalf("seed %d: LowerIR: %v\n%s", seed, err, replay)
		}
		const rowReason = "generic call with a row type argument of the caller"
		if n := rep.Fallback[rowReason]; n != 0 {
			t.Fatalf("seed %d: %d bodies still have fallback %q\n%s\n%s", seed, n, rowReason, replay, src)
		}
		for _, name := range []string{"holdVia", "holdMore", "holdBoth"} {
			if !slices.Contains(rep.IR, name) {
				t.Fatalf("seed %d: %s has no IR body (fallbacks %v)\n%s\n%s", seed, name, rep.Fallback, replay, src)
			}
		}
		for _, name := range []string{"hold", "holdC"} {
			if !slices.Contains(rep.IR, name) {
				t.Fatalf("seed %d: %s has no IR body (fallbacks %v; IR %v)\n%s", seed, name, rep.Fallback, rep.IR, replay)
			}
		}
		var live []string
		for _, f := range files {
			if f.Path != IRFileName {
				continue
			}
			var ir irFile
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
			for _, fn := range ir.Funcs {
				body, err := json.Marshal(fn.Body)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(body), `"HeldLive"`) {
					live = append(live, fn.Name)
				}
			}
		}
		for _, name := range []string{"hold", "holdC"} {
			if !slices.Contains(live, name) {
				t.Fatalf("seed %d: %s's IR body does not write the held context in place (bodies that do: %v)\n%s", seed, name, live, replay)
			}
		}
	}
}

// TestInForceRunsOnTheIR runs TestInForceContextsAgree's programs on the
// IR path (ergo run, the product's build): each prints the binding
// model's lines, under the nested with and replace forms the generator
// draws.
//
// It builds Go because the question is what the held context does when it
// runs on the patched compiler's IR (the copy it takes, the fields it
// copies, HeldLive), which no in-process check reads.
func TestInForceRunsOnTheIR(t *testing.T) {
	needsBuild(t)
	for _, seed := range inforceIRSeeds() {
		src, want, _ := inforceProgram(seed)
		dir := t.TempDir()
		path := filepath.Join(dir, "main.ergo")
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(ergoCLI(t), "run", path)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if got := string(withoutBuildNote(out)); err != nil || got != want {
			t.Errorf("seed %d: the IR path (ergo run) and the binding model differ: %v\nReplay: go test -run '^TestInForceRunsOnTheIR$' ./internal/sketch/lower -args -inforce.seed=%d\n--- source\n%s\n--- IR output\n%s--- model\n%s", seed, err, seed, src, got, want)
		}
	}
}
