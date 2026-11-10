package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/report"
)

// TestCheckEvaluatesWithoutBuild drives `ergo check` in process (the
// command adapter, checkCommand) over generated programs, as one file or
// as a package folder of two files, each rewritten in place from run to
// run: pure tests that pass, fail an assert, let a typed failure escape,
// hit a defect or call a Go function (a skip), laws that hold or not, an
// unused declaration that calls Go, and sometimes a static error. Go is
// not on PATH and the emission cache and temporary roots are empty
// folders. For each run the test wants: the exit status of the model (2
// defect, 1 refuted or static error, 0 else); an evaluation only for a
// source that checks, with the model's records in ergo test's order and
// a replay command for each record that did not pass; the evaluation's
// revision equal to the envelope's, which is the SHA-256 of the file's
// current bytes for one file; and nothing written to either root. No
// process-level run is needed: with Go absent from PATH any fallback to a
// Go build fails in process too, and the empty roots show none started.
// Mutants: check-eval-bypassed (check prints the envelope without
// evaluating), check-host-fallback (check hands a source whose evaluation
// did not pass everything to ergo test), eval-stale-source-identity (the
// evaluation names a text other than the analysed one: a folder's).
// A failing seed prints its sources, the envelope and
// ERGO_CHECK_EVAL_SEED=<seed> to replay it.
func TestCheckEvaluatesWithoutBuild(t *testing.T) {
	empty := t.TempDir()
	xdg, tmp := t.TempDir(), t.TempDir()
	t.Setenv("PATH", empty)
	t.Setenv("XDG_CACHE_HOME", xdg)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("GOFLAGS", "")
	work := t.TempDir()
	file := filepath.Join(work, "p.ergo")
	folder := filepath.Join(work, "pkg")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}

	seeds := []uint64{}
	if s := os.Getenv("ERGO_CHECK_EVAL_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := range uint64(40) {
			seeds = append(seeds, i+1)
		}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0xc4ec))
		p := genCheckProgram(r)
		helpers, tests := p.sources()
		names, want := p.model()
		inFolder := r.IntN(2) == 0
		target := file
		if inFolder {
			target = folder
			writeFile(t, filepath.Join(folder, "a.ergo"), helpers)
			writeFile(t, filepath.Join(folder, "b.ergo"), tests)
		} else {
			writeFile(t, file, helpers+tests)
		}
		stdout, code := checkInProcess(t, target)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d (folder %v): %s\nsource:\n%s%s\nstdout: %s\nreplay: ERGO_CHECK_EVAL_SEED=%d go test -run TestCheckEvaluatesWithoutBuild ./cmd/ergo", seed, inFolder, fmt.Sprintf(format, args...), helpers, tests, stdout, seed)
		}
		var env report.CheckEnvelope
		if err := json.Unmarshal(stdout, &env); err != nil {
			fail("the output is not one check envelope: %v", err)
		}
		for _, root := range []string{xdg, tmp} {
			if entries, _ := os.ReadDir(root); len(entries) != 0 {
				fail("check wrote into %s: %v", root, entries)
			}
		}
		if p.staticBad {
			if code != 1 || env.Checked || env.Evaluation != nil {
				fail("a static error: exit %d, checked %v, evaluation %v; want 1, false, none", code, env.Checked, env.Evaluation)
			}
			continue
		}
		e := env.Evaluation
		if !env.Checked || e == nil {
			fail("a source that checks has no evaluation")
		}
		var gotNames, got []string
		for _, o := range e.Obligations {
			gotNames, got = append(gotNames, o.Name), append(got, o.Status)
			if (o.Status == "passed") != (o.Replay == "") || (o.Replay != "" && !strings.HasPrefix(o.Replay, "ergo test "+target+" --seed 1")) {
				fail("%s (%s) has replay %q", o.Name, o.Status, o.Replay)
			}
		}
		if !slices.Equal(gotNames, names) || !slices.Equal(got, want) {
			fail("model %v %v, check %v %v", names, want, gotNames, got)
		}
		wantCode := 0
		switch {
		case slices.Contains(want, "defect"):
			wantCode = 2
		case slices.Contains(want, "refuted"):
			wantCode = 1
		}
		if code != wantCode {
			fail("exit %d, want %d", code, wantCode)
		}
		if e.Revision != env.Revision || (!inFolder && e.Revision != report.Revision(helpers+tests)) {
			fail("the evaluation names revision %s, the envelope %s", e.Revision, env.Revision)
		}
	}
}

// checkInProcess runs checkCommand on target and returns what it printed
// on stdout and the exit status main gives its error.
func checkInProcess(t *testing.T, target string) ([]byte, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.Bytes()
	}()
	runErr := command([]string{"check", target})
	os.Stdout = saved
	w.Close()
	out := <-done
	r.Close()
	code := 0
	switch e := runErr.(type) {
	case nil:
	case exitStatusError:
		code = e.code
	case usageError:
		code = 2
	default:
		code = 1
	}
	return out, code
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

type checkObligation struct {
	name string
	kind int // 0 pass, 1 assert, 2 typed failure, 3 defect, 4 Go call (skip); laws: 0 holds, 1 false
}

type checkProgram struct {
	tests, laws []checkObligation
	staticBad   bool
}

func genCheckProgram(r *rand.Rand) checkProgram {
	p := checkProgram{staticBad: r.IntN(8) == 0}
	for i := range 1 + r.IntN(5) {
		p.tests = append(p.tests, checkObligation{name: fmt.Sprintf("T%d", i), kind: r.IntN(5)})
	}
	for i := range r.IntN(3) {
		p.laws = append(p.laws, checkObligation{name: fmt.Sprintf("law L%d", i), kind: r.IntN(2)})
	}
	return p
}

// sources are the program's helpers and its obligations, one file each
// in a folder.
func (p checkProgram) sources() (helpers, tests string) {
	var h strings.Builder
	h.WriteString("extern go strings.ToUpper func upper(s string) string\n\n")
	h.WriteString("type Bad { Code int }\n\n")
	h.WriteString("func boom(n int) int fails Bad {\n\tif n > 0 {\n\t\treturn Bad{Code: n}\n\t}\n\tn\n}\n\n")
	h.WriteString("func half(x int) int { x / x }\n\n")
	h.WriteString("func twice(n int) int { n + n }\n\n")
	// Unused and calling Go: it skips nothing.
	h.WriteString("func shout() string { upper(\"q\") }\n\n")
	if p.staticBad {
		h.WriteString("func wrong() int { \"s\" }\n\n")
	}
	var b strings.Builder
	for _, o := range p.tests {
		body := []string{
			"assert twice(2) == 4",
			"assert twice(2) == 5",
			"let x = try boom(1)\n\tassert x == 0",
			"assert half(0) == 1",
			"assert upper(\"a\") == \"A\"",
		}[o.kind]
		fmt.Fprintf(&b, "test %s {\n\t%s\n}\n\n", o.name, body)
	}
	for _, o := range p.laws {
		op := []string{"==", "!="}[o.kind]
		fmt.Fprintf(&b, "law %s(n int) { twice(n) - n %s n }\n\n", strings.TrimPrefix(o.name, "law "), op)
	}
	return h.String(), b.String()
}

// model is each record's status: a defect stops the run.
func (p checkProgram) model() (names, statuses []string) {
	stopped := false
	add := func(name, status string) {
		names = append(names, name)
		if stopped {
			status = "not run"
		}
		statuses = append(statuses, status)
		stopped = stopped || status == "defect"
	}
	for _, o := range p.tests {
		add(o.name, []string{"passed", "refuted", "refuted", "defect", "skipped"}[o.kind])
	}
	for _, o := range p.laws {
		add(o.name, []string{"passed", "refuted"}[o.kind])
	}
	return names, statuses
}
