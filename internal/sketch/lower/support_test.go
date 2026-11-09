package lower

import (
	"bytes"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The test harness: check a source with the real checker, lower it, and
// build many lowered programs as packages of one Go module, so one go
// command compiles, vets and links them all.

// testSourcePath is the admitted path the tests' //line directives name.
const testSourcePath = "/ergo-test/main.ergo"

// lowered is one source's outcome.
type lowered struct {
	name     string
	src      string
	file     *syntax.File
	pkg      *check.Package
	diags    []check.Diagnostic
	files    []File
	refused  []Refusal
	err      error // a lowering error other than a refusal
	panicked any
}

// lowerSource parses, checks and lowers src. Without entry, a program with
// no runnable main still lowers, with an empty Go main, so every
// declaration of a library-shaped fixture is lowered and compiled.
func lowerSource(name, src string, entry bool) *lowered {
	out := &lowered{name: name, src: src}
	if entry {
		probeLog("lower", probeSum([]byte(src)))
	} else {
		probeLog("lower-lib", probeSum([]byte(src)))
	}
	file, err := syntax.Parse(src)
	if err != nil {
		out.err = err
		return out
	}
	out.file = file
	out.pkg, out.diags = check.Check("main", []*syntax.File{file}, nil)
	if len(out.diags) > 0 {
		return out
	}
	func() {
		defer func() { out.panicked = recover() }()
		u := Unit{Package: out.pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
		code, err := lowerPackage(u, !entry)
		var refused *Refused
		switch {
		case errors.As(err, &refused):
			out.refused = refused.Refusals
		case err != nil:
			out.err = err
		default:
			out.files = []File{{Path: "go.mod", Data: []byte(goMod)}, {Path: "main.go", Data: code}}
		}
	}()
	return out
}

// mainGo is the lowered main.go.
func (o *lowered) mainGo() string {
	for _, f := range o.files {
		if f.Path == "main.go" {
			return string(f.Data)
		}
	}
	return ""
}

// buildAll writes each program as package p/NAME of one module and runs go
// vet and go build on them all, and returns each program's executable. A
// program that does not compile or vet fails the test with its Go
// diagnostics and its source. Without link, the question is only whether
// the Go type-checks: typecheckGo answers it in process, and
// TestCorpusCompilesWithGc keeps gc's answer in the full tier.
func buildAll(t *testing.T, programs []*lowered, link bool) map[string]string {
	t.Helper()
	if !link {
		typecheckGo(t, programs)
		return nil
	}
	if len(programs) == 0 {
		return nil
	}
	root := t.TempDir()
	write := func(rel string, data []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := unparsedGo(programs); err != nil {
		t.Error(err)
		return nil
	}
	write("go.mod", []byte(goMod))
	for _, f := range Runtime() {
		write(f.Path, f.Data)
	}
	byPkg := map[string]*lowered{}
	for _, p := range programs {
		write("p/"+p.name+"/main.go", []byte(p.mainGo()))
		byPkg[p.name] = p
	}
	run := func(args ...string) (string, bool) {
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err == nil
	}
	report := func(what, output string) {
		pkgs := map[string]bool{}
		for _, m := range regexp.MustCompile(`(?m)^# ergo\.local/program/p/(\S+)`).FindAllStringSubmatch(output, -1) {
			pkgs[m[1]] = true
		}
		for _, m := range regexp.MustCompile(`(?m)p/([^/\s]+)/main\.go`).FindAllStringSubmatch(output, -1) {
			pkgs[m[1]] = true
		}
		var names []string
		for n := range pkgs {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Errorf("go %s failed for %v:\n%s", what, names, firstLines(output, 60))
		for i, n := range names {
			if p := byPkg[n]; p != nil && i < 3 {
				t.Logf("source of %s:\n%s\nGo:\n%s", n, p.src, p.mainGo())
			}
		}
	}
	// Ergo has no unreachable-code rule, so a checked program may keep
	// statements after a total match whose every arm returns; the vet check
	// "unreachable" reports that source, not the lowering (state.md follow-up).
	// -trimpath keeps the module's temporary directory out of the compiled
	// runtime packages, so each buildAll module reuses the build cache's
	// runtime instead of compiling it again (gate-fast plan section 4).
	if out, ok := run("vet", "-trimpath", "-unreachable=false", "./p/..."); !ok {
		report("vet", out)
		return nil
	}
	bin := filepath.Join(root, "bin")
	if out, ok := run("build", "-trimpath", "-o", bin+string(filepath.Separator), "./p/..."); !ok {
		report("build", out)
		return nil
	}
	exes := map[string]string{}
	for _, p := range programs {
		exe := filepath.Join(bin, p.name)
		if _, err := os.Stat(exe + ".exe"); err == nil {
			exe += ".exe"
		}
		exes[p.name] = exe
		probeBuilt(exe, p.mainGo())
	}
	return exes
}

// unparsedGo is the parse guard of buildAll (lower-gaps, batch 11): the
// first program whose main.go go/parser does not parse, named with its
// Ergo source and its Go, so an unparsable emit is a lowering failure of
// one named program, not a build error of the whole module.
func unparsedGo(programs []*lowered) error {
	for _, p := range programs {
		if _, err := parser.ParseFile(token.NewFileSet(), p.name+"/main.go", p.mainGo(), parser.SkipObjectResolution); err != nil {
			return fmt.Errorf("%s: the emitted Go does not parse (a compiler defect): %v\nsource:\n%s\nGo:\n%s", p.name, err, p.src, p.mainGo())
		}
	}
	return nil
}

// runExe runs a built program and returns its stdout, stderr and exit
// code.
func runExe(t *testing.T, exe string) (string, string, int) {
	t.Helper()
	probeRan(exe)
	cmd := exec.Command(exe)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runProgram(t, cmd)
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

// runProgram runs a built program as cmd.Run does, under the test kit's
// memory limit (ERGO_PROGRAM_MEMORY, 2 GiB by default) and the highest
// oom_score_adj (internal/proclimit; docs/tooling.md, "Test memory"). A
// program the limit stopped fails the test with the program and the limit.
// Every helper that runs a built program goes through it.
func runProgram(t testing.TB, cmd *exec.Cmd) error {
	t.Helper()
	err := runBounded(t, cmd)
	var limited *proclimit.LimitError
	if errors.As(err, &limited) {
		t.Fatal(limited)
	}
	return err
}

// runBounded is runProgram that returns the *proclimit.LimitError of a
// program the limit stopped.
func runBounded(t testing.TB, cmd *exec.Cmd) error {
	t.Helper()
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	return proclimit.Run(cmd, limit)
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = append(lines[:n], fmt.Sprintf("… (%d more bytes)", len(s)-len(strings.Join(lines[:n], "\n"))))
	}
	return strings.Join(lines, "\n")
}

// mustLower lowers a source that must lower, with its entry.
func mustLower(t *testing.T, name, src string) *lowered {
	t.Helper()
	o := lowerSource(name, src, true)
	switch {
	case o.err != nil:
		t.Fatalf("%s: %v\n%s", name, o.err, src)
	case len(o.diags) > 0:
		t.Fatalf("%s: checker diagnostics %v\n%s", name, o.diags, src)
	case o.panicked != nil:
		t.Fatalf("%s: lowering panicked: %v\n%s", name, o.panicked, src)
	case len(o.refused) > 0:
		t.Fatalf("%s: refused %+v\n%s", name, o.refused, src)
	}
	return o
}
