package sketchfront

import (
	"fmt"
	"slices"
	"sync"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/check/snapshot"
	"ergo.local/prototype/internal/sketch/fingerprint"
	"ergo.local/prototype/internal/sketch/syntax"
)

// CheckCache holds the checked packages of a process's earlier checks, so
// a later check takes a package as it was instead of checking it again
// (compile-speed/cs5b.md §6; plan §5.3, the early cutoff). A package is
// taken when its files' bytes, its environment and the interface
// fingerprints of its imports are what they were: its check would be the
// same. Its export segment is then decoded against the current check of
// its imports, whose objects it names by object path (snapshot.UserExport),
// so an importer is reused while an import it reaches is checked again
// with an unchanged interface.
//
// The cache lives in one process (tests, and the coming ergo serve): the
// segments name std's objects as this process holds them. Persisting it
// across processes is the next step (the installed std snapshot's ids).
//
// Decided: the key holds the files' bytes, not their token text (the
// source fingerprint), by correctness: a decoded package's syntax trees
// carry their positions, so a comment edit checks its own package again,
// and its importers stop at its unchanged interface. Reverse when
// positions move to a side table (plan §5.3).
type CheckCache struct {
	mu      sync.Mutex
	u       *snapshot.UserExport
	entries map[string]cacheEntry // by key
}

// cacheEntry is one package's last clean check.
type cacheEntry struct {
	seg int // its segment in u
	// bindings are the module bindings the program had read when the
	// package was checked, by Go import path, with their text: a superset
	// of the ones the check read, so an edit of one checks it again.
	bindings map[string]string
	// goBodies are the module Go packages the program had read, by
	// import path, with the digest of their built files (loader.goBodies),
	// a superset as bindings are: an edit of a body checks it again.
	goBodies map[string]string
	files    int
}

// NewCheckCache returns an empty cache.
func NewCheckCache() *CheckCache { return &CheckCache{entries: map[string]cacheEntry{}} }

// CheckDir is the package CheckDir with the cache: each package whose
// check would be the one cached is taken from it (stage counter
// PackagesReused) and the others are checked (PackagesChecked) and
// cached. The analysis equals CheckDir's.
func (c *CheckCache) CheckDir(dir string) (Analysis, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return checkDirCached(hostFS{}, dir, hostPlatform(), c)
}

// key is the cache key of p checked as checkPath: its files' names and
// bytes, the environment and the interface fingerprints of its imports.
// ok is false when an import has no interface (it was not clean).
func (c *CheckCache) key(l *loader, p *loadedPackage, checkPath string) (string, bool) {
	parts := []string{"ergo-checkcache-1", p.path, checkPath, l.environment(p)}
	for _, f := range p.files {
		parts = append(parts, f.rel, f.text)
	}
	imports := slices.Clone(p.imports)
	slices.SortFunc(imports, func(a, b localImport) int {
		switch {
		case a.path < b.path:
			return -1
		case a.path > b.path:
			return 1
		}
		return 0
	})
	for _, imp := range imports {
		fp, ok := l.fps[l.resolvedImportPath(p, imp.path)]
		if !ok || fp.Interface == "" {
			return "", false
		}
		parts = append(parts, imp.path, fp.Interface)
	}
	return fingerprint.Of(parts...), true
}

// reuse returns the cached check of p under key, decoded against the
// current check of its imports, or false.
func (c *CheckCache) reuse(l *loader, p *loadedPackage, key string) (*check.Package, bool) {
	e, ok := c.entries[key]
	if !ok || c.u == nil || e.files != len(p.files) {
		return nil, false
	}
	for path, text := range e.bindings {
		if now, ok := l.goBinding(path); !ok || now != text {
			return nil, false
		}
	}
	for path, digest := range e.goBodies {
		l.GoPackage(path)
		if l.goBodies[path] != digest {
			return nil, false
		}
	}
	c.u.SetImporter(l)
	pkg, err := c.u.DecodeSegment(e.seg, func(path string) *check.Package {
		if q := l.pkgs[l.resolvedImportPath(p, path)]; q != nil {
			return q.pkg
		}
		return nil
	}, l)
	if err != nil || pkg == nil || len(pkg.SourceFiles()) != len(p.files) {
		return nil, false
	}
	return pkg, true
}

// store caches pkg, p's clean check, under key, and writes its segment so
// later packages of this check name its objects by path. A package the
// export cannot carry drops the export: the cache starts again empty.
func (c *CheckCache) store(l *loader, p *loadedPackage, key string, pkg *check.Package) {
	if c.u == nil {
		u, err := snapshot.NewUserExport(l)
		if err != nil {
			return
		}
		c.u = u
	}
	c.u.SetImporter(l)
	seg, err := c.u.Add(pkg)
	if err != nil {
		c.u, c.entries = nil, map[string]cacheEntry{}
		return
	}
	if old, ok := c.entries[key]; ok && old.seg != seg {
		c.u.Release(old.seg)
	}
	c.entries[key] = cacheEntry{seg: seg, bindings: copyBindings(l.bindings), goBodies: copyBindings(l.goBodies), files: len(p.files)}
}

func copyBindings(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// environment is what the check of p reads besides its files and its
// imports: the module, the platform, go.mod and go.sum, and the assets p
// embeds (Analysis.Fingerprints without the bindings, which the cache
// compares by their text).
func (l *loader) environment(p *loadedPackage) string {
	s := fmt.Sprintf("module %q\nplatform %s/%s\ngo.mod %q\ngo.sum %q\n", l.module, l.platform.GOOS, l.platform.GOARCH, l.goModule.GoModFile(), l.goModule.Sum)
	names := make([]string, 0, len(p.assets))
	for name := range p.assets {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		s += fmt.Sprintf("asset %q %q\n", name, p.assets[name])
	}
	return s
}

// fingerprintAfter records p's fingerprints after its check or its reuse,
// for the keys of its importers.
func (l *loader) fingerprintAfter(p *loadedPackage, checkPath string, files []*syntax.File) {
	var imports []fingerprint.Package
	for _, imp := range p.imports {
		imports = append(imports, l.fps[l.resolvedImportPath(p, imp.path)])
	}
	l.fps[p.path] = check.FingerprintOf(p.path, checkPath, files, p.pkg, p.pkg != nil && !p.broken, l.environment(p), imports)
}
