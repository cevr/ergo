package irspike

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/lower"
)

// tsTag marks the programs whose cases run a TypeScript client against the
// binary (tag ts, scripts/examples): their cases need bun and the client's
// dependencies, so this test builds both paths and runs their test builds,
// but not their cases.
const tsTag = "ts"

// hungAfter stops a program that does not end: a hung case, named, never a
// verdict on speed.
const hungAfter = 5 * time.Minute

// caseRun is one scripted run of a binary: the files of a case folder.
type caseRun struct {
	name  string
	dir   string
	args  []string
	env   []string
	stdin string // the stdin file, or ""
	setup bool
}

func readLines(file string) []string {
	data, err := os.ReadFile(file)
	if err != nil || len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func loadCases(prog string) []caseRun {
	dirs, _ := filepath.Glob(filepath.Join(prog, "cases", "*"))
	var out []caseRun
	for _, d := range dirs {
		c := caseRun{name: filepath.Base(d), dir: d, args: readLines(filepath.Join(d, "args")), env: readLines(filepath.Join(d, "env"))}
		if _, err := os.Stat(filepath.Join(d, "stdin")); err == nil {
			c.stdin = filepath.Join(d, "stdin")
		}
		if _, err := os.Stat(filepath.Join(d, "setup.sh")); err == nil {
			c.setup = true
		}
		out = append(out, c)
	}
	return out
}

// exec runs argv in cwd with env and stdin in a process group of its own,
// under the kernel memory bound; a run past hungAfter is killed with its
// group and named.
func execBounded(t *testing.T, argv []string, cwd string, env []string, stdin string) outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), hungAfter)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = cwd, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	if stdin != "" {
		f, err := os.Open(stdin)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		cmd.Stdin = f
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	_, err := MeasureBounded(cmd, programMemory)
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // anything the program left
	}
	if ctx.Err() != nil {
		t.Fatalf("%v hung past %s (killed)", argv, hungAfter)
	}
	o := outcome{stdout: out.String(), stderr: defectText(errb.String())}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		o.exit = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", argv, err)
	}
	return o
}

// defectText is a run's stderr without Go's goroutine trace, whose frames
// and addresses differ between the paths by construction.
func defectText(s string) string {
	if i := strings.Index(s, "\ngoroutine "); i >= 0 {
		return s[:i]
	}
	return s
}

func runCase(t *testing.T, bin string, c caseRun) outcome {
	t.Helper()
	work := t.TempDir()
	env := []string{"HOME=" + work, "PATH=" + os.Getenv("PATH")}
	env = append(env, c.env...)
	if c.setup {
		if o := execBounded(t, []string{"sh", filepath.Join(c.dir, "setup.sh"), c.dir}, work, env, ""); o.exit != 0 {
			t.Fatalf("%s: setup.sh exit %d: %s", c.dir, o.exit, o.stderr)
		}
	}
	return execBounded(t, append([]string{bin}, c.args...), work, env, c.stdin)
}

func tagged(prog, tag string) bool {
	for _, l := range readLines(filepath.Join(prog, "tags")) {
		if strings.TrimSpace(l) == tag {
			return true
		}
	}
	return false
}

// programOf is the example program folder a target folder belongs to.
func programOf(root, dir string) string {
	rel, _ := filepath.Rel(filepath.Join(root, "examples"), dir)
	return filepath.Join(root, "examples", strings.SplitN(filepath.ToSlash(rel), "/", 2)[0])
}

// emitTests lowers u's test build by path p into dir.
func emitTests(u lower.Unit, p Path, dir string) error {
	lowering := lower.LowerTests
	if p == IR {
		lowering = func(u lower.Unit) ([]lower.File, error) {
			files, _, err := lower.LowerIRTests(u)
			return files, err
		}
	}
	files, err := lowering(u)
	if err != nil {
		return err
	}
	return writeModule(dir, files)
}

// TestIRAgreesOnExamples is the agreement property over the examples
// (plan-tier §1.2): every example binary and every example test build,
// built by the source path (the reference) and by the IR path; every
// scripted case of a program run on both binaries, every test build run
// on both; stdout, exit status and the defect text must be equal. A target
// that does not build is a failure, never a skip, and the number of
// targets and cases run must equal the folders' (mutant
// irt-example-skipped). These tests build Go because the question is the
// patched compiler's machine code for the IR bodies, which no in-process
// check reaches.
func TestIRAgreesOnExamples(t *testing.T) {
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
	built, cases, wantCases, irBodies := 0, 0, 0, 0
	compare := func(what string, a, b outcome) {
		if a != b {
			t.Errorf("%s: the paths differ\nsource: %+v\nir:     %+v\nreplay: ERGO_IRSPIKE_EXAMPLE=%s go test -run TestIRAgreesOnExamples ./internal/experimental/irspike", what, a, b, what)
		}
	}
	for _, dir := range mains {
		prog := programOf(root, dir)
		if only != "" && filepath.Base(prog) != only {
			continue
		}
		u, err := Load(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		work := t.TempDir()
		sb, ib, rep := both(t, u, src, ir, work, BuildOptions{})
		built++
		irBodies += len(rep.IR)
		// The program's cases run on its own binary: examples/P/main.ergo
		// or examples/P/cmd/P/main.ergo.
		if dir != prog && dir != filepath.Join(prog, "cmd", filepath.Base(prog)) {
			continue
		}
		pcases := loadCases(prog)
		if tagged(prog, tsTag) {
			continue
		}
		wantCases += len(pcases)
		for _, c := range pcases {
			compare(filepath.Base(prog)+"/"+c.name, runCase(t, sb, c), runCase(t, ib, c))
			cases++
		}
	}
	for _, dir := range tests {
		prog := programOf(root, dir)
		if only != "" && filepath.Base(prog) != only {
			continue
		}
		u, err := Load(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		work := t.TempDir()
		var bins [2]string
		for i, p := range []Path{Source, IR} {
			mod := filepath.Join(work, p.String())
			if err := emitTests(u, p, mod); err != nil {
				t.Fatalf("%s test build (%s): %v", dir, p, err)
			}
			tc := src
			if p == IR {
				tc = ir
			}
			bins[i] = filepath.Join(work, p.String()+".test")
			if _, err := Build(tc, p, mod, bins[i], BuildOptions{}); err != nil {
				t.Fatalf("%s test build: %v", dir, err)
			}
		}
		built++
		run := caseRun{name: "test", args: []string{"--source", dir}}
		compare(filepath.Base(prog)+" test "+dir, runCase(t, bins[0], run), runCase(t, bins[1], run))
	}
	if only != "" {
		return
	}
	if want := len(mains) + len(tests); built != want || len(mains) != exampleBinaries(root) || cases != wantCases || wantCases == 0 {
		t.Fatalf("built %d of %d targets (%d binaries, the folders hold %d), ran %d of %d cases", built, want, len(mains), exampleBinaries(root), cases, wantCases)
	}
	t.Logf("%d targets built by both paths, %d cases agree, %d IR bodies", built, cases, irBodies)
}
