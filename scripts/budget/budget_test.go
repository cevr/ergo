// Package budget tests the host's wrappers of `ergo budget`, light.sh and
// heavy.sh: the arguments they give it. A fake ergo records them and runs
// the command, so no budget is taken.
package budget

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fakeErgo = `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_ARGS"
while [ "$1" != "--" ]; do shift; done
shift
exec "$@"
`

// host is a budget file of 3 cores, a crit file naming the workspace
// "ws-crit", a fake ergo, and two git workspaces, ws-crit and ws-lane.
type host struct {
	dir, conf, crit, bin, args string
}

func newHost(t *testing.T) host {
	t.Helper()
	d := t.TempDir()
	h := host{dir: d, conf: filepath.Join(d, "budget.conf"), crit: filepath.Join(d, "crit"), bin: filepath.Join(d, "ergo"), args: filepath.Join(d, "args")}
	for f, text := range map[string]string{h.conf: "cores 3\n", h.crit: "ws-crit\n", h.bin: fakeErgo} {
		if err := os.WriteFile(f, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, ws := range []string{"ws-crit", "ws-lane"} {
		if out, err := exec.Command("git", "--no-optional-locks", "init", "-q", filepath.Join(d, ws)).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
	}
	return h
}

// run runs script from workspace ws with the host's variables and extra,
// and answers the arguments it gave ergo.
func (h host) run(t *testing.T, script, ws string, extra []string, argv ...string) []string {
	t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(must(os.Getwd()), script)}, argv...)...)
	cmd.Dir = filepath.Join(h.dir, ws)
	// The caller's own width (a test under `ergo budget` has GOMAXPROCS
	// set) is not the run's.
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k != "GOMAXPROCS" && k != "HEAVY_CORES" && k != "ERGO_GATE_CPUS" && k != "HEAVY_HELD" && k != "ERGO_BUDGET_CEILING" {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "TMPDIR="+filepath.Join(h.dir, "tmp"), "ERGO_BUDGET="+h.conf, "ERGO_BUDGET_CRIT="+h.crit, "ERGO_BUDGET_BIN="+h.bin, "FAKE_ARGS="+h.args, "HEAVY_DIR="+filepath.Join(h.dir, "heavy"))
	cmd.Env = append(cmd.Env, extra...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", script, argv, err, out)
	}
	got, err := os.ReadFile(h.args)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(got)), "\n")
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// flag answers the value of --name in ergo's arguments.
func flag(args []string, name string) string {
	for i, a := range args {
		if a == "--" {
			break
		}
		if a == "--"+name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// TestLightCritComesOnlyFromTheCritFile: a light run is crit when its
// workspace is in the crit file, and lane otherwise, whatever the caller
// sets (2026-11-20: crit runs held 5 of 7 slots and lane waiters starved 40
// minutes, granted by LIGHT_CRIT in lanes' scripts). Mutant:
// budget-light-crit-from-env.
func TestLightCritComesOnlyFromTheCritFile(t *testing.T) {
	h := newHost(t)
	if got := h.run(t, "light.sh", "ws-crit", nil, "true"); flag(got, "class") != "crit" || flag(got, "lane") != "ws-crit" {
		t.Errorf("a run of ws-crit, in the crit file: %q, want class crit, lane ws-crit", got)
	}
	if got := h.run(t, "light.sh", "ws-lane", []string{"LIGHT_CRIT=1"}, "true"); flag(got, "class") != "lane" {
		t.Errorf("a run of ws-lane with LIGHT_CRIT=1: %q, want class lane", got)
	}
}

// TestLightPassesTheHostCeiling checks the wrapper's argv with and without
// a ceiling, so the new wrapper still works with an old ergo binary until
// the host sets the variable. Mutants: budget-light-ceiling-ignored and
// ceiling-flag-always-passed.
func TestLightPassesTheHostCeiling(t *testing.T) {
	h := newHost(t)
	prefix := []string{"budget", "--config", h.conf, "--width", "4", "--class", "lane", "--lane", "ws-lane"}
	payload := []string{"--", "nice", "-n", "19", "true"}
	for _, c := range []struct{ env, args []string }{
		{nil, nil},
		{[]string{"ERGO_BUDGET_CEILING="}, nil},
		{[]string{"ERGO_BUDGET_CEILING=3G"}, []string{"--ceiling", "3G"}},
	} {
		want := slices.Concat(prefix, c.args, payload)
		if got := h.run(t, "light.sh", "ws-lane", c.env, "true"); !slices.Equal(got, want) {
			t.Errorf("environment %q: argv %q; want %q", c.env, got, want)
		}
	}
}

// TestRunsGetTheirOwnScratch: a light run gets a TMPDIR and GOTMPDIR of its
// own under the scratch root, removed when it ends, and its start sweeps a
// folder a killed run left (its lock free) but not one whose run still holds
// its lock (2026-11-20: killed runs left /tmp/ergo-mutants-*, /tmp/Test*
// and /tmp/go-build*, tens of GiB). Mutant: budget-scratch-no-sweep.
func TestRunsGetTheirOwnScratch(t *testing.T) {
	h := newHost(t)
	// The root is ergo-budget in the caller's TMPDIR, the volume the run's
	// scratch was on before.
	root := filepath.Join(h.dir, "tmp", "ergo-budget")
	// A link out of the root, in the run's folder and in a stale one:
	// removing either removes the link, never what it points to.
	outside := filepath.Join(h.dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"stale", "live"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, d, "left"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	live, err := os.Create(filepath.Join(root, "live.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if err := syscall.Flock(int(live.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stale.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(h.dir, "tmpdir")
	if err := os.Symlink(outside, filepath.Join(root, "stale", "out")); err != nil {
		t.Fatal(err)
	}
	h.run(t, "light.sh", "ws-lane", nil, "sh", "-c", `printf '%s\n%s\n' "$TMPDIR" "$GOTMPDIR" > "$0" && touch "$TMPDIR/made" && ln -s "$1" "$TMPDIR/out"`, out, outside)
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	dirs := strings.Fields(string(got))
	if len(dirs) != 2 || dirs[0] != dirs[1] || filepath.Dir(dirs[0]) != root {
		t.Fatalf("the run's TMPDIR and GOTMPDIR: %q, want one folder under %s", dirs, root)
	}
	if _, err := os.Stat(dirs[0]); !os.IsNotExist(err) {
		t.Errorf("the run's folder %s is left after it ended (%v)", dirs[0], err)
	}
	if _, err := os.Stat(filepath.Join(root, "stale")); !os.IsNotExist(err) {
		t.Errorf("a killed run's folder (lock free) was not swept (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(root, "live", "left")); err != nil {
		t.Errorf("a running run's folder (lock held) was swept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Errorf("a removal followed a link out of the scratch root: %v", err)
	}
}

// TestHeavyTakesItsDeclaredWidthInItsUnit: a heavy run takes the -p of its
// go test as its width, at most the budget's cores, in a unit of its own;
// its lane is heavy:<workspace>, and
// crit only from the crit file. It runs systemd units because that is what
// heavy.sh does. Mutants: budget-heavy-width-uncapped,
// budget-heavy-crit-from-env.
func TestHeavyTakesItsDeclaredWidthInItsUnit(t *testing.T) {
	if exec.Command("systemctl", "--user", "show-environment").Run() != nil {
		t.Skip("needs the user's systemd")
	}
	h := newHost(t)
	name := func(s string) string { return fmt.Sprintf("HEAVY_NAME=budget-test-%s-%d", s, time.Now().UnixNano()) }
	got := h.run(t, "heavy.sh", "ws-lane", []string{name("p"), "HEAVY_CRIT=1", "HEAVY_CLASS=crit"}, "sh", "-c", "true", "go", "test", "-p", "2")
	if flag(got, "width") != "2" || flag(got, "class") != "lane" || flag(got, "lane") != "heavy:ws-lane" {
		t.Errorf("heavy -p 2 from ws-lane with HEAVY_CRIT=1: %q, want width 2, class lane, lane heavy:ws-lane", got)
	}
	got = h.run(t, "heavy.sh", "ws-crit", []string{name("wide"), "HEAVY_CORES=8"}, "true")
	if flag(got, "width") != "3" || flag(got, "class") != "crit" {
		t.Errorf("heavy HEAVY_CORES=8 from ws-crit on a budget of 3: %q, want width 3, class crit", got)
	}
}
