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

	"ergo.local/prototype/internal/sketch/lawcache"
	runtimefiles "ergo.local/prototype/runtime"
)

// foreignKeyCacheVersion names the encoding of a kept foreign key and of
// its key's parts: a change of either is a new version.
const foreignKeyCacheVersion = "fk-v2"

// UseForeignKeyCache keeps computed foreign closure keys in the store open
// gives (the law-verdict store, lawcache: one format for every kept
// answer, SHARED-RESULT-CACHE), opened at the first lookup. A nil open,
// or an open that gives nil, keeps nothing. A miss or an entry that does
// not decode falls back to computing the key.
func UseForeignKeyCache(open func() *lawcache.Store) {
	fkDiskCache.set(open)
}

type fkLocalCached struct {
	Key  ForeignKey        `json:"key"`
	Deps map[string]string `json:"deps"` // "variantTag\x00importPath" -> filesHash
}

// fkCacheStore is the store of foreign keys, opened once.
type fkCacheStore struct {
	mu     sync.Mutex
	open   func() *lawcache.Store
	opened bool
	store  *lawcache.Store
}

var fkDiskCache = &fkCacheStore{}

func (c *fkCacheStore) set(open func() *lawcache.Store) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.open, c.opened, c.store = open, false, nil
}

// get is the store, opened at its first use.
func (c *fkCacheStore) get() *lawcache.Store {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.opened && c.open != nil {
		c.store, c.opened = c.open(), true
	}
	return c.store
}

// entryKey is the store key of a foreign key: its kind ("runtime" or
// "local"), the namespace of the environment that computed it, and the
// lookup's own key. None of them names a workspace's folder: a local
// module's entry is checked against its files' hashes as they are now
// (localDepsValid).
func entryKey(kind, ns, key string) string {
	return lawcache.Key("foreign key "+kind+"/"+foreignKeyCacheVersion, ns, key)
}

func (c *fkCacheStore) getRuntime(ns, key string) (ForeignKey, bool) {
	text, ok := c.get().Get(entryKey("runtime", ns, key))
	var k ForeignKey
	if !ok || json.Unmarshal([]byte(text), &k) != nil {
		return ForeignKey{}, false
	}
	return k, true
}

func (c *fkCacheStore) putRuntime(ns, key string, val ForeignKey) {
	if data, err := json.Marshal(val); err == nil {
		c.get().Keep(entryKey("runtime", ns, key), string(data))
	}
}

func (c *fkCacheStore) getLocal(ns, key string) (fkLocalCached, bool) {
	text, ok := c.get().Get(entryKey("local", ns, key))
	var entry fkLocalCached
	if !ok || json.Unmarshal([]byte(text), &entry) != nil {
		return fkLocalCached{}, false
	}
	return entry, true
}

func (c *fkCacheStore) putLocal(ns, key string, entry fkLocalCached) {
	if data, err := json.Marshal(entry); err == nil {
		c.get().Keep(entryKey("local", ns, key), string(data))
	}
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
