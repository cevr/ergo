package snapshot

import (
	"encoding/binary"
	"fmt"
	"slices"

	"ergo.local/prototype/internal/sketch/check"
)

// CrossRef is one reference that a user package's segment makes to an
// object that an earlier user package's segment wrote: the reference
// CS5b-2 makes symbolic (compile-speed/cs5b.md §2, §3).
type CrossRef struct {
	// From and To are the paths of the referring and the referred package.
	From, To string
	// Obj is the object referred to; nil for a syntax node, which Node
	// holds when the segment names it by node path.
	Obj, Node any
	// Path is Obj's object path in To (check.ResolvePath), or Node's node path
	// (ResolveNodePath), taken right after To's check; HasPath is false
	// when no path reaches it.
	Path    string
	HasPath bool
}

// UserExport writes std as this process holds it, then each user package
// as it is checked, as segments of one export, in the order CS5b-3 will:
// a package's segment right after its own check, before any importer's.
// A user segment names an object of an earlier user segment by its
// object path (tag 4), so Decode reads it against whichever check of that
// package is current. It records the references each user segment makes
// into earlier user segments (Refs).
type UserExport struct {
	e     *encoder
	first int // the index of the first user segment
	paths []string
	segs  []segment
	// nodeCross marks the user segments whose syntax table names a syntax
	// node of an earlier user segment: a field of one package's tree that
	// points into another's, which no node path names.
	nodeCross map[int]bool
	// pkgs are the packages of the user segments, and nodeTables their
	// node paths, by segment index, each taken when a later segment first
	// names one of its nodes (CS5b-4).
	pkgs       map[int]*check.Package
	nodeTables map[int]map[any]nodePath
	// stdObjs and stdNodes are std's objects and syntax nodes by id, for
	// Decode, built on first use.
	stdObjs *objTable
	// tables are the object paths of each user segment's package, by
	// segment index, taken when its segment is written.
	tables map[int]map[any]string
	// Record makes cross record Refs (the tests' measure).
	Record bool
	Refs   []CrossRef
	// sets are the programs' Go bindings (withGoBindings); host marks the
	// segments of their binding packages, written before the first user
	// segment that reaches them. Like std's, a host segment is the
	// loader's state, never checked again by an edit: a user segment names
	// its objects by id, and Decode reads them as this process holds them
	// (B7e).
	sets []*check.GoBindings
	host map[int]bool
	// done is each binding package's host segment.
	done map[*check.Package]int
}

// fixed reports a segment of std or of a Go binding package.
func (u *UserExport) fixed(seg int) bool { return seg < u.first || u.host[seg] }

// NewUserExport writes std's segments. importers are the values the user
// packages' checkers hold as their importer (a loader): the export names
// them as externals, as it names check.Std.
func NewUserExport(importers ...any) (u *UserExport, err error) {
	defer recoverExport(&err)
	// Every std package is in std's segments, so a user segment names std
	// objects there and never writes one (the std snapshot holds them all).
	base, paths, packages, err := check.StdSnapshotNow()
	if err != nil {
		return nil, err
	}
	e := newEncoder()
	importers = withGoBindings(importers)
	for i, imp := range importers {
		e.ext[imp] = uint64(len(externals()) + i)
	}
	u = &UserExport{e: e, first: len(packages) + 1, tables: map[int]map[any]string{}, nodeCross: map[int]bool{}, pkgs: map[int]*check.Package{}, nodeTables: map[int]map[any]nodePath{}, host: map[int]bool{}, done: map[*check.Package]int{}}
	for _, imp := range importers {
		if set, ok := imp.(*check.GoBindings); ok {
			u.sets = append(u.sets, set)
		}
	}
	u.segs = append(u.segs, e.segment("", func() { e.encPtrstdBase(base) }))
	u.paths = append(u.paths, "")
	for i, pkg := range packages {
		u.segs = append(u.segs, e.segment(paths[i], func() { e.encPtrPackage(pkg) }))
		u.paths = append(u.paths, paths[i])
	}
	e.crossRef = func(seg int, v any) { u.cross(seg, v) }
	e.symbolic = func(seg int, v any) (string, bool) {
		cur := len(e.objStarts) - 1
		if u.fixed(seg) || u.fixed(cur) || seg == cur {
			return "", false
		}
		p, ok := u.tables[seg][v]
		if !ok {
			e.failf("%T of %s has no object path", v, u.paths[seg])
		}
		return p, true
	}
	e.symbolicNode = func(x any) (int, nodePath, bool) {
		id, ok := e.nodes.Known(x)
		if !ok {
			return 0, "", false
		}
		seg, cur := segmentOf(e.nodeStarts, id), len(e.nodeStarts)-1
		if u.fixed(seg) || u.fixed(cur) || seg == cur {
			return 0, "", false
		}
		t, ok := u.nodeTables[seg]
		if !ok {
			t = nodePaths(u.pkgs[seg])
			u.nodeTables[seg] = t
		}
		p, ok := t[x]
		if !ok {
			e.failf("a %T of %s has no node path", x, u.paths[seg])
		}
		u.crossNode(seg, x, p)
		return seg, p, true
	}
	below := e.nodes.Below
	e.nodes.Below = func(id uint64) {
		below(id)
		seg, cur := segmentOf(e.nodeStarts, id), len(e.nodeStarts)-1
		if !u.fixed(seg) && !u.fixed(cur) && seg != cur {
			u.nodeCross[cur] = true
		}
		u.cross(seg, nil)
	}
	return u, nil
}

func (u *UserExport) crossNode(seg int, x any, p nodePath) {
	n := len(u.Refs)
	u.cross(seg, nil)
	if len(u.Refs) > n {
		r := &u.Refs[n]
		r.Node, r.Path, r.HasPath = x, string(p), true
	}
}

func (u *UserExport) cross(seg int, v any) {
	cur := len(u.e.objStarts) - 1
	if u.Record && !u.fixed(seg) && !u.fixed(cur) && seg != cur {
		r := CrossRef{From: u.paths[cur], To: u.paths[seg], Obj: v}
		if v != nil {
			r.Path, r.HasPath = u.tables[seg][v]
		}
		u.Refs = append(u.Refs, r)
	}
}

// Add writes pkg's segment and returns its index. A package the codec
// cannot carry (a function value in its state) is an error; the export is
// then unusable, and its caller drops it. A derive record is not carried
// (exportgen skipFields): a caller that decodes a segment does not add a
// package whose record is not empty.
func (u *UserExport) Add(pkg *check.Package) (seg int, err error) {
	defer recoverExport(&err)
	u.addHosts()
	if k, ok := u.done[pkg]; ok {
		return k, nil // a Go binding package, written by addHosts
	}
	seg = len(u.e.objStarts)
	u.paths = append(u.paths, pkg.Path)
	u.tables[seg] = check.ObjectPaths(pkg)
	u.pkgs[seg] = pkg
	u.segs = append(u.segs, u.e.segment(pkg.Path, func() { u.e.encPtrPackage(pkg) }))
	return seg, nil
}

// SetImporter makes imp the importer the export names as its first
// importer external (NewUserExport's first importer): each check has a
// loader of its own, and a decoded checker holds the current one.
func (u *UserExport) SetImporter(imp any) {
	slot := uint64(len(externals()))
	for x, i := range u.e.ext {
		if i == slot {
			delete(u.e.ext, x)
		}
	}
	u.e.ext[imp] = slot
}

// Release drops what segment seg keeps for encoding later segments (its
// object paths) and its bytes: a later segment of the same package
// replaces it, and no later segment may name its objects.
func (u *UserExport) Release(seg int) {
	delete(u.tables, seg)
	delete(u.pkgs, seg)
	delete(u.nodeTables, seg)
	if seg >= u.first && seg < len(u.segs) {
		u.segs[seg].data = nil
	}
}

// addHosts writes a segment for each binding package of the programs' Go
// bindings that no segment holds yet, in path order. A binding package one
// of them reaches, made since, joins that segment.
func (u *UserExport) addHosts() {
	for _, set := range u.sets {
		pkgs := set.HostBindings()
		paths := make([]string, 0, len(pkgs))
		for path := range pkgs {
			paths = append(paths, path)
		}
		slices.Sort(paths)
		for _, path := range paths {
			p := pkgs[path]
			if _, ok := u.done[p]; p == nil || ok {
				continue
			}
			u.done[p] = len(u.e.objStarts)
			u.host[len(u.e.objStarts)] = true
			u.paths = append(u.paths, path)
			u.segs = append(u.segs, u.e.segment(path, func() { u.e.encPtrPackage(p) }))
			u.stdObjs = nil
		}
	}
}

// Decode reads the segment of the user package at path against current,
// the current check of each package it imports (by path): its references
// into them resolve by object path there. importers are the importers the
// decoded checkers hold, as NewUserExport's. It runs in the process that
// wrote the export, which reads std's objects as it holds them.
func (u *UserExport) Decode(path string, current func(path string) *check.Package, importers ...any) (pkg *check.Package, err error) {
	i := -1
	for k := u.first; k < len(u.segs); k++ {
		if u.paths[k] == path {
			i = k
		}
	}
	if i < 0 {
		return nil, fmt.Errorf("check: no user segment %s", path)
	}
	return u.DecodeSegment(i, current, importers...)
}

// DecodeSegment is Decode of the segment Add returned.
func (u *UserExport) DecodeSegment(i int, current func(path string) *check.Package, importers ...any) (pkg *check.Package, err error) {
	path := u.paths[i]
	if i < u.first || i >= len(u.segs) || u.segs[i].data == nil {
		return nil, fmt.Errorf("check: segment %d of %s is released", i, path)
	}
	if u.nodeCross[i] {
		return nil, fmt.Errorf("check: the syntax table of %s points into another user package's syntax", path)
	}
	defer recoverExport(&err)
	e := u.e
	if u.stdObjs == nil {
		starts := append(slicesClone(e.objStarts), e.next)
		u.stdObjs = &objTable{starts: starts, segs: make([][]any, len(starts)-1)}
		for k := range len(starts) - 1 {
			if u.fixed(k) {
				u.stdObjs.segs[k] = make([]any, starts[k+1]-starts[k])
			}
		}
		for x, id := range e.ids {
			if k := segmentOf(e.objStarts, id); k >= 0 && u.fixed(k) {
				u.stdObjs.segs[k][id-starts[k]] = x
			}
		}
	}
	// A fresh node table each time: the segment's own nodes are new.
	nodes := e.nodes.Table(append(slicesClone(e.nodeStarts), e.nodes.Next()), u.fixed)
	seg := u.segs[i]
	n, k := binary.Uvarint(seg.data)
	if k <= 0 || n > uint64(len(seg.data)-k) {
		return nil, fmt.Errorf("check: malformed segment %s", path)
	}
	if err := nodes.Decode(i, seg.data[k:k+int(n)]); err != nil {
		return nil, err
	}
	ext := externals()
	ext = append(ext, withGoBindings(importers)...)
	d := &decoder{buf: seg.data[k+int(n):], ext: ext, nodes: nodes, table: u.stdObjs, seg: i, start: seg.objStart, userNodes: true}
	d.imports = func(s int) *check.Package {
		if s < u.first || s >= i {
			return nil
		}
		return current(u.paths[s])
	}
	pkg = d.decPtrPackage()
	if d.pos != len(d.buf) {
		d.failf("%d bytes after the segment", len(d.buf)-d.pos)
	}
	d.afterDecode()
	return pkg, nil
}

// withGoBindings is importers and, after them, the Go bindings of each
// that is a GoImporter: the program's Go packages are the loader's state,
// read through its lookup function, so the export names the set as it
// names the loader, and a user segment carries a binding package's
// checked state with no function value (B7e).
func withGoBindings(importers []any) []any {
	out := append([]any(nil), importers...)
	for _, imp := range importers {
		if gi, ok := imp.(check.GoImporter); ok && gi.GoBindings() != nil {
			out = append(out, gi.GoBindings())
		}
	}
	return out
}

func slicesClone(s []uint64) []uint64 { return append([]uint64(nil), s...) }

func recoverExport(err *error) {
	if r := recover(); r != nil {
		if x, ok := r.(exportError); ok {
			*err = x
			return
		}
		panic(r)
	}
}
