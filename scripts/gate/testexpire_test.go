package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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

// TestReachableTestCachesAreExpired is the property of the expiry (spec
// Q-R5-1): for every step the gate declares, the Go cache that its command
// environment resolves to, and the one each reduced form resolves to
// (without GOCACHE; without GOCACHE and XDG_CACHE_HOME), is in the set the
// gate expires, and a run expires each one of them: go clean -testcache,
// under the environment that names it, writes its testexpire.txt. The
// caller's GOCACHE, XDG_CACHE_HOME and HOME are three new directories, so
// the three forms name three distinct caches.
func TestReachableTestCachesAreExpired(t *testing.T) {
	if realTools["go"] == "" {
		t.Skip("no go on PATH")
	}
	root, steps := declaredGateSteps(t)
	base := t.TempDir()
	home := filepath.Join(base, "home")
	direct, xdg := filepath.Join(base, "gocache"), filepath.Join(base, "xdg")
	for _, dir := range []string{direct, filepath.Join(xdg, "go-build"), filepath.Join(home, ".cache", "go-build")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOCACHE", direct)
	t.Setenv("XDG_CACHE_HOME", xdg)
	t.Setenv("HOME", home)
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
		for _, form := range [][]string{environment, without(environment, "GOCACHE"), without(environment, "GOCACHE", "XDG_CACHE_HOME")} {
			dir := goEnvCache(t, root, form)
			resolved[dir] = true
			if !slices.Contains(expired, dir) {
				t.Errorf("step %q: a child environment resolves to the Go cache %s, which the gate does not expire (%q)", step.Name, dir, expired)
			}
		}
	}
	for _, want := range []string{direct, filepath.Join(xdg, "go-build"), filepath.Join(home, ".cache", "go-build")} {
		if !resolved[want] {
			t.Errorf("no step environment resolves to %s; the property is vacuous for that form (resolved %v)", want, resolved)
		}
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
// it (Go then uses $HOME/.cache/go-build). The inner test reads input.txt
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
	// A HOME of its own: the fresh environment without GOCACHE uses its
	// .cache/go-build, a cache no other test shares.
	t.Setenv("HOME", t.TempDir())
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
