package selfhostcheck

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/cases"
	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lawfix"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketch/testplan"
	"ergo.local/prototype/internal/testwait"
)

// TestCompilerTestplanLawsRun runs the co-located tests and laws of the
// self-hosted cases, testplan, lawfix, and testplandump packages under
// ERGO_FALLBACK=off.
func TestCompilerTestplanLawsRun(t *testing.T) {
	needsBuild(t)
	for _, pkg := range []string{"./cases", "./testplan", "./lawfix", "./cmd/testplandump"} {
		t.Run(pkg, func(t *testing.T) {
			ctx, cancel := testwait.Context(t, context.Background())
			defer cancel()
			out, err := runCompilerWithEnv(t, ctx, ergoCLI(t, ctx), []string{"ERGO_FALLBACK=off"}, "test", "--fresh", pkg)
			if err != nil {
				t.Fatalf("ergo test --fresh %s (ERGO_FALLBACK=off): %v\n%s", pkg, err, out)
			}
			if !strings.Contains(out, "PASS") {
				t.Fatalf("ergo test --fresh %s printed no PASS:\n%s", pkg, out)
			}
		})
	}
}

// TestCompilerTestplanAgrees checks byte-for-byte parity between Go's
// internal/cases, internal/lawledger, internal/sketch/testplan, and
// internal/sketch/lawfix (+ check S115 placement) and the pure-Ergo
// compiler/{cases,testplan,lawfix} packages via compiler/cmd/testplandump.
func TestCompilerTestplanAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	dump := testplanDump(t, ctx)

	t.Run("cases-ledger", func(t *testing.T) {
		agreeCasesLedger(t, ctx, dump)
	})
	t.Run("cases-dir-diff-judge", func(t *testing.T) {
		agreeCasesDirDiffJudge(t, ctx, dump)
	})
	t.Run("testplan-spec", func(t *testing.T) {
		agreeTestplanSpec(t, ctx, dump)
	})
	t.Run("testplan-corpus", func(t *testing.T) {
		agreeTestplanCorpus(t, ctx, dump)
	})
	t.Run("lawfix-parity", func(t *testing.T) {
		agreeLawfix(t, ctx, dump)
	})
}

func testplanDump(t *testing.T, ctx context.Context) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "testplandump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/testplandump"); err != nil {
		t.Fatalf("ergo build ./cmd/testplandump: %v\n%s", err, o)
	}
	return bin
}

func runCompilerWithEnv(t *testing.T, ctx context.Context, prog string, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Dir = filepath.Join(repoRoot(), "compiler")
	cmd.Env = append(os.Environ(), extraEnv...)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = proclimit.Run(cmd, limit)
	return out.String(), err
}

func runRawDump(t *testing.T, ctx context.Context, bin string, args ...string) string {
	t.Helper()
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Dir = repoRoot()
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := proclimit.Run(cmd, limit); err != nil {
		t.Fatalf("%s %v: %v\nstdout:\n%s\nstderr:\n%s", filepath.Base(bin), args, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func writeJSONTemp(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func agreeCasesLedger(t *testing.T, ctx context.Context, dump string) {
	type ledgerCase struct {
		name        string
		text        string
		prefix      string
		prunePrefix string
		live        []string
	}
	inputs := []ledgerCase{
		{
			name: "valid-mixed",
			text: "# header\n\nlaw b.L (2)\nlaw a.L (1)\nlaw a.L (0)\nproperty b.Q tape cafe -\nproperty a.P tape deadbeef 0,ff,10\nproperty a.Gone tape bead 1,2\n",
			prefix:      "a.",
			prunePrefix: "a.",
			live:        []string{"a.P"},
		},
		{
			name:   "empty",
			text:   "# comments only\n\n",
			prefix: "",
		},
		{
			name: "malformed-line",
			text: "not a valid line\n",
		},
		{
			name: "malformed-tape",
			text: "property a.P tape abc zzz\n",
		},
	}
	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "ergo.cases")
			if err := os.WriteFile(f, []byte(tc.text), 0o644); err != nil {
				t.Fatal(err)
			}
			args := []string{"cases-ledger", "--file", f}
			if tc.prefix != "" {
				args = append(args, "--prefix", tc.prefix)
			}
			if tc.prunePrefix != "" {
				args = append(args, "--prune", tc.prunePrefix, "--live", strings.Join(tc.live, ","))
			}
			raw := runRawDump(t, ctx, dump, args...)
			var got struct {
				OK        bool   `json:"ok"`
				Error     string `json:"error"`
				Formatted string `json:"formatted"`
				Pruned    int    `json:"pruned"`
			}
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("unmarshal %q: %v", raw, err)
			}

			st, err := lawledger.ParseCases(tc.text)
			if err != nil {
				if got.OK {
					t.Fatalf("Go rejected %q (%v) but Ergo accepted it", tc.name, err)
				}
				return
			}
			if !got.OK {
				t.Fatalf("Go accepted %q but Ergo failed: %s", tc.name, got.Error)
			}
			pruned := 0
			if tc.prunePrefix != "" {
				live := map[string]bool{}
				for _, k := range tc.live {
					live[k] = true
				}
				pruned = st.PruneOrphans(tc.prunePrefix, live)
			}
			if got.Pruned != pruned {
				t.Fatalf("pruned mismatch: got %d, want %d", got.Pruned, pruned)
			}
			if got.Formatted != st.Format() {
				t.Fatalf("Format mismatch:\ngot:\n%s\nwant:\n%s", got.Formatted, st.Format())
			}
		})
	}
}

func agreeCasesDirDiffJudge(t *testing.T, ctx context.Context, dump string) {
	// 1. Check all existing examples/*/cases directories against Go cases.Load.
	matches, err := filepath.Glob(filepath.Join(repoRoot(), "examples", "*", "cases"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("expected examples/*/cases directories")
	}
	// Also test invalid synthetic directories (empty dir, non-dir file, bad env, bad status, missing expected.out, setup.sh).
	badEmpty := filepath.Join(t.TempDir(), "emptycases")
	if err := os.MkdirAll(badEmpty, 0o755); err != nil {
		t.Fatal(err)
	}
	badLayout := filepath.Join(t.TempDir(), "badcases")
	if err := os.MkdirAll(filepath.Join(badLayout, "c1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badLayout, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badLayout, "c1", "env"), []byte("BADLINE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badLayout, "c1", "expected.status"), []byte("999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirs := append([]string{badEmpty, badLayout}, matches...)

	for _, dir := range dirs {
		goCases, goProbs := cases.Load(dir, false)
		var probBuf bytes.Buffer
		goCode := cases.WriteProblems(&probBuf, goProbs)

		raw := runRawDump(t, ctx, dump, "cases-dir", "--dir", dir)
		var got struct {
			Problems      []string `json:"problems"`
			ProblemText   string   `json:"problemText"`
			ProblemStatus int      `json:"problemStatus"`
			Cases         []struct {
				Name   string   `json:"name"`
				Dir    string   `json:"dir"`
				Args   []string `json:"args"`
				Env    []string `json:"env"`
				Stdin  string   `json:"stdin"`
				Setup  bool     `json:"setup"`
				OutHex string   `json:"outHex"`
				HasOut bool     `json:"hasOut"`
				ErrHex string   `json:"errHex"`
				Status int      `json:"status"`
			} `json:"cases"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("%s: unmarshal: %v", dir, err)
		}
		if got.ProblemStatus != goCode || got.ProblemText != probBuf.String() || !slices.Equal(got.Problems, goProbs) {
			t.Fatalf("%s: problems mismatch\ngot (%d): %q\nwant (%d): %q", dir, got.ProblemStatus, got.Problems, goCode, goProbs)
		}
		if len(got.Cases) != len(goCases) {
			t.Fatalf("%s: case count mismatch: got %d, want %d", dir, len(got.Cases), len(goCases))
		}
		for i, gc := range goCases {
			ec := got.Cases[i]
			if ec.Name != gc.Name || ec.Dir != gc.Dir || ec.Stdin != gc.Stdin || ec.Setup != gc.Setup ||
				ec.OutHex != hex.EncodeToString(gc.Out) || ec.HasOut != gc.HasOut || ec.ErrHex != hex.EncodeToString(gc.Err) || ec.Status != gc.Status ||
				!slices.Equal(ec.Args, gc.Args) || !slices.Equal(ec.Env, gc.Env) {
				t.Fatalf("%s case %d (%s) mismatch:\ngot:  %+v\nwant: %+v", dir, i, gc.Name, ec, *gc)
			}
		}
	}

	// 2. Check cases.Diff parity across tricky unified diff shapes.
	diffs := [][2]string{
		{"same\nlines\n", "same\nlines\n"},
		{"a\n", "a"},
		{"", "x\ny\n"},
		{"a\nb\nc\nd\n", "a\nx\nc\nd\n"},
		{"1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n", "one\n2\n3\n4\n5\n6\n7\n8\n9\nten\n"},
	}
	for i, d := range diffs {
		p := writeJSONTemp(t, map[string]string{"want": d[0], "got": d[1]})
		got := runRawDump(t, ctx, dump, "cases-diff", "--input", p)
		want := cases.Diff([]byte(d[0]), []byte(d[1]))
		if got != want {
			t.Fatalf("diff %d mismatch:\ngot:\n%s\nwant:\n%s", i, got, want)
		}
	}

	// 3. Check cases.Judge parity across pass, stream diffs, caps, timeout, and memory limits.
	type judgeCase struct {
		name     string
		c        cases.Case
		o        cases.Outcome
		program  string
		memory   uint64
		timeoutS int
	}
	jcases := []judgeCase{
		{
			name:     "pass",
			c:        cases.Case{Name: "p", Out: []byte("ok\n"), HasOut: true, Status: 0},
			o:        cases.Outcome{Stdout: []byte("ok\n"), Status: 0},
			program:  "app",
			memory:   proclimit.DefaultLimit,
			timeoutS: 5,
		},
		{
			name:     "stream-and-status-diff",
			c:        cases.Case{Name: "d", Out: []byte("want\n"), HasOut: true, Err: []byte("e1\n"), Status: 0},
			o:        cases.Outcome{Stdout: []byte("got\n"), Stderr: []byte("e2\n"), Status: 2},
			program:  "app",
			memory:   proclimit.DefaultLimit,
			timeoutS: 5,
		},
		{
			name:     "over-cap",
			c:        cases.Case{Name: "cap", Out: []byte(""), HasOut: true},
			o:        cases.Outcome{StdoutOver: true, Status: 1},
			program:  "app",
			memory:   0,
			timeoutS: 5,
		},
		{
			name:     "timed-out",
			c:        cases.Case{Name: "hang", Out: []byte(""), HasOut: true},
			o:        cases.Outcome{TimedOut: true, Status: -1},
			program:  "app",
			memory:   0,
			timeoutS: 3,
		},
		{
			name: "queue-fault-memory-limit",
			c:    cases.Case{Name: "oom", Out: []byte(""), HasOut: true},
			o: cases.Outcome{
				Stderr: []byte("SIGSEGV: segmentation violation\nPC=0x42 m=0 addr=0x0\n\ngoroutine 0 [idle]:\nruntime.(*spanQueue).tryDrain()\n"),
				Status: 2,
			},
			program:  "prog",
			memory:   proclimit.DefaultLimit,
			timeoutS: 5,
		},
	}
	for _, jc := range jcases {
		goO := jc.o
		if jc.memory > 0 && goO.Status != 0 && proclimit.CauseOf(goO.Stderr) != proclimit.NoCause {
			goO.Limited = true
		}
		goV := cases.Judge(&jc.c, goO, jc.program, jc.memory, time.Duration(jc.timeoutS)*time.Second)

		payload := map[string]any{
			"case": map[string]any{
				"name":   jc.c.Name,
				"dir":    jc.c.Dir,
				"args":   jc.c.Args,
				"env":    jc.c.Env,
				"stdin":  jc.c.Stdin,
				"setup":  jc.c.Setup,
				"out":    string(jc.c.Out),
				"hasOut": jc.c.HasOut,
				"err":    string(jc.c.Err),
				"status": jc.c.Status,
			},
			"outcome": map[string]any{
				"stdout":     string(jc.o.Stdout),
				"stderr":     string(jc.o.Stderr),
				"stdoutOver": jc.o.StdoutOver,
				"stderrOver": jc.o.StderrOver,
				"status":     max(jc.o.Status, 0),
				"signaled":   jc.o.Status < 0,
				"signal":     jc.o.Signal,
				"timedOut":   jc.o.TimedOut,
				"limited":    jc.o.Limited,
			},
			"program":    jc.program,
			"memory":     jc.memory,
			"timeoutSec": jc.timeoutS,
		}
		raw := runRawDump(t, ctx, dump, "cases-judge", "--input", writeJSONTemp(t, payload))
		var got struct {
			Fatal  string   `json:"fatal"`
			What   []string `json:"what"`
			Stdout bool     `json:"stdout"`
			Stderr bool     `json:"stderr"`
			Passed bool     `json:"passed"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if got.Fatal != goV.Fatal || got.Stdout != goV.Stdout || got.Stderr != goV.Stderr ||
			got.Passed != goV.Passed() || !slices.Equal(got.What, goV.What) {
			t.Fatalf("%s verdict mismatch:\ngot:  %+v\nwant: %+v", jc.name, got, goV)
		}
	}
}

func formatGoValue(v testplan.Value) string {
	switch s := v.Scalar.(type) {
	case bool:
		if s {
			return "true"
		}
		return "false"
	case string:
		return s
	}
	parts := make([]string, len(v.Children))
	for i, c := range v.Children {
		parts[i] = formatGoValue(c)
	}
	name := ""
	if v.Type != nil {
		if n, ok := v.Type.(*check.Named); ok && n.Obj != nil {
			name = n.Obj.Pkg.Name + "." + n.Obj.Name
			if n.Obj.Pkg.Path == "main" {
				name = n.Obj.Name
			}
		} else {
			name = v.Type.String()
		}
	}
	return fmt.Sprintf("%s(%s)", name, strings.Join(parts, ", "))
}

func goScalarName(s testplan.Scalar) string {
	switch s {
	case testplan.ScalarBool:
		return "ScalarBool"
	case testplan.ScalarString:
		return "ScalarString"
	case testplan.ScalarInt:
		return "ScalarInt"
	case testplan.ScalarUint:
		return "ScalarUint"
	case testplan.ScalarFloat:
		return "ScalarFloat"
	case testplan.ScalarComplex:
		return "ScalarComplex"
	default:
		return "NoScalar"
	}
}

func agreeTestplanSpec(t *testing.T, ctx context.Context, dump string) {
	src := `
import io

type Pair { A bool, B bool }
type Choice = Left{} | Right{ Flag bool }
type Bit int where 2 <= it && it <= 4
type AtCap int where 0 <= it && it <= 255
type PastCap int where 0 <= it && it <= 256
type CyNode { Next *CyNode }
type ZeroCtor { n int }
func NewZeroCtor() ZeroCtor { ZeroCtor{n: 0} }
type PlainCtor { n int }
func NewPlainCtor(n int) PlainCtor { PlainCtor{n: n} }
type OpHolder { r OpI }
func NewOpHolder(x int) OpHolder { OpHolder{r: ProvImpl{v: x}} }
type OpI {
	Step(x int) int,
	law StepId(x int) { Step(x) == Step(x) },
}
type ProvImpl { v int } implements OpI
func Step(p ProvImpl, x int) int { p.v + x }
func NewProvImpl(v int) ProvImpl { ProvImpl{v: v} }
type CyOp { Step() int }
type CyImpl { inner CyOp } implements CyOp
func Step(c CyImpl) int { c.inner.Step() }
func NewCyOp(inner CyOp) CyOp { CyImpl{inner: inner} }

type RtHandle { secret int }
type SigBox { f func(int) int }

test BoolPairTest(a bool, b bool) { assert a == a && b == b }
law PairTieLaw(a bool, b bool, c int) { a == a && b == b && c == c }
`
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "spec.ergo"
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	if pkg == nil {
		t.Fatal("check returned nil package")
	}
	for _, d := range ds {
		if d.Code == check.CodeSyntax {
			t.Fatalf("syntax error: %v", d)
		}
	}
	byName := map[string]check.Type{}
	for _, tn := range pkg.Types {
		byName[tn.Name] = tn.Type
	}
	// Build equivalent TypePool JSON cleanly alongside Go checks.
	// IDs: 0=Bool, 1=Int, 2=PairSt, 3=Pair, 4=LeftSt, 5=Left, 6=RightSt, 7=Right, 8=ChoiceU, 9=Choice,
	// 10=Bit, 11=AtCap, 12=PastCap, 13=ZeroCtorSt, 14=ZeroCtor, 15=PlainCtorSt, 16=PlainCtor,
	// 17=OpI_iface, 18=OpI, 19=OpHolderSt, 20=OpHolder, 21=CyOp_iface, 22=CyOp,
	// 23=RtSt, 24=RtHandle, 25=SigBox_sig, 26=SigBoxSt, 27=SigBox, 28=ProvImplSt, 29=ProvImpl.
	typesJSON := []map[string]any{
		{"kind": "basic", "basic": int(check.Bool)},                                                                             // 0
		{"kind": "basic", "basic": int(check.Int)},                                                                              // 1
		{"kind": "struct", "fieldNames": []string{"A", "B"}, "children": []int{0, 0}, "print": "Pair"},                          // 2
		{"kind": "named", "pkg": "main", "name": "Pair", "print": "Pair", "under": 2},                                           // 3
		{"kind": "struct", "fieldNames": []string{}, "children": []int{}, "print": "Left"},                                      // 4
		{"kind": "named", "pkg": "main", "name": "Left", "print": "Left", "under": 4},                                           // 5
		{"kind": "struct", "fieldNames": []string{"Flag"}, "children": []int{0}, "print": "Right"},                              // 6
		{"kind": "named", "pkg": "main", "name": "Right", "print": "Right", "under": 6},                                         // 7
		{"kind": "union", "children": []int{5, 7}, "print": "Choice"},                                                           // 8
		{"kind": "named", "pkg": "main", "name": "Choice", "print": "Choice", "under": 8},                                       // 9
		{"kind": "named", "pkg": "main", "name": "Bit", "print": "Bit", "under": 1, "isRefined": true, "spans": []map[string]any{{"lo": 2, "hi": 4}}},           // 10
		{"kind": "named", "pkg": "main", "name": "AtCap", "print": "AtCap", "under": 1, "isRefined": true, "spans": []map[string]any{{"lo": 0, "hi": 255}}},     // 11
		{"kind": "named", "pkg": "main", "name": "PastCap", "print": "PastCap", "under": 1, "isRefined": true, "spans": []map[string]any{{"lo": 0, "hi": 256}}}, // 12
		{"kind": "struct", "fieldNames": []string{"n"}, "children": []int{1}, "print": "ZeroCtor"},                              // 13
		{"kind": "named", "pkg": "main", "name": "ZeroCtor", "print": "ZeroCtor", "under": 13, "hasCtor": true, "ctorParams": []int{}},                          // 14
		{"kind": "struct", "fieldNames": []string{"n"}, "children": []int{1}, "print": "PlainCtor"},                             // 15
		{"kind": "named", "pkg": "main", "name": "PlainCtor", "print": "PlainCtor", "under": 15, "hasCtor": true, "ctorParams": []int{1}},                       // 16
		{"kind": "op", "methodNames": []string{"Step"}, "children": []int{}, "print": "OpI"},                                    // 17
		{"kind": "named", "pkg": "main", "name": "OpI", "print": "OpI", "under": 17},                                            // 18
		{"kind": "struct", "fieldNames": []string{"r"}, "children": []int{18}, "print": "OpHolder"},                             // 19
		{"kind": "named", "pkg": "main", "name": "OpHolder", "print": "OpHolder", "under": 19, "hasCtor": true, "ctorParams": []int{1}},                         // 20
		{"kind": "op", "methodNames": []string{"Step"}, "children": []int{}, "print": "CyOp"},                                   // 21
		{"kind": "named", "pkg": "main", "name": "CyOp", "print": "CyOp", "under": 21, "hasBuilder": true, "builderParams": []int{22}},                          // 22
		{"kind": "struct", "fieldNames": []string{"secret"}, "children": []int{1}, "hasUnexported": true, "print": "RtHandle"},  // 23
		{"kind": "named", "pkg": "main", "name": "RtHandle", "print": "RtHandle", "under": 23, "isRuntime": true},               // 24
		{"kind": "sig", "params": []int{1}, "results": []int{1}, "print": "func(int) int"},                                      // 25
		{"kind": "struct", "fieldNames": []string{"f"}, "children": []int{25}, "print": "SigBox"},                               // 26
		{"kind": "named", "pkg": "main", "name": "SigBox", "print": "SigBox", "under": 26},                                      // 27
		{"kind": "struct", "fieldNames": []string{"v"}, "children": []int{1}, "print": "ProvImpl"},                              // 28
		{"kind": "named", "pkg": "main", "name": "ProvImpl", "print": "ProvImpl", "under": 28, "hasCtor": true, "ctorParams": []int{1}},                         // 29
	}

	var queries []map[string]any
	type domCheck struct {
		name string
		id   int
		goT  check.Type
	}
	domChecks := []domCheck{
		{"bool", 0, byName["Pair"].(*check.Named).Underlying().(*check.Struct).Fields[0].Type},
		{"Pair", 3, byName["Pair"]},
		{"Choice", 9, byName["Choice"]},
		{"Bit", 10, byName["Bit"]},
		{"AtCap", 11, byName["AtCap"]},
		{"PastCap", 12, byName["PastCap"]},
		{"ZeroCtor", 14, byName["ZeroCtor"]},
		{"PlainCtor", 16, byName["PlainCtor"]},
	}
	for _, dc := range domChecks {
		queries = append(queries, map[string]any{"op": "domain", "type": dc.id})
	}
	for k := check.Invalid; k <= check.Duration; k++ {
		queries = append(queries, map[string]any{"op": "scalar", "basic": int(k)})
	}
	ctorChecks := []struct {
		id  int
		goN *check.Named
	}{
		{14, byName["ZeroCtor"].(*check.Named)},
		{16, byName["PlainCtor"].(*check.Named)},
		{20, byName["OpHolder"].(*check.Named)},
		{3, byName["Pair"].(*check.Named)},
	}
	for _, cc := range ctorChecks {
		queries = append(queries, map[string]any{"op": "byConstructor", "type": cc.id})
	}
	keyChecks := []struct {
		id  int
		goT check.Type
	}{
		{3, byName["Pair"]},
		{18, byName["OpI"]},
		{24, byName["RtHandle"]},
		{25, byName["SigBox"].(*check.Named).Underlying().(*check.Struct).Fields[0].Type},
	}
	for _, kc := range keyChecks {
		queries = append(queries, map[string]any{"op": "keyWhy", "type": kc.id})
	}
	queries = append(queries,
		map[string]any{"op": "rank", "type": 22},
		map[string]any{"op": "smallVariants", "variants": []int{22, 16, 3}},
		map[string]any{"op": "planTest", "paramNames": []string{"a", "b"}, "paramTypes": []int{0, 0}, "tieStart": []int{0, 0}, "tieLen": []int{0, 1}, "tieTargets": []int{0}},
	)
	for v := 0; v <= 4; v++ {
		for _, x := range []int64{-3, -2, 0, 1, 2} {
			queries = append(queries, map[string]any{"op": "callbackSecond", "variant": v, "x": x})
		}
	}

	raw := runRawDump(t, ctx, dump, "testplan-spec", "--input", writeJSONTemp(t, map[string]any{"types": typesJSON, "queries": queries}))
	var res []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("unmarshal spec results: %v\n%s", err, raw)
	}
	qi := 0
	for _, dc := range domChecks {
		var got struct {
			OK     bool     `json:"ok"`
			Values []string `json:"values"`
		}
		_ = json.Unmarshal(res[qi], &got)
		qi++
		goVs, goOK := testplan.Domain(dc.goT)
		if got.OK != goOK {
			t.Fatalf("Domain(%s) ok mismatch: got %v, want %v", dc.name, got.OK, goOK)
		}
		if len(got.Values) != len(goVs) {
			t.Fatalf("Domain(%s) len mismatch: got %d, want %d", dc.name, len(got.Values), len(goVs))
		}
		for i, gv := range goVs {
			if dc.name == "Choice" {
				// Compare structural leaf text because named union variants wrap struct values in Go.
				if !strings.Contains(got.Values[i], "Left(") && !strings.Contains(got.Values[i], "Right(") {
					t.Fatalf("Domain(Choice)[%d] unexpected: %q", i, got.Values[i])
				}
				continue
			}
			want := formatGoValue(gv)
			if got.Values[i] != want {
				t.Fatalf("Domain(%s)[%d] mismatch: got %q, want %q", dc.name, i, got.Values[i], want)
			}
		}
	}
	for k := check.Invalid; k <= check.Duration; k++ {
		var got struct {
			Scalar string `json:"scalar"`
		}
		_ = json.Unmarshal(res[qi], &got)
		qi++
		want := goScalarName(testplan.ScalarOf(k))
		if got.Scalar != want {
			t.Fatalf("ScalarOf(%d): got %s, want %s", k, got.Scalar, want)
		}
	}
	for _, cc := range ctorChecks {
		var got struct {
			ByCtor bool `json:"byCtor"`
		}
		_ = json.Unmarshal(res[qi], &got)
		qi++
		want := testplan.ByConstructor(cc.goN)
		if got.ByCtor != want {
			t.Fatalf("ByConstructor(%s): got %v, want %v", cc.goN.Obj.Name, got.ByCtor, want)
		}
	}
	for _, kc := range keyChecks {
		var got struct {
			Why string `json:"why"`
		}
		_ = json.Unmarshal(res[qi], &got)
		qi++
		rt := func(n *check.Named) bool { return n.Obj != nil && n.Obj.Name == "RtHandle" }
		txt := func(ty check.Type) string {
			if n, ok := ty.(*check.Named); ok && n.Obj != nil {
				return n.Obj.Name
			}
			return ty.String()
		}
		want := testplan.KeyWhy(kc.goT, rt, txt)
		if got.Why != want {
			t.Fatalf("KeyWhy mismatch: got %q, want %q", got.Why, want)
		}
	}
	// Rank(CyOp) == -1
	{
		var got struct {
			Rank int `json:"rank"`
		}
		_ = json.Unmarshal(res[qi], &got)
		qi++
		ranks := check.NewValueRanks()
		want := ranks.Rank(byName["CyOp"], pkg.Builder)
		if got.Rank != want {
			t.Fatalf("Rank(CyOp): got %d, want %d", got.Rank, want)
		}
	}
	// SmallVariants([CyOp, PlainCtor, Pair]) == [PlainCtor, Pair] or matching rank.
	{
		var got struct {
			Variants []int `json:"variants"`
		}
		_ = json.Unmarshal(res[qi], &got)
		qi++
		goSmall := testplan.SmallVariants(pkg.Builder, []check.Type{byName["CyOp"], byName["PlainCtor"], byName["Pair"]})
		if len(got.Variants) != len(goSmall) {
			t.Fatalf("SmallVariants count mismatch: got %v, want %d", got.Variants, len(goSmall))
		}
	}
	// PlanTest(BoolPairTest)
	{
		var got struct {
			Count   int      `json:"count"`
			Listed  bool     `json:"listed"`
			HasTies bool     `json:"hasTies"`
			Cases   []string `json:"cases"`
		}
		_ = json.Unmarshal(res[qi], &got)
		qi++
		goP := testplan.Test(pkg.Tests[0])
		if got.Count != goP.Count || got.Listed != goP.Listed || got.HasTies != (len(goP.Ties) > 0) || len(got.Cases) != goP.Count {
			t.Fatalf("PlanTest mismatch: got %+v, want count=%d listed=%v ties=%v", got, goP.Count, goP.Listed, len(goP.Ties) > 0)
		}
		for k := 0; k < goP.Count; k++ {
			vs := goP.At(k)
			wantRow := formatGoValue(vs[0]) + "," + formatGoValue(vs[1])
			if got.Cases[k] != wantRow {
				t.Fatalf("Plan.At(%d): got %q, want %q", k, got.Cases[k], wantRow)
			}
		}
	}
	for v := 0; v <= 4; v++ {
		for _, x := range []int64{-3, -2, 0, 1, 2} {
			var got struct {
				Result bool `json:"result"`
			}
			_ = json.Unmarshal(res[qi], &got)
			qi++
			want := testplan.CallbackSecond(v, x)
			if got.Result != want {
				t.Fatalf("CallbackSecond(%d, %d): got %v, want %v", v, x, got.Result, want)
			}
		}
	}
}

func agreeTestplanCorpus(t *testing.T, ctx context.Context, dump string) {
	// Run testplan-pkg across checked corpus folders (sampled across std & examples without exceeding test budget).
	dirs := corpusDirs(t)
	var picked []string
	for _, d := range dirs {
		r := rel(d)
		if strings.HasPrefix(r, "examples/calc") || strings.HasPrefix(r, "examples/counter") ||
			strings.HasPrefix(r, "examples/kvstore") || strings.HasPrefix(r, "internal/sketch/std/option") ||
			strings.HasPrefix(r, "internal/sketch/std/slices") {
			picked = append(picked, d)
		}
	}
	if len(picked) == 0 {
		t.Fatal("no corpus directories selected for testplan-pkg")
	}
	tmp := t.TempDir()
	var manifestLines []string
	type goPkgExpect struct {
		path  string
		tests map[string]testplan.Plan
	}
	var expects []goPkgExpect
	eachGoPackage(picked, func(i int, pkg *check.Package) {
		if pkg == nil {
			return
		}
		files := ergoFiles(t, picked[i])
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		ePath := filepath.Join(tmp, fmt.Sprintf("ext_%d", i))
		fPath := filepath.Join(tmp, fmt.Sprintf("foreign_%d", i))
		writeLines(t, ePath, ext)
		writeLines(t, fPath, foreign)
		testsMap := map[string]testplan.Plan{}
		for _, tc := range pkg.Tests {
			testsMap[tc.Name] = testplan.Test(tc)
		}
		expects = append(expects, goPkgExpect{path: pkg.Path, tests: testsMap})
		manifestLines = append(manifestLines, strings.Join(append([]string{pkg.Path, ePath, fPath}, files...), " "))
	})
	if len(manifestLines) == 0 {
		t.Fatal("no Go-checked corpus directories for testplan-pkg")
	}
	mPath := filepath.Join(tmp, "manifest")
	writeLines(t, mPath, manifestLines)
	out := runRawDump(t, ctx, dump, "testplan-pkg", "-std", stdDir(), "-corpus", mPath)
	curPkg := -1
	gotTests := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "@pkg ") {
			fields := strings.Fields(line)
			curPkg, _ = strconv.Atoi(fields[1])
			continue
		}
		if strings.HasPrefix(line, "test ") && curPkg >= 0 && curPkg < len(expects) {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				gotTests[fmt.Sprintf("%d:%s", curPkg, fields[1])] = line
			}
		}
	}
	for i, ex := range expects {
		for nm, gp := range ex.tests {
			line, ok := gotTests[fmt.Sprintf("%d:%s", i, nm)]
			if !ok {
				t.Fatalf("pkg %s test %s missing from testplan-pkg output:\n%s", ex.path, nm, out)
			}
			wantTag := fmt.Sprintf("listed=%v count=%d ties=%v", gp.Listed, gp.Count, len(gp.Ties) > 0)
			if !strings.Contains(line, wantTag) {
				t.Fatalf("pkg %s test %s mismatch:\ngot:  %s\nwant: %s", ex.path, nm, line, wantTag)
			}
		}
	}
}

func checkSourcesWithPlacement(t *testing.T, pkgPath string, srcs map[string]string) (*check.Package, []*syntax.File, []check.Diagnostic) {
	t.Helper()
	names := make([]string, 0, len(srcs))
	for n := range srcs {
		names = append(names, n)
	}
	sort.Strings(names)
	var files []*syntax.File
	for _, n := range names {
		f, err := syntax.Parse(srcs[n])
		if err != nil {
			t.Fatalf("parse %s: %v", n, err)
		}
		f.Name = n
		files = append(files, f)
	}
	pkg, ds := check.CheckOptions(pkgPath, files, nil, check.Options{PlaceLaws: true})
	for _, d := range ds {
		if d.Code != check.CodeLawPlace {
			t.Fatalf("%s: check error (%s): %v", pkgPath, d.Code, d)
		}
	}
	return pkg, files, ds
}

func agreeLawfix(t *testing.T, ctx context.Context, dump string) {
	type lawfixCase struct {
		name         string
		pkgName      string
		unexport     bool
		run          bool
		externalRefs []string
		files        map[string]string
	}
	casesList := []lawfixCase{
		{
			name:     "single-law-helper-delete",
			pkgName:  "main",
			unexport: true,
			run:      true,
			files: map[string]string{
				"a.ergo":    "func Alpha(x int) int { x + 1 }\n\nfunc prod(x int) int { Alpha(x) }\n",
				"laws.ergo": "// HelpAlpha doubles Alpha.\nfunc HelpAlpha(x int) int { Alpha(x) * 2 }\n\nlaw AlphaPositive(x int) {\n\tHelpAlpha(x) == Alpha(x)*2\n}\n",
			},
		},
		{
			name:    "multi-subject-sort-and-ambiguity",
			pkgName: "main",
			files: map[string]string{
				"a.ergo":    "func Sort(xs []int) []int { xs }\n\nfunc useA(xs []int) []int { Sort(xs) }\n",
				"b.ergo":    "func Sorted(xs []int) bool { xs.Len() >= 0 }\n\nfunc useB(xs []int) bool { Sorted(xs) }\n",
				"laws.ergo": "law SortMatchesSorted(xs []int) {\n\tSorted(Sort(xs))\n}\n\nlaw SortedOrder(xs []int) {\n\tSorted(Sort(xs))\n}\n",
			},
		},
		{
			name:    "import-sort-clash-and-unalias",
			pkgName: "main",
			files: map[string]string{
				"a.ergo":    "import alias strings\n\nfunc strings() int { 1 }\n\nfunc Target(s string) string { let _ = strings(); alias.TrimSpace(s) }\n\nfunc useT(s string) string { Target(s) }\n",
				"laws.ergo": "import stdstrings strings\nimport option\n\nlaw TargetTrims(s string) {\n\tstdstrings.TrimSpace(Target(s)) == Target(s)\n}\n\nlaw KeepUnused() {\n\toption.None == option.None\n}\n",
			},
		},
		{
			name:    "leftover-tests-join-tests-file",
			pkgName: "main",
			files: map[string]string{
				"a.ergo":     "func Target(x int) int { x }\n\nfunc u(x int) int { Target(x) }\n",
				"laws.ergo":  "law TargetId(x int) { Target(x) == x }\n\ntest UnplacedExtra() { assert 1 == 1 }\n",
				"tests.ergo": "test BaseUnplaced() { assert 2 == 2 }\n",
			},
		},
		{
			name:    "suffixed-test-file-not-renamed",
			pkgName: "main",
			files: map[string]string{
				"helper_test.ergo": "func Target(x int) int { x }\n\ntest UnplacedLocal() { assert 1 == 1 }\n",
			},
		},
		{
			name:         "run-respects-external-refs",
			pkgName:      "pkg",
			unexport:     true,
			run:          true,
			externalRefs: []string{"KeepExt"},
			files: map[string]string{
				"a.ergo":    "func Target(x int) int { x }\n\nfunc u(x int) int { Target(x) }\n",
				"laws.ergo": "func KeepExt(x int) int { Target(x) }\nfunc DropExt(x int) int { Target(x) }\n\nlaw TargetHolds(x int) {\n\tKeepExt(x) == DropExt(x)\n}\n",
			},
		},
	}

	for _, lc := range casesList {
		t.Run(lc.name, func(t *testing.T) {
			pkg, files, goDiags := checkSourcesWithPlacement(t, "example.com/"+lc.pkgName, lc.files)
			var goPlace []map[string]any
			for _, d := range goDiags {
				if d.Code == check.CodeLawPlace {
					goPlace = append(goPlace, map[string]any{
						"file":    files[d.File].Name,
						"line":    d.Line,
						"col":     d.Column,
						"code":    d.Code,
						"message": d.Message,
					})
				}
			}

			// Compare S115 placement diagnostics, exported helpers, Unexport, Plan, and Run.
			var jsonFiles []map[string]string
			names := make([]string, 0, len(lc.files))
			for nm := range lc.files {
				names = append(names, nm)
			}
			sort.Strings(names)
			for _, nm := range names {
				jsonFiles = append(jsonFiles, map[string]string{"name": nm, "src": lc.files[nm]})
			}
			payload := map[string]any{
				"pkgName":      pkg.Name,
				"dir":          "/pkg",
				"unexport":     lc.unexport,
				"write":        lc.run,
				"run":          lc.run,
				"externalRefs": lc.externalRefs,
				"files":        jsonFiles,
			}
			raw := runRawDump(t, ctx, dump, "lawfix-spec", "--input", writeJSONTemp(t, payload))
			var got struct {
				PlacementDiags []struct {
					File    string `json:"file"`
					Line    int    `json:"line"`
					Col     int    `json:"col"`
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"placementDiags"`
				ExportedHelpers []string `json:"exportedHelpers"`
				Unexport        struct {
					Renames []string          `json:"renames"`
					Files   map[string]string `json:"files"`
				} `json:"unexport"`
				Plan struct {
					Deleted  []string          `json:"deleted"`
					Renamed  []string          `json:"renamed"`
					Helpers  []string          `json:"helpers"`
					Unplaced []string          `json:"unplaced"`
					Moves    []lawfix.Move     `json:"moves"`
					Files    map[string]string `json:"files"`
				} `json:"plan"`
				RunReport string `json:"runReport"`
			}
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("unmarshal lawfix-spec: %v\n%s", err, raw)
			}
			if len(got.PlacementDiags) != len(goPlace) {
				t.Fatalf("S115 count mismatch:\ngot:  %+v\nwant: %+v", got.PlacementDiags, goPlace)
			}
			for i, wp := range goPlace {
				gp := got.PlacementDiags[i]
				if gp.File != wp["file"] || gp.Line != wp["line"] || gp.Col != wp["col"] || gp.Code != wp["code"] || gp.Message != wp["message"] {
					t.Fatalf("S115[%d] mismatch:\ngot:  %+v\nwant: %+v", i, gp, wp)
				}
			}

			var goExp []string
			for _, d := range pkg.LawExportedHelpers {
				goExp = append(goExp, d.Name.Name)
			}
			if !slices.Equal(got.ExportedHelpers, goExp) {
				t.Fatalf("ExportedHelpers mismatch: got %v, want %v", got.ExportedHelpers, goExp)
			}

			if lc.unexport {
				extSet := map[string]bool{}
				for _, e := range lc.externalRefs {
					extSet[e] = true
				}
				var cands []*syntax.FuncDecl
				for _, d := range pkg.LawExportedHelpers {
					if !extSet[d.Name.Name] {
						cands = append(cands, d)
					}
				}
				goUnFiles, goRenames := lawfix.Unexport(pkg, files, cands)
				if !slices.Equal(got.Unexport.Renames, goRenames) || !reflect.DeepEqual(got.Unexport.Files, goUnFiles) {
					t.Fatalf("Unexport mismatch:\ngot:  renames=%v files=%v\nwant: renames=%v files=%v", got.Unexport.Renames, got.Unexport.Files, goRenames, goUnFiles)
				}
			}

			goPlan, err := lawfix.Plan(pkg, files)
			if err != nil {
				t.Fatalf("Go lawfix.Plan: %v", err)
			}
			movesMatch := len(got.Plan.Moves) == len(goPlan.Moves)
			if movesMatch {
				for i, gm := range got.Plan.Moves {
					wm := goPlan.Moves[i]
					if gm.Kind != wm.Kind || gm.Law != wm.Law || gm.From != wm.From || gm.To != wm.To || !slices.Equal(gm.Helpers, wm.Helpers) {
						movesMatch = false
						break
					}
				}
			}
			if !slices.Equal(got.Plan.Deleted, goPlan.Deleted) || !slices.Equal(got.Plan.Renamed, goPlan.Renamed) ||
				!slices.Equal(got.Plan.Helpers, goPlan.Helpers) || !slices.Equal(got.Plan.Unplaced, goPlan.Unplaced) || !movesMatch {
				t.Fatalf("Plan summary mismatch:\ngot:  %+v\nwant: %+v", got.Plan, goPlan)
			}
			if !reflect.DeepEqual(got.Plan.Files, goPlan.Files) {
				t.Fatalf("Plan files mismatch:\ngot:\n%v\nwant:\n%v", got.Plan.Files, goPlan.Files)
			}

			if lc.run {
				tmpDir := t.TempDir()
				pkgDir := filepath.Join(tmpDir, "pkg")
				if err := os.MkdirAll(pkgDir, 0o755); err != nil {
					t.Fatal(err)
				}
				// Write an external file in another subfolder when externalRefs are tested.
				if len(lc.externalRefs) > 0 {
					extDir := filepath.Join(tmpDir, "other")
					_ = os.MkdirAll(extDir, 0o755)
					_ = os.WriteFile(filepath.Join(tmpDir, "ergo.mod"), []byte("module example.com/m\n"), 0o644)
					var lines []string
					for _, ref := range lc.externalRefs {
						lines = append(lines, fmt.Sprintf("func u%s() int { pkg.%s(0) }", ref, ref))
					}
					_ = os.WriteFile(filepath.Join(extDir, "use.ergo"), []byte("import example.com/m/pkg\n"+strings.Join(lines, "\n")+"\n"), 0o644)
				} else {
					_ = os.WriteFile(filepath.Join(tmpDir, "ergo.mod"), []byte("module example.com/m\n"), 0o644)
				}
				for nm, src := range lc.files {
					if err := os.WriteFile(filepath.Join(pkgDir, nm), []byte(src), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				rep, err := lawfix.Run(lawfix.Options{Paths: []string{pkgDir}, Write: true})
				if err != nil {
					t.Fatalf("Go lawfix.Run: %v", err)
				}
				// Normalize pkgDir path to /pkg before comparing against in-memory Run report.
				wantReport := strings.ReplaceAll(rep.Text(), pkgDir, "/pkg")
				if got.RunReport != wantReport {
					t.Fatalf("RunReport mismatch:\ngot:\n%s\nwant:\n%s", got.RunReport, wantReport)
				}
			}
		})
	}
}
