package toolchain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// The shared IR compiler root (SHARED-IRC; owner q16, orchestrator
// 2026-11-20). The patched compile is a host artifact of its key alone, so
// every process of the host, a Go test's too, takes it from one root:
//
//	<irc>/toolchain/<release>.<goos>-<goarch>/patched-<key>/   the entry
//	<irc>/toolchain/<release>.<goos>-<goarch>/used/patched-<key>   its last use
//
// It is the one written exception to the rule that a Go test builds in a
// cache of its own (docs/tooling.md, Emission cache bound), and it is
// earned by these:
//
//   - The key covers every input of the binary (ircInputs): the patch's
//     digest, the release, the release's go command by content and its
//     path, the host, and every GO*/CGO_* variable the build still sees
//     that is not a location (an unknown one is hashed and named in the
//     manifest and the build's note).
//   - An entry proves itself: it is built beside the root and renamed in,
//     with a manifest of its key, its inputs and each file's digest; a
//     reader takes it only when the manifest's key is its own and, once a
//     process, each file has its digest. Anything else is a miss that
//     rebuilds and replaces the entry, and says so.
//   - An entry is read-only after the rename, and only Patch writes under
//     the root.
//   - One builder at a time for a release and host (the lock file).
//   - The root keeps the ircKeep entries used last, and always the one a
//     build just made (evict).
//
// ERGO_IRSPIKE_* still name another compile for the IR tests.

// IRCVariable names the shared IR compiler root, as ERGO_CACHE names the
// emission cache. It is a location: it moves the artifact, not an answer.
const IRCVariable = "ERGO_IRC"

// IRCRoot is $ERGO_IRC when it is set (absolute, or an error), else
// os.UserCacheDir()/ergo/irc.
func IRCRoot(getenv func(string) string) (string, error) {
	if dir := getenv(IRCVariable); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("%s=%q is not absolute", IRCVariable, dir)
		}
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("no shared IR compiler root: %w (set %s)", err, IRCVariable)
	}
	return filepath.Join(base, "ergo", "irc"), nil
}

// ircKeep is how many entries a root keeps for a release and host.
const ircKeep = 8

// buildEnvLocations are the GO*/CGO_* variables the compile's build sees
// that change no byte of it: where Go keeps or reports things. Every
// other one it sees is in the key (runBuildCompile sets or removes the
// rest).
var buildEnvLocations = []string{
	"GOCACHE", "GOTMPDIR", "GOPATH", "GOTELEMETRY", "GOTELEMETRYDIR",
	"GOMAXPROCS", "GOMEMLIMIT", "GODEBUG", "GOTRACEBACK", "GOPROXY",
	"GOSUMDB", "GONOSUMDB", "GOPRIVATE", "GONOPROXY", "GOINSECURE", "GOVCS",
}

// buildEnvPinned are the variables runBuildCompile sets itself.
var buildEnvPinned = []string{"GOROOT", "GOTOOLCHAIN", "GOFLAGS", "GOWORK", "CGO_ENABLED", "GOMODCACHE", "GOENV"}

// ircInputs are the inputs of the patched compile of the release at
// source with the patch digest, by name, and the variables the build
// sees that no class names.
func ircInputs(source, release, digest string, environ []string) (map[string]string, []string, error) {
	goCmd, err := fileDigest(filepath.Join(source, "bin", "go"+exe()))
	if err != nil {
		return nil, nil, err
	}
	in := map[string]string{
		"schema":  patchedSchema,
		"patch":   digest,
		"release": release,
		"source":  source,
		"go":      goCmd,
		"host":    hostTools(),
	}
	var unknown []string
	for _, kv := range buildEnviron(environ, nil) {
		name, value, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, "GO") && !strings.HasPrefix(name, "CGO_") {
			continue
		}
		if slices.Contains(buildEnvLocations, name) || slices.Contains(buildEnvPinned, name) {
			continue
		}
		in["env "+name] = value
		unknown = append(unknown, name)
	}
	slices.Sort(unknown)
	return in, unknown, nil
}

// keyOf is the key of inputs.
func keyOf(in map[string]string) string {
	var k keyer
	for _, name := range slices.Sorted(mapKeys(in)) {
		k.add(name, in[name])
	}
	return k.sum()
}

// manifest is what an entry says of itself.
type manifest struct {
	Key    string            `json:"key"`
	Inputs map[string]string `json:"inputs"`
	// Files are the entry's regular files by slash path, with their
	// sha256; a link (to the release) is keyed by the release's inputs.
	Files map[string]string `json:"files"`
}

const manifestName = "manifest.json"

// writeManifest writes the manifest of the entry built at dir.
func writeManifest(dir, key string, in map[string]string) error {
	m := manifest{Key: key, Inputs: in, Files: map[string]string{}}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		sum, err := fileDigest(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		m.Files[filepath.ToSlash(rel)] = sum
		return nil
	})
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "\t")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, manifestName), data, 0o444)
}

// verified remembers the entries whose files this process checked.
var verified sync.Map

// entryProblem is why the entry at dir is not the artifact of key, or
// "" when it is: its manifest names key, and (once a process) every file
// it lists has its digest and the compile is among them.
func entryProblem(dir, key string) string {
	if _, err := os.Lstat(dir); err != nil {
		return "not built yet"
	}
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return "it has no manifest"
	}
	var m manifest
	if json.Unmarshal(data, &m) != nil {
		return "its manifest does not read"
	}
	if m.Key != key {
		return "its manifest names another key"
	}
	if !patchedReady(filepath.Join(dir, "goroot")) {
		return "its compile or its release is missing"
	}
	if _, ok := verified.Load(dir); ok {
		return ""
	}
	compile := filepath.ToSlash(filepath.Join("goroot", "pkg", "tool", hostTools(), "compile"+exe()))
	if m.Files[compile] == "" {
		return "its manifest lists no compile"
	}
	for _, name := range slices.Sorted(mapKeys(m.Files)) {
		sum, err := fileDigest(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil || sum != m.Files[name] {
			return name + " is not the file its manifest lists"
		}
	}
	verified.Store(dir, true)
	return ""
}

// readOnly takes the write bits off every file and folder under dir
// (links are left alone).
func readOnly(dir string) error {
	var dirs []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			dirs = append(dirs, path)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.Chmod(path, info.Mode().Perm()&^0o222)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// The folders last, the deepest first: a folder without write bits
	// takes no chmod of its entries' names.
	slices.Reverse(dirs)
	for _, d := range dirs {
		if err := os.Chmod(d, 0o555); err != nil {
			return err
		}
	}
	return nil
}

// removeEntry removes a read-only entry: its folders get their write bits
// back first.
func removeEntry(dir string) error {
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(path, 0o755)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

// usedOnce remembers the entries this process marked used.
var usedOnce sync.Map

// markUsed records now as the last use of the entry named name in dir,
// once a process.
func markUsed(dir, name string) {
	if _, done := usedOnce.LoadOrStore(filepath.Join(dir, name), true); done {
		return
	}
	used := filepath.Join(dir, "used")
	if os.MkdirAll(used, 0o755) != nil {
		return
	}
	mark := filepath.Join(used, name)
	now := time.Now()
	if os.Chtimes(mark, now, now) != nil {
		if f, err := os.Create(mark); err == nil {
			f.Close()
		}
	}
}

// evict removes, from dir, the entries past the keep used last, never
// keep's own. The caller holds dir's lock.
func evict(dir, keep string, n int) error {
	names, err := filepath.Glob(filepath.Join(dir, "patched-*"))
	if err != nil {
		return err
	}
	type entry struct {
		name string
		at   time.Time
	}
	var entries []entry
	for _, path := range names {
		name := filepath.Base(path)
		if name == keep {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, "used", name))
		if err != nil {
			info, err = os.Stat(path)
		}
		if err != nil {
			continue
		}
		entries = append(entries, entry{name, info.ModTime()})
	}
	// Newest use first; equal times by name, so the order is the same on
	// every host.
	slices.SortFunc(entries, func(a, b entry) int {
		if c := b.at.Compare(a.at); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})
	var errs []error
	for i, e := range entries {
		if i < n-1 {
			continue
		}
		errs = append(errs, removeEntry(filepath.Join(dir, e.name)))
		errs = append(errs, os.RemoveAll(filepath.Join(dir, "work", e.name)))
		os.Remove(filepath.Join(dir, "used", e.name))
	}
	return errors.Join(errs...)
}

// PatchedWork is the writable folder of the entry whose GOROOT is root
// (Patched.Root): what builds with that compile keep, such as the IR
// files' copies a -ergoir flag names or a Go build cache keyed by the
// compile. The entry itself is read-only, and shared by every process of
// the host, so this folder is beside it (work/<entry>), at the same path
// in each process (the go command keys a compile on its flags); eviction
// removes it with its entry.
func PatchedWork(root string) string {
	entry := filepath.Dir(root)
	return filepath.Join(filepath.Dir(entry), "work", filepath.Base(entry))
}

// fileDigest is the sha256 of the file at path, in hex.
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
