package check

import "ergo.local/prototype/internal/sketch/std"

// CrossRef is one reference that a user package's segment makes to an
// object that an earlier user package's segment wrote: the reference
// CS5b-2 makes symbolic (compile-speed/cs5b.md §2, §3).
type CrossRef struct {
	// From and To are the paths of the referring and the referred package.
	From, To string
	// Obj is the object referred to; nil for a syntax node.
	Obj any
}

// UserExport writes std as this process holds it, then each user package
// as it is checked, as segments of one export, in the order CS5b-3 will:
// a package's segment right after its own check, before any importer's.
// It keeps no bytes; it records the references each user segment makes
// into earlier user segments (Refs).
type UserExport struct {
	e     *encoder
	first int // the index of the first user segment
	paths []string
	Refs  []CrossRef
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
	s, err := stdSnapshotNow()
	if err != nil {
		return nil, err
	}
	e := newEncoder()
	for i, imp := range importers {
		e.ext[imp] = uint64(len(externals()) + i)
	}
	u = &UserExport{e: e, first: len(s.packages) + 1}
	e.segment("", func() { e.encPtrstdBase(s.base) })
	u.paths = append(u.paths, "")
	for i, pkg := range s.packages {
		e.segment(s.paths[i], func() { e.encPtrPackage(pkg) })
		u.paths = append(u.paths, s.paths[i])
	}
	e.crossRef = func(seg int, v any) { u.cross(seg, v) }
	below := e.nodes.Below
	e.nodes.Below = func(id uint64) {
		below(id)
		u.cross(segmentOf(e.nodeStarts, id), nil)
	}
	return u, nil
}

func (u *UserExport) cross(seg int, v any) {
	cur := len(u.e.objStarts) - 1
	if seg >= u.first && cur >= u.first && seg != cur {
		u.Refs = append(u.Refs, CrossRef{From: u.paths[cur], To: u.paths[seg], Obj: v})
	}
}

// Add writes pkg's segment. A package the codec cannot carry (a function
// value in its state) is an error; the export is then unusable.
func (u *UserExport) Add(pkg *Package) (err error) {
	defer recoverExport(&err)
	u.paths = append(u.paths, pkg.Path)
	// A derive record is not carried (exportgen skipFields); the measure
	// does not need it.
	u.e.segment(pkg.Path, func() { u.e.encPtrPackage(pkg) })
	return nil
}

func recoverExport(err *error) {
	if r := recover(); r != nil {
		if x, ok := r.(exportError); ok {
			*err = x
			return
		}
		panic(r)
	}
}
