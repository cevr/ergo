// Package cases runs a program's recorded cases (examples/README.md,
// Cases; docs/tooling.md, ergo test --cases): each case folder holds the
// program's arguments, standard input, environment and a setup script,
// and the standard output, standard error and exit status the program must
// give, byte for byte. A case runs in a fresh empty folder, with HOME
// there, in a process group of its own and under the kernel's memory
// bound (internal/proclimit). `ergo test PKG --cases DIR` and the examples
// runner (scripts/examples) both run cases through this package, so there
// is one runner.
package cases

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/internal/proclimit"
)

// Files are the files a case folder may hold; expected.out is the only
// one it must hold.
var Files = []string{"args", "stdin", "env", "setup.sh", "expected.out", "expected.err", "expected.status"}

// Case is one case folder, read.
type Case struct {
	Name string
	Dir  string
	// Args are the lines of args; Env the KEY=VALUE lines of env.
	Args []string
	Env  []string
	// Stdin is the stdin file, or "" for empty input.
	Stdin string
	// Setup is true when the folder holds setup.sh.
	Setup bool
	// Out, Err and Status are the goldens; HasOut says expected.out exists.
	Out    []byte
	HasOut bool
	Err    []byte
	Status int
}

// Load reads every case folder of dir. A problem names its folder as
// BASE/NAME, BASE being dir's last element. A folder that holds no case is
// a problem: a run of no case passes nothing. record admits a case with no
// expected.out (the examples runner's -record writes it).
func Load(dir string, record bool) ([]*Case, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []string{err.Error()}
	}
	base := filepath.Base(dir)
	var cases []*Case
	var problems []string
	for _, entry := range entries {
		label := base + "/" + entry.Name()
		if !entry.IsDir() {
			problems = append(problems, label+": not a case folder")
			continue
		}
		c, found := LoadCase(filepath.Join(dir, entry.Name()), entry.Name(), record)
		for _, problem := range found {
			problems = append(problems, label+": "+problem)
		}
		if len(found) == 0 {
			cases = append(cases, c)
		}
	}
	if len(entries) == 0 {
		problems = append(problems, base+"/ holds no case")
	}
	return cases, problems
}

// LoadCase reads the case folder dir, named name. The case's paths are
// absolute: it runs in another folder, where a relative dir names nothing.
func LoadCase(dir, name string, record bool) (*Case, []string) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, []string{err.Error()}
	}
	c := &Case{Name: name, Dir: dir}
	var problems []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []string{err.Error()}
	}
	present := map[string]bool{}
	for _, entry := range entries {
		file := entry.Name()
		if !slices.Contains(Files, file) {
			problems = append(problems, fmt.Sprintf("unexpected %s; a case holds expected.out and may hold %s", file, strings.Join(slices.DeleteFunc(slices.Clone(Files), func(f string) bool { return f == "expected.out" }), ", ")))
			continue
		}
		if !entry.Type().IsRegular() {
			problems = append(problems, file+" is not a regular file")
			continue
		}
		present[file] = true
	}
	read := func(file string) []byte {
		if !present[file] {
			return nil
		}
		content, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			problems = append(problems, err.Error())
		}
		return content
	}
	if !present["expected.out"] && !record {
		problems = append(problems, "no expected.out")
	}
	c.Out, c.HasOut = read("expected.out"), present["expected.out"]
	c.Err = read("expected.err")
	c.Args = Lines(read("args"))
	for _, line := range Lines(read("env")) {
		if key, _, ok := strings.Cut(line, "="); !ok || key == "" {
			problems = append(problems, fmt.Sprintf("env: line %q is not KEY=VALUE", line))
		}
		c.Env = append(c.Env, line)
	}
	if present["expected.status"] {
		text := strings.TrimSuffix(string(read("expected.status")), "\n")
		status, err := strconv.Atoi(text)
		if err != nil || status < 0 || status > 255 || strconv.Itoa(status) != text {
			problems = append(problems, fmt.Sprintf("expected.status: %q is not an exit status (decimal 0 to 255)", text))
		}
		c.Status = status
	}
	if present["stdin"] {
		c.Stdin = filepath.Join(dir, "stdin")
	}
	c.Setup = present["setup.sh"]
	return c, problems
}

// Lines splits a file into its lines: the final newline ends the last
// line, an empty line is an empty string, and an empty file has no lines.
func Lines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
}

// Options are how Exec runs a case.
type Options struct {
	// Timeout stops a hung setup.sh or command (0: none).
	Timeout time.Duration
	// Memory bounds the command (0: none); setup.sh is never bounded.
	Memory uint64
	// Prepare, when set, gives the folder the case runs in and its
	// environment, from the fresh folder and the case's environment, and
	// what to undo after the case (the examples runner's TypeScript leg
	// runs in a copy of the program).
	Prepare func(work string, env []string) (cwd string, out []string, undo func(), err error)
}

// Failure is why a case could not run: its folder or its command could
// not be made or started, or setup.sh failed. Output, when set, is
// setup.sh's, within the cap.
type Failure struct {
	What   string
	Output *Outcome
}

// Exec runs the case c: in a fresh empty folder that Live made, with
// exactly PATH, HOME (the folder) and the case's env, setup.sh first (run
// as `sh setup.sh CASEDIR`), then command with the case's arguments and
// stdin. The Outcome is always of a command that started.
func (l *Live) Exec(c *Case, command []string, opt Options) (Outcome, *Failure) {
	work, err := l.MakeDir("", "ergo-case-")
	if err != nil {
		return Outcome{}, &Failure{What: fmt.Sprintf("make its directory: %v", err)}
	}
	defer l.RemoveDir(work)
	env := []string{"HOME=" + work}
	if path, ok := os.LookupEnv("PATH"); ok {
		env = append([]string{"PATH=" + path}, env...)
	}
	env = append(env, c.Env...)
	cwd := work
	if opt.Prepare != nil {
		var undo func()
		cwd, env, undo, err = opt.Prepare(work, env)
		if err != nil {
			return Outcome{}, &Failure{What: err.Error()}
		}
		defer undo()
	}
	if c.Setup {
		o, err := l.Execute([]string{"sh", filepath.Join(c.Dir, "setup.sh"), c.Dir}, cwd, env, "", opt.Timeout, 0)
		if err != nil || !o.Passed() {
			what := fmt.Sprintf("setup.sh exit %d", o.Status)
			switch {
			case err != nil:
				what = fmt.Sprintf("setup.sh could not start: %v", err)
			case o.OverCap() != "":
				what = "setup.sh " + o.OverCap()
			case o.TimedOut || o.Status < 0:
				what = "setup.sh" + o.Why(opt.Timeout)
			}
			f := &Failure{What: what}
			if err == nil && o.OverCap() == "" {
				f.Output = &o
			}
			return Outcome{}, f
		}
	}
	o, err := l.Execute(append(slices.Clone(command), c.Args...), cwd, env, c.Stdin, opt.Timeout, opt.Memory)
	if err != nil {
		return Outcome{}, &Failure{What: fmt.Sprintf("could not start: %v", err)}
	}
	return o, nil
}

// Verdict is how a case's run differs from its goldens. Fatal, when set,
// replaces every other verdict and prints no diff: the memory bound, the
// output cap or a hang ended the run.
type Verdict struct {
	Fatal          string
	What           []string
	Stdout, Stderr bool
}

// Passed says the run matched its goldens.
func (v Verdict) Passed() bool { return v.Fatal == "" && len(v.What) == 0 }

// Judge compares the run o of c, whose command was program and ran under
// memory and timeout, with c's goldens.
func Judge(c *Case, o Outcome, program string, memory uint64, timeout time.Duration) Verdict {
	switch {
	case o.Limited:
		return Verdict{Fatal: fmt.Sprintf("program %s exceeded its memory limit of %s (set %s to change it, 0 for none)", program, proclimit.Format(memory), proclimit.Variable)}
	case o.OverCap() != "":
		// A diff of a near-miss of 1 MiB is the dump the cap exists to stop.
		return Verdict{Fatal: o.OverCap()}
	case o.TimedOut:
		return Verdict{Fatal: strings.TrimPrefix(o.Why(timeout), " ")}
	}
	var v Verdict
	v.Stdout = !bytes.Equal(o.Stdout, c.Out) || !c.HasOut
	v.Stderr = !bytes.Equal(o.Stderr, c.Err)
	if v.Stdout {
		v.What = append(v.What, "stdout differs")
	}
	if v.Stderr {
		v.What = append(v.What, "stderr differs")
	}
	if o.Status != c.Status {
		v.What = append(v.What, fmt.Sprintf("status differs: got %s, want %d", o.StatusText(), c.Status))
	}
	return v
}

// WriteDiff writes the diff of each stream that differs.
func (v Verdict) WriteDiff(w io.Writer, c *Case, o Outcome) {
	if v.Stdout {
		fmt.Fprintf(w, "stdout:\n%s", Diff(c.Out, o.Stdout))
	}
	if v.Stderr {
		fmt.Fprintf(w, "stderr:\n%s", Diff(c.Err, o.Stderr))
	}
}

// WriteProblems writes the layout problems Load found, one FAIL line each,
// then "FAIL", and returns 1; with none it writes nothing and returns 0.
func WriteProblems(w io.Writer, problems []string) int {
	if len(problems) == 0 {
		return 0
	}
	for _, p := range problems {
		fmt.Fprintf(w, "FAIL %s\n", p)
	}
	fmt.Fprintln(w, "FAIL")
	return 1
}

// Run runs the loaded cases against the program binary under memory and
// writes one line per case, "ok NAME" or "FAIL NAME: why" with the diff,
// then "PASS (N cases)" or "FAIL (F of N cases)". It returns 1 when a case
// fails; else 0. Load refuses a folder of no case, so a run of no case
// never passes. A case runs with no deadline: it ends when its program
// does.
func (l *Live) Run(w io.Writer, binary string, loaded []*Case, memory uint64) int {
	failed := 0
	for _, c := range loaded {
		o, f := l.Exec(c, []string{binary}, Options{Memory: memory})
		if f != nil {
			fmt.Fprintf(w, "FAIL %s: %s\n", c.Name, f.What)
			if f.Output != nil {
				writeIndented(w, *f.Output)
			}
			failed++
			continue
		}
		v := Judge(c, o, binary, memory, 0)
		switch {
		case v.Fatal != "":
			fmt.Fprintf(w, "FAIL %s: %s\n", c.Name, v.Fatal)
			failed++
		case len(v.What) > 0:
			fmt.Fprintf(w, "FAIL %s: %s\n", c.Name, strings.Join(v.What, "; "))
			v.WriteDiff(w, c, o)
			failed++
		default:
			fmt.Fprintf(w, "ok %s\n", c.Name)
		}
	}
	if failed > 0 {
		fmt.Fprintf(w, "FAIL (%d of %d cases)\n", failed, len(loaded))
		return 1
	}
	fmt.Fprintf(w, "PASS (%d cases)\n", len(loaded))
	return 0
}

// writeIndented writes a command's output indented, so no line of it
// reads as a verdict.
func writeIndented(w io.Writer, o Outcome) {
	for _, stream := range [][]byte{o.Stdout, o.Stderr} {
		for _, line := range Lines(stream) {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
}
