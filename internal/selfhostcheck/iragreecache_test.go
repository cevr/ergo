package selfhostcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/lower"
)

// The IR agreement run keeps each side's answer in the lawcache store
// (internal/sketch/lawcache, the one result store) when
// irAgreeCacheVariable names the store's root: the Go builder's lowering
// of each program, and irdump's whole output for the manifest. A key is
// the contents of every file the answer may read, never a path outside
// the repository or a stamp; paths inside it are written "@/<slash
// path>". The store namespaces each entry by the code that answers: the
// test binary for the Go side, the irdump binary for the other.
// irAgreeFreshVariable=1 reads no entry and still writes the fresh ones.
const (
	irAgreeCacheVariable = "ERGO_IR_AGREE_CACHE"
	irAgreeFreshVariable = "ERGO_IR_AGREE_FRESH"
)

// irAgreeStore is the store of the cache root the environment names,
// answers made by the code whose bytes are in file; nil keeps nothing.
func irAgreeStore(kind, file string) *lawcache.Store {
	root := os.Getenv(irAgreeCacheVariable)
	if root == "" {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(data)
	return lawcache.Open(root, kind+":"+hex.EncodeToString(sum[:]))
}

func irAgreeFresh() bool { return os.Getenv(irAgreeFreshVariable) == "1" }

// irWithin writes path relative to the repository as "@/<slash path>";
// a path outside it stays absolute.
func irWithin(path string) string {
	r, err := filepath.Rel(repoRoot(), path)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return path
	}
	return "@/" + filepath.ToSlash(r)
}

// irRootPrefix is the repository's folder as answers spell it, which a
// stored answer holds as "@/".
func irRootPrefix() string { return filepath.Clean(repoRoot()) + string(filepath.Separator) }

// irAnswerEnv is the environment an answer may read: each GO, CGO_ and
// ERGO_ variable except those that only name a place or this test's own
// switches, sorted, and the Go release of the test binary.
func irAnswerEnv() []string {
	places := map[string]bool{"GOCACHE": true, "GOTMPDIR": true, "GOMODCACHE": true, "GOPATH": true, "GOENV": true, "ERGO_CACHE": true, "ERGO_TEST_CLI": true}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if places[name] || strings.HasPrefix(name, "ERGO_IR_AGREE_") {
			continue
		}
		if strings.HasPrefix(name, "GO") || strings.HasPrefix(name, "CGO_") || strings.HasPrefix(name, "ERGO_") {
			env = append(env, kv)
		}
	}
	slices.Sort(env)
	return append(env, "go "+runtime.Version())
}

// irDigests hashes files and folders once per run.
type irDigests struct {
	mu    sync.Mutex
	trees map[string]string
	flats map[string]string
}

var irDigest = &irDigests{trees: map[string]string{}, flats: map[string]string{}}

// tree is the digest of every file under dir: its "@/" path and bytes.
func (d *irDigests) tree(t *testing.T, dir string) string {
	return d.memo(t, d.trees, dir, true)
}

// flat is the digest of the files directly in dir.
func (d *irDigests) flat(t *testing.T, dir string) string {
	return d.memo(t, d.flats, dir, false)
}

func (d *irDigests) memo(t *testing.T, m map[string]string, dir string, deep bool) string {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if s, ok := m[dir]; ok {
		return s
	}
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == dir {
				return fs.SkipAll
			}
			return err
		}
		if e.IsDir() {
			if path != dir && (!deep || e.Name() == ".git") {
				return fs.SkipDir
			}
			return nil
		}
		if !e.Type().IsRegular() {
			fmt.Fprintf(h, "%s %v\x00", irWithin(path), e.Type())
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s %d:", irWithin(path), len(data))
		h.Write(data)
		return nil
	})
	if err != nil {
		t.Fatalf("digest %s: %v", dir, err)
	}
	s := hex.EncodeToString(h.Sum(nil))
	m[dir] = s
	return s
}

// irModuleDigest is the digest of every file of the module folder dir,
// the Go and Ergo files of its subfolders too; of the repository's own
// folder (a fixture without ergo.mod), its files alone.
func irModuleDigest(t *testing.T, dir string) string {
	if filepath.Clean(dir) == filepath.Clean(repoRoot()) {
		return irDigest.flat(t, dir)
	}
	return irDigest.tree(t, dir)
}

// irProgramKey is the key of the Go builder's lowering of pr: the
// sources of every unit it reaches, the files of its folder and module
// folder (go.mod, go.sum, its Go files; the module folder's whole tree), std and the repository's local
// Go modules, every replaced Go module folder, the functions it reaches,
// whether it is a test build, and the environment.
func irProgramKey(t *testing.T, pr irGoProgram) string {
	t.Helper()
	parts := []string{"agree golower/1", irWithin(pr.dir), irWithin(pr.module), fmt.Sprint(pr.tests)}
	parts = append(parts, irAnswerEnv()...)
	parts = append(parts, "std "+irDigest.tree(t, stdDir()), "modules "+irDigest.tree(t, filepath.Join(repoRoot(), "modules")))
	parts = append(parts, "dir "+irDigest.flat(t, pr.dir), "module "+irModuleDigest(t, pr.module))
	seen := map[string]bool{}
	var walk func(u lower.Unit)
	walk = func(u lower.Unit) {
		for _, r := range u.Go.Replace {
			if filepath.IsAbs(r.New) && !seen["replace "+r.New] {
				seen["replace "+r.New] = true
				parts = append(parts, "replace "+irWithin(r.New)+" "+irDigest.tree(t, r.New))
			}
		}
		for _, s := range u.Sources {
			if !seen[s.Path] {
				seen[s.Path] = true
				parts = append(parts, "source "+irWithin(s.Path), s.Text)
			}
		}
		for _, i := range u.Imports {
			walk(i)
		}
	}
	walk(pr.u)
	for _, f := range pr.reach {
		parts = append(parts, "reach "+f.Name)
	}
	return lawcache.Key(parts...)
}

// irLowerStore is the Go side's store: answers of this test binary.
type irLowerStore struct {
	once  sync.Once
	store *lawcache.Store
	hits  int
}

var irLowerCache = &irLowerStore{}

// lower is lowerGoIR of pr, read from the store when it holds the
// answer for pr's key, and kept there when it did not.
func (c *irLowerStore) lower(t *testing.T, pr irGoProgram) (irLowered, error) {
	t.Helper()
	c.once.Do(func() {
		if exe, err := os.Executable(); err == nil {
			c.store = irAgreeStore("agree-golower", exe)
		}
	})
	if c.store == nil {
		return lowerGoIR(pr)
	}
	key := irProgramKey(t, pr)
	if !irAgreeFresh() {
		if text, ok := c.store.Get(key); ok {
			var l irLowered
			if err := json.Unmarshal([]byte(text), &l); err == nil {
				c.hits++
				return l, nil
			}
		}
	}
	l, err := lowerGoIR(pr)
	if err != nil {
		return l, err
	}
	data, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(data); !strings.Contains(text, filepath.Clean(repoRoot())) && !strings.Contains(text, `"@/`) {
		c.store.Put(key, text)
	}
	return l, nil
}

// irDumpKey is the key of irdump's output for args: every manifest
// line's import path, its Go kind and type files' lines, the bytes of
// each source file it names, the files of each folder's module folder,
// the roots and tests lists, std, the local Go modules and the
// environment.
func irDumpKey(t *testing.T, manifest, roots, tests []string) string {
	t.Helper()
	parts := []string{"agree irdump/1"}
	parts = append(parts, irAnswerEnv()...)
	parts = append(parts, "std "+irDigest.tree(t, stdDir()), "modules "+irDigest.tree(t, filepath.Join(repoRoot(), "modules")))
	for _, line := range manifest {
		f := strings.Fields(line)
		if len(f) < 3 {
			t.Fatalf("manifest line %q", line)
		}
		parts = append(parts, "package "+f[0])
		for _, side := range f[1:3] {
			data, err := os.ReadFile(side)
			if err != nil {
				t.Fatal(err)
			}
			parts = append(parts, string(data))
		}
		for _, src := range f[3:] {
			data, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			parts = append(parts, "source "+irWithin(src), string(data))
			dir := filepath.Dir(src)
			parts = append(parts, "dir "+irDigest.flat(t, dir), "module "+irModuleDigest(t, moduleRoot(dir)))
		}
	}
	parts = append(parts, "roots")
	parts = append(parts, roots...)
	parts = append(parts, "tests")
	parts = append(parts, tests...)
	return lawcache.Key(parts...)
}

// irDumpStored is irdump's output kept under key by the irdump binary
// bin, its repository paths spelled again in this checkout.
func irDumpStored(bin, key string) ([]string, bool) {
	if irAgreeFresh() {
		return nil, false
	}
	text, ok := irAgreeStore("agree-irdump", bin).Get(key)
	if !ok {
		return nil, false
	}
	return lines(strings.ReplaceAll(text, "@/", irRootPrefix())), true
}

// irDumpKeep keeps irdump's output out under key, its repository paths
// written "@/"; an output that names another place of this run (a
// temporary file) or holds "@/" already is not kept.
func irDumpKeep(bin, key string, out []string, tmp string) {
	text := strings.Join(out, "\n")
	if strings.Contains(text, "@/") || strings.Contains(text, tmp) {
		return
	}
	text = strings.ReplaceAll(text, irRootPrefix(), "@/")
	if strings.Contains(text, filepath.Clean(repoRoot())) {
		return
	}
	irAgreeStore("agree-irdump", bin).Put(key, text)
}
