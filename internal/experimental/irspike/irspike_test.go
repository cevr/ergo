package irspike

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// programMemory bounds every program a test runs (the kernel enforces it).
const programMemory = 1 << 30

// needsBuild marks a test that builds Go: the fast tier (-short) skips it.
// These tests build Go because the question is the derivative compiler's
// machine code for an IR body, which no in-process check reaches; and they
// need the two toolchains: the spike's receipts configure them
// (Toolchains), and otherwise they are ergo's own (ErgoToolchains), so
// the full tier runs these tests with no variable. A toolchain that cannot
// be had fails the test: the full tier never skips them.
func needsBuild(t *testing.T) (src, ir Toolchain) {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	src, ir, ok := Toolchains()
	if ok {
		return src, ir
	}
	tcs, err := ergoToolchains()
	if err != nil {
		t.Fatalf("the IR tests' toolchains: %v", err)
	}
	return tcs[0], tcs[1]
}

// ergoToolchains is ErgoToolchains once per test process: the parts of a
// split each ask for them.
var ergoToolchains = sync.OnceValues(func() ([2]Toolchain, error) {
	src, ir, err := ErgoToolchains()
	return [2]Toolchain{src, ir}, err
})

// TestIRTestsRunInTheFullTier: in the full tier the IR tests get their
// toolchains, from ergo's cache when no variable names them, and never
// skip (before IRT0b every gate run skipped them all). Mutant: needsBuild
// skips without the variables (irt-ir-tests-skip).
func TestIRTestsRunInTheFullTier(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds the patched compile")
	}
	ran := false
	t.Run("needsBuild", func(t *testing.T) {
		_, ir := needsBuild(t)
		out, _ := exec.Command(filepath.Join(ir.GOROOT, "pkg", "tool", runtime.GOOS+"_"+runtime.GOARCH, "compile"), "-h").CombinedOutput()
		if !strings.Contains(string(out), "-ergoir") {
			t.Fatalf("the IR toolchain's compile has no -ergoir:\n%s", out)
		}
		ran = true
	})
	if !ran {
		t.Fatal("the IR tests skip in the full tier")
	}
}

// outcome is what a program did: its stdout, exit status and the first
// line of its stderr (a panic's message; the trace below it names Go
// frames that differ between the paths by construction).
type outcome struct {
	stdout, stderr string
	exit           int
}

func run(t *testing.T, bin string, env ...string) outcome {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	_, err := MeasureBounded(cmd, programMemory)
	o := outcome{stdout: out.String(), stderr: firstLine(errb.String())}
	if ee, ok := err.(*exec.ExitError); ok {
		o.exit = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s: %v", bin, err)
	}
	return o
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// both builds u by both paths in dir and returns the binaries and the IR
// report.
func both(t *testing.T, u lower.Unit, src, ir Toolchain, dir string, o BuildOptions) (srcBin, irBin string, rep lower.IRReport) {
	t.Helper()
	if _, err := Emit(u, Source, filepath.Join(dir, "source")); err != nil {
		t.Fatal(err)
	}
	rep, err := Emit(u, IR, filepath.Join(dir, "ir"))
	if err != nil {
		t.Fatal(err)
	}
	srcBin, irBin = filepath.Join(dir, "source.bin"), filepath.Join(dir, "ir.bin")
	if _, err := Build(src, Source, filepath.Join(dir, "source"), srcBin, o); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(ir, IR, filepath.Join(dir, "ir"), irBin, o); err != nil {
		t.Fatal(err)
	}
	return srcBin, irBin, rep
}

// irKinds adds the statement and expression kinds of an IR file to seen.
func irKinds(t *testing.T, file string, seen map[string]bool) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if k, ok := v["k"].(string); ok {
				seen[k] = true
				if op, ok := v["op"].(string); ok {
					seen["op "+op] = true
				}
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(v)
}

func envInt(name string, def uint64) uint64 {
	if s := os.Getenv(name); s != "" {
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// TestIRMatchesSource is the spike's acceptance property: for generated
// programs, the IR path and the source path print the same, exit the same
// and fail with the same message (the out-of-range seeds are the negative
// control of the checked index), with every IR kind reached and a fallback
// taken. A failing seed prints the Ergo source, both outcomes and its
// replay command.
func TestIRMatchesSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 16)
	seen := map[string]bool{}
	fallbacks := 0
	panics := 0
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := Generate(seed)
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
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\nreplay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run TestIRMatchesSource ./internal/experimental/irspike\n%s", seed, a, b, seed, text)
		}
		if a.exit != 0 {
			panics++
		}
		irKinds(t, filepath.Join(dir, "ir", lower.IRFileName), seen)
		for _, n := range rep.Fallback {
			fallbacks += n
		}
	}
	if count < 16 {
		return // a replay: the coverage needs the whole run
	}
	var missing []string
	for _, k := range []string{"def", "set", "ret", "if", "while", "range", "expr", "local", "const", "bin", "un", "call", "mcall", "field", "index", "lit", "len",
		"op +", "op -", "op *", "op /", "op %", "op <", "op ==", "op &&", "op ||", "op !"} {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 || fallbacks == 0 || panics == 0 {
		t.Fatalf("the run did not reach: kinds %v, fallbacks %d, failing seeds %d", missing, fallbacks, panics)
	}
}

// TestIRBodyThatDoesNotCheck: an IR body whose types do not check is a
// compile error at its .ergo position, never a passthrough (mutant M6 in
// the derivative; irspike-ir-line-shifted in this repository).
func TestIRBodyThatDoesNotCheck(t *testing.T) {
	_, ir := needsBuild(t)
	u, err := Load(filepath.Join("testdata", "kernel", "main.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "ir")
	if _, err := Emit(u, IR, dir); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, lower.IRFileName)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// walk's modulus becomes a string: acc*31 + c.Weight) % "1000003".
	bad := bytes.Replace(data, []byte(`"t":"int","v":"1000003"`), []byte(`"t":"string","v":"1000003"`), 1)
	if bytes.Equal(bad, data) {
		t.Fatal("the kernel's IR has no constant 1000003")
	}
	if err := os.WriteFile(file, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Build(ir, IR, dir, filepath.Join(dir, "bin"), BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "main.ergo:38:") || !strings.Contains(err.Error(), "ergoir: %: string is not int") {
		t.Fatalf("want the ergoir error at main.ergo:38, got %v", err)
	}
}

// TestIRCacheKeysOnIR: an edit of the IR alone (the Go text unchanged)
// builds again; the build cache never serves the old body (mutant M5).
func TestIRCacheKeysOnIR(t *testing.T) {
	_, ir := needsBuild(t)
	u, err := Load(filepath.Join("testdata", "kernel", "main.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "ir")
	if _, err := Emit(u, IR, dir); err != nil {
		t.Fatal(err)
	}
	outputs := map[string]bool{}
	// walk's modulus, in an IR body; main.go does not change.
	for i, edit := range []string{`"v":"1000003"`, `"v":"1000033"`} {
		file := filepath.Join(dir, lower.IRFileName)
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.Replace(data, []byte(`"v":"1000003"`), []byte(edit), 1)
		if err := os.WriteFile(file, data, 0o644); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(dir, fmt.Sprint("bin", i))
		if _, err := Build(ir, IR, dir, bin, BuildOptions{}); err != nil {
			t.Fatal(err)
		}
		outputs[run(t, bin).stdout] = true
	}
	if len(outputs) != 2 {
		t.Fatalf("an IR-only edit served the old build: %v", outputs)
	}
}
