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
// run ends; the few host trees the steps must read are bound read-only
// (sandboxReadOnly), and checked at the start of every run, so none shows
// a path a process of this user could have written or a Go cache
// (checkInputs, inputs.go); the checkout is a fresh export of the head
// commit, fetched by its hash, not the caller's working tree; and the
// module cache is empty but for archives built from the module contents
// go.sum verifies (seedModules).
// So a cache a step fills lives only as long as the run, two runs never
// share one, and no state from before the run is visible but the commit
// and the verified modules (Bazel's sandboxed actions and Nix's build
// sandbox with its fixed-output inputs are the prior art; neither trusts a
// tool's own cache choice).
//
// The writable directories are on disk, not tmpfs, measured: on tmpfs the
// gate's own tests refused their output spools (the gate keeps a step's
// output on disk, never in memory), a directory-order test of
// internal/sketch/build failed, and every byte a step wrote (a cold build
// cache, gigabytes) would be charged to the step's memory budget.
//
// The run's processes enter the sandbox per step (sandbox.wrap): each step
// still runs as the gate's child, under the gate's memory limiter and with
// its output in a spool on the host's disk.
//
// The sandbox is bubblewrap (bwrap) in a user namespace the gate creates,
// so the gate needs no privilege. Where it cannot run (off Linux, without
// bwrap or nsenter, without user namespaces) the merge tier fails closed:
// it exits non-zero with one line and gives no verdict.

// sandboxPath is a host path the sandbox binds read-only, and why.
// Maskable says whether checkInputs may mask a path inside it, or must
// refuse the run (GOROOT: the run needs all of it).
type sandboxPath struct {
	Path     string
	Reason   string
	Maskable bool
}

// sandboxPlan is everything the sandbox mounts. sandboxArgv turns it into
// bubblewrap's argv; nothing else does.
type sandboxPlan struct {
	// Head is the commit the sandbox exports into Checkout.
	Head string
	// Checkout is the host path of the gate's checkout. The export is at
	// the same path inside, so the steps' declarations need no rewrite.
	Checkout string
	// Source is the host's Git common directory, which the export fetches
	// Head from on the host (exportCommit). The sandbox does not bind it.
	Source string
	// ReadOnly are the declared read-only trees (sandboxReadOnly): the
	// input of admission, never bound as spelled.
	ReadOnly []sandboxPath
	// Admitted is what the sandbox binds from the host: each declared
	// tree by its resolved identity, and the masks over it (admitInputs).
	// sandboxArgv reads only this.
	Admitted admittedSet
	// Programs are the host programs the steps run, resolved; no mask may
	// hide one (sandboxPrograms).
	Programs []string
	// ModCache is the module cache the steps use (GOMODCACHE): a scratch
	// path, which the sandbox fills with archives built from the module
	// contents in HostModCache that the head commit's go.sum files verify
	// (seedModules).
	ModCache, HostModCache string
	// GoRoot is the toolchain the steps run (GOROOT), first on their PATH.
	GoRoot string
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

// admittedRoot is a declared read-only tree by its resolved identity
// (spec Q-R8-1): Resolved is the declared path with every symbolic link,
// in the path and in its parents, followed. The input check walks
// Resolved, and the sandbox binds Resolved at Path, the path the steps
// name, so the tree checked is the tree bound whatever its spelling.
type admittedRoot struct {
	sandboxPath
	Resolved string
}

// admittedSet is what the sandbox admits from the host: the roots and the
// masks over them, at their paths in the sandbox. admitInputs makes it,
// and sandboxArgv reads nothing else, so the check and the binding cannot
// disagree by spelling.
type admittedSet struct {
	Roots []admittedRoot
	Masks []sandboxMask
}

// linkReader says whether a path is a symbolic link, and its target.
type linkReader func(path string) (target string, link bool)

// hostLink reads a symbolic link on the host.
func hostLink(path string) (string, bool) {
	target, err := os.Readlink(path)
	return target, err == nil
}

// resolvePath is path with every symbolic link followed, component by
// component, as the kernel follows them: a link's target replaces it,
// from the root when absolute, and ".." goes up from where the links led.
// A component that does not exist ends no link, so the rest is appended as
// written (a directory a step makes later).
func resolvePath(path string, readlink linkReader) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%q is not absolute", path)
	}
	pending := strings.Split(path, "/")
	current := "/"
	for hops := 0; len(pending) > 0; {
		name := pending[0]
		pending = pending[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}
		next := filepath.Join(current, name)
		target, link := readlink(next)
		if !link {
			current = next
			continue
		}
		if hops++; hops > 255 {
			return "", fmt.Errorf("%s: too many levels of symbolic links", path)
		}
		if filepath.IsAbs(target) {
			current = "/"
		}
		pending = append(strings.Split(target, "/"), pending...)
	}
	return current, nil
}

// resolveRoots resolves each declared tree.
func resolveRoots(trees []sandboxPath, readlink linkReader) ([]admittedRoot, error) {
	roots := make([]admittedRoot, 0, len(trees))
	for _, tree := range trees {
		resolved, err := resolvePath(tree.Path, readlink)
		if err != nil {
			return nil, fmt.Errorf("read-only path: %w", err)
		}
		roots = append(roots, admittedRoot{sandboxPath: tree, Resolved: resolved})
	}
	return roots, nil
}

// admitRoots resolves the plan's declared trees and refuses the plan when
// the sandbox cannot give its run a verdict (checkSandboxPlan).
func (plan sandboxPlan) admitRoots(readlink linkReader) ([]admittedRoot, error) {
	if !plan.Clean {
		return nil, errors.New("the checkout has changes; the merge tier checks a fresh export of the head commit, so commit or remove them")
	}
	roots, err := resolveRoots(plan.ReadOnly, readlink)
	if err != nil {
		return nil, err
	}
	if err := checkSandboxPlan(plan, roots, readlink); err != nil {
		return nil, err
	}
	return roots, nil
}

// sandboxFDs are the descriptors bwrap receives from the gate: the user
// and pid namespaces the gate created, and the pipe bwrap names its child
// on (--info-fd).
const (
	sandboxUserNSFD = 3
	sandboxPidNSFD  = 4
	sandboxInfoFD   = 5
)

// sandboxReadyScript runs in the sandbox before any step: it creates the
// steps' temporary directories in the export, says ready, and holds the
// sandbox open until the gate closes its standard input.
const sandboxReadyScript = `set -eu
for dir in "$@"; do mkdir -p "$dir"; done
echo ready
exec cat >/dev/null
`

// sandboxArgv is bubblewrap's argv for a plan: the one place the
// sandbox's mounts are decided. Mounts are ordered parents first, so a
// read-only path under a scratch path (GOROOT under HOME) is bound over
// it, and a mask over the tree that holds it.
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
	for _, root := range plan.Admitted.Roots {
		mounts = append(mounts, mount{root.Path, []string{"--ro-bind", root.Resolved, root.Path}})
	}
	for _, mask := range plan.Admitted.Masks {
		mounts = append(mounts, mount{mask.Path, maskArgs(mask)})
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
	argv = append(argv, "--chdir", "/", "--", "/bin/sh", "-c", sandboxReadyScript, "sandbox")
	return append(argv, plan.Dirs...)
}

// sandboxReadOnly are the trees the steps read from the host, with the
// reason for each; the merge tier binds these read-only and nothing else
// (docs/tooling.md, "Merge gate", lists them). Each was found necessary by
// running the merge tier without it; /etc was not (the merge tier passed
// without it), so the host's configuration (git's, the resolver's, the
// user database) is not an input of the verdict. No module cache is bound:
// the run's own is filled from go.sum (seedModules).
func sandboxReadOnly(goRoot string) []sandboxPath {
	paths := []sandboxPath{
		{"/usr", "the programs the steps run (sh, git, cc, ps, bwrap) and their libraries", true},
	}
	if goRoot != "" {
		paths = append(paths, sandboxPath{goRoot, "the Go toolchain (GOROOT), first on the steps' PATH, with GOTOOLCHAIN=local", false})
	}
	return paths
}

// sandboxForbidden are the host paths no read-only binding may expose, as
// a path or a parent: a cache or temporary directory another run writes
// would hand its results to this one. HOME and the host's module cache
// are forbidden as a path or parent; a declared path under one (GOROOT in
// the module cache) is not.
func sandboxForbidden(home, goCache, xdgCacheHome, tempDir, goTmpDir, goModCache string) (forbidden, homes []string) {
	for _, path := range []string{"/tmp", "/var/tmp", goCache, xdgCacheHome, tempDir, goTmpDir} {
		if path != "" {
			forbidden = append(forbidden, path)
		}
	}
	if home != "" {
		forbidden = append(forbidden, filepath.Join(home, ".cache"), filepath.Join(home, "Library", "Caches"))
		homes = append(homes, home)
	}
	if goModCache != "" {
		homes = append(homes, goModCache)
	}
	return forbidden, homes
}

// checkSandboxPlan refuses a plan that would let a step write or read a
// host path the run must not see: a read-only binding of a forbidden path,
// its parent, or a path under it, or of HOME or HOME's parent; a scratch
// path that is not absolute; a read-only binding over the checkout. Each
// root is compared by its declared and its resolved path, and each
// forbidden path, HOME, the checkout and the runs' directory by their
// spelling and their resolved path (spec Q-R8-1), so a symbolic link on
// either side does not hide an overlap.
func checkSandboxPlan(plan sandboxPlan, roots []admittedRoot, readlink linkReader) error {
	spellings := func(path string) []string {
		if resolved, err := resolvePath(path, readlink); err == nil && resolved != filepath.Clean(path) {
			return []string{path, resolved}
		}
		return []string{path}
	}
	runs := ""
	if filepath.IsAbs(plan.Fresh) && filepath.Dir(plan.Fresh) != plan.Fresh {
		runs = filepath.Dir(plan.Fresh)
	}
	for _, root := range roots {
		if !filepath.IsAbs(root.Path) {
			return fmt.Errorf("read-only path %q is not absolute", root.Path)
		}
		for _, path := range []string{root.Path, root.Resolved} {
			for _, forbidden := range plan.Forbidden {
				for _, f := range spellings(forbidden) {
					if within(path, f) || within(f, path) {
						return fmt.Errorf("read-only path %s (%s) exposes %s", root.Path, path, f)
					}
				}
			}
			for _, home := range plan.Homes {
				for _, h := range spellings(home) {
					if within(h, path) {
						return fmt.Errorf("read-only path %s (%s) exposes %s", root.Path, path, h)
					}
				}
			}
			for _, checkout := range spellings(plan.Checkout) {
				if within(checkout, path) || within(path, checkout) {
					return fmt.Errorf("read-only path %s (%s) overlaps the checkout %s", root.Path, path, checkout)
				}
			}
			if runs == "" {
				continue
			}
			for _, r := range spellings(runs) {
				if within(path, r) || within(r, path) {
					return fmt.Errorf("read-only path %s (%s) exposes the runs' directories %s", root.Path, path, r)
				}
			}
		}
	}
	for _, path := range plan.Scratch {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("scratch path %q is not absolute", path)
		}
	}
	if plan.ModCache != "" && !slices.Contains(plan.Scratch, plan.ModCache) {
		return fmt.Errorf("the module cache %s is not a scratch path", plan.ModCache)
	}
	if runs == "" {
		return fmt.Errorf("the run's directory %q is not an absolute path below the root", plan.Fresh)
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
	common := exec.Command("git", "--no-optional-locks", "rev-parse", "--path-format=absolute", "--git-common-dir")
	common.Dir = root
	source, err := runOutput(common)
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
	modCache := paths.GOMODCACHE
	if modCache != "" {
		if modCache, err = filepath.Abs(modCache); err != nil {
			return sandboxPlan{}, err
		}
	}
	plan := sandboxPlan{
		Head:         identity.head,
		Checkout:     root,
		Source:       strings.TrimSpace(string(source)),
		ReadOnly:     sandboxReadOnly(paths.GOROOT),
		Programs:     sandboxPrograms(),
		ModCache:     modCache,
		HostModCache: modCache,
		GoRoot:       paths.GOROOT,
		Links:        sandboxLinks(),
	}
	plan.Scratch = sandboxScratch(home, xdg, paths.GOCACHE, tempDir, goTmpDir, g.cacheDir, root, modCache)
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
	plan.Forbidden, plan.Homes = sandboxForbidden(home, paths.GOCACHE, xdg, tempDir, goTmpDir, modCache)
	return plan, nil
}

// sandboxScratch are the writable mounts: every directory a step may write
// by name, so each is empty in the sandbox whatever the host holds there.
// Go needs GOTMPDIR to exist; its mount creates it. The module cache is one
// too: the sandbox fills it with modules built from verified contents
// (seedModules).
func sandboxScratch(home, xdgCacheHome, goCache, tempDir, goTmpDir, gateCache, checkout, goModCache string) []string {
	var scratch []string
	for _, path := range []string{"/tmp", "/var/tmp", home, xdgCacheHome, goCache, tempDir, goTmpDir, gateCache, checkout, goModCache} {
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
	output, err := runOutput(command)
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
	// environment is a step's environment inside.
	environment(env []string) []string
	close() error
}

// sandboxEnvironment is a step's environment in the sandbox: the run's own
// module cache, no module download (GOPROXY=off), no change to a go.mod
// (-mod=readonly after the step's GOFLAGS, round 4's -count=1 among them),
// no workspace (GOWORK=off), no toolchain switch (GOTOOLCHAIN=local), and
// GOROOT's go first on PATH.
func sandboxEnvironment(env []string, modCache, goRoot string) []string {
	set := map[string]string{"GOPROXY": "off", "GOTOOLCHAIN": "local", "GOWORK": "off", "GOFLAGS": "-mod=readonly"}
	if modCache != "" {
		set["GOMODCACHE"] = modCache
	}
	path, goFlags := "", ""
	var out []string
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		switch name {
		case "PATH":
			path = value
			continue
		case "GOFLAGS":
			goFlags = value
			continue
		}
		if _, ok := set[name]; !ok {
			out = append(out, kv)
		}
	}
	set["GOFLAGS"] = strings.TrimSpace(goFlags + " " + set["GOFLAGS"])
	if goRoot != "" {
		path = strings.TrimSuffix(filepath.Join(goRoot, "bin")+string(os.PathListSeparator)+path, string(os.PathListSeparator))
	}
	out = append(out, "PATH="+path)
	for _, name := range []string{"GOMODCACHE", "GOPROXY", "GOTOOLCHAIN", "GOWORK", "GOFLAGS"} {
		if value, ok := set[name]; ok {
			out = append(out, name+"="+value)
		}
	}
	return out
}
