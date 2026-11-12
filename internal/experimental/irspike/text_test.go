package irspike

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRTextChecks checks the Go text view of the IR bodies (plan-tier
// §2, TXT) on every example binary and test build that has IR bodies:
// GoText builds it through IR with the derivative printing those bodies,
// then
//   - the printed module builds with the derivative without -ergoir, so
//     Go's own parser and types2 check the IR bodies, independently of the
//     IR decoder's checks;
//   - go vet finds nothing in the IR bodies (the source emitter's bodies
//     of the same module are not this test's);
//   - each IR function's structure as compiled from the text
//     (-ergoirshape) equals its structure as compiled from the IR: the
//     text is what was compiled, not only Go that checks (mutant
//     ir-text-not-what-compiled).
//
// The functions compared must be every IR body of every such target. These
// tests build Go because the question is what the patched compiler
// compiled, and types2 and vet of its printed text; no in-process check
// reaches the derivative's trees.
func TestIRTextChecks(t *testing.T) {
	src, ir := needsBuild(t)
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	mains, tests, err := ExampleTargets(root)
	if err != nil {
		t.Fatal(err)
	}
	only := os.Getenv("ERGO_IRSPIKE_EXAMPLE") // a replay of one program
	withIR, checked, funcs, wantFuncs := 0, 0, 0, 0
	check := func(what string, files []lower.File, rep lower.IRReport) {
		if len(rep.IR) == 0 {
			return
		}
		withIR++
		wantFuncs += len(rep.IR)
		replay := fmt.Sprintf("replay: ERGO_IRSPIKE_EXAMPLE=%s go test -run TestIRTextChecks ./internal/experimental/irspike", filepath.Base(programOf(root, strings.TrimSuffix(what, " test"))))
		work := t.TempDir()
		text := filepath.Join(work, "text")
		irShape, textShape := filepath.Join(work, "ir.shape"), filepath.Join(work, "text.shape")
		if err := goText(files, ir, filepath.Join(work, "ir.bin"), text, irShape, nil); err != nil {
			t.Errorf("%s: %v\n%s", what, err, replay)
			return
		}
		if _, err := Build(ir, Text, text, filepath.Join(work, "text.bin"), BuildOptions{GCFlags: []string{"-ergoirshape=" + textShape}}); err != nil {
			t.Errorf("%s: the printed Go text does not build: %v\n%s", what, err, replay)
			return
		}
		vet := exec.Command(filepath.Join(src.GOROOT, "bin", "go"), "vet", "-mod=readonly", ".")
		vet.Dir, vet.Env = text, goEnv(src.GOROOT, src.GOCACHE)
		var out strings.Builder
		vet.Stdout, vet.Stderr = &out, &out
		if _, err := Measure(vet); err != nil {
			// Vet reports the source emitter's bodies too; a finding is
			// the IR's when it lies in an IR body's .ergo lines.
			if found, ok := irVetFindings(t, files, out.String()); !ok || len(found) > 0 {
				t.Errorf("%s: go vet of the printed Go text: %v\n%s\n%s", what, err, strings.Join(found, "\n"), replay)
				if !ok {
					t.Log(out.String())
				}
				return
			}
		}
		compiled, printed := readShapes(t, irShape), readShapes(t, textShape)
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
		checked++
	}
	for _, dir := range mains {
		if only != "" && filepath.Base(programOf(root, dir)) != only {
			continue
		}
		u, err := Load(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		files, rep, err := lower.LowerIR(u)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		check(dir, files, rep)
	}
	for _, dir := range tests {
		if only != "" && filepath.Base(programOf(root, dir)) != only {
			continue
		}
		u, err := Load(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		files, rep, err := lower.LowerIRTests(u)
		if err != nil {
			t.Fatalf("%s test build: %v", dir, err)
		}
		check(dir+" test", files, rep)
	}
	if checked != withIR || funcs != wantFuncs || (only == "" && funcs == 0) {
		t.Fatalf("checked %d of %d targets with IR bodies, %d of %d IR functions", checked, withIR, funcs, wantFuncs)
	}
	t.Logf("%d targets with IR bodies, %d IR functions: their Go text builds, vets and compiles to what was compiled", checked, funcs)
}

// irVetFindings are the lines of vet's output at positions inside an IR
// body (its .ergo file, from its first to its last line); ok is false when
// vet failed with no finding at an .ergo position at all.
func irVetFindings(t *testing.T, files []lower.File, out string) (found []string, ok bool) {
	t.Helper()
	var ir struct {
		Files []string `json:"files"`
		Funcs []struct {
			Pos [3]int `json:"pos"`
			End [3]int `json:"end"`
		} `json:"funcs"`
	}
	for _, f := range files {
		if f.Path == lower.IRFileName {
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
		}
	}
	at := regexp.MustCompile(`^(.+\.ergo):(\d+):\d+: `)
	for _, l := range strings.Split(out, "\n") {
		m := at.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		ok = true
		line, _ := strconv.Atoi(m[2])
		for _, fn := range ir.Funcs {
			if fn.Pos[0] >= 0 && fn.Pos[0] < len(ir.Files) && ir.Files[fn.Pos[0]] == m[1] && fn.Pos[1] <= line && line <= fn.End[1] {
				found = append(found, l)
				break
			}
		}
	}
	return found, ok
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
