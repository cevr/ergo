package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// goEnvCache is go env GOCACHE in dir under environment, run with the real
// go command, apart from the gate's own query (reachableTestCaches).
func goEnvCache(t *testing.T, dir string, environment []string) string {
	t.Helper()
	command := exec.Command(realTools["go"], "env", "GOCACHE")
	command.Dir = dir
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go env GOCACHE: %v", err)
	}
	return strings.TrimSpace(string(output))
}

// without is environment without the named variables, built here and not
// by the gate's reducedEnvironments.
func without(environment []string, names ...string) []string {
	var kept []string
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if !slices.Contains(names, name) {
			kept = append(kept, entry)
		}
	}
	return kept
}

// lookup is the value of the last entry for name in environment, or "".
func lookup(environment []string, name string) string {
	value := ""
	for _, entry := range environment {
		if key, v, ok := strings.Cut(entry, "="); ok && key == name {
			value = v
		}
	}
	return value
}

// withExpiry turns the expiry on for t's gate runs (TestMain turns it off,
// expireBeforeRun) with Go caches of t's own: GOCACHE, XDG_CACHE_HOME and
// HOME are new directories and GOENV is off, so no form of a step's
// environment names a cache that another process uses.
func withExpiry(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("GOCACHE", filepath.Join(base, "gocache"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "xdg"))
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("GOENV", "off")
	expireBeforeRun = true
	t.Cleanup(func() { expireBeforeRun = false })
}

// TestExpiryIsOnOutsideTests: the gate expires before every run unless its
// own TestMain says otherwise. The declaration is true, and no file of the
// gate that is not a test writes it.
func TestExpiryIsOnOutsideTests(t *testing.T) {
	fset := token.NewFileSet()
	packages, err := parser.ParseDir(fset, ".", func(info fs.FileInfo) bool { return !strings.HasSuffix(info.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := false
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.ValueSpec:
					for i, name := range node.Names {
						if name.Name == "expireBeforeRun" {
							value, ok := node.Values[i].(*ast.Ident)
							declared = ok && value.Name == "true"
						}
					}
				case *ast.AssignStmt:
					for _, lhs := range node.Lhs {
						if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "expireBeforeRun" {
							t.Errorf("%s: gate code writes expireBeforeRun; only the gate's TestMain may turn the expiry off", fset.Position(node.Pos()))
						}
					}
				}
				return true
			})
		}
	}
	if !declared {
		t.Error("expireBeforeRun is not declared true")
	}
}

// TestFixtureGatesKeepTheCallersCaches: a fixture gate that a test of the
// gate runs leaves the caller's Go cache unexpired, so a real gate whose
// step runs these tests keeps its other steps' test results within the
// run (cause-r5.log).
func TestFixtureGatesKeepTheCallersCaches(t *testing.T) {
	if realTools["go"] == "" {
		t.Skip("no go on PATH")
	}
	dir := goEnvCache(t, ".", os.Environ())
	before, _ := os.ReadFile(filepath.Join(dir, "testexpire.txt"))
	root := fixture(t)
	if code, out := runGate(t, root, t.TempDir(), []*Step{{Name: "go version", Argv: []string{"go", "version"}}}); code != 0 {
		t.Fatalf("fixture gate: exit %d\n%s", code, out)
	} else if strings.Contains(out, "gate: expired Go test results") {
		t.Errorf("a fixture gate expired Go test results:\n%s", out)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "testexpire.txt"))
	if !bytes.Equal(before, after) {
		t.Errorf("a fixture gate expired the caller's Go cache %s: testexpire.txt %q, then %q", dir, before, after)
	}
}

// expectedGoCaches is the Go cache that each form of an environment
// (cacheReductions: as it is; without GOCACHE; without GOCACHE and
// XDG_CACHE_HOME) resolves to on goos, when the environment sets GOCACHE to
// gocache, XDG_CACHE_HOME to xdg and HOME to home and GOENV is off. It
// follows cmd/go, not the gate: cache.DefaultDir takes GOCACHE, else
// os.UserCacheDir()/go-build, and os.UserCacheDir is $HOME/Library/Caches on
// darwin and ios (XDG_CACHE_HOME is not read) and $XDG_CACHE_HOME, else
// $HOME/.cache, on the other Unix systems (Go 1.27 src/os/file.go
// UserCacheDir). ok is false on a system the gate does not run on (windows,
// plan9).
func expectedGoCaches(goos, gocache, xdg, home string) (forms []string, ok bool) {
	var withXDG, withoutXDG string
	switch goos {
	case "windows", "plan9":
		return nil, false
	case "darwin", "ios":
		withXDG = filepath.Join(home, "Library", "Caches", "go-build")
		withoutXDG = withXDG
	default:
		withXDG = filepath.Join(xdg, "go-build")
		withoutXDG = filepath.Join(home, ".cache", "go-build")
	}
	return []string{gocache, withXDG, withoutXDG}, true
}

// TestExpectedGoCaches pins expectedGoCaches for both systems the gate runs
// on, so the property test below is not written for one of them: on darwin
// the two reduced forms name the same cache under HOME, and XDG_CACHE_HOME
// names none (gate-testcache-gap darwin-r5.log: Linux paths there made the
// property fail).
func TestExpectedGoCaches(t *testing.T) {
	for _, row := range []struct {
		goos string
		want []string
	}{
		{"linux", []string{"/g", "/x/go-build", "/h/.cache/go-build"}},
		{"freebsd", []string{"/g", "/x/go-build", "/h/.cache/go-build"}},
		{"darwin", []string{"/g", "/h/Library/Caches/go-build", "/h/Library/Caches/go-build"}},
		{"ios", []string{"/g", "/h/Library/Caches/go-build", "/h/Library/Caches/go-build"}},
	} {
		got, ok := expectedGoCaches(row.goos, "/g", "/x", "/h")
		if !ok || !slices.Equal(got, row.want) {
			t.Errorf("%s: expectedGoCaches = %q, %v; want %q", row.goos, got, ok, row.want)
		}
	}
	for _, goos := range []string{"windows", "plan9"} {
		if _, ok := expectedGoCaches(goos, "/g", "/x", "/h"); ok {
			t.Errorf("%s: expectedGoCaches claims a layout for a system the gate does not run on", goos)
		}
	}
}

// TestReachableTestCachesAreExpired is the property of the expiry (spec
// Q-R5-1): for every step the gate declares, the Go cache that its command
// environment resolves to, and the one each reduced form resolves to
// (without GOCACHE; without GOCACHE and XDG_CACHE_HOME), is in the set the
// gate expires, and a run expires each one of them: go clean -testcache,
// under the environment that names it, writes its testexpire.txt. The
// caller's GOCACHE, XDG_CACHE_HOME and HOME are three new directories, and
// each form must resolve to the cache cmd/go gives it on this system
// (expectedGoCaches): three distinct caches on Linux, two on darwin. Each
// expected cache exists before the run, since go clean -testcache writes
// nothing in a cache directory that does not exist (and such a directory
// holds no result to replay).
func TestReachableTestCachesAreExpired(t *testing.T) {
	if realTools["go"] == "" {
		t.Skip("no go on PATH")
	}
	root, steps := declaredGateSteps(t)
	base := t.TempDir()
	home := filepath.Join(base, "home")
	direct, xdg := filepath.Join(base, "gocache"), filepath.Join(base, "xdg")
	wantForms, ok := expectedGoCaches(runtime.GOOS, direct, xdg, home)
	if !ok {
		t.Skipf("the gate does not run on %s", runtime.GOOS)
	}
	for _, dir := range wantForms {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOCACHE", direct)
	t.Setenv("XDG_CACHE_HOME", xdg)
	t.Setenv("HOME", home)
	t.Setenv("GOENV", "off")
	g := &gate{root: root}
	caches, err := g.reachableTestCaches(steps)
	if err != nil {
		t.Fatal(err)
	}
	var expired []string
	for _, cache := range caches {
		expired = append(expired, cache.dir)
	}
	resolved := map[string]bool{}
	for _, step := range steps {
		environment := g.commandEnvironment(step)
		// The layout cmd/go gives this step's own variables (a step may set
		// its own GOCACHE).
		stepForms, _ := expectedGoCaches(runtime.GOOS, lookup(environment, "GOCACHE"), lookup(environment, "XDG_CACHE_HOME"), lookup(environment, "HOME"))
		for i, form := range [][]string{environment, without(environment, "GOCACHE"), without(environment, "GOCACHE", "XDG_CACHE_HOME")} {
			dir := goEnvCache(t, root, form)
			resolved[dir] = true
			if !slices.Contains(expired, dir) {
				t.Errorf("step %q: a child environment resolves to the Go cache %s, which the gate does not expire (%q)", step.Name, dir, expired)
			}
			if dir != stepForms[i] {
				t.Errorf("step %q: form %d resolves to %s on %s, not %s (expectedGoCaches)", step.Name, i, dir, runtime.GOOS, stepForms[i])
			}
		}
	}
	for _, want := range wantForms {
		if !resolved[want] {
			t.Errorf("no step environment resolves to %s; the property is vacuous for that form (resolved %v)", want, resolved)
		}
	}
	if distinct := len(slices.Compact(slices.Sorted(slices.Values(wantForms)))); distinct < 2 {
		t.Errorf("the forms name %d distinct caches on %s; the property is vacuous", distinct, runtime.GOOS)
	}

	// A run expires each cache: its testexpire.txt names a time from the
	// run.
	started := time.Now()
	line, err := g.expireTestResults(steps)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(line)
	for dir := range resolved {
		content, err := os.ReadFile(filepath.Join(dir, "testexpire.txt"))
		if err != nil {
			t.Errorf("the run did not expire %s: %v", dir, err)
			continue
		}
		at, err := strconv.ParseInt(strings.TrimSpace(string(content)), 10, 64)
		if err != nil || time.Unix(0, at).Before(started) {
			t.Errorf("%s/testexpire.txt is %q, not a time from this run (%s)", dir, content, started)
		}
		if !strings.Contains(line, dir) {
			t.Errorf("the log line does not name %s: %s", dir, line)
		}
	}
}

// freshLauncher starts go test ./inner as a program does that gives its
// child an environment: inherited (nil Cmd.Env), or a fresh one built from a
// list of names, with GOCACHE or without it. No GOFLAGS name and no count
// occur in it.
const freshLauncher = `package main

import (
	"os"
	"os/exec"
)

func main() {
	child := exec.Command("go", "test", "./inner")
	names := map[string][]string{
		"fresh-gocache":    {"PATH", "HOME", "GOCACHE", "GOMEMLIMIT", "TMPDIR"},
		"fresh-no-gocache": {"PATH", "HOME", "GOMEMLIMIT", "TMPDIR"},
	}[os.Args[1]]
	for _, name := range names {
		child.Env = append(child.Env, name+"="+os.Getenv(name))
	}
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if child.Run() != nil {
		os.Exit(1)
	}
}
`

// freshInner reads input.txt only through a child process, which Go's test
// cache does not see.
const freshInner = `package inner

import (
	"os/exec"
	"strings"
	"testing"
)

func TestExternal(t *testing.T) {
	content, err := exec.Command("cat", "../input.txt").Output()
	if err != nil || strings.TrimSpace(string(content)) != "good" {
		t.Fatalf("external input bad: %s %v", content, err)
	}
}
`

// TestFreshChildEnvironmentsDoNotReplay is the round 4 verifier's B1 as a
// real regression: a step runs a program that starts go test with an
// inherited environment, a fresh one with GOCACHE, or a fresh one without
// it (Go then uses os.UserCacheDir()/go-build under HOME). The inner test reads input.txt
// through a child. Between two gate runs input.txt changes from good to bad:
// the second run fails on it and prints no (cached). The witness first shows
// that the launcher's inner result does replay outside the gate, so the
// expiry, and not an uncacheable test, is what makes the second run fresh.
// The merge tier gets the same check.
func TestFreshChildEnvironmentsDoNotReplay(t *testing.T) {
	if realTools["go"] == "" {
		t.Skip("no go on PATH")
	}
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOWORK", "off")
	withExpiry(t)
	for _, mode := range []string{"fresh-gocache", "fresh-no-gocache", "inherited"} {
		for _, tier := range []string{"gate", "merge"} {
			t.Run(mode+" "+tier, func(t *testing.T) {
				root := fixture(t)
				write(t, root, "go.mod", "module example.com/fresh\n\ngo 1.21\n")
				write(t, root, "inner/inner_test.go", freshInner)
				write(t, root, "cmd/launcher/main.go", freshLauncher)
				write(t, root, "input.txt", "good\n")
				launcher := []string{"go", "run", "./cmd/launcher", mode}
				// The witness: outside the gate, a second launch replays the
				// inner result.
				for round := range 2 {
					command := exec.Command(launcher[0], launcher[1:]...)
					command.Dir = root
					output, err := command.CombinedOutput()
					if err != nil {
						t.Fatalf("witness launch %d: %v\n%s", round+1, err, output)
					}
					if round == 1 && !bytes.Contains(output, []byte("(cached)")) {
						t.Fatalf("the inner result does not replay outside the gate; the check is vacuous:\n%s", output)
					}
				}
				step := &Step{Name: "launcher", Argv: launcher}
				if code, out := runGate(t, root, t.TempDir(), []*Step{step}); code != 0 {
					t.Fatalf("first gate run, good input: exit %d\n%s", code, out)
				}
				write(t, root, "input.txt", "bad\n")
				var code int
				var out string
				switch tier {
				case "gate":
					code, out = runGate(t, root, t.TempDir(), []*Step{step})
				case "merge":
					var buffer bytes.Buffer
					code = wholeFixture(t, root, []*Step{step}).mergeTier(&buffer)
					out = buffer.String()
				}
				t.Logf("second %s run, bad input: exit %d\n%s", tier, code, out)
				if code == 0 || !strings.Contains(out, "external input bad") {
					t.Errorf("the second %s run passed or failed for another reason on the changed input (exit %d)", tier, code)
				}
				if strings.Contains(out, "(cached)") {
					t.Errorf("the second %s run replayed a cached go test result", tier)
				}
			})
		}
	}
}
