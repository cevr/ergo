package irspike

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// programMemory bounds every program a test runs (the kernel enforces it).
const programMemory = 1 << 30

// needsBuild marks a test that builds Go: the fast tier (-short) skips it.
// These tests build Go because the question is the derivative compiler's
// machine code for an IR body, which no in-process check reaches; and they
// need the two toolchains, which only the spike's receipts configure.
func needsBuild(t *testing.T) (src, ir Toolchain) {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	src, ir, ok := Toolchains()
	if !ok {
		t.Skip(ErrNoToolchains)
	}
	return src, ir
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
		"op +", "op -", "op *", "op /", "op %", "op <", "op ==", "op &&", "op ||", "op !",
		// IRG1: scalars and small forms.
		"var", "discard", "break", "continue", "pcall", "conv", "global",
		"op &", "op |", "op ^", "op &^", "op <<", "op >>"} {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 || fallbacks == 0 || panics == 0 {
		t.Fatalf("the run did not reach: kinds %v, fallbacks %d, failing seeds %d", missing, fallbacks, panics)
	}
}

// TestIRRuntimeChecks runs the kernel and the deep-recursion program, built
// by the IR path, under the race detector and under the collector's
// checkmark mode with a collection at almost every allocation, and requires
// the source path's output. deep's frames hold pointers live across calls
// while the stack grows, so a wrong stack map or pointer map of an IR body
// would show as a crash or a different total.
func TestIRRuntimeChecks(t *testing.T) {
	src, ir := needsBuild(t)
	for _, name := range []string{"kernel", "deep"} {
		u, err := Load(filepath.Join("testdata", name, "main.ergo"))
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		if len(rep.IR) == 0 {
			t.Fatalf("%s: no IR body", name)
		}
		want := run(t, sb)
		for _, env := range [][]string{{"GODEBUG=gccheckmark=1", "GOGC=1"}, {"GODEBUG=gcstoptheworld=1", "GOGC=1"}} {
			if got := run(t, ib, env...); got != want {
				t.Errorf("%s %v: ir %+v, source %+v", name, env, got, want)
			}
		}
		rdir := t.TempDir()
		_, rb, _ := both(t, u, src, ir, rdir, BuildOptions{Race: true})
		if got := run(t, rb, "GORACE=halt_on_error=1"); got != want {
			t.Errorf("%s -race: ir %+v, source %+v", name, got, want)
		}
	}
}

// TestIRBodyThatDoesNotCheck: an IR body whose types do not check is a
// compile error at its .ergo position, never a passthrough (mutant M6).
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
