package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestErgoModuleRequirement is MD1's end-to-end check (grammar.md, Source
// files 1, Modules): a program imports a package of another Ergo module,
// example.com/counter, served by a file:// proxy in the test's folder (no
// network). Before go.mod requires it, ergo check names the fetch command;
// ergo get refuses a module whose ergo.mod names another path and puts
// go.mod and go.sum back (MD2); `ergo get example.com/counter@v1.0.0` (the
// command it names) writes the pin and the hashes and fills the module
// cache; then ergo check passes with
// only a stub go on PATH that records any start, ergo run runs the program,
// and ergo test runs the root's test and the law of the dependency's
// operation type against the root's provider, and not the dependency's own
// test. Last, a file edited in the cached copy is S104 at the import.
//
// Mutant it alone catches: the loader ignores GOMODCACHE, so it reads
// another module cache than the go command fills (md1-modcache-env-ignored).
// It also catches a dependency's test run by the root's ergo test
// (md1-dependency-test-run, which TestErgoPackages catches for a package of
// the same module), and it is the one check that the loader's hash and the
// command it names agree with the go command's download. For ergo get it
// alone catches a fetched module not read again (md2-get-unverified) and a
// refused fetch that leaves go.mod and go.sum changed (md2-get-kept). For
// MD3 the module uses Go: the check refuses it until ergo.mod trusts it,
// ergo inspect deps lists its Go use and the trust line, and the program
// then builds and runs the dependency's Go call. It alone catches the Go
// uses dropped from ergo inspect deps (md3-deps-go-dropped) and the trust
// state hidden there (md3-deps-trust-hidden).
//
// Builds Go because the claims are the go command's: its download, its
// go.sum lines and its extracted copy, and the test binary that ergo test
// links and runs; no in-process check sees them. Each run is bounded by
// proclimit.
func TestErgoModuleRequirement(t *testing.T) {
	needsBuild(t)
	binary := buildTestCLI(t)
	tmp := t.TempDir()
	app := filepath.Join(tmp, "app")
	proxy := filepath.Join(tmp, "proxy")
	serveModule(t, proxy, "example.com/counter", "v1.0.0", map[string]string{
		"go.mod":   "module example.com/counter\n\ngo 1.27\n",
		"ergo.mod": "module example.com/counter\n",
		"scale/scale.ergo": `import go strings

// Shout is s in upper case, by Go's strings.ToUpper.
func Shout(s string) string {
	strings.ToUpper(s)
}

// Scale is the contract of a scaling step.
type Scale {
	Times(n int) int,

	// Nothing scales to nothing.
	law TimesZero() {
		Times(0) == 0
	},
}

func secret() int {
	1
}

test DependencyTestStaysHome {
	assert secret() == 2
}
`,
	})
	serveModule(t, proxy, "example.com/misnamed", "v1.0.0", map[string]string{
		"go.mod":         "module example.com/misnamed\n\ngo 1.27\n",
		"ergo.mod":       "module example.com/other\n",
		"part/part.ergo": "func One() int {\n\t1\n}\n",
	})
	writeFiles(t, app, map[string]string{
		"ergo.mod": "module app\n",
		"go.mod":   "module app\n\ngo 1.27\n",
		"cmd/app/main.ergo": `import example.com/counter/scale

// Double is the root's provider of the dependency's operation type.
type Double {}

func Times(d Double, n int) int {
	n * 2
}

func main() needs Console {
	with context { scale.Scale: Double } {
		Console.Println("\(scale.Scale.Times(21))")
	}
	Console.Println(scale.Shout("ok"))
}

test DoubleScales {
	with context { scale.Scale: Double } {
		assert scale.Scale.Times(2) == 4
	}
}
`,
	})
	env := goModuleEnv(tmp, proxy)
	stubDir := filepath.Join(tmp, "stub")
	mark := filepath.Join(tmp, "go-started")
	writeFiles(t, stubDir, map[string]string{"go": "#!/bin/sh\n: > " + mark + "\nexit 1\n"})
	if err := os.Chmod(filepath.Join(stubDir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubEnv := append(slices.DeleteFunc(slices.Clone(env), func(kv string) bool { return strings.HasPrefix(kv, "PATH=") }), "PATH="+stubDir)
	run := func(env []string, name string, args ...string) (string, int) {
		t.Helper()
		command := exec.Command(name, args...)
		command.Dir, command.Env = app, env
		var out bytes.Buffer
		command.Stdout, command.Stderr = &out, &out
		err := proclimit.Run(command, 4<<30)
		var exit *exec.ExitError
		switch {
		case err == nil:
			return out.String(), 0
		case errors.As(err, &exit):
			return out.String(), exit.ExitCode()
		}
		t.Fatalf("%s %v: %v\n%s", name, args, err, out.String())
		return "", -1
	}

	const unpinned = `cmd/app/main.ergo:1:8: error S104: unknown package "example.com/counter/scale": it is not std, not in module app, and in no module that go.mod requires; require the module that holds it: ergo get MODULE@VERSION`
	if out, code := run(stubEnv, binary, "diagnostics", "cmd/app"); code == 0 || !strings.Contains(out, unpinned) {
		t.Fatalf("ergo diagnostics before the requirement: exit %d, want %q in\n%s", code, unpinned, out)
	}
	// A module whose ergo.mod names another path is fetched by go get, then
	// refused, and go.mod and go.sum are put back.
	goMod, err := os.ReadFile(filepath.Join(app, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	const misnamed = "example.com/misnamed: the ergo.mod of module example.com/misnamed@v1.0.0 names module example.com/other, not example.com/misnamed; go.mod and go.sum are as they were"
	if out, code := run(env, binary, "get", "example.com/misnamed@v1.0.0"); code != 1 || !strings.Contains(out, misnamed) {
		t.Fatalf("ergo get example.com/misnamed@v1.0.0: exit %d, want 1 and %q in\n%s", code, misnamed, out)
	}
	if now, err := os.ReadFile(filepath.Join(app, "go.mod")); err != nil || !bytes.Equal(now, goMod) {
		t.Fatalf("go.mod after a refused ergo get: %v\n%s", err, now)
	}
	if _, err := os.Stat(filepath.Join(app, "go.sum")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("go.sum after a refused ergo get: %v, want none", err)
	}
	if out, code := run(env, binary, "get", "example.com/counter@v1.0.0"); code != 0 || !strings.HasSuffix(out, "required example.com/counter v1.0.0\n") {
		t.Fatalf("ergo get example.com/counter@v1.0.0: exit %d\n%s", code, out)
	}
	// The module uses Go, so the root must trust it (MD3); ergo inspect
	// deps shows what it would trust.
	const untrusted = `example.com/counter@v1.0.0/scale/scale.ergo:1:8: error S108: module example.com/counter@v1.0.0 uses Go (import go strings), and the ergo.mod of module app does not trust its Go`
	if out, code := run(stubEnv, binary, "diagnostics", "cmd/app"); code == 0 || !strings.Contains(out, untrusted) {
		t.Fatalf("ergo diagnostics with an untrusted module's Go: exit %d, want %q in\n%s", code, untrusted, out)
	}
	const listed = "example.com/counter@v1.0.0 (Go not trusted)\n  example.com/counter/scale: not checked\n    import go strings\n"
	if out, code := run(stubEnv, binary, "inspect", "deps", "cmd/app"); code != 1 || !strings.HasPrefix(out, listed) {
		t.Fatalf("ergo inspect deps before the trust line: exit %d, want 1 and %q first in\n%s", code, listed, out)
	}
	writeFiles(t, app, map[string]string{"ergo.mod": "module app\ntrust go example.com/counter\n"})
	if out, code := run(stubEnv, binary, "diagnostics", "cmd/app"); code != 0 {
		t.Fatalf("ergo diagnostics with the requirement: exit %d\n%s", code, out)
	}
	const trusted = "example.com/counter@v1.0.0 (Go trusted)\n  example.com/counter/scale: needs nothing\n    import go strings\n"
	if out, code := run(stubEnv, binary, "inspect", "deps", "cmd/app"); code != 0 || out != trusted {
		t.Fatalf("ergo inspect deps with the trust line: exit %d, want %q, got\n%s", code, trusted, out)
	}
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("ergo diagnostics started a go command")
	}
	if out, code := run(env, binary, "run", "cmd/app"); code != 0 || !strings.HasSuffix(out, "42\nOK\n") {
		t.Fatalf("ergo run cmd/app: exit %d, want 42 and OK\n%s", code, out)
	}
	out, code := run(env, binary, "test", "cmd/app")
	if code != 0 || !strings.Contains(out, "DoubleScales") || !strings.Contains(out, "TimesZero for") || strings.Contains(out, "DependencyTestStaysHome") {
		t.Fatalf("ergo test cmd/app: exit %d, want DoubleScales and the law TimesZero for the root's Double, and not the dependency's test\n%s", code, out)
	}

	// The copy's folders are writable (-modcacherw): a file added there,
	// even one no package reads, is refused.
	added := filepath.Join(tmp, "modcache", "example.com", "counter@v1.0.0", "scale", "notes.txt")
	if err := os.WriteFile(added, []byte("added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const changed = `cmd/app/main.ergo:1:8: error S104: unknown package "example.com/counter/scale": the module cache's copy of example.com/counter v1.0.0 does not match go.sum`
	if out, code := run(stubEnv, binary, "diagnostics", "cmd/app"); code == 0 || !strings.Contains(out, changed) {
		t.Fatalf("ergo diagnostics after an edit of the cached copy: exit %d, want %q in\n%s", code, changed, out)
	}
}
