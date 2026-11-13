package eval_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketchfront"
)

// TestEvalAgreesOnExamples runs every example program's main at check time
// (ORACLE O4; eval.RunMain), case by case, with the case's arguments,
// standard input and environment, in an empty working directory, and
// holds its standard output, standard error and exit status to the case's
// recorded files, which the built program matches (scripts/examples). A
// case's setup.sh runs as the examples runner runs it, with sh in an empty
// directory that is also $HOME, and the tree it leaves seeds the
// simulated file system (setupFiles). A ts program's case (Bun runs it,
// and drives the built program), a setup that leaves what the simulated
// file system does not hold (a special file), and a run that reaches a form the evaluator
// does not run are listed and counted, never passed; any other difference
// fails. The log's last lines are the census: how many cases agreed, were
// skipped and why.
//
// It is the full tier's (it checks every example program): -short skips it.
// It builds and runs no Go: the recorded files are the built program's.
// The setup scripts run under the kernel's memory bound (proclimit).
func TestEvalAgreesOnExamples(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: runs every example program")
	}
	root, err := filepath.Abs("../../../examples")
	if err != nil {
		t.Fatal(err)
	}
	progs, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	agreed, ran := 0, 0
	skips := map[string][]string{}
	var differ []string
	for _, prog := range progs {
		dir := filepath.Join(root, prog.Name())
		cases, err := os.ReadDir(filepath.Join(dir, "cases"))
		if err != nil {
			continue // no cases: nothing to run
		}
		if tags, err := os.ReadFile(filepath.Join(dir, "tags")); err == nil && slices.Contains(strings.Fields(string(tags)), "ts") {
			// A ts program's case runs Bun, which drives the built
			// program (scripts/examples, ts.go): the args are Bun's.
			for _, c := range cases {
				skips["a ts case, which Bun runs"] = append(skips["a ts case, which Bun runs"], prog.Name()+"/"+c.Name())
			}
			continue
		}
		mainDir, ok := mainPackage(dir)
		if !ok {
			skips["no main package"] = append(skips["no main package"], prog.Name())
			continue
		}
		a, err := sketchfront.CheckDir(mainDir)
		if err != nil {
			t.Fatalf("%s: %v", prog.Name(), err)
		}
		pkgs := a.Checked()
		if len(pkgs) == 0 {
			skips["the program does not check here"] = append(skips["the program does not check here"], prog.Name())
			continue
		}
		for _, c := range cases {
			label := prog.Name() + "/" + c.Name()
			g, err := readGolden(filepath.Join(dir, "cases", c.Name()))
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			var files []eval.File
			if g.setup {
				var why string
				files, why = setupFiles(t, filepath.Join(dir, "cases", c.Name()))
				if why != "" {
					skips[why] = append(skips[why], label)
					continue
				}
			}
			ran++
			got, err := eval.RunMain(pkgs[len(pkgs)-1], eval.DefaultBudget(), eval.Process{Args: g.args, Env: g.env, Stdin: g.stdin, Files: files})
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			if got.Skipped != "" || got.Incomplete != "" {
				why := got.Skipped + got.Incomplete
				skips[why] = append(skips[why], fmt.Sprintf("%s@%s:%d", label, filepath.Base(got.Site.File), got.Site.Line))
				continue
			}
			if d := disagreement(g, got); d != "" {
				differ = append(differ, fmt.Sprintf("%s: %s", label, d))
				continue
			}
			agreed++
		}
	}
	skipped := 0
	var reasons []string
	for why, at := range skips {
		skipped += len(at)
		reasons = append(reasons, why)
	}
	sort.Slice(reasons, func(i, j int) bool { return len(skips[reasons[i]]) > len(skips[reasons[j]]) })
	t.Logf("census: %d cases run, %d agree, %d differ; %d listed as skipped", ran, agreed, len(differ), skipped)
	for _, why := range reasons {
		at := skips[why]
		t.Logf("  %d\t%s\t%v", len(at), why, at[:min(3, len(at))])
	}
	if len(differ) > 0 {
		t.Fatalf("%d cases differ from their recorded output:\n%s", len(differ), strings.Join(differ, "\n"))
	}
	if agreed == 0 {
		t.Fatal("no case ran to its end at check time")
	}
}

// mainPackage is the folder of the program's main: the program's own
// folder, or cmd/<name> in a program of packages.
func mainPackage(dir string) (string, bool) {
	if _, err := os.Stat(filepath.Join(dir, "main.ergo")); err == nil {
		return dir, true
	}
	cmds, err := os.ReadDir(filepath.Join(dir, "cmd"))
	if err != nil || len(cmds) != 1 {
		return "", false
	}
	return filepath.Join(dir, "cmd", cmds[0].Name()), true
}

// golden is a case's recorded run (scripts/examples, caseFiles).
type golden struct {
	args, env           []string
	stdin, stdout, errs string
	status              int
	setup               bool
}

func readGolden(dir string) (golden, error) {
	var g golden
	read := func(name string) (string, bool, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return string(b), err == nil, err
	}
	// As the runner reads them: the final newline ends the last line, an
	// empty line is an empty string, and an empty file has no lines.
	lines := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	}
	var err error
	var s string
	var ok bool
	if s, _, err = read("args"); err != nil {
		return g, err
	}
	g.args = lines(s)
	if s, _, err = read("env"); err != nil {
		return g, err
	}
	g.env = lines(s)
	if g.stdin, _, err = read("stdin"); err != nil {
		return g, err
	}
	if g.stdout, _, err = read("expected.out"); err != nil {
		return g, err
	}
	if g.errs, _, err = read("expected.err"); err != nil {
		return g, err
	}
	if s, ok, err = read("expected.status"); err != nil {
		return g, err
	} else if ok {
		if g.status, err = strconv.Atoi(strings.TrimSpace(s)); err != nil {
			return g, err
		}
	}
	_, g.setup, err = read("setup.sh")
	return g, err
}

// disagreement says how a run differs from its recorded one, "" when it
// does not: its standard output, its standard error, its status.
func disagreement(g golden, got eval.Exit) string {
	var what []string
	if got.Stdout != g.stdout {
		what = append(what, fmt.Sprintf("stdout %q, recorded %q", clip(got.Stdout), clip(g.stdout)))
	}
	if got.Stderr != g.errs {
		what = append(what, fmt.Sprintf("stderr %q, recorded %q", clip(got.Stderr), clip(g.errs)))
	}
	if got.Status != g.status {
		what = append(what, fmt.Sprintf("status %d, recorded %d", got.Status, g.status))
	}
	return strings.Join(what, "; ")
}

// setupFiles runs the case's setup.sh as the examples runner does (sh
// with the case directory as $1, in an empty directory that is $HOME) and
// returns the tree it leaves, relative to that directory. why names what
// the simulated file system cannot hold (a special file),
// or the setup's failure; files is then nil.
func setupFiles(t *testing.T, caseDir string) (files []eval.File, why string) {
	t.Helper()
	work := t.TempDir()
	cmd := exec.Command("sh", filepath.Join(caseDir, "setup.sh"), caseDir)
	cmd.Dir = work
	cmd.Env = []string{"HOME=" + work}
	if p, ok := os.LookupEnv("PATH"); ok {
		cmd.Env = append(cmd.Env, "PATH="+p)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := proclimit.Run(cmd, proclimit.DefaultLimit); err != nil {
		t.Fatalf("%s: setup.sh: %v\n%s", caseDir, err, out.String())
	}
	err := filepath.WalkDir(work, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == work {
			return err
		}
		rel, err := filepath.Rel(work, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			files = append(files, eval.File{Path: filepath.ToSlash(rel), Link: target})
		case d.IsDir():
			files = append(files, eval.File{Path: filepath.ToSlash(rel), Dir: true, Perm: int(info.Mode().Perm())})
		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files = append(files, eval.File{Path: filepath.ToSlash(rel), Data: string(data), Perm: int(info.Mode().Perm())})
		default:
			why = "a setup.sh that makes a special file"
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		t.Fatalf("%s: read the setup's tree: %v", caseDir, err)
	}
	if why != "" {
		return nil, why
	}
	return files, ""
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// TestHarnessFlagsEachStream holds disagreement to its job: a run that
// differs from its recording in exactly one of stdout, stderr and status
// is a disagreement that names that one, and an equal run is none.
//
// Mutants only it kills (testdata/mutants.json): oracle-o4-harness-compares-self,
// oracle-o4-harness-drops-stderr, oracle-o4-harness-ignores-exit.
func TestHarnessFlagsEachStream(t *testing.T) {
	g := golden{stdout: "a\n", errs: "b\n", status: 3}
	same := eval.Exit{Stdout: "a\n", Stderr: "b\n", Status: 3}
	if d := disagreement(g, same); d != "" {
		t.Fatalf("an equal run differs: %s", d)
	}
	for name, got := range map[string]eval.Exit{
		"stdout": {Stdout: "x\n", Stderr: "b\n", Status: 3},
		"stderr": {Stdout: "a\n", Stderr: "x\n", Status: 3},
		"status": {Stdout: "a\n", Stderr: "b\n", Status: 0},
	} {
		if d := disagreement(g, got); !strings.HasPrefix(d, name) {
			t.Errorf("a run whose %s differs gives %q", name, d)
		}
	}
}
