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
	"sync/atomic"
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
	if path == "" {
		return ""
	}
	root := filepath.Clean(repoRoot())
	clean := filepath.Clean(path)
	if out := lawcache.Within(root, clean); out != clean || out == "@" || strings.HasPrefix(out, "@/") {
		return out
	}
	if realRoot, err := filepath.EvalSymlinks(root); err == nil {
		if realPath, err := filepath.EvalSymlinks(clean); err == nil {
			if out := lawcache.Within(realRoot, realPath); out != realPath || out == "@" || strings.HasPrefix(out, "@/") {
				return out
			}
		}
	}
	return path
}

// irRootPrefix is the repository's folder as answers spell it, which a
// stored answer holds as "@/".
func irRootPrefix() string {
	return lawcache.Unroot(filepath.Clean(repoRoot()), "@") + string(filepath.Separator)
}

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

type irDigestEntry struct {
	once sync.Once
	sum  string
}

// irDigests hashes files and folders once per run.
type irDigests struct {
	mu    sync.Mutex
	trees map[string]*irDigestEntry
	flats map[string]*irDigestEntry
}

func newIRDigests() *irDigests {
	return &irDigests{trees: map[string]*irDigestEntry{}, flats: map[string]*irDigestEntry{}}
}

var irDigest = newIRDigests()

// tree is the digest of every file under dir: its "@/" path and bytes.
func (d *irDigests) tree(t *testing.T, dir string) string {
	return d.memo(t, d.trees, dir, true)
}

// flat is the digest of the files directly in dir.
func (d *irDigests) flat(t *testing.T, dir string) string {
	return d.memo(t, d.flats, dir, false)
}

func (d *irDigests) memo(t *testing.T, m map[string]*irDigestEntry, dir string, deep bool) string {
	if dir == "" {
		return ""
	}
	d.mu.Lock()
	e := m[dir]
	if e == nil {
		e = &irDigestEntry{}
		m[dir] = e
	}
	d.mu.Unlock()
	e.once.Do(func() {
		h := sha256.New()
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) && path == dir {
					return fs.SkipAll
				}
				return err
			}
			if entry.IsDir() {
				if path != dir && (!deep || entry.Name() == ".git") {
					return fs.SkipDir
				}
				return nil
			}
			if !entry.Type().IsRegular() {
				fmt.Fprintf(h, "%s %v\x00", irWithin(path), entry.Type())
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
			if t != nil {
				t.Fatalf("digest %s: %v", dir, err)
			}
			panic(fmt.Sprintf("digest %s: %v", dir, err))
		}
		e.sum = hex.EncodeToString(h.Sum(nil))
	})
	return e.sum
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
	if t != nil {
		t.Helper()
	}
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
	once   sync.Once
	store  *lawcache.Store
	hits   atomic.Int64
	mu     sync.Mutex
	unkept []string
}

var irLowerCache = &irLowerStore{}

func (c *irLowerStore) hitCount() int { return int(c.hits.Load()) }

func (c *irLowerStore) unkeptReasons() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.unkept)
}

type irFileEnvelope struct {
	V      int             `json:"v"`
	Files  []string        `json:"files"`
	Funcs  json.RawMessage `json:"funcs"`
	Shared json.RawMessage `json:"shared,omitempty"`
}

func irMapSegments(p string, fn func(string) (string, bool, bool)) (string, bool) {
	parts := strings.Split(p, "#ergo-inline-")
	changed := false
	for i, part := range parts {
		chain := strings.Split(part, ">")
		chainChanged := false
		for j, seg := range chain {
			u, segChanged, ok := fn(seg)
			if !ok {
				return "", false
			}
			if segChanged {
				chain[j] = u
				chainChanged = true
			}
		}
		if chainChanged {
			parts[i] = strings.Join(chain, ">")
			changed = true
		}
	}
	if !changed {
		return p, true
	}
	return strings.Join(parts, "#ergo-inline-"), true
}

func irWithinSegments(p string) (string, bool) {
	return irMapSegments(p, func(part string) (string, bool, bool) {
		if part == "@" || strings.HasPrefix(part, "@/") {
			return "", false, false
		}
		if w := irWithin(part); w != part {
			return w, true, true
		}
		return part, false, true
	})
}

func irUnrootSegments(root, p string) (string, bool) {
	changed := false
	out, _ := irMapSegments(p, func(part string) (string, bool, bool) {
		if part == "@" || strings.HasPrefix(part, "@/") {
			changed = true
			return lawcache.Unroot(root, part), true, true
		}
		return part, false, true
	})
	return out, changed
}

func irLowerPack(l irLowered) (string, bool) {
	text, why := irLowerPackReason(l)
	return text, why == ""
}

func irNamesContext(text, root string) string {
	candidates := []string{root}
	if realRoot, err := filepath.EvalSymlinks(root); err == nil && realRoot != root {
		candidates = append(candidates, realRoot)
	}
	for _, c := range candidates {
		if i := strings.Index(text, c); i >= 0 {
			lo := max(i-24, 0)
			hi := min(i+len(c)+48, len(text))
			return text[lo:hi]
		}
	}
	return "matched"
}

func irWithinRaw(raw json.RawMessage, root string) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return raw, false
	}
	s := string(raw)
	r := strings.ReplaceAll(s, irRootPrefix(), "@/")
	if realRoot, err := filepath.EvalSymlinks(root); err == nil && realRoot != root {
		r = strings.ReplaceAll(r, realRoot+string(filepath.Separator), "@/")
	}
	if r == s {
		return raw, false
	}
	return json.RawMessage(r), true
}

func irUnrootRaw(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return raw, false
	}
	s := string(raw)
	r := strings.ReplaceAll(s, "@/", irRootPrefix())
	if r == s {
		return raw, false
	}
	return json.RawMessage(r), true
}

func irLowerPackReason(l irLowered) (string, string) {
	root := filepath.Clean(repoRoot())
	packed := irLowered{
		Files:   make([]irLoweredFile, len(l.Files)),
		Exports: l.Exports,
	}
	copy(packed.Files, l.Files)
	for i, file := range packed.Files {
		var env irFileEnvelope
		dec := json.NewDecoder(strings.NewReader(file.Data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&env); err != nil {
			return "", "decode: " + err.Error()
		}
		if strings.Contains(string(env.Funcs), "@/") || strings.Contains(string(env.Shared), "@/") {
			return "", "contains @/"
		}
		changed := false
		for j, p := range env.Files {
			w, ok := irWithinSegments(p)
			if !ok {
				return "", "within: " + p
			}
			if w != p {
				env.Files[j] = w
				changed = true
			}
		}
		if w, ok := irWithinRaw(env.Funcs, root); ok {
			env.Funcs = w
			changed = true
		}
		if w, ok := irWithinRaw(env.Shared, root); ok {
			env.Shared = w
			changed = true
		}
		if changed {
			b, err := json.Marshal(&env)
			if err != nil {
				return "", "marshal-env: " + err.Error()
			}
			packed.Files[i].Data = string(b)
		}
	}
	data, err := json.Marshal(packed)
	if err != nil {
		return "", "marshal-packed: " + err.Error()
	}
	text := string(data)
	if lawcache.Names(text, root) {
		return "", "names: " + irNamesContext(text, root)
	}
	return text, ""
}

func irLowerUnpack(text string) (irLowered, bool) {
	var l irLowered
	if err := json.Unmarshal([]byte(text), &l); err != nil {
		return irLowered{}, false
	}
	root := filepath.Clean(repoRoot())
	for i := range l.Files {
		if !strings.Contains(l.Files[i].Data, `"@"`) && !strings.Contains(l.Files[i].Data, "@/") {
			continue
		}
		var env irFileEnvelope
		dec := json.NewDecoder(strings.NewReader(l.Files[i].Data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&env); err != nil {
			return irLowered{}, false
		}
		changed := false
		for j, p := range env.Files {
			if u, ok := irUnrootSegments(root, p); ok {
				env.Files[j] = u
				changed = true
			}
		}
		if u, ok := irUnrootRaw(env.Funcs); ok {
			env.Funcs = u
			changed = true
		}
		if u, ok := irUnrootRaw(env.Shared); ok {
			env.Shared = u
			changed = true
		}
		if changed {
			b, err := json.Marshal(&env)
			if err != nil {
				return irLowered{}, false
			}
			l.Files[i].Data = string(b)
		}
	}
	return l, true
}

// lower is lowerGoIR of pr, read from the store when it holds the
// answer for pr's key, and kept there when it did not.
func (c *irLowerStore) lower(t *testing.T, pr irGoProgram) (irLowered, error) {
	if t != nil {
		t.Helper()
	}
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
			if l, ok := irLowerUnpack(text); ok {
				c.hits.Add(1)
				return l, nil
			}
		}
	}
	l, err := lowerGoIR(pr)
	if err != nil {
		return l, err
	}
	if text, why := irLowerPackReason(l); why == "" {
		c.store.Keep(key, text)
	} else {
		c.mu.Lock()
		c.unkept = append(c.unkept, fmt.Sprintf("%s(tests=%v): %s", irWithin(pr.dir), pr.tests, why))
		c.mu.Unlock()
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
	root := filepath.Clean(repoRoot())
	text = strings.ReplaceAll(text, irRootPrefix(), "@/")
	if realRoot, err := filepath.EvalSymlinks(root); err == nil && realRoot != root {
		text = strings.ReplaceAll(text, realRoot+string(filepath.Separator), "@/")
	}
	if lawcache.Names(text, root) {
		return
	}
	irAgreeStore("agree-irdump", bin).Keep(key, text)
}
