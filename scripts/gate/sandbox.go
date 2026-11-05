package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The merge tier's sandbox. The merge tier's uncached run is the merge
// verdict, so no state a run leaves on the host may decide it: Go's test
// cache, at any path a tool may pick (GOCACHE, GOENV, HOME, XDG_CACHE_HOME,
// a fresh child environment), and every other file a step writes. The
// sandbox gives the run a filesystem in which no host path is writable but
// the run's own: the root is a tmpfs; every directory a step writes (HOME,
// the temporary directories, the caches, the checkout) is bound from its
// own directory under the run's directory (sandboxPlan.Fresh), which the
// sandbox creates, so it is empty when the run starts, and removes when the
// run ends; the few host paths the steps must read are bound read-only
// (sandboxReadOnly); and the checkout is a fresh export of the head commit
// from the repository's object store, not the caller's working tree. So a
// cache a step fills lives only as long as the run, and two runs never
// share one (Bazel's sandboxed actions and Nix's build sandbox are the
// prior art; neither trusts a tool's own cache choice).
//
// The writable directories are on disk, not tmpfs, measured: on tmpfs the
// gate's own tests refused their output spools (the gate keeps a step's
// output on disk, never in memory), a directory-order test of
// internal/sketch/build failed, and the uncached Go tests' peak memory
// rose from 2.6G to 6.7G against an 8G step budget, the build cache's pages
// charged to the step.
//
// The run's processes enter the sandbox per step (sandbox.wrap): each step
// still runs as the gate's child, under the gate's memory limiter and with
// its output in a spool on the host's disk.
//
// The sandbox is bubblewrap (bwrap) in a user namespace the gate creates,
// so the gate needs no privilege. Where it cannot run (off Linux, without
// bwrap or nsenter, without user namespaces) the merge tier fails closed:
// it exits non-zero with one line and gives no verdict.

// sourceMount is where the sandbox binds the repository's object store,
// read-only, for the export. It is outside every path the steps name.
const sourceMount = "/run/ergo-gate/source.git"

// sandboxPath is a host path the sandbox binds read-only, and why.
type sandboxPath struct {
	Path   string
	Reason string
}

// sandboxPlan is everything the sandbox mounts. sandboxArgv turns it into
// bubblewrap's argv; nothing else does.
type sandboxPlan struct {
	// Head is the commit the sandbox exports into Checkout.
	Head string
	// Checkout is the host path of the gate's checkout. The export is at
	// the same path inside, so the steps' declarations need no rewrite.
	Checkout string
	// Source is the host's Git common directory, bound read-only at
	// sourceMount.
	Source string
	// ReadOnly are the host paths bound read-only (sandboxReadOnly).
	ReadOnly []sandboxPath
	// Scratch are the paths a step writes, by name (sandboxScratch), each
	// bound read-write from its own empty directory under Fresh. The root
	// is a tmpfs.
	Scratch []string
	// Fresh is the run's own directory on the host's disk: the sandbox
	// creates it, so it cannot exist before (startSandbox fails if it
	// does), and removes it when it closes. Scratch path i is bound from
	// sandboxFresh(Fresh, i); no other host path is writable. Its parent
	// holds every run's, so no read-only path may expose it.
	Fresh string
	// Dirs are directories the export creates before any step: the steps'
	// temporary directories.
	Dirs []string
	// Links are the host's top-level symbolic links into a read-only path
	// (/bin to usr/bin), recreated as links.
	Links [][2]string
	// Clean is whether the checkout matches Head. The export is Head, so a
	// checkout with changes cannot have a merge verdict.
	Clean bool
	// Forbidden and Homes are what no read-only path may expose
	// (sandboxForbidden, checkSandboxPlan).
	Forbidden, Homes []string
}

// admissible says why the sandbox cannot give the plan's run a verdict, or
// nil.
func (plan sandboxPlan) admissible() error {
	if !plan.Clean {
		return errors.New("the checkout has changes; the merge tier checks a fresh export of the head commit, so commit or remove them")
	}
	return checkSandboxPlan(plan, plan.Forbidden, plan.Homes)
}

// sandboxFDs are the descriptors bwrap receives from the gate: the user
// and pid namespaces the gate created, and the pipe bwrap names its child
// on (--info-fd).
const (
	sandboxUserNSFD = 3
	sandboxPidNSFD  = 4
	sandboxInfoFD   = 5
)

// sandboxExportScript runs in the sandbox before any step: it exports the
// head commit into the checkout from the read-only object store, creates
// the steps' temporary directories, says ready, and holds the sandbox open
// until the gate closes its standard input.
const sandboxExportScript = `set -eu
source=$1 checkout=$2 head=$3
shift 3
git clone -q --shared --no-checkout "$source" "$checkout"
git -C "$checkout" -c advice.detachedHead=false checkout -q --detach "$head"
for dir in "$@"; do mkdir -p "$dir"; done
echo ready
exec cat >/dev/null
`

// sandboxArgv is bubblewrap's argv for a plan: the one place the
// sandbox's mounts are decided. Mounts are ordered parents first, so a
// read-only path under a scratch path (the module cache under HOME) is
// bound over it.
func sandboxArgv(plan sandboxPlan) []string {
	argv := []string{
		"--userns", strconv.Itoa(sandboxUserNSFD),
		"--pidns", strconv.Itoa(sandboxPidNSFD),
		"--unshare-pid",
		"--die-with-parent",
		"--info-fd", strconv.Itoa(sandboxInfoFD),
		"--proc", "/proc",
		"--dev", "/dev",
	}
	type mount struct {
		path string
		args []string
	}
	var mounts []mount
	for i, path := range plan.Scratch {
		mounts = append(mounts, mount{path, []string{"--bind", sandboxFresh(plan.Fresh, i), path}})
	}
	for _, path := range plan.ReadOnly {
		mounts = append(mounts, mount{path.Path, []string{"--ro-bind", path.Path, path.Path}})
	}
	// Parents first; at one path the scratch before the read-only binding.
	slices.SortStableFunc(mounts, func(a, b mount) int {
		return strings.Count(filepath.Clean(a.path), "/") - strings.Count(filepath.Clean(b.path), "/")
	})
	for _, link := range plan.Links {
		argv = append(argv, "--symlink", link[1], link[0])
	}
	for _, m := range mounts {
		argv = append(argv, m.args...)
	}
	argv = append(argv, "--ro-bind", plan.Source, sourceMount, "--chdir", "/", "--", "/bin/sh", "-c", sandboxExportScript, "sandbox", sourceMount, plan.Checkout, plan.Head)
	return append(argv, plan.Dirs...)
}

// sandboxHostPaths are the paths the steps read from the host, with the
// reason for each; the merge tier binds these read-only and nothing else
// (docs/tooling.md, "Merge gate", lists them). Each was found necessary by
// running the merge tier without it.
func sandboxReadOnly(goModCache, goRoot string) []sandboxPath {
	paths := []sandboxPath{
		{"/usr", "the programs the steps run (sh, git, go, cc, ps) and their libraries"},
		{"/etc", "the dynamic linker's cache, the user database git and ps look the user up in, and git's system configuration"},
	}
	if goModCache != "" {
		paths = append(paths, sandboxPath{goModCache, "the Go module cache: the modules the build reads, and the Go toolchain go.mod selects, which no step may download"})
	}
	if goRoot != "" && !slices.ContainsFunc(paths, func(p sandboxPath) bool { return within(goRoot, p.Path) }) {
		paths = append(paths, sandboxPath{goRoot, "the Go toolchain (GOROOT)"})
	}
	return paths
}

// sandboxForbidden are the host paths no read-only binding may expose, as
// a path or a parent: a cache or temporary directory another run writes
// would hand its results to this one. HOME itself is forbidden as a path
// or parent; a declared path under it (the module cache) is not.
func sandboxForbidden(home, goCache, xdgCacheHome, tempDir, goTmpDir string) (forbidden, homes []string) {
	for _, path := range []string{"/tmp", "/var/tmp", goCache, xdgCacheHome, tempDir, goTmpDir} {
		if path != "" {
			forbidden = append(forbidden, path)
		}
	}
	if home != "" {
		forbidden = append(forbidden, filepath.Join(home, ".cache"), filepath.Join(home, "Library", "Caches"))
		homes = append(homes, home)
	}
	return forbidden, homes
}

// checkSandboxPlan refuses a plan that would let a step write or read a
// host path the run must not see: a read-only binding of a forbidden path,
// its parent, or a path under it, or of HOME or HOME's parent; a scratch
// path that is not absolute; a read-only binding over the checkout.
func checkSandboxPlan(plan sandboxPlan, forbidden, homes []string) error {
	for _, path := range plan.ReadOnly {
		if !filepath.IsAbs(path.Path) {
			return fmt.Errorf("read-only path %q is not absolute", path.Path)
		}
		for _, f := range forbidden {
			if within(path.Path, f) || within(f, path.Path) {
				return fmt.Errorf("read-only path %s exposes %s", path.Path, f)
			}
		}
		for _, home := range homes {
			if within(home, path.Path) {
				return fmt.Errorf("read-only path %s exposes HOME %s", path.Path, home)
			}
		}
		if within(plan.Checkout, path.Path) || within(path.Path, plan.Checkout) {
			return fmt.Errorf("read-only path %s overlaps the checkout %s", path.Path, plan.Checkout)
		}
	}
	for _, path := range plan.Scratch {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("scratch path %q is not absolute", path)
		}
	}
	if !filepath.IsAbs(plan.Fresh) || filepath.Dir(plan.Fresh) == plan.Fresh {
		return fmt.Errorf("the run's directory %q is not an absolute path below the root", plan.Fresh)
	}
	runs := filepath.Dir(plan.Fresh)
	for _, path := range plan.ReadOnly {
		if within(path.Path, runs) || within(runs, path.Path) {
			return fmt.Errorf("read-only path %s exposes the runs' directories %s", path.Path, runs)
		}
	}
	return nil
}

// sandboxFresh is the directory under a run's own directory that scratch
// path i is bound from.
func sandboxFresh(fresh string, i int) string {
	return filepath.Join(fresh, strconv.Itoa(i))
}

// within reports whether path is parent or the same path.
func within(path, parent string) bool {
	path, parent = filepath.Clean(path), filepath.Clean(parent)
	if path == parent || parent == "/" {
		return true
	}
	return strings.HasPrefix(path, parent+string(filepath.Separator))
}

// goPaths are the Go paths the steps' go resolves on the host.
type goPaths struct {
	GOCACHE, GOMODCACHE, GOROOT string
}

// sandboxPlanFor plans the sandbox of an uncached run of steps over a clean
// checkout at head. The scratch paths are named from the step environment
// as the steps see it, so a step's HOME, caches and temporary directories
// are all inside the sandbox.
func (g *gate) sandboxPlanFor(steps []*Step) (sandboxPlan, error) {
	identity := g.whole.identity
	root, err := filepath.Abs(g.root)
	if err != nil {
		return sandboxPlan{}, err
	}
	common := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	common.Dir = root
	source, err := common.Output()
	if err != nil {
		return sandboxPlan{}, fmt.Errorf("locate the repository's object store: %w", err)
	}
	paths, err := hostGoPaths(root)
	if err != nil {
		return sandboxPlan{}, err
	}
	home, _ := stepVariable("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	xdg, _ := stepVariable("XDG_CACHE_HOME")
	if xdg == "" && home != "" {
		xdg = filepath.Join(home, ".cache")
	}
	tempDir := os.TempDir()
	goTmpDir, _ := stepVariable("GOTMPDIR")
	plan := sandboxPlan{
		Head:     identity.head,
		Checkout: root,
		Source:   strings.TrimSpace(string(source)),
		ReadOnly: sandboxReadOnly(paths.GOMODCACHE, paths.GOROOT),
		Links:    sandboxLinks(),
	}
	plan.Scratch = sandboxScratch(home, xdg, paths.GOCACHE, tempDir, goTmpDir, g.cacheDir, root)
	// The runs' directory is in the gate's cache, on disk (the gate keeps
	// its spools there), and hidden in the sandbox under the cache's own
	// scratch binding. The name is random, so no two runs share one.
	if g.cacheDir == "" {
		return sandboxPlan{}, errors.New("no gate cache directory for the run's directory")
	}
	runs, err := filepath.Abs(filepath.Join(g.cacheDir, "sandbox"))
	if err != nil {
		return sandboxPlan{}, err
	}
	plan.Fresh = filepath.Join(runs, strings.ToLower(rand.Text()))
	for _, step := range steps {
		plan.Dirs = append(plan.Dirs, stepTempDir(root, step.Name))
	}
	plan.Clean = identity.clean
	plan.Forbidden, plan.Homes = sandboxForbidden(home, paths.GOCACHE, xdg, tempDir, goTmpDir)
	return plan, nil
}

// sandboxScratch are the writable mounts: every directory a step may write
// by name, so each is empty in the sandbox whatever the host holds there.
// Go needs GOTMPDIR to exist; its mount creates it.
func sandboxScratch(home, xdgCacheHome, goCache, tempDir, goTmpDir, gateCache, checkout string) []string {
	var scratch []string
	for _, path := range []string{"/tmp", "/var/tmp", home, xdgCacheHome, goCache, tempDir, goTmpDir, gateCache, checkout} {
		if path == "" {
			continue
		}
		if absolute, err := filepath.Abs(path); err == nil {
			path = absolute
		}
		if !slices.Contains(scratch, path) {
			scratch = append(scratch, path)
		}
	}
	return scratch
}

// sandboxLinks are the host's top-level links into /usr (a merged-/usr
// system's /bin, /lib, /lib64 and /sbin), recreated in the sandbox.
func sandboxLinks() [][2]string {
	var links [][2]string
	for _, name := range []string{"/bin", "/lib", "/lib64", "/sbin"} {
		target, err := os.Readlink(name)
		if err != nil {
			continue
		}
		if resolved := filepath.Join("/", target); within(resolved, "/usr") {
			links = append(links, [2]string{name, target})
		}
	}
	return links
}

// hostGoPaths asks the steps' go, with the step environment, where its
// caches and toolchain are.
func hostGoPaths(root string) (goPaths, error) {
	command := exec.Command("go", "env", "-json", "GOCACHE", "GOMODCACHE", "GOROOT")
	command.Dir = root
	command.Env = stepEnvironment()
	output, err := command.Output()
	if err != nil {
		return goPaths{}, fmt.Errorf("go env: %w", err)
	}
	var paths goPaths
	if err := json.Unmarshal(output, &paths); err != nil {
		return goPaths{}, fmt.Errorf("go env: %w", err)
	}
	return paths, nil
}

// sandboxWrap is the argv that runs argv in the sandbox whose first
// process is pid, in dir: nsenter joins its user, mount and pid namespaces
// and its root, keeps the caller's credentials (so the step holds no
// capability), and runs argv as its child, from dir. nsenter opens a
// --wd directory before it enters the mount namespace, so the host's
// directory would be the step's; a shell inside changes to dir instead.
func sandboxWrap(nsenter string, pid int, dir string, argv []string) []string {
	return append([]string{nsenter, "--target", strconv.Itoa(pid), "--user", "--mount", "--pid", "--preserve-credentials", "--root", "--wd", "--", "/bin/sh", "-c", `cd "$0" && exec "$@"`, dir}, argv...)
}

// enclosure runs a run's steps somewhere other than the host: the merge
// tier's sandbox.
type enclosure interface {
	// wrap is the argv that runs argv inside, in dir.
	wrap(argv []string, dir string) []string
	close() error
}
