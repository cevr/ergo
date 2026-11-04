package build

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// Cache is the emission cache rooted at Root. A published module lives at
// Root/modules/KEY; builders stage under Root/staging, on the same file
// system, so publication is one rename. A module whose compiler identity is
// process-scoped is built in the process's private root,
// Root/private/ID/modules/KEY, and never published (CompilerIdentity).
type Cache struct{ Root string }

// privateLifetime is how long a private root stays after a build last took
// it. A trim, which a Module call starts at most once per trimInterval,
// removes an older root only when no build holds its lease (lease.go).
const privateLifetime = unusedLimit

// DefaultRoot is $XDG_CACHE_HOME/ergo when XDG_CACHE_HOME is absolute,
// else os.UserCacheDir()/ergo. The XDG base directory specification makes
// a relative value invalid and to be ignored, so on the systems where
// os.UserCacheDir reads XDG_CACHE_HOME a relative value falls back to
// $HOME/.cache, which os.UserCacheDir would refuse instead.
func DefaultRoot() (string, error) {
	xdg := os.Getenv("XDG_CACHE_HOME")
	if xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "ergo"), nil
	}
	var dir string
	var err error
	switch runtime.GOOS {
	case "windows", "darwin", "ios", "plan9":
		dir, err = os.UserCacheDir()
	default:
		var home string
		if home, err = os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".cache")
		}
	}
	if err != nil {
		return "", fmt.Errorf("no cache directory for generated Go: %w", err)
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("no cache directory for generated Go: %q is not absolute", dir)
	}
	return filepath.Join(dir, "ergo"), nil
}

// Manifest describes a published module. Complete is written last into
// staging, and the whole directory is published by one rename, so a
// manifest that says complete names outputs that are all present.
type Manifest struct {
	Schema   string       `json:"schema"`
	Key      string       `json:"key"`
	Input    Summary      `json:"input"`
	Outputs  []FileDigest `json:"outputs"`
	Complete bool         `json:"complete"`
}

// Module is a generated module: published, or private to the process.
type Module struct {
	// Dir is the module's absolute directory: Root/modules/KEY when it is
	// published, Root/private/ID/modules/KEY when it is private.
	Dir string
	Key string
	// Reused reports that a complete, verified entry served the build and
	// nothing was emitted or written.
	Reused bool
	// Private reports that the compiler identity is process-scoped, so the
	// module was built in the process's private root and not published.
	Private  bool
	Manifest Manifest
	// release gives back the build's shared lock on the module's directory
	// and, for a private module, its lease on the private root.
	release func()
}

// Release ends the build's hold on its module. From Module until Release
// the build holds a shared lock on the module's directory, so no trim moves
// or removes it, and a private module's root keeps its lease. Call it after
// the link that reads the module, on every path, for a published module
// too; it is safe to call more than once.
func (m Module) Release() {
	if m.release != nil {
		m.release()
	}
}

// closer is a release that closes f once, which drops its lock.
func closer(f *os.File) func() {
	var once sync.Once
	return func() { once.Do(func() { f.Close() }) }
}

// Emitter produces the generated files of a module, apart from the runtime
// files of its Input. It runs only on a miss.
type Emitter func() ([]File, error)

// errCorrupt marks an entry that exists but does not verify.
var errCorrupt = errors.New("corrupt cache entry")

// testHookStaged, when set, runs after staging is complete and before the
// rename that publishes it. Tests use it to interrupt publication.
var testHookStaged func(staging string) error

// testHookEntryOpened, when set, runs after a build has opened an entry
// and before it locks it.
var testHookEntryOpened func(dir string)

// Module returns the published module of in, emitting and publishing it on
// a miss. A complete entry whose files match its manifest is reused as it
// is, and marked as used (trim.go). An entry that does not verify (a
// missing, extra, altered or non-regular file, or an unreadable or foreign
// manifest) is moved aside and removed, then replaced. When builders race,
// the first rename wins and every other builder verifies and uses the
// winner's entry.
//
// The build holds a shared lock on the module's directory, from before it
// verifies the entry, or from before it writes the first byte of a new one,
// until the caller calls Release on the module, after its link; on failure
// Module holds nothing when it returns. A trim never moves a directory a
// build holds. Module starts the cache's trim when one is due, after it has
// decided its own module and while it holds it.
//
// When in.Compiler is process-scoped, the same steps run in the process's
// private root instead, and the module is never published: a key that only
// one process can trust never names a shared entry. The build also holds
// the root's lease until Release.
func (c Cache) Module(in Input, emit Emitter) (Module, error) {
	if !filepath.IsAbs(c.Root) {
		return Module{}, fmt.Errorf("build: cache root %q is not absolute", c.Root)
	}
	defer func() { c.maybeTrim(time.Now()) }()
	if !processScoped(in.Compiler) {
		return c.module(in, emit)
	}
	private, release, err := c.private(in.Compiler)
	if err != nil {
		return Module{}, err
	}
	held := false
	defer func() {
		if !held {
			release() // a failed or panicking build holds nothing
		}
	}()
	module, err := private.module(in, emit)
	if err != nil {
		return Module{}, err
	}
	entry := module.release
	module.Private, held = true, true
	module.release = func() { entry(); release() }
	return module, nil
}

// private is the private root of the process whose identity is compiler.
// The build takes one use of the root's lease, which keeps the root through
// the build and its link (lease.go), and marks the root as just used. It
// returns the lease's release.
func (c Cache) private(compiler string) (Cache, func(), error) {
	sum := sha256.Sum256([]byte(compiler))
	own := filepath.Join(c.Root, "private", hex.EncodeToString(sum[:16]))
	release, err := acquireLease(own)
	if err != nil {
		return Cache{}, nil, err
	}
	now := time.Now()
	if err := os.Chtimes(own, now, now); err != nil {
		release()
		return Cache{}, nil, err
	}
	return Cache{Root: own}, release, nil
}

// module is Module in the root c, which publishes.
func (c Cache) module(in Input, emit Emitter) (Module, error) {
	summary, err := in.Summarize()
	if err != nil {
		return Module{}, err
	}
	key := summary.Key()
	dir := filepath.Join(c.Root, "modules", key)
	var files []File
	emitted := false
	// Each pass either returns or removes one corrupt entry; a third pass
	// means another writer keeps corrupting the path.
	for pass := 0; pass < 3; pass++ {
		entry, err := c.reuse(dir, key, summary)
		if err == nil {
			return Module{Dir: dir, Key: key, Reused: !emitted, Manifest: entry.Manifest, release: entry.release}, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Module{}, err
		}
		if !emitted {
			if files, err = emit(); err != nil {
				return Module{}, err
			}
			emitted = true
		}
		published, held, err := c.publish(dir, key, summary, in.Runtime, files)
		if err == nil {
			return Module{Dir: dir, Key: key, Manifest: published, release: closer(held)}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return Module{}, err
		}
		// Another builder published first; verify its entry.
	}
	return Module{}, fmt.Errorf("build: cache entry %s keeps failing verification", dir)
}

// hit is a verified entry's manifest and the release of the build's lock
// on it.
type hit struct {
	Manifest
	release func()
}

// reuse takes the entry at dir for a build: it opens it, takes a shared
// lock, checks that dir still names the locked directory, verifies it and
// marks it used, and returns it holding the lock. It returns an error
// satisfying fs.ErrNotExist on a miss: no entry, or a corrupt one that it
// moved aside and released. A trim that moved the entry between the open
// and the lock makes it open dir again, so a build never verifies or uses
// a path while it holds the lock of another directory.
func (c Cache) reuse(dir, key string, summary Summary) (hit, error) {
	for range 16 {
		f, err := os.Open(dir)
		if errors.Is(err, fs.ErrNotExist) {
			return hit{}, fs.ErrNotExist
		}
		if errors.Is(err, fs.ErrPermission) {
			// An entry the build may not open is damaged as one it may not
			// read: move it aside and build again, as verify would.
			if err := c.discard(dir, key, summary); err != nil {
				return hit{}, err
			}
			return hit{}, fs.ErrNotExist
		}
		if err != nil {
			return hit{}, err
		}
		if testHookEntryOpened != nil {
			testHookEntryOpened(dir)
		}
		if err := lockShared(f); err != nil {
			f.Close()
			return hit{}, err
		}
		info, same := sameFile(f, dir)
		if !same {
			f.Close() // a trim moved the entry while this waited
			continue
		}
		manifest, err := verify(dir, key, summary)
		if err == nil {
			mark(dir, info, time.Now())
			return hit{Manifest: manifest, release: closer(f)}, nil
		}
		if errors.Is(err, errCorrupt) {
			err = c.discard(dir, key, summary)
			f.Close()
			if err != nil {
				return hit{}, err
			}
			return hit{}, fs.ErrNotExist
		}
		f.Close()
		return hit{}, err
	}
	return hit{}, fmt.Errorf("build: cache entry %s keeps being removed", dir)
}

// publish writes the runtime files, the emitted files and then the
// manifest into a private staging directory and renames it to dir. The
// builder takes a shared lock on the staging directory before it writes a
// byte, and on success returns that directory, open and locked: the lock
// follows the directory through the rename, so the new entry is held from
// its first byte until the build releases it. It returns an error
// satisfying fs.ErrExist when an entry is already there. Staging is
// removed, and its lock released, unless the rename published it.
func (c Cache) publish(dir, key string, summary Summary, runtime, emitted []File) (Manifest, *os.File, error) {
	manifest := Manifest{Schema: SchemaVersion, Key: key, Input: summary}
	seen := map[string]bool{}
	all := append(append([]File{}, runtime...), emitted...)
	for _, f := range all {
		if err := checkModulePath(f.Path, seen); err != nil {
			return Manifest{}, nil, err
		}
		manifest.Outputs = append(manifest.Outputs, digestOf(f.Path, f.Data))
	}
	slices.SortFunc(manifest.Outputs, func(a, b FileDigest) int { return strings.Compare(a.Path, b.Path) })
	manifest.Complete = true
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, nil, err
	}
	stagingRoot := filepath.Join(c.Root, "staging")
	for _, d := range []string{stagingRoot, filepath.Dir(dir)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return Manifest{}, nil, err
		}
	}
	staging, err := os.MkdirTemp(stagingRoot, key+".")
	if err != nil {
		return Manifest{}, nil, err
	}
	var lock *os.File
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
			if lock != nil {
				lock.Close()
			}
		}
	}()
	// The lock comes before any write: a trim removes a staging directory
	// only when it takes its lock, and only when it is older than
	// stagingLifetime, which covers the two calls before the lock.
	if lock, err = os.Open(staging); err != nil {
		return Manifest{}, nil, err
	}
	if err := lockShared(lock); err != nil {
		return Manifest{}, nil, err
	}
	if _, same := sameFile(lock, staging); !same {
		return Manifest{}, nil, fmt.Errorf("build: staging %s was removed", staging)
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		return Manifest{}, nil, err
	}
	for _, f := range all {
		if err := writeNew(filepath.Join(staging, filepath.FromSlash(f.Path)), f.Data); err != nil {
			return Manifest{}, nil, err
		}
	}
	if err := writeNew(filepath.Join(staging, ManifestName), append(encoded, '\n')); err != nil {
		return Manifest{}, nil, err
	}
	if testHookStaged != nil {
		if err := testHookStaged(staging); err != nil {
			return Manifest{}, nil, err
		}
	}
	if err := os.Rename(staging, dir); err != nil {
		return Manifest{}, nil, err
	}
	published = true
	return manifest, lock, nil
}

// writeNew creates path, and its parent directories, with data. It never
// replaces an existing file.
func writeNew(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// discard moves the entry at dir aside into staging, under a name no
// builder uses, and removes it there. An entry another builder already
// moved is not an error. When another builder replaced the corrupt entry
// with a valid one between this builder's verification and the move, the
// moved entry verifies, and it is moved back unless the path is taken
// again, so recovery never throws away a valid module.
func (c Cache) discard(dir, key string, summary Summary) error {
	stagingRoot := filepath.Join(c.Root, "staging")
	if err := os.MkdirAll(stagingRoot, 0o755); err != nil {
		return err
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	aside := filepath.Join(stagingRoot, key+".discard."+hex.EncodeToString(nonce[:]))
	if err := os.Rename(dir, aside); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("build: move aside corrupt cache entry: %w", err)
	}
	if _, err := verify(aside, key, summary); err == nil && os.Rename(aside, dir) == nil {
		return nil
	}
	return os.RemoveAll(aside)
}

// verify reads the entry at dir. It returns fs.ErrNotExist when there is
// none, errCorrupt when it is not a complete module of key and summary
// whose files are exactly its outputs with their digests, and the manifest
// otherwise. It only reads.
func verify(dir, key string, summary Summary) (Manifest, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Manifest{}, fs.ErrNotExist
		}
		return Manifest{}, err
	}
	corrupt := func(format string, args ...any) (Manifest, error) {
		return Manifest{}, fmt.Errorf("%w: %s: %s", errCorrupt, dir, fmt.Sprintf(format, args...))
	}
	if !info.IsDir() {
		return corrupt("not a directory")
	}
	data, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return corrupt("manifest: %v", err)
	}
	var m Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return corrupt("manifest: %v", err)
	}
	want, _ := json.Marshal(summary)
	got, _ := json.Marshal(m.Input)
	if m.Schema != SchemaVersion || m.Key != key || !m.Complete || !bytes.Equal(want, got) {
		return corrupt("manifest does not describe this complete input")
	}
	outputs := map[string]FileDigest{}
	for _, o := range m.Outputs {
		outputs[o.Path] = o
	}
	if len(outputs) != len(m.Outputs) {
		return corrupt("manifest repeats an output")
	}
	found := 0
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ManifestName {
			return nil
		}
		want, listed := outputs[rel]
		if !listed || !d.Type().IsRegular() {
			return fmt.Errorf("unlisted or non-regular file %s", rel)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if len(data) != want.Size || hex.EncodeToString(sum[:]) != want.SHA256 {
			return fmt.Errorf("file %s does not match its digest", rel)
		}
		found++
		return nil
	})
	if err != nil {
		return corrupt("%v", err)
	}
	if found != len(outputs) {
		return corrupt("%d of %d outputs present", found, len(outputs))
	}
	return m, nil
}
