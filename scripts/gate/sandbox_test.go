package main

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// unsandboxed runs the merge tier's uncached run on the host. Only tests of
// the tier's verdict logic over fixture steps that read host files use it;
// the sandbox has its own tests below.
func unsandboxed(sandboxPlan) (enclosure, error) { return hostEnclosure{}, nil }

type hostEnclosure struct{}

func (hostEnclosure) wrap(argv []string, _ string) []string { return argv }
func (hostEnclosure) close() error                          { return nil }

// sandboxMount is one mount in bwrap's argv, in order.
type sandboxMount struct {
	op, source, dest string
}

// parseSandboxArgv reads bwrap's argv as bwrap does and returns its mounts
// in order. An option it does not know fails the test: --dev-bind,
// --bind-try and --overlay are never among the known ones, and the
// property checks every --bind.
func parseSandboxArgv(t *testing.T, argv []string) []sandboxMount {
	t.Helper()
	arity := map[string]int{"--userns": 1, "--pidns": 1, "--unshare-pid": 0, "--die-with-parent": 0, "--info-fd": 1, "--proc": 1, "--dev": 1, "--tmpfs": 1, "--bind": 2, "--ro-bind": 2, "--symlink": 2, "--chdir": 1}
	var mounts []sandboxMount
	for i := 0; i < len(argv); {
		op := argv[i]
		if op == "--" {
			return mounts
		}
		n, ok := arity[op]
		if !ok || i+n >= len(argv) {
			t.Fatalf("bwrap argv has option %q the sandbox does not admit: %q", op, argv)
		}
		switch op {
		case "--tmpfs":
			mounts = append(mounts, sandboxMount{op, "", argv[i+1]})
		case "--ro-bind", "--bind":
			mounts = append(mounts, sandboxMount{op, argv[i+1], argv[i+2]})
		}
		i += n + 1
	}
	t.Fatalf("bwrap argv has no command: %q", argv)
	return nil
}

// effectiveMount is the mount a path is on: the last one in argv order
// whose destination holds it, or none (the sandbox's root tmpfs).
func effectiveMount(mounts []sandboxMount, path string) sandboxMount {
	found := sandboxMount{op: "--tmpfs", dest: "/"}
	for _, m := range mounts {
		if within(path, m.dest) {
			found = m
		}
	}
	return found
}

// sandboxProperty checks the sandbox's guarantees on bwrap's argv for the
// plan's environment, independently of checkSandboxPlan: no host path is
// writable (every binding is read-only, at its own path, but the object
// store at sourceMount), and every writable binding is its own directory
// under the run's (plan.Fresh); HOME, TMPDIR, GOTMPDIR, XDG_CACHE_HOME,
// GOCACHE and the checkout are each on one; and the read-only set exposes
// neither host HOME, /tmp, /var/tmp, GOCACHE, GOTMPDIR, $HOME/.cache,
// $HOME/Library/Caches, the runs' directory, nor a
// parent of one of them, nor a path under one but HOME.
func sandboxProperty(t *testing.T, plan sandboxPlan, env map[string]string) []string {
	t.Helper()
	var problems []string
	mounts := parseSandboxArgv(t, sandboxArgv(plan))
	// fresh is a writable binding from the run's own directory.
	fresh := func(m sandboxMount) bool {
		return m.op == "--bind" && filepath.IsAbs(plan.Fresh) && filepath.Dir(m.source) == filepath.Clean(plan.Fresh)
	}
	bound := map[string]bool{}
	for _, m := range mounts {
		if m.op == "--ro-bind" && m.source != m.dest && !(m.source == plan.Source && m.dest == sourceMount) {
			problems = append(problems, fmt.Sprintf("%s bound at another path %s", m.source, m.dest))
		}
		if m.op == "--bind" {
			if !fresh(m) {
				problems = append(problems, fmt.Sprintf("host path %s is writable at %s", m.source, m.dest))
			}
			if bound[m.source] {
				problems = append(problems, fmt.Sprintf("%s is bound twice", m.source))
			}
			bound[m.source] = true
		}
	}
	for _, name := range []string{"HOME", "TMPDIR", "XDG_CACHE_HOME", "GOCACHE", "GOTMPDIR", "checkout"} {
		if path := env[name]; path != "" {
			if m := effectiveMount(mounts, path); !fresh(m) {
				problems = append(problems, fmt.Sprintf("%s %s is on %s %s, not the run's own directory", name, path, m.op, m.source))
			}
		}
	}
	home := env["HOME"]
	exposed := []string{"/tmp", "/var/tmp", env["GOCACHE"], env["TMPDIR"], env["GOTMPDIR"], env["XDG_CACHE_HOME"], filepath.Join(home, ".cache"), filepath.Join(home, "Library", "Caches"), filepath.Dir(plan.Fresh)}
	for _, m := range mounts {
		if m.op != "--ro-bind" || m.dest == sourceMount {
			continue
		}
		if within(home, m.source) {
			problems = append(problems, fmt.Sprintf("read-only %s exposes HOME %s", m.source, home))
		}
		for _, path := range exposed {
			if path != "" && (within(path, m.source) || within(m.source, path)) {
				problems = append(problems, fmt.Sprintf("read-only %s exposes %s", m.source, path))
			}
		}
	}
	return problems
}

// TestSandboxArgvProperty generates environments (homes, caches, temporary
// directories, module caches and toolchains at and around each other),
// plans the sandbox for each through the gate's own planning, and checks
// that the plan is refused exactly when bwrap's argv for it would break a
// guarantee (sandboxProperty), and that an admitted plan breaks none.
func TestSandboxArgvProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	pick := func(choices ...string) string { return choices[random.IntN(len(choices))] }
	admitted, refused := 0, 0
	for i := range 4000 {
		home := pick("/home/u", "/Users/u", "/var/lib/u", "/tmp/u", "/srv/home/u")
		goCache := pick(home+"/.cache/go-build", home+"/Library/Caches/go-build", "/var/cache/go", "/tmp/go-build", home+"/go/pkg/mod/cache")
		xdg := pick("", home+"/.cache", home+"/xdg", "/srv/xdg", "/usr/cache")
		tempDir := pick("/tmp", "/var/tmp/u", home+"/.cache/tmp", "/srv/tmp")
		modCache := pick(home+"/go/pkg/mod", "/opt/gomod", home+"/.cache/gomod", home, "/home", "/srv/xdg/mod", "", "/usr/share/gomod")
		goRoot := pick(modCache+"/golang.org/toolchain@v0.0.1-go1.27.0.linux-amd64", "/usr/local/go", "/opt/go", home, "/tmp/go", goCache+"/root")
		checkout := pick(home+"/src/repo", "/srv/repo", "/tmp/repo", "/opt/gomod/repo")
		gateCache := pick("", home+"/.cache/ergo-gate", "/var/cache/gate")
		goTmpDir := pick("", tempDir+"/gotmp", "/srv/gotmp", "/opt/gomod/tmp")
		xdgCache := xdg
		if xdgCache == "" {
			xdgCache = home + "/.cache"
		}
		run := pick("/var/cache/gate/sandbox/r1", home+"/.cache/ergo-gate/sandbox/r1", "/usr/cache/sandbox/r1", "/opt/gomod/sandbox/r1", "sandbox/r1")
		plan := sandboxPlan{Head: "head", Checkout: checkout, Source: "/srv/repo/.git", Clean: true, Fresh: run,
			ReadOnly: sandboxReadOnly(modCache, goRoot), Links: [][2]string{{"/bin", "usr/bin"}},
			Scratch: sandboxScratch(home, xdgCache, goCache, tempDir, goTmpDir, gateCache, checkout)}
		plan.Forbidden, plan.Homes = sandboxForbidden(home, goCache, xdgCache, tempDir, goTmpDir)
		env := map[string]string{"HOME": home, "TMPDIR": tempDir, "XDG_CACHE_HOME": xdgCache, "GOCACHE": goCache, "GOTMPDIR": goTmpDir, "checkout": checkout}
		problems := sandboxProperty(t, plan, env)
		err := plan.admissible()
		switch {
		case err == nil && len(problems) > 0:
			t.Fatalf("case %d: admitted a plan that breaks the sandbox: %v\nenv %v\nargv %q", i, problems, env, sandboxArgv(plan))
		case err != nil && len(problems) == 0 && !overlapsCheckout(plan):
			t.Fatalf("case %d: refused a plan that keeps every guarantee: %v\nenv %v", i, err, env)
		case err == nil:
			admitted++
		default:
			refused++
		}
	}
	if admitted < 400 || refused < 400 {
		t.Fatalf("admitted %d and refused %d plans; the generator does not reach both sides", admitted, refused)
	}
}

// overlapsCheckout reports a read-only path at or around the checkout,
// which the plan refuses though the property does not name it.
func overlapsCheckout(plan sandboxPlan) bool {
	return slices.ContainsFunc(plan.ReadOnly, func(p sandboxPath) bool {
		return within(plan.Checkout, p.Path) || within(p.Path, plan.Checkout)
	})
}

// requireSandbox skips off Linux, where the merge tier fails closed
// (TestMergeTierFailsClosed), and puts the real go first on PATH, as the
// steps of a real gate find it: TestMain's stand-in answers go env with a
// GOROOT in a temporary directory, which the sandbox refuses to bind.
func requireSandbox(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the merge tier's sandbox is Linux only")
	}
	if realTools["go"] == "" {
		t.Skip("no go on PATH")
	}
	t.Setenv("PATH", filepath.Dir(realTools["go"])+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestSandboxPlanOfThisHost: the plan the gate makes here keeps every
// guarantee, and inside the sandbox it starts a host path the steps read
// is read-only, a write to HOME, GOCACHE or the temporary directory stays
// inside, and the checkout is the head commit, without an ignored file the
// host checkout has.
func TestSandboxPlanOfThisHost(t *testing.T) {
	requireSandbox(t)
	root := fixture(t)
	write(t, root, "runs/stale", "from an earlier run\n")
	g := wholeFixture(t, root, nil)
	plan, err := g.sandboxPlanFor([]*Step{{Name: "probe"}})
	if err != nil {
		t.Fatal(err)
	}
	paths, err := hostGoPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := stepVariable("HOME")
	xdg, _ := stepVariable("XDG_CACHE_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".cache")
	}
	goTmpDir, _ := stepVariable("GOTMPDIR")
	env := map[string]string{"HOME": home, "TMPDIR": os.TempDir(), "XDG_CACHE_HOME": xdg, "GOCACHE": paths.GOCACHE, "GOTMPDIR": goTmpDir, "checkout": root}
	if problems := sandboxProperty(t, plan, env); len(problems) > 0 {
		t.Fatalf("this host's plan breaks the sandbox: %v", problems)
	}
	box, err := startSandbox(plan)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			box.close()
		}
	}()
	// The run's directory is new, or the sandbox does not start.
	if again, err := startSandbox(plan); err == nil || !strings.Contains(err.Error(), "the run's directory") {
		if again != nil {
			again.close()
		}
		t.Errorf("a second sandbox over the run's directory %s: %v; want refused", plan.Fresh, err)
	}
	inside := func(script string, args ...string) (string, error) {
		argv := box.wrap(append([]string{"sh", "-c", script, "sh"}, args...), root)
		output, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		return string(output), err
	}
	for _, path := range plan.ReadOnly {
		if output, err := inside(`touch "$1/.gate-sandbox-probe"`, path.Path); err == nil || !strings.Contains(output, "Read-only file system") {
			t.Errorf("wrote under read-only %s: %v %s", path.Path, err, output)
		}
	}
	for _, path := range []string{home, paths.GOCACHE, os.TempDir()} {
		probe := filepath.Join(path, "gate-sandbox-probe")
		if output, err := inside(`mkdir -p "$(dirname "$1")" && echo x > "$1"`, probe); err != nil {
			t.Errorf("could not write %s inside: %v %s", probe, err, output)
		}
		if _, err := os.Lstat(probe); err == nil {
			os.Remove(probe)
			t.Errorf("a write to %s inside the sandbox reached the host", probe)
		}
	}
	head := strings.TrimSpace(gitIn(t, root, "rev-parse", "HEAD"))
	output, err := inside(`git rev-parse HEAD && git status --porcelain && test ! -e runs/stale && echo fresh`)
	if err != nil || output != head+"\nfresh\n" {
		t.Errorf("the checkout inside is not a fresh export of %s: %v\n%s", head, err, output)
	}
	// What the run wrote goes with it, a directory a step made read-only
	// too.
	if output, err := inside(`mkdir -p "$1/locked" && echo x > "$1/locked/file" && chmod 500 "$1/locked"`, os.TempDir()); err != nil {
		t.Fatalf("could not lock a directory inside: %v %s", err, output)
	}
	closed = true
	if err := box.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(plan.Fresh); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the run's directory %s outlived the sandbox: %v", plan.Fresh, err)
	}
}

// TestMergeTierFailsClosed: without its sandbox the merge tier exits
// non-zero with one line and runs no step: off Linux; without bwrap; and
// for a checkout with changes, whose head the export would not match.
func TestMergeTierFailsClosed(t *testing.T) {
	root := fixture(t)
	counter := filepath.Join(t.TempDir(), "count")
	steps := []*Step{{Name: "counted", Argv: outsideCount(counter)}}
	check := func(t *testing.T, g *gate, want string) {
		t.Helper()
		g.limit = passThroughLimiter{}
		var out bytes.Buffer
		code := g.mergeTier(&out)
		if code == 0 || strings.Count(out.String(), "\n") != 1 || !strings.HasPrefix(out.String(), "gate: --merge needs its sandbox") || !strings.Contains(out.String(), want) {
			t.Fatalf("exit %d, want non-zero with one line naming %q:\n%s", code, want, out.String())
		}
		if countIn(t, counter) != 0 {
			t.Fatalf("a step ran without the sandbox:\n%s", out.String())
		}
	}
	if runtime.GOOS != "linux" {
		check(t, wholeFixture(t, root, steps), "no sandbox on "+runtime.GOOS)
		return
	}
	t.Run("no bwrap", func(t *testing.T) {
		requireSandbox(t)
		bin := t.TempDir()
		for _, tool := range []string{"go", "git", "sh", "cat", "nsenter"} {
			path, err := exec.LookPath(tool)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path, filepath.Join(bin, tool)); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", bin)
		check(t, wholeFixture(t, root, steps), "no bwrap")
	})
	t.Run("sandbox fails to start", func(t *testing.T) {
		g := wholeFixture(t, root, steps)
		g.openSandbox = func(sandboxPlan) (enclosure, error) {
			return nil, errors.New("no user namespace: operation not permitted")
		}
		check(t, g, "no user namespace")
	})
	t.Run("bwrap fails to mount", func(t *testing.T) {
		requireSandbox(t)
		g := wholeFixture(t, root, steps)
		var fresh string
		g.openSandbox = func(plan sandboxPlan) (enclosure, error) {
			plan.ReadOnly = append(plan.ReadOnly, sandboxPath{"/nonexistent-gate-sandbox-path", "a path the host lacks"})
			fresh = plan.Fresh
			return startSandbox(plan)
		}
		began := time.Now()
		check(t, g, "Can't find source path /nonexistent-gate-sandbox-path")
		if took := time.Since(began); took > time.Minute {
			t.Errorf("the failed sandbox took %s to give up", took)
		}
		if _, err := os.Lstat(fresh); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the failed sandbox left its run directory %s: %v", fresh, err)
		}
	})
	t.Run("changes", func(t *testing.T) {
		requireSandbox(t)
		write(t, root, "src/input.txt", "changed\n")
		t.Cleanup(func() { gitIn(t, root, "checkout", "--", "src/input.txt") })
		g := wholeFixture(t, root, steps)
		check(t, g, "the checkout has changes")
	})
}

// recordingEnclosure wraps each argv in a script that notes it ran inside.
type recordingEnclosure struct{ log string }

func (r recordingEnclosure) wrap(argv []string, _ string) []string {
	return append([]string{"sh", "-c", `echo "$1" >> "$0"; shift; exec "$@"`, r.log, "enclosed"}, argv...)
}
func (recordingEnclosure) close() error { return nil }

// TestEnclosedRunWrapsEveryStep: a run with an enclosure runs every step
// through it, whatever the step's shape: independent or needing another,
// hermetic, with tool digests, a go test.
func TestEnclosedRunWrapsEveryStep(t *testing.T) {
	root := fixture(t)
	log := filepath.Join(t.TempDir(), "log")
	steps := []*Step{
		{Name: "plain", Argv: []string{"true"}},
		{Name: "needs", Needs: []string{"plain"}, Argv: []string{"true"}},
		{Name: "hermetic", Hermetic: true, Argv: []string{"true"}},
		{Name: "tools", ToolEnv: []string{"sh"}, Argv: []string{"true"}},
		{Name: "go test", Argv: []string{"go", "version"}},
		{Name: "keyed", Inputs: &Inputs{Paths: []string{"src/"}}, Argv: []string{"true"}},
	}
	g := &gate{root: root, cacheDir: t.TempDir(), steps: steps, noCache: true, enclosure: recordingEnclosure{log}}
	var out bytes.Buffer
	if code := g.run(&out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if got := countLines(t, log); got != len(steps) {
		t.Fatalf("%d of %d steps ran in the enclosure:\n%s", got, len(steps), out.String())
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(content), "\n")
}

// sandboxLauncher starts go test ./inner as programs do that pick the
// environment of their child: inherited, fresh with GOCACHE or without it,
// or fresh with a HOME, an XDG_CACHE_HOME, or a GOENV file naming a GOCACHE
// at a host path ($2) of the tool's own choosing.
const sandboxLauncher = `package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	child := exec.Command("go", "test", "./inner")
	fresh := func(names ...string) {
		for _, name := range names {
			child.Env = append(child.Env, name+"="+os.Getenv(name))
		}
	}
	home := os.Getenv("HOME")
	switch os.Args[1] {
	case "inherited":
	case "fresh-gocache":
		fresh("PATH", "HOME", "GOCACHE", "GOMEMLIMIT", "TMPDIR")
	case "fresh-no-gocache":
		fresh("PATH", "HOME", "GOMEMLIMIT", "TMPDIR")
	case "tool-home":
		fresh("PATH", "GOMEMLIMIT", "TMPDIR")
		child.Env = append(child.Env, "HOME="+filepath.Join(home, "tool-home"))
	case "tool-xdg":
		fresh("PATH", "HOME", "GOMEMLIMIT", "TMPDIR")
		child.Env = append(child.Env, "XDG_CACHE_HOME="+filepath.Join(home, "tool-xdg"))
	case "tool-goenv":
		goenv := filepath.Join(home, "tool-goenv")
		if os.WriteFile(goenv, []byte("GOCACHE="+os.Args[2]+"\n"), 0o644) != nil {
			os.Exit(2)
		}
		fresh("PATH", "HOME", "GOMEMLIMIT", "TMPDIR")
		child.Env = append(child.Env, "GOENV="+goenv)
	}
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if child.Run() != nil {
		os.Exit(1)
	}
}
`

// sandboxInner reads input.txt only through a child process, which Go's
// test cache does not see.
const sandboxInner = `package inner

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

// verifierR5Launcher is the round-5 verifier's probe tool, verbatim
// (gate-testcache-gap round-5-receipts/zz_verify_r5_test.go.txt): it picks
// the child's cache through XDG_CACHE_HOME, HOME, GOENV or a GOCACHE link.
const verifierR5Launcher = `package main
import ("os";"os/exec";"path/filepath")
func main() {
 c:=exec.Command("go","test","./inner")
 for _,n:=range []string{"PATH","HOME","GOMEMLIMIT","TMPDIR"} {c.Env=append(c.Env,n+"="+os.Getenv(n))}
 switch os.Args[1] {
 case "xdg": c.Env=append(c.Env,"XDG_CACHE_HOME="+filepath.Join(os.Getenv("HOME"),"tool-xdg"))
 case "home": c.Env=append(c.Env,"HOME="+filepath.Join(os.Getenv("HOME"),"tool-home"))
 case "goenv": c.Env=append(c.Env,"GOENV="+filepath.Join(os.Getenv("HOME"),"tool-goenv"))
 case "symlink": c.Env=append(c.Env,"GOCACHE="+filepath.Join(os.Getenv("HOME"),"cache-link"))
 }
 c.Stdout=os.Stdout;c.Stderr=os.Stderr
 if c.Run()!=nil {os.Exit(1)}
}

`

// sandboxRow is one regression row: a program the step runs, which starts
// the inner go test with an environment of its own choosing.
type sandboxRow struct {
	name, launcher string
	args           []string
	// prepare sets the host up as the verifier's probe did (a GOENV file
	// or a GOCACHE link in HOME).
	prepare func(t *testing.T, home, hostCache string)
}

func sandboxRows() []sandboxRow {
	var rows []sandboxRow
	for _, mode := range []string{"inherited", "fresh-gocache", "fresh-no-gocache", "tool-home", "tool-xdg", "tool-goenv"} {
		rows = append(rows, sandboxRow{name: mode, launcher: sandboxLauncher, args: []string{mode}})
	}
	rows = append(rows,
		sandboxRow{name: "r5 xdg", launcher: verifierR5Launcher, args: []string{"xdg"}},
		sandboxRow{name: "r5 home", launcher: verifierR5Launcher, args: []string{"home"}},
		sandboxRow{name: "r5 goenv", launcher: verifierR5Launcher, args: []string{"goenv"}, prepare: func(t *testing.T, home, hostCache string) {
			write(t, home, "tool-goenv", "GOCACHE="+filepath.Join(hostCache, "tool-config-cache")+"\n")
		}},
		sandboxRow{name: "r5 symlink", launcher: verifierR5Launcher, args: []string{"symlink"}, prepare: func(t *testing.T, home, hostCache string) {
			if err := os.Symlink(hostCache, filepath.Join(home, "cache-link")); err != nil {
				t.Fatal(err)
			}
		}},
	)
	return rows
}

// sandboxFixture is a committed module with a step per row, each running
// the row's launcher.
func sandboxFixture(t *testing.T, rows []sandboxRow, hostCache string) (string, []*Step) {
	t.Helper()
	root := fixture(t)
	write(t, root, "go.mod", "module example.com/sandboxed\n\ngo 1.21\n")
	write(t, root, "inner/inner_test.go", sandboxInner)
	write(t, root, "input.txt", "good\n")
	var steps []*Step
	for i, row := range rows {
		dir := fmt.Sprintf("cmd/launcher%d", i)
		write(t, root, dir+"/main.go", row.launcher)
		args := slices.Clone(row.args)
		if row.args[0] == "tool-goenv" {
			args = append(args, hostCache)
		}
		steps = append(steps, &Step{Name: row.name, Argv: append([]string{"go", "run", "./" + dir}, args...)})
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-qm", "launchers")
	return root, steps
}

// sandboxHome gives the test its own HOME, so no row writes the user's,
// with GOENV left to follow it, and the caches Go picks under it. The
// module cache stays the caller's: under a HOME in the temporary directory
// the sandbox would refuse to bind it.
func sandboxHome(t *testing.T) string {
	t.Helper()
	modCache, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOMODCACHE", strings.TrimSpace(string(modCache)))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOWORK", "off")
	os.Unsetenv("GOENV")
	os.Unsetenv("XDG_CACHE_HOME")
	return home
}

func mergeOnce(t *testing.T, g *gate) (int, string) {
	t.Helper()
	g.limit = passThroughLimiter{}
	var out bytes.Buffer
	code := g.mergeTier(&out)
	return code, out.String()
}

// TestMergeTierSandboxRegression is round 5's B1 as a real regression, over
// every way a program picks its child's Go test cache: inherited, a fresh
// environment with or without GOCACHE, a HOME, an XDG_CACHE_HOME or a GOENV
// file with its own GOCACHE at a host path, and the round-5 verifier's
// probe tool in each of its modes. The inner test reads input.txt through a
// child, which Go's test cache does not see. The witness first shows the
// launcher's result replays outside the gate. Then the merge tier passes on
// the good input; the bad input is committed; the second merge tier fails,
// on the uncached run, with the inner test's own failure in every row: no
// cache the first run or the host held answered for it. The inherited row
// has a fixture of its own: it carries the gate's -count=1, so its inner
// tier fails on the bad input first and the uncached run never starts.
// The other rows share one fixture, one step each, so one cold build serves
// them all.
func TestMergeTierSandboxRegression(t *testing.T) {
	requireSandbox(t)
	rows := sandboxRows()
	for _, group := range [][]sandboxRow{rows[:1], rows[1:]} {
		t.Run(group[0].name, func(t *testing.T) {
			home := sandboxHome(t)
			hostCache := t.TempDir()
			for _, row := range group {
				if row.prepare != nil {
					row.prepare(t, home, hostCache)
				}
			}
			root, steps := sandboxFixture(t, group, hostCache)
			for _, step := range steps {
				for round := range 2 {
					command := exec.Command(step.Argv[0], step.Argv[1:]...)
					command.Dir = root
					output, err := command.CombinedOutput()
					if err != nil {
						t.Fatalf("%s: witness launch %d: %v\n%s", step.Name, round+1, err, output)
					}
					if round == 1 && !bytes.Contains(output, []byte("(cached)")) {
						t.Fatalf("%s: the inner result does not replay outside the gate; the row is vacuous:\n%s", step.Name, output)
					}
				}
			}
			g := wholeFixture(t, root, steps)
			if code, out := mergeOnce(t, g); code != 0 {
				t.Fatalf("first merge tier, good input: exit %d\n%s", code, out)
			}
			write(t, root, "input.txt", "bad\n")
			gitIn(t, root, "commit", "-qam", "bad input")
			// The same gate cache: the first run's record and receipts stay.
			cache := g.cacheDir
			g = wholeFixture(t, root, steps)
			g.cacheDir = cache
			code, out := mergeOnce(t, g)
			t.Logf("second merge tier, bad input: exit %d\n%s", code, out)
			if code != 1 || !strings.Contains(out, "external input bad") {
				t.Fatalf("the second merge tier passed, or failed for another reason (exit %d)", code)
			}
			start := strings.Index(out, "merge: every step uncached\n")
			if start < 0 {
				if len(group) != 1 || !strings.Contains(out, "merge: FAILED: the inner tier failed\n") {
					t.Fatal("the merge tier ran no uncached run, and not for the inherited row's failed inner tier")
				}
				return
			}
			uncached := out[start:]
			for _, step := range steps {
				if !strings.Contains(uncached, "merge: "+step.Name+": inner pass, uncached FAILED\n") {
					t.Errorf("%s: the uncached run did not fail on the changed input", step.Name)
				}
			}
			if strings.Count(uncached, "external input bad") < len(steps) {
				t.Errorf("not every uncached step failed on the changed input")
			}
			if strings.Contains(uncached, "(cached)") {
				t.Errorf("the uncached run replayed a cached go test result")
			}
		})
	}
}

// TestMergeTierSandboxConcurrent is round 5's M2: two merge tiers run at
// once, each with a check that needs its own Go test cache (its launcher's
// second go test must replay its first) and an input of its own, good in
// one and bad in the other. Each gives its own verdict: neither run's cache
// is the other's, and nothing one run does reaches the other's.
func TestMergeTierSandboxConcurrent(t *testing.T) {
	requireSandbox(t)
	sandboxHome(t)
	check := []string{"sh", "-c", `go run ./cmd/launcher0 fresh-no-gocache && go run ./cmd/launcher0 fresh-no-gocache | tee /dev/stderr | grep -q "(cached)"`}
	var roots []string
	for _, input := range []string{"good", "bad"} {
		root, _ := sandboxFixture(t, []sandboxRow{{name: "launcher", launcher: sandboxLauncher, args: []string{"fresh-no-gocache"}}}, "")
		if input == "bad" {
			write(t, root, "input.txt", "bad\n")
			gitIn(t, root, "commit", "-qam", "bad input")
		}
		roots = append(roots, root)
	}
	codes, outs := make([]int, 2), make([]string, 2)
	var wait sync.WaitGroup
	for i, root := range roots {
		g := wholeFixture(t, root, []*Step{{Name: "cache-sensitive", Argv: check}})
		g.limit = passThroughLimiter{}
		wait.Go(func() {
			var out bytes.Buffer
			codes[i] = g.mergeTier(&out)
			outs[i] = out.String()
		})
	}
	wait.Wait()
	if codes[0] != 0 || !strings.Contains(outs[0], "merge: cache-sensitive: inner pass, uncached pass\n") {
		t.Errorf("the good run's merge tier exited %d:\n%s", codes[0], outs[0])
	}
	if codes[1] != 1 || !strings.Contains(outs[1], "external input bad") || strings.Contains(outs[1], ", uncached pass\n") {
		t.Errorf("the bad run's merge tier exited %d or passed its uncached run:\n%s", codes[1], outs[1])
	}
}
