package lanework

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeWork is a stand-in for `work` in the source base: `--json list`
// answers the lanes it made, and `--json new -branch B -from S NAME`
// clones the base into a lane on B at S and, as work does, gives every
// remote of the clone the push guard (modules/work: Guard).
const fakeWork = `#!/bin/sh
set -eu
dir=@DIR@
case "$2" in
list)
	printf '['
	sep=
	if [ -f "$dir/lanes" ]; then
		while read -r p; do
			printf '%s{"name":"%s","id":"id-%s","path":"%s","state":{"_tag":"Active"}}' "$sep" "$(basename "$p")" "$(basename "$p")" "$p"
			sep=,
		done < "$dir/lanes"
	fi
	printf ']\n'
	;;
new)
	path=$dir/lanes-dir/$7
	git clone -q "$PWD" "$path"
	git -C "$path" checkout -q -b "$4" "$6"
	git -C "$path" remote add main-checkout "$(git config remote.main-checkout.url)"
	for r in origin main-checkout; do git -C "$path" config "remote.$r.pushurl" @GUARD@; done
	echo "$path" >> "$dir/lanes"
	printf '{"path":"%s"}\n' "$path"
	;;
esac
`

// fixture is a main checkout, a work base cloned from it, and the fake
// work tool, with lanework.sh pointed at them.
type fixture struct {
	t                    *testing.T
	dir, main, src, work string
	env                  []string
}

func newFixture(t *testing.T, guard string) *fixture {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("lanework.sh needs jq")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, dir: dir, main: filepath.Join(dir, "main"), src: filepath.Join(dir, "src"), work: filepath.Join(dir, "work")}
	f.env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"ERGO_WORK_SOURCE="+f.src, "ERGO_MAIN_CHECKOUT="+f.main, "WORK="+f.work, "LANE_MIN_FREE_GIB=0",
		"LANE_GOCACHE="+filepath.Join(dir, "gocache"))
	script := strings.NewReplacer("@DIR@", dir, "@GUARD@", guard).Replace(fakeWork)
	if err := os.WriteFile(f.work, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f.git(dir, "init", "-q", "-b", "main", f.main)
	f.git(f.main, "commit", "-q", "--allow-empty", "-m", "base")
	f.git(dir, "clone", "-q", f.main, f.src)
	f.git(f.src, "remote", "add", "main-checkout", f.main)
	if err := os.WriteFile(filepath.Join(f.src, "work.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// lanework runs the script from dir.
func (f *fixture) lanework(dir string, args ...string) (string, error) {
	cmd := exec.Command("sh", append([]string{"lanework.sh"}, args...)...)
	if abs, err := filepath.Abs("lanework.sh"); err == nil {
		cmd.Args[1] = abs
	}
	cmd.Dir, cmd.Env = dir, f.env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// TestPushReachesTheMainCheckoutOfAGuardedLane: push hands a lane's branch
// to the main checkout although the lane's main-checkout remote carries a
// push guard: work new's (no-push:workspaces-never-push-origin) on a lane
// work made, or an older create's string. FK2, LH, G, IR2, SQ and others
// could not hand back (2026-11-19). Mutant: push to the main checkout's
// remote again (lanework-push-by-remote).
func TestPushReachesTheMainCheckoutOfAGuardedLane(t *testing.T) {
	for _, guard := range []string{"no-push:workspaces-never-push-origin", "no-push:lanes-never-push-origin"} {
		f := newFixture(t, guard)
		lane := filepath.Join(f.dir, "lanes-dir", "lane-a")
		cmd := exec.Command(f.work, "--json", "new", "-branch", "lane-a", "-from", "main", "lane-a")
		cmd.Dir, cmd.Env = f.src, f.env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fake work new: %v\n%s", err, out)
		}
		f.git(lane, "commit", "-q", "--allow-empty", "-m", "work")
		if out, err := f.lanework(lane, "push"); err != nil {
			t.Fatalf("%s: push from a guarded lane: %v\n%s", guard, err, out)
		}
		if got, want := f.git(f.main, "rev-parse", "refs/heads/lane-a"), f.git(lane, "rev-parse", "HEAD"); got != want {
			t.Errorf("%s: the main checkout's lane-a is %s; want the lane's head %s", guard, got, want)
		}
		if got := f.git(lane, "config", "remote.origin.pushurl"); got != guard {
			t.Errorf("%s: push changed origin's guard to %q", guard, got)
		}
	}
}

// TestLanesShareOneGoCache: two lanes create makes, and the env a lane
// made before the shared cache regenerates, all name the one shared
// GOCACHE, and its folder exists. 137 lane caches held about 240 GB of
// their own (SHARED-GOCACHE, 2026-11-22). Mutant: the env names the lane's
// own cache again (lanework-env-per-lane).
func TestLanesShareOneGoCache(t *testing.T) {
	f := newFixture(t, "no-push:workspaces-never-push-origin")
	want := "export GOCACHE=" + filepath.Join(f.dir, "gocache")
	var lanes []string
	for _, name := range []string{"lane-a", "lane-b"} {
		out, err := f.lanework(f.dir, "create", name, "main")
		if err != nil {
			t.Fatalf("create %s: %v\n%s", name, err, out)
		}
		lanes = append(lanes, out[strings.LastIndexByte(out, '\n')+1:])
	}
	for _, lane := range lanes {
		env, err := os.ReadFile(filepath.Join(lane, ".git", "lane.env"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(env), want+"\n") {
			t.Errorf("%s's lane.env is\n%s\nwant the line %q", lane, env, want)
		}
		if out, err := f.lanework(lane, "env"); err != nil || !strings.Contains(out, want+"\n") {
			t.Errorf("env in %s: %v\n%s\nwant the line %q", lane, err, out, want)
		}
	}
	if info, err := os.Stat(filepath.Join(f.dir, "gocache")); err != nil || !info.IsDir() {
		t.Errorf("the shared cache's folder: %v", err)
	}
}

// TestLaneRefusesACacheACloneCopies: create refuses, before it makes a
// lane, a GOCACHE that is relative, has a .. part, or lies inside the
// source or its workspace storage, where work new's clone or a snapshot
// of the base would copy it. Each row's mutant drops one check
// (lanework-cache-relative, lanework-cache-dots, lanework-cache-in-base,
// lanework-cache-in-storage).
func TestLaneRefusesACacheACloneCopies(t *testing.T) {
	f := newFixture(t, "no-push:workspaces-never-push-origin")
	for _, cache := range []string{
		"gocache",
		f.dir + "/x/../src/c", // not Join, which cleans the .. away
		filepath.Join(f.src, ".git", "gocache"),
		filepath.Join(f.dir, ".work", "src", "gocache"),
	} {
		f.env = append(f.env, "LANE_GOCACHE="+cache)
		out, err := f.lanework(f.dir, "create", "lane-c", "main")
		if err == nil || !strings.Contains(out, "shared Go cache") {
			t.Errorf("create with GOCACHE %s: %v\n%s\nwant a refusal of the cache", cache, err, out)
		}
		if _, err := os.Stat(filepath.Join(f.dir, "lanes")); err == nil {
			t.Fatalf("create with GOCACHE %s made a lane", cache)
		}
	}
}

// TestCreateLeavesOnlyOriginGuarded: a lane create makes keeps origin's
// push guard and drops the one work new puts on main-checkout, the remote
// its pushDefault names, so a plain git push of the lane reaches the main
// checkout too. Mutant: create keeps work's guard on the main checkout's
// remote (lanework-create-keeps-guard).
func TestCreateLeavesOnlyOriginGuarded(t *testing.T) {
	f := newFixture(t, "no-push:workspaces-never-push-origin")
	out, err := f.lanework(f.dir, "create", "lane-b", "main")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	lane := out[strings.LastIndexByte(out, '\n')+1:]
	if got := f.git(lane, "config", "remote.origin.pushurl"); !strings.HasPrefix(got, "no-push:") {
		t.Errorf("origin's push URL is %q; want a no-push guard", got)
	}
	f.git(lane, "commit", "-q", "--allow-empty", "-m", "work")
	f.git(lane, "push", "-q")
	if got, want := f.git(f.main, "rev-parse", "refs/heads/lane-b"), f.git(lane, "rev-parse", "HEAD"); got != want {
		t.Errorf("a plain git push left the main checkout's lane-b at %s; want %s", got, want)
	}
}
