package check

import (
	"fmt"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The checked std snapshot's seam in the checker (CHK1b): the state a
// snapshot carries and installs, read and written here. The codec, the
// file format and the install live in internal/sketch/check/snapshot
// (SELF-CODEC), so the compile of this package, on the critical path of
// ergo's own build, does not carry them; the codec reaches the model's
// unexported names through codec_access_gen.go.

// stdBase is the first segment of the export: the process-wide state that
// universe and prelude hold, and the id counter after every std package is
// checked.
type stdBase struct {
	universe  *Scope
	errorName *TypeName
	goPkg     *Package
	ref       *Named
	any       *Named // go.Any (GB2)
	prelude   preludeResult
	nextID    int
}

// SnapshotSource is an installed snapshot, as the std importer reads it:
// Load decodes the package of segment key path (a std path or a
// ModuleSegment), with the segments it names, calling count for each
// package segment decoded with its key and package, and reports false
// when the snapshot does not hold it or it does not decode; BytesRead
// counts the bytes read from it (Work.StdSnapshotBytesRead). A snapshot
// does not carry the std mark: the std importer's count marks each
// package of a std segment, as its load marks a std package it checks
// (check.go).
type SnapshotSource interface {
	Load(path string, count func(string, *Package)) (*Package, bool)
	BytesRead() int64
}

// StdSnapshotNow checks the prelude and every std package (from source,
// unless this process installed a snapshot) and returns what a snapshot
// of them carries: the base, and every std package with its path, in an
// order where a package follows the packages it imports. It fails when a
// std package does not check or declares a derive, which a snapshot does
// not carry (exportgen skipFields).
func StdSnapshotNow() (base *stdBase, paths []string, pkgs []*Package, err error) {
	prelude()
	imp := Std.(*stdImporter)
	if err := imp.importAll(); err != nil {
		return nil, nil, nil, err
	}
	base = &stdBase{
		universe:  universeOnce.scope,
		errorName: universeOnce.errorName,
		goPkg:     universeOnce.goPkg,
		ref:       universeOnce.ref,
		any:       universeOnce.any,
		prelude:   preludeOnce.r,
	}
	// The order the packages became available is an order where every
	// package follows its imports.
	imp.mu.Lock()
	for _, path := range imp.order {
		paths = append(paths, path)
		pkgs = append(pkgs, imp.entries[path].pkg)
	}
	imp.mu.Unlock()
	// Std is one module for a law no case can feed (lawmodule.go): with
	// every std package checked, a binding no context of std feeds is an
	// error of std, as a module's is of its module. A package decoded
	// from an installed snapshot was judged when that snapshot was made.
	ds := ReportUnfed(pkgs)
	for i, pkg := range pkgs {
		if d := ds[pkg]; len(d) > 0 {
			name := ""
			for _, f := range pkg.files {
				if f.index == d[0].File {
					name = f.syntax.Name + ":"
				}
			}
			return nil, nil, nil, &ImportError{Path: paths[i], Reason: fmt.Sprintf("%s%s", name, d[0]), Broken: true}
		}
	}
	for _, pkg := range append([]*Package{preludeOnce.r.pkg}, pkgs...) {
		if pkg != nil && !pkg.Derive.Empty() {
			return nil, nil, nil, fmt.Errorf("check: std package %s has a derive record, which the std snapshot does not carry", pkg.Path)
		}
	}
	nextID.Lock()
	base.nextID = nextID.n
	nextID.Unlock()
	return base, paths, pkgs, nil
}

// BasePrelude is the prelude package of a snapshot's base, whose files a
// snapshot's base segment reaches first.
func BasePrelude(base *stdBase) *Package { return base.prelude.pkg }

// ErrInstalled refuses a snapshot after this process has built its
// universe: the checked state is already in use.
var ErrInstalled = fmt.Errorf("check: the universe is already built; a std snapshot installs only before the first check")

// InstallBase makes a decoded snapshot's base this process's universe and
// prelude, and lets Std decode each std package from src on its first
// import. It must run before the first check; after it, it returns
// ErrInstalled and changes nothing.
func InstallBase(base *stdBase, src SnapshotSource) error {
	installed := false
	universeOnce.Do(func() {
		installed = true
		universeOnce.scope = base.universe
		universeOnce.errorName = base.errorName
		universeOnce.goPkg = base.goPkg
		universeOnce.ref = base.ref
		universeOnce.any = base.any
	})
	if !installed {
		return ErrInstalled
	}
	installPrelude(base.prelude) // check.go marks it std
	imp := Std.(*stdImporter)
	imp.mu.Lock()
	imp.snap = src
	imp.mu.Unlock()
	nextID.Lock()
	nextID.n = max(nextID.n, base.nextID)
	nextID.Unlock()
	return nil
}

// RestoreChecker rebuilds what the codec does not carry of a decoded
// checker: its type table substitutes rows through it (newChecker), and
// starts its memos empty (exportgen skipFields).
func RestoreChecker(c *checker) {
	if c.types != nil {
		c.types.substRow = c.substRow
		c.types.of = map[Type]int32{}
		c.types.foreign = map[*Row]*Row{}
		c.types.resolved = map[*Row]*Row{}
		c.types.varRows = map[*RowVar]*Row{}
		c.types.scoped = map[Type]int32{}
		c.types.scopedRows = map[*Row]*Row{}
		c.types.scopedVarRows = map[*RowVar]*Row{}
	}
}

// ModuleSegment is the segment key of the package at path of a required
// module, module naming the module's version and content hash: no std
// path holds the separator, so a module segment never serves std, and a
// segment of another version or content is another key.
func ModuleSegment(path, module string) string { return path + "\x00" + module }

// SegmentPackage is the package path of a segment key.
func SegmentPackage(key string) string {
	if i := strings.IndexByte(key, 0); i >= 0 {
		return key[:i]
	}
	return key
}

// InstalledPackage is the module package of segment key (ModuleSegment)
// in the snapshot this process installed, decoded with the segments it
// names, and whether the snapshot holds it and it decodes. The caller
// checks the package from source otherwise. Work.ModulePackagesDecoded
// counts each module segment decoded.
func InstalledPackage(key string) (*Package, bool) {
	if !strings.Contains(key, "\x00") {
		return nil, false
	}
	imp := Std.(*stdImporter)
	imp.mu.Lock()
	snap := imp.snap
	imp.mu.Unlock()
	if snap == nil {
		return nil, false
	}
	return snap.Load(key, imp.decoded)
}

// ModuleImporter wraps the importer a required module's package is checked
// with, so the package can go into a snapshot (snapshot.ExportSnapshot):
// the codec carries the wrapper and not the importer, which serves the
// check alone (a checked package imports nothing later). A package decoded
// from a snapshot is never checked again; its wrapper imports nothing.
func ModuleImporter(imp interface {
	GoImporter
	EmbedImporter
}) Importer {
	return &moduleImporter{next: imp}
}

type moduleImporter struct {
	next interface {
		GoImporter
		EmbedImporter
	}
}

func (m *moduleImporter) Import(path string) (*Package, error) {
	if m.next == nil {
		return nil, fmt.Errorf("check: a package decoded from a snapshot imports nothing (%s)", path)
	}
	return m.next.Import(path)
}

func (m *moduleImporter) GoBindings() *GoBindings {
	if m.next == nil {
		return NewGoBindings(nil)
	}
	return m.next.GoBindings()
}

func (m *moduleImporter) ResolveEmbed(file *syntax.File, pattern string, tree bool) ([]EmbedFile, error) {
	if m.next == nil {
		return nil, fmt.Errorf("a package decoded from a snapshot embeds nothing")
	}
	return m.next.ResolveEmbed(file, pattern, tree)
}

// HostBindings are the binding packages of a program's Go bindings, by
// path (a user export writes each as a segment of its own; B7e).
func (g *GoBindings) HostBindings() map[string]*Package { return g.pkgs }

// LiveScopesForTest are this process's universe and prelude scopes, so a
// test sees that a late install changes neither. Only tests call it.
func LiveScopesForTest() (u, p *Scope) { return universe(), prelude().scope }

// DeferredRowForTest is the smallest repro of export-subst-row-lost
// (snapshot's TestStdSnapshotRestoresDeferredRows): a base whose ref is a
// Named over a generic inferred row, its concrete use not yet resolved,
// and the row's solution as a base holds it. Only tests call it.
func DeferredRowForTest() (base *stdBase, solution func(*stdBase) string, err error) {
	file, err := syntax.Parse("type Failed {}\nfunc apply[E](f func() fails E) { try f() }\n")
	if err != nil {
		return nil, nil, err
	}
	c := newChecker("main", Std, prelude().scope)
	c.opts.Infer = true
	c.run([]*syntax.File{file})
	if ds := c.sorted(); len(ds) != 0 {
		return nil, nil, fmt.Errorf("%v", ds)
	}
	f := c.pkg.Funcs[0]
	failed := c.pkg.Types[0].Type
	bound := &RowArg{Row: newRow().with(c.types, failed)}
	view := c.subst(f.errVar, map[*TypeParam]Type{f.Sig.TParams[0]: bound}).(*RowVar)
	// ref is a graph root; under holds the deferred row and ctx reaches
	// its owning checker. Solution is not forced before the encode.
	base = &stdBase{ref: &Named{under: view}}
	return base, func(b *stdBase) string { return b.ref.under.(*RowVar).Solution().String() }, nil
}

// ResetStdForTest forgets this process's universe, prelude, std packages
// and id counter, so a test can check the same program from source and
// from a snapshot in one process (the equality property of CHK1b). Only
// tests call it, while no check runs; the compiler never does.
func ResetStdForTest() {
	universeOnce = universeState{}
	preludeOnce = preludeState{}
	Std = newStdImporter()
	nextID.Lock()
	nextID.n = 0
	nextID.Unlock()
}
