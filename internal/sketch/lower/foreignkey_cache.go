package lower

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	runtimefiles "ergo.local/prototype/runtime"
)

const foreignKeyCacheVersion = "fk-v1"

// UseForeignKeyCache enables persistent disk caching of computed foreign
// closure keys under root (typically build.DefaultRoot()). Passing "" disables
// disk caching. Cache failures always fall back cleanly to live computation.
func UseForeignKeyCache(root string) {
	fkDiskCache.setRoot(root)
}

type fkDiskFile struct {
	Version   string                   `json:"version"`
	Namespace string                   `json:"namespace"`
	Runtime   map[string]ForeignKey    `json:"runtime,omitempty"`
	Local     map[string]fkLocalCached `json:"local,omitempty"`
}

type fkLocalCached struct {
	Key  ForeignKey        `json:"key"`
	Deps map[string]string `json:"deps"` // "variantTag\x00importPath" -> filesHash
}

type fkCacheStore struct {
	mu     sync.Mutex
	root   string
	loaded map[string]*fkDiskFile // namespace -> in-memory mirror of disk file
}

var fkDiskCache = &fkCacheStore{
	loaded: map[string]*fkDiskFile{},
}

func (c *fkCacheStore) setRoot(root string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.root == root {
		return
	}
	c.root = root
	c.loaded = map[string]*fkDiskFile{}
}

func (c *fkCacheStore) loadLocked(ns string) *fkDiskFile {
	if c.root == "" {
		return nil
	}
	if f, ok := c.loaded[ns]; ok {
		return f
	}
	f := &fkDiskFile{
		Version:   foreignKeyCacheVersion,
		Namespace: ns,
		Runtime:   map[string]ForeignKey{},
		Local:     map[string]fkLocalCached{},
	}
	path := c.filePath(ns)
	if data, err := os.ReadFile(path); err == nil {
		var disk fkDiskFile
		if json.Unmarshal(data, &disk) == nil && disk.Version == foreignKeyCacheVersion && disk.Namespace == ns {
			if disk.Runtime != nil {
				f.Runtime = disk.Runtime
			}
			if disk.Local != nil {
				f.Local = disk.Local
			}
		}
	}
	c.loaded[ns] = f
	return f
}

func (c *fkCacheStore) filePath(ns string) string {
	shortNS := ns
	if len(shortNS) > 32 {
		shortNS = shortNS[:32]
	}
	return filepath.Join(c.root, "foreignkeys", "fk-"+shortNS+".json")
}

func (c *fkCacheStore) saveLocked(ns string, f *fkDiskFile) {
	if c.root == "" {
		return
	}
	target := c.filePath(ns)
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(f)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".fk-*")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
	}
}

func (c *fkCacheStore) getRuntime(ns, key string) (ForeignKey, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.loadLocked(ns)
	if f == nil {
		return ForeignKey{}, false
	}
	k, ok := f.Runtime[key]
	return k, ok
}

func (c *fkCacheStore) putRuntime(ns, key string, val ForeignKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.loadLocked(ns)
	if f == nil {
		return
	}
	if prev, ok := f.Runtime[key]; ok && prev == val {
		return
	}
	f.Runtime[key] = val
	c.saveLocked(ns, f)
}

func (c *fkCacheStore) getLocal(ns, key string) (fkLocalCached, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.loadLocked(ns)
	if f == nil {
		return fkLocalCached{}, false
	}
	entry, ok := f.Local[key]
	return entry, ok
}

func (c *fkCacheStore) putLocal(ns, key string, entry fkLocalCached) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.loadLocked(ns)
	if f == nil {
		return
	}
	f.Local[key] = entry
	c.saveLocked(ns, f)
}

// runtimeSourcesHash hashes all embedded runtime Go and assembly sources once
// per process so any edit to runtime/* immediately invalidates the disk cache.
var runtimeSourcesHash = sync.OnceValue(func() string {
	files, err := runtimefiles.Files("runtime")
	if err != nil {
		return ""
	}
	sorted := append([]runtimefiles.File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, f := range sorted {
		if strings.HasSuffix(f.Path, "_test.go") || !strings.HasSuffix(f.Path, ".go") && !strings.HasSuffix(f.Path, ".s") {
			continue
		}
		h.Write([]byte(f.Path))
		h.Write([]byte{0})
		h.Write(f.Data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
})

// stdEnvFingerprint hashes the active Go standard library environment
// (Go version, GOROOT path, whether std is typed, and GOROOT/VERSION +
// GOROOT/src/go.mod contents) so toolchain or GOROOT changes never reuse
// stale closure keys.
func stdEnvFingerprint() string {
	s := hostStd()
	goroot := ""
	if s != nil {
		goroot = s.ctx.GOROOT
	}
	h := sha256.New()
	writePart := func(label, val string) {
		h.Write([]byte(label))
		h.Write([]byte{0})
		h.Write([]byte(val))
		h.Write([]byte{0})
	}
	writePart("ver", foreignKeyCacheVersion)
	writePart("go", goruntime.Version())
	writePart("goroot", goroot)
	writePart("untyped", stdUntypedWhy())
	if goroot != "" {
		if b, err := os.ReadFile(filepath.Join(goroot, "VERSION")); err == nil {
			writePart("goroot-version", string(b))
		}
		if b, err := os.ReadFile(filepath.Join(goroot, "src", "go.mod")); err == nil {
			writePart("goroot-gomod", string(b))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func runtimeCacheNamespace() string {
	h := sha256.New()
	h.Write([]byte("runtime\x00"))
	h.Write([]byte(runtimeSourcesHash()))
	h.Write([]byte{0})
	h.Write([]byte(stdEnvFingerprint()))
	return hex.EncodeToString(h.Sum(nil))
}

func localCacheNamespace() string {
	h := sha256.New()
	h.Write([]byte("local\x00"))
	h.Write([]byte(stdEnvFingerprint()))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *ForeignKeySource) localCacheKey(mod, target string) string {
	var b strings.Builder
	if s.Target != nil {
		b.WriteString(s.Target.ID())
	} else {
		b.WriteString("nil")
	}
	for _, v := range s.Variants {
		b.WriteByte('|')
		b.WriteString(v.Tag)
		b.WriteByte('=')
		b.WriteString(v.Target.ID())
	}
	b.WriteString("|always=")
	b.WriteString(strings.Join(s.Always, ","))
	b.WriteByte('|')
	b.WriteString(mod)
	b.WriteByte('|')
	b.WriteString(target)
	return b.String()
}

func (s *ForeignKeySource) localDepsValid(mod string, deps map[string]string) bool {
	if len(deps) == 0 {
		return false
	}
	for k, wantHash := range deps {
		prefix, depPkg, ok := strings.Cut(k, "\x00")
		if !ok {
			return false
		}
		m, _, d, _ := s.module(depPkg)
		if m != mod || d == "" {
			return false
		}
		var reader func(string) []GoSourceFile
		if prefix == "" {
			reader = s.ReadDir
		} else {
			idx, err := strconv.Atoi(prefix)
			if err != nil || idx < 0 || idx >= len(s.Variants) {
				return false
			}
			reader = s.Variants[idx].Read
		}
		if reader == nil {
			if wantHash == "<unloaded>" {
				continue
			}
			return false
		}
		files := reader(d)
		if wantHash == "<unloaded>" {
			if files != nil {
				return false
			}
			continue
		}
		if files == nil || filesHash(files) != wantHash {
			return false
		}
	}
	return true
}
