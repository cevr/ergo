package toolchain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"ergo.local/prototype/internal/pinset"
	"ergo.local/prototype/toolchain/ir"
)

// The patched compile is the second artifact of the toolchain cache (owner
// q131, q136; plan-tier §1.4, IRT0b). Every ergo build compiles Ergo
// bodies through typed IR, which only the patched cmd/compile reads
// (-ergoir). ergo applies the patch that toolchain/ir/lock ports to the
// Go release a build runs, to that release's own source, and builds
// cmd/compile once:
//
//	<cache>/toolchain/<release>.<goos>-<goarch>/patched-<key>/goroot   a GOROOT
//	<cache>/toolchain/<release>.<goos>-<goarch>/patched-<key>/src      the patched files
//
// The goroot is the release with the patched compile: every other entry
// is a symbolic link to the release's own, so the go command, the linker
// and the standard library are the release's. key digests the patch and
// the release's GOROOT, and the directory names the release and the host
// (compile is a host program for every target). It appears by one rename
// under the release's install lock (Ensure's), whole or not at all.
//
// The patched compile without -ergoir compiles exactly as the stock one
// does (stock equivalence, checked for every port by scripts/irport), and
// prints the stock -V=full line, so Go's build cache shares the standard
// library between them. A compile with -ergoir is keyed by its flag and its
// files: the direct path keys every compile on the compile's content.

// Patched is a Go release with ergo's patched compile.
type Patched struct {
	// Root is the GOROOT a build runs with: the release, with the
	// patched compile in its tool directory.
	Root string
	// Go is Root's go command.
	Go string
	// Release is the Go release, such as go1.27.4.
	Release string
	// Digest is the sha256 of the patch, as toolchain/ir/lock records it.
	Digest string
}

// PatchConfig is one patched compile.
type PatchConfig struct {
	// Cache is ergo's cache directory.
	Cache string
	// Source is the GOROOT of the pristine release, with its source.
	Source string
	// Log receives one line when the compile is built; nil is none.
	Log io.Writer

	// lock is the lock and patches; nil is toolchain/ir's (tests).
	lock fs.FS
	// Pins, when set, are the pin sets a tools set of the patched compile
	// is taken from (ToolsKey) instead of building it; nil builds it.
	Pins *pinset.Source

	// build builds cmd/compile; nil is buildCompile (tests).
	build func(source, overlay, out string) error
	// waiting runs when another build holds the lock (tests).
	waiting func()
}

// patchedSchema versions the artifact's key.
const patchedSchema = "ergo-ir-patched-1"

// Patch returns the release at c.Source with the patched compile, building
// the compile first when the cache does not hold it. A release that
// toolchain/ir/lock does not port is refused: ergo never patches a Go the
// patch was not ported to.
func Patch(c PatchConfig) (Patched, error) {
	source, err := filepath.Abs(c.Source)
	if err != nil {
		return Patched{}, err
	}
	data, err := os.ReadFile(filepath.Join(source, "VERSION"))
	if err != nil {
		return Patched{}, fmt.Errorf("the Go at %s has no VERSION file: %w", source, err)
	}
	release, _, _ := strings.Cut(string(data), "\n")
	release = strings.TrimSpace(release)
	lockFS := c.lock
	if lockFS == nil {
		lockFS = ir.Files
	}
	port, err := portOf(lockFS, release)
	if err != nil {
		return Patched{}, err
	}
	var k keyer
	k.add("schema", patchedSchema)
	k.add("patch", port.digest)
	k.add("source", source)
	dir := Config{Cache: c.Cache, Version: release}.Dir()
	out := filepath.Join(dir, "patched-"+k.sum()[:20])
	p := Patched{Root: filepath.Join(out, "goroot"), Release: release, Digest: port.digest}
	p.Go = filepath.Join(p.Root, "bin", "go"+exe())
	if patchedReady(p.Root) {
		return p, nil
	}
	if !filepath.IsAbs(c.Cache) {
		return Patched{}, fmt.Errorf("toolchain cache %q is not absolute", c.Cache)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Patched{}, err
	}
	unlock, err := lock(filepath.Join(dir, "lock"), c.waiting)
	if err != nil {
		return Patched{}, err
	}
	defer unlock()
	if patchedReady(p.Root) {
		return p, nil
	}
	// Under the lock no build runs: what an interrupted one left goes, and
	// so does an artifact whose release has gone.
	if locks {
		leftovers, _ := filepath.Glob(filepath.Join(dir, "tmp-*"))
		for _, path := range leftovers {
			os.RemoveAll(path)
		}
	}
	os.RemoveAll(out)
	build := c.build
	if build == nil {
		build = buildCompile
	}
	if from, ok := c.toolsSet(release, port.digest); ok {
		build = func(_, _, out string) error { return copyExecutable(from, out) }
	} else if c.Log != nil {
		fmt.Fprintf(c.Log, "ergo: building the IR compiler for %s (once per Go release and patch)\n", release)
	}
	tmp, err := os.MkdirTemp(dir, "tmp-")
	if err != nil {
		return Patched{}, err
	}
	err = patchInto(tmp, source, port, build)
	if err == nil {
		err = os.Chmod(tmp, 0o755) // MkdirTemp's 0700, as the release's tree has
	}
	if err != nil {
		os.RemoveAll(tmp)
		return Patched{}, fmt.Errorf("build the IR compiler for %s: %w", release, err)
	}
	if err := os.Rename(tmp, out); err != nil {
		os.RemoveAll(tmp)
		if patchedReady(p.Root) {
			return p, nil // another build won the rename (no flock)
		}
		return Patched{}, err
	}
	return p, nil
}

// patchedReady reports whether goroot holds a patched compile and its
// release is still there.
func patchedReady(goroot string) bool {
	info, err := os.Stat(filepath.Join(goroot, "pkg", "tool", hostTools(), "compile"+exe()))
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	info, err = os.Stat(filepath.Join(goroot, "bin", "go"+exe()))
	return err == nil && info.Mode().IsRegular()
}

func hostTools() string { return runtime.GOOS + "_" + runtime.GOARCH }

// patchInto applies the port's patch to the release at source, writes the
// patched files under tmp/src, links the release into tmp/goroot and
// builds the patched compile into it.
func patchInto(tmp, source string, port port, build func(source, overlay, out string) error) error {
	applied, err := ApplyPatch(source, port.data)
	if err != nil {
		return err
	}
	if len(applied.Rejected) > 0 {
		return fmt.Errorf("%s does not apply to %s: %s", port.name, source, strings.Join(applied.Rejected, "; "))
	}
	overlay := map[string]string{}
	for _, name := range slices.Sorted(mapKeys(applied.Files)) {
		file := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file, applied.Files[name], 0o644); err != nil {
			return err
		}
		overlay[filepath.Join(source, filepath.FromSlash(name))] = file
	}
	data, err := json.Marshal(struct{ Replace map[string]string }{overlay})
	if err != nil {
		return err
	}
	overlayFile := filepath.Join(tmp, "overlay.json")
	if err := os.WriteFile(overlayFile, data, 0o644); err != nil {
		return err
	}
	goroot := filepath.Join(tmp, "goroot")
	if err := linkRelease(source, goroot); err != nil {
		return err
	}
	return build(source, overlayFile, filepath.Join(goroot, "pkg", "tool", hostTools(), "compile"+exe()))
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// linkRelease makes goroot the release at source, every entry a symbolic
// link to the release's, except the host tool directory's compile, which
// the build writes.
func linkRelease(source, goroot string) error {
	keep := []string{"pkg", filepath.Join("pkg", "tool"), filepath.Join("pkg", "tool", hostTools())}
	var walk func(rel string) error
	walk = func(rel string) error {
		if err := os.MkdirAll(filepath.Join(goroot, rel), 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(filepath.Join(source, rel))
		if err != nil {
			return err
		}
		for _, e := range entries {
			name := filepath.Join(rel, e.Name())
			switch {
			case slices.Contains(keep, name):
				if err := walk(name); err != nil {
					return err
				}
			case name == filepath.Join("pkg", "tool", hostTools(), "compile"+exe()):
			default:
				if err := os.Symlink(filepath.Join(source, name), filepath.Join(goroot, name)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk("")
}

// buildCompile builds the release's cmd/compile, with the overlay's
// patched files, into out: the release's own go command, for the host,
// with cgo off and no flags of the user's. Go's build cache keeps every
// package the patch leaves alone. The module cache is a new directory: the
// go command refuses an overlay of a file under GOMODCACHE, and a release
// that Go's toolchain switch installed lies there.
func buildCompile(source, overlay, out string) error {
	return buildCompileFor("", "")(source, overlay, out)
}

// buildCompileFor is buildCompile for the host goos/goarch; empty is this
// host. A tools set for another host is cross-built (ProduceTools).
func buildCompileFor(goos, goarch string) func(source, overlay, out string) error {
	return func(source, overlay, out string) error {
		var target []string
		if goos != "" {
			target = []string{"GOOS=" + goos, "GOARCH=" + goarch}
		}
		return runBuildCompile(source, overlay, out, target)
	}
}

func runBuildCompile(source, overlay, out string, target []string) error {
	cmd := exec.Command(filepath.Join(source, "bin", "go"+exe()), "build", "-trimpath", "-overlay="+overlay, "-o", out, "cmd/compile")
	cmd.Dir = filepath.Join(source, "src")
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains([]string{"GOROOT", "GOTOOLCHAIN", "GOFLAGS", "GOWORK", "GOOS", "GOARCH", "GOAMD64", "GOARM64", "GOEXPERIMENT", "CGO_ENABLED", "GOMODCACHE"}, name)
	})
	cmd.Env = append(env, "GOROOT="+source, "GOTOOLCHAIN=local", "GOFLAGS=", "GOWORK=off", "CGO_ENABLED=0",
		"GOMODCACHE="+filepath.Join(filepath.Dir(overlay), "modcache"))
	cmd.Env = append(cmd.Env, target...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build cmd/compile: %v\n%s", err, stderr.String())
	}
	return nil
}

// SourceOf is the GOROOT of the release t runs: the managed release, or
// what the go command reports in a module whose go.mod is goMod, kept
// under cache. A build's go command runs in the generated module, so
// Go's toolchain switch (GOTOOLCHAIN) picks there the release the build
// would have run.
func SourceOf(t Toolchain, cache string, goMod []byte) (string, error) {
	if t.Root != "" {
		return t.Root, nil
	}
	sum := sha256.Sum256(goMod)
	probe := filepath.Join(cache, "toolchain", "probe-"+hex.EncodeToString(sum[:])[:16])
	if data, err := os.ReadFile(filepath.Join(probe, "go.mod")); err != nil || !bytes.Equal(data, goMod) {
		if err := os.MkdirAll(probe, 0o755); err != nil {
			return "", err
		}
		tmp, err := os.CreateTemp(probe, "tmp-")
		if err != nil {
			return "", err
		}
		_, err = tmp.Write(goMod)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Chmod(tmp.Name(), 0o644)
		}
		if err == nil {
			err = os.Rename(tmp.Name(), filepath.Join(probe, "go.mod"))
		}
		if err != nil {
			os.Remove(tmp.Name())
			return "", err
		}
	}
	cmd := exec.Command(t.Go, "env", "GOROOT")
	cmd.Dir = probe
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s env GOROOT: %v: %s", t.Go, err, strings.TrimSpace(stderr.String()))
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("%s env GOROOT is empty", t.Go)
	}
	return root, nil
}

// port is the patch toolchain/ir/lock ports to one release.
type port struct {
	name, digest string
	data         []byte
}

// portOf reads the lock: the port line of release, and the patch it names,
// whose content must be the lock's digest.
func portOf(lockFS fs.FS, release string) (port, error) {
	data, err := fs.ReadFile(lockFS, "lock")
	if err != nil {
		return port{}, err
	}
	digests := map[string]string{}
	var name string
	var ported []string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 3 && f[0] == "patch":
			digests[f[1]] = strings.TrimPrefix(f[2], "sha256:")
		case len(f) == 3 && f[0] == "port":
			ported = append(ported, f[1])
			if f[1] == release {
				name = f[2]
			}
		}
	}
	if name == "" {
		return port{}, fmt.Errorf("Go %s has no port of the IR compiler patch (toolchain/ir/lock ports %s); build with one of those (ERGO_GO)", release, strings.Join(ported, ", "))
	}
	patch, err := fs.ReadFile(lockFS, name)
	if err != nil {
		return port{}, err
	}
	sum := sha256.Sum256(patch)
	digest := hex.EncodeToString(sum[:])
	if digests[name] != digest {
		return port{}, fmt.Errorf("toolchain/ir/lock does not lock %s at its content (sha256:%s)", name, digest)
	}
	return port{name: name, digest: digest, data: patch}, nil
}

// keyer hashes named fields, each length-prefixed.
type keyer struct{ buf bytes.Buffer }

func (k *keyer) add(name, value string) {
	fmt.Fprintf(&k.buf, "%d:%s%d:%s", len(name), name, len(value), value)
}

func (k *keyer) sum() string {
	s := sha256.Sum256(k.buf.Bytes())
	return hex.EncodeToString(s[:])
}
