package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/experimental/irspike"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// irRun is one run of a built program: its input and what it did.
type irRun struct {
	args           []string
	stdin          string
	stdout, stderr string
	exit           int
}

// TestBuildGoesThroughIR: `ergo build` lowers the program through IR and
// compiles it with the patched compile it builds into its cache from the
// release it runs (internal/toolchain, Patch), on the direct path and on
// go build alike, and the program agrees with the reference: the source
// lowering compiled by the stock compile (sketchfront without IR, go
// build). The programs are examples/lru with its recorded cases, and
// three generated programs of the IR subset (irspike.Generate), whose
// functions have IR bodies only: a compile that does not read the IR
// file fails on their missing bodies.
//
// It builds Go because the question is what the patched compile made of
// the CLI's IR module; no in-process check reaches compiled bodies. Each
// program runs under a 1 GiB memory bound the kernel enforces. Mutants:
// the CLI lowers by source (irt-build-source-path); the direct compile
// passes no -ergoir (irt-direct-no-ergoir, the build falls back to go
// build, which LINK= shows); go build passes none (irt-gobuild-no-ergoir).
func TestBuildGoesThroughIR(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go with the patched compile")
	}
	binary := buildTestCLI(t)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	type program struct {
		name, dir, target string
		runs              []irRun
		generated         bool
	}
	lru := program{name: "lru", dir: filepath.Join(root, "examples", "lru"), target: "./cmd/lru"}
	cases, err := filepath.Glob(filepath.Join(lru.dir, "cases", "*"))
	if err != nil || len(cases) == 0 {
		t.Fatalf("lru's cases: %v %v", cases, err)
	}
	for _, c := range cases {
		var r irRun
		if data, err := os.ReadFile(filepath.Join(c, "args")); err == nil {
			r.args = strings.Fields(string(data))
		}
		if data, err := os.ReadFile(filepath.Join(c, "stdin")); err == nil {
			r.stdin = string(data)
		}
		lru.runs = append(lru.runs, r)
	}
	programs := []program{lru}
	for seed := uint64(1); seed <= 3; seed++ {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "main.ergo"), []byte(irspike.Generate(seed)), 0o644); err != nil {
			t.Fatal(err)
		}
		programs = append(programs, program{name: fmt.Sprintf("generated seed %d", seed), dir: dir, target: "main.ergo", runs: []irRun{{}}, generated: true})
	}
	xdg := filepath.Join(t.TempDir(), "xdg")
	for _, p := range programs {
		out := t.TempDir()
		bins := map[string]string{"direct": filepath.Join(out, "direct"), "go build": filepath.Join(out, "gobuild"), "reference": filepath.Join(out, "reference")}
		for _, path := range []string{"direct", "go build"} {
			command := exec.Command(binary, "build", "-work", "-o", bins[path], p.target)
			command.Dir = p.dir
			command.Env = append(os.Environ(), "XDG_CACHE_HOME="+xdg)
			if path == "go build" {
				command.Env = append(command.Env, build.GoBuildVariable+"=go")
			}
			stdout, stderr, code := runSmokeCommand(t, command)
			if code != 0 {
				t.Fatalf("%s: ergo build (%s): exit %d %s\n%s", p.name, path, code, stdout, stderr)
			}
			link := regexp.MustCompile(`(?m)^LINK=(.*)$`).FindStringSubmatch(string(stderr))
			if path == "direct" && (link == nil || !strings.HasPrefix(link[1], "direct")) {
				t.Fatalf("%s: ergo build did not take the direct path: %s", p.name, stderr)
			}
			ir, err := os.ReadFile(filepath.Join(workDir(t, string(stderr)), build.IRFile))
			if err != nil {
				t.Fatalf("%s: the build's module has no IR file: %v", p.name, err)
			}
			var f struct{ Funcs []json.RawMessage }
			if err := json.Unmarshal(ir, &f); err != nil {
				t.Fatal(err)
			}
			if p.generated && len(f.Funcs) == 0 {
				t.Fatalf("%s: no function of the IR subset has an IR body", p.name)
			}
		}
		referenceBuild(t, p.dir, p.target, bins["reference"])
		for _, r := range p.runs {
			want := runBounded(t, bins["reference"], r)
			for _, path := range []string{"direct", "go build"} {
				if got := runBounded(t, bins[path], r); got.stdout != want.stdout || got.stderr != want.stderr || got.exit != want.exit {
					t.Errorf("%s (%s) with %q and stdin %q: got %+v, the reference %+v", p.name, path, r.args, r.stdin, got, want)
				}
			}
		}
	}
}

// referenceBuild builds the program at dir/target by the source lowering
// and go build with the go command on PATH: the stock compile.
func referenceBuild(t *testing.T, dir, target, out string) {
	t.Helper()
	path := filepath.Join(dir, target)
	var a sketchfront.Analysis
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		if a, err = sketchfront.CheckDir(path); err != nil {
			t.Fatal(err)
		}
	} else {
		text, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		a = sketchfront.Check(string(text))
	}
	var stderr bytes.Buffer
	_, err := sketchfront.Build(a, sketchfront.BuildRequest{
		Mode:      build.ModeBuild,
		Source:    path,
		Output:    out,
		Cache:     build.Cache{Root: t.TempDir()},
		Toolchain: build.Toolchain{Go: "go", GoBuild: true},
		Stderr:    &stderr,
	})
	if err != nil {
		t.Fatalf("reference build of %s: %v\n%s", path, err, stderr.String())
	}
}

// runBounded runs bin with r's input under a 1 GiB memory bound.
func runBounded(t *testing.T, bin string, r irRun) irRun {
	t.Helper()
	cmd := exec.Command(bin, r.args...)
	cmd.Stdin = strings.NewReader(r.stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := proclimit.Run(cmd, 1<<30)
	r.stdout, r.stderr = stdout.String(), stderr.String()
	if strings.HasPrefix(r.stderr, "panic: ") {
		// The trace below a panic's message names Go frames, which
		// differ between the lowerings by construction (irspike).
		r.stderr, _, _ = strings.Cut(r.stderr, "\n")
	}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		r.exit = exit.ExitCode()
	case err != nil:
		t.Fatalf("run %s: %v", bin, err)
	}
	return r
}
