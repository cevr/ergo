package irspike

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// irText checks the Go text view of the IR bodies (plan-tier §2, TXT) on
// every example binary and test build that has IR bodies, split by target
// (TestIRTextChecksPart0 …; TestSplitsCoverTheirDomain checks that the
// parts run every target once):
// GoText builds it through IR with the derivative printing those bodies,
// then
//   - the printed module builds with the derivative without -ergoir, so
//     Go's own parser and types2 check the IR bodies, independently of the
//     IR decoder's checks;
//   - go vet finds nothing in the printed module (the IR bodies and the
//     source emitter's bodies around them);
//   - each IR function's structure as compiled from the text
//     (-ergoirshape) equals its structure as compiled from the IR: the
//     text is what was compiled, not only Go that checks (mutant
//     ir-text-not-what-compiled).
//
// The functions compared must be every IR body of the target. These tests
// build Go because the question is what the patched compiler compiled,
// and types2 and vet of its printed text; no in-process check reaches the
// derivative's trees.
var irText = newSplit(split[exampleTarget]{
	name:  "TestIRTextChecks",
	parts: textParts,
	cases: exampleDomain,
	key:   exampleTarget.key,
	run:   textCheckOn,
})

// textCheckOn checks the Go text of one target's IR bodies.
func textCheckOn(t *testing.T, target exampleTarget) {
	src, ir := needsBuild(t)
	u, err := Load(target.dir)
	if err != nil {
		t.Fatalf("%s: %v", target.dir, err)
	}
	lowering := lower.LowerIR
	if target.test {
		lowering = lower.LowerIRTests
	}
	files, rep, err := lowering(u)
	if err != nil {
		t.Fatalf("%s: %v", target.key(), err)
	}
	textCheck(t, src, ir, target.key(), files, rep)
}

// TestIRTextRepros runs the text checks on the smallest repros of fixed
// bugs, each with the function it needs an IR body of:
//   - duplocals (TXT r1): two try temporaries of one scope were both
//     named v and err, so the text did not compile, or := reused the
//     first variable (mutant ir-temp-name-reused);
//   - shadow (IRG3b): a source local kept its Ergo spelling, so a local
//     named strconv shadowed the package its int's text calls (mutant
//     ir-local-raw-spelling);
//   - pollend (IRG3b): a loop body ending in a return or a break was
//     followed by its back-edge poll, unreachable code to go vet
//     (mutant ir-loop-poll-after-terminating-body).
//
// It builds Go for the reason TestIRTextChecks does.
func TestIRTextRepros(t *testing.T) {
	src, ir := needsBuild(t)
	for _, r := range []struct{ name, fn string }{{"duplocals", "two"}, {"shadow", "shadow"}, {"pollend", "firstOf"}} {
		t.Run(r.name, func(t *testing.T) {
			u, err := Load(filepath.Join("testdata", r.name+".ergo"))
			if err != nil {
				t.Fatal(err)
			}
			files, rep, err := lower.LowerIRTests(u)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(rep.IR, r.fn) {
				t.Fatalf("%s: %s has no IR body: %v", r.name, r.fn, rep.Fallback)
			}
			textCheck(t, src, ir, r.name, files, rep)
		})
	}
}

// textCheck checks the Go text of the IR bodies of one lowered module
// (what names it): it builds, vets and compiles to what was compiled,
// for every IR function of rep.
func textCheck(t *testing.T, src, ir Toolchain, what string, files []lower.File, rep lower.IRReport) {
	t.Helper()
	if len(rep.IR) == 0 {
		t.Logf("%s has no IR bodies", what)
		return
	}
	replay := fmt.Sprintf("replay: go test -run '%s' ./internal/experimental/irspike", t.Name())
	work := t.TempDir()
	text := filepath.Join(work, "text")
	irShape, textShape := filepath.Join(work, "ir.shape"), filepath.Join(work, "text.shape")
	if err := goText(files, ir, filepath.Join(work, "ir.bin"), text, irShape, nil); err != nil {
		t.Fatalf("%s: %v\n%s", what, err, replay)
	}
	if _, err := Build(ir, Text, text, filepath.Join(work, "text.bin"), BuildOptions{GCFlags: []string{"-ergoirshape=" + textShape}}); err != nil {
		t.Fatalf("%s: the printed Go text does not build: %v\n%s", what, err, replay)
	}
	vet := exec.Command(filepath.Join(src.GOROOT, "bin", "go"), "vet", "-mod=readonly", ".")
	vet.Dir, vet.Env = text, goEnv(src.GOROOT, src.GOCACHE)
	var out strings.Builder
	vet.Stdout, vet.Stderr = &out, &out
	if _, err := Measure(vet); err != nil {
		t.Fatalf("%s: go vet of the printed Go text: %v\n%s\n%s", what, err, out.String(), replay)
	}
	compiled, printed := readShapes(t, irShape), readShapes(t, textShape)
	funcs := 0
	for _, fn := range rep.IR {
		a, ok := compiled[fn]
		if !ok {
			t.Errorf("%s: %s: the IR build wrote no structure for it\n%s", what, fn, replay)
			continue
		}
		if b := printed[fn]; a != b {
			t.Errorf("%s: %s: the printed text is not what was compiled\n%s\n%s", what, fn, firstDifference(a, b), replay)
			continue
		}
		funcs++
	}
	if funcs != len(rep.IR) {
		t.Fatalf("%s: checked %d of %d IR functions", what, funcs, len(rep.IR))
	}
	t.Logf("%s: %d IR functions: their Go text builds, vets and compiles to what was compiled", what, funcs)
}

// readShapes reads an -ergoirshape file: function name -> its structure.
func readShapes(t *testing.T, file string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	name, body := "", []string(nil)
	for _, l := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(l, "func "):
			name, body = strings.TrimPrefix(l, "func "), nil
		case l == "end":
			out[name] = strings.Join(body, "\n")
		default:
			body = append(body, l)
		}
	}
	return out
}

// firstDifference shows two structures from their first differing line.
func firstDifference(compiled, printed string) string {
	a, b := strings.Split(compiled, "\n"), strings.Split(printed, "\n")
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(0, i-3)
	return fmt.Sprintf("compiled (from line %d):\n%s\nprinted text compiles to:\n%s", from+1, strings.Join(a[from:min(len(a), i+4)], "\n"), strings.Join(b[from:min(len(b), i+4)], "\n"))
}
