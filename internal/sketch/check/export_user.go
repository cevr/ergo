package check

import (
	"encoding/binary"
	"fmt"

	"ergo.local/prototype/internal/sketch/std"
)

// CrossRef is one reference that a user package's segment makes to an
// object that an earlier user package's segment wrote: the reference
// CS5b-2 makes symbolic (compile-speed/cs5b.md §2, §3).
type CrossRef struct {
	// From and To are the paths of the referring and the referred package.
	From, To string
	// Obj is the object referred to; nil for a syntax node.
	Obj any
	// Path is Obj's object path in To (ResolvePath), taken right after
	// To's check; HasPath is false when no path reaches it.
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
	// nodeCross marks the user segments that name a syntax node of an
	// earlier user segment, which no path names yet (CS5b-4).
	nodeCross map[int]bool
	// stdObjs and stdNodes are std's objects and syntax nodes by id, for
	// Decode, built on first use.
	stdObjs *objTable
	// tables are the object paths of each user segment's package, by
	// segment index, taken when its segment is written.
	tables map[int]map[any]objPath
	Refs   []CrossRef
}

// NewUserExport writes std's segments. importers are the values the user
// packages' checkers hold as their importer (a loader): the export names
// them as externals, as it names Std.
func NewUserExport(importers ...any) (u *UserExport, err error) {
	defer recoverExport(&err)
	prelude()
	// Every std package is in std's segments, so a user segment names std
	// objects there and never writes one (the std snapshot holds them all).
	for _, path := range std.Packages() {
		if _, err := Std.Import(path); err != nil {
			return nil, err
		}
	}
	s, err := stdSnapshotNow(nil, nil)
	if err != nil {
		return nil, err
	}
	e := newEncoder()
	for i, imp := range importers {
		e.ext[imp] = uint64(len(externals()) + i)
	}
	u = &UserExport{e: e, first: len(s.packages) + 1, tables: map[int]map[any]objPath{}, nodeCross: map[int]bool{}}
	u.segs = append(u.segs, e.segment("", func() { e.encPtrstdBase(s.base) }))
	u.paths = append(u.paths, "")
	for i, pkg := range s.packages {
		u.segs = append(u.segs, e.segment(s.paths[i], func() { e.encPtrPackage(pkg) }))
		u.paths = append(u.paths, s.paths[i])
	}
	e.crossRef = func(seg int, v any) { u.cross(seg, v) }
	e.symbolic = func(seg int, v any) (objPath, bool) {
		cur := len(e.objStarts) - 1
		if seg < u.first || cur < u.first || seg == cur {
			return "", false
		}
		p, ok := u.tables[seg][v]
		if !ok {
			e.failf("%T of %s has no object path", v, u.paths[seg])
		}
		return p, true
	}
	below := e.nodes.Below
	e.nodes.Below = func(id uint64) {
		below(id)
		seg, cur := segmentOf(e.nodeStarts, id), len(e.nodeStarts)-1
		if seg >= u.first && cur >= u.first && seg != cur {
			u.nodeCross[cur] = true
		}
		u.cross(seg, nil)
	}
	return u, nil
}

func (u *UserExport) cross(seg int, v any) {
	cur := len(u.e.objStarts) - 1
	if seg >= u.first && cur >= u.first && seg != cur {
		r := CrossRef{From: u.paths[cur], To: u.paths[seg], Obj: v}
		if v != nil {
			var p objPath
			p, r.HasPath = u.tables[seg][v]
			r.Path = string(p)
		}
		u.Refs = append(u.Refs, r)
	}
}

// Add writes pkg's segment. A package the codec cannot carry (a function
// value in its state) is an error; the export is then unusable.
func (u *UserExport) Add(pkg *Package) (err error) {
	defer recoverExport(&err)
	u.paths = append(u.paths, pkg.Path)
	// A derive record is not carried (exportgen skipFields); the measure
	// does not need it.
	u.tables[len(u.e.objStarts)] = objectPaths(pkg)
	u.segs = append(u.segs, u.e.segment(pkg.Path, func() { u.e.encPtrPackage(pkg) }))
	return nil
}

// Decode reads the segment of the user package at path against current,
// the current check of each package it imports (by path): its references
// into them resolve by object path there. importers are the importers the
// decoded checkers hold, as NewUserExport's. It runs in the process that
// wrote the export, which reads std's objects as it holds them.
func (u *UserExport) Decode(path string, current func(path string) *Package, importers ...any) (pkg *Package, err error) {
	i := -1
	for k := u.first; k < len(u.segs); k++ {
		if u.paths[k] == path {
			i = k
		}
	}
	if i < 0 {
		return nil, fmt.Errorf("check: no user segment %s", path)
	}
	if u.nodeCross[i] {
		return nil, fmt.Errorf("check: the segment of %s names a syntax node of another user package (CS5b-4)", path)
	}
	defer recoverExport(&err)
	e := u.e
	if u.stdObjs == nil {
		starts := append(slicesClone(e.objStarts), e.next)
		u.stdObjs = &objTable{starts: starts, segs: make([][]any, len(starts)-1)}
		for k := range u.first {
			u.stdObjs.segs[k] = make([]any, starts[k+1]-starts[k])
		}
		for x, id := range e.ids {
			if k := segmentOf(e.objStarts, id); k < u.first {
				u.stdObjs.segs[k][id-starts[k]] = x
			}
		}
	}
	// A fresh node table each time: the segment's own nodes are new.
	nodes := e.nodes.Table(append(slicesClone(e.nodeStarts), e.nodes.Next()), u.first)
	seg := u.segs[i]
	n, k := binary.Uvarint(seg.data)
	if k <= 0 || n > uint64(len(seg.data)-k) {
		return nil, fmt.Errorf("check: malformed segment %s", path)
	}
	if err := nodes.Decode(i, seg.data[k:k+int(n)]); err != nil {
		return nil, err
	}
	ext := externals()
	ext = append(ext, importers...)
	d := &decoder{buf: seg.data[k+int(n):], ext: ext, nodes: nodes, table: u.stdObjs, seg: i, start: seg.objStart}
	d.imports = func(s int) *Package {
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
