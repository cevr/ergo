package lint

import (
	"slices"
	"strings"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/graphrows"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The graph rows of the layout rules (CI-SELECT §12.6 layer 1,
// GRAPH-LINT): each rule reads the rows graphrows writes for the
// program, the same rows std codegraph and `ergo graph` read, never a
// second walk of the checker's structures (§12.8 Risk 1).

// layoutRows is the run's graph rows with the indexes the layout rules
// read, built once per run (runShared.layoutRows).
type layoutRows struct {
	*programGraph
	// imports holds each package -> package import.
	imports map[[2]uint32]bool
	// reexport is a let's re-exported declaration, for the lets of the
	// linted module's own packages.
	reexport map[uint32]uint32
	// names is each program package's name, by path.
	names map[string]string
	// impls is the program's implementation packages (implementations).
	impls map[string]bool
	// reach lists, by file index, the other files of its package whose
	// declarations the file's own use (fileReaches); built on first use.
	reach      rowLists
	reachBuilt bool
}

// unitPath is u's import path: the loader's, else the checker's.
func unitPath(u Unit) string {
	if len(u.Path) > 0 {
		return u.Path
	}
	return u.Package.Path
}

// layoutRows is the run's layout rows, built on first use; units are the
// program's packages (Pass.Program).
func (s *runShared) layoutRows(units []Unit) *layoutRows {
	if s.layout == nil {
		s.layout = layoutRowsOf(s.programGraph(), units)
	}
	return s.layout
}

// layoutRowsOf indexes g for the layout rules; units are the program's
// packages.
func layoutRowsOf(g *programGraph, units []Unit) *layoutRows {
	names := map[string]string{}
	for _, u := range units {
		names[unitPath(u)] = u.Package.Name
	}
	lr := &layoutRows{programGraph: g, imports: map[[2]uint32]bool{}, reexport: map[uint32]uint32{}, names: names, impls: implementations(units)}
	for _, e := range g.t.Edges {
		switch e.Kind {
		case graphrows.Imports:
			lr.imports[[2]uint32{lr.pkg[e.From], e.To}] = true
		case graphrows.ReExports:
			if lr.subject[lr.pkg[e.From]] {
				lr.reexport[e.From] = e.To
			}
		}
	}
	return lr
}

// path is a package node's path.
func (lr *layoutRows) path(pkg uint32) string { return lr.t.Str(lr.t.Nodes[pkg].Name) }

// layerOf is the index of the first layer that names path: the path
// itself, or with a trailing "/..." every path under its prefix (std
// codegraph's layerOf); -1 when none does.
func layerOf(layers []string, path string) int {
	for i, l := range layers {
		if l == path || strings.HasSuffix(l, "/...") && strings.HasPrefix(path, strings.TrimSuffix(l, "...")) {
			return i
		}
	}
	return -1
}

// layerOptions is layer's ordering: package patterns from the top (the
// binaries) down.
type layerOptions struct {
	Layers []string `json:"layers"`
}

// fileReaches reports whether file node from reaches file node to by uses
// between files of one package.
func (lr *layoutRows) fileReaches(from, to uint32) bool {
	if !lr.reachBuilt {
		lr.reach = rowListsOf(lr.t, len(lr.files), func(i int) int {
			e := lr.t.Edges[i]
			if e.Kind != graphrows.Uses {
				return -1
			}
			a, b := lr.file[e.From], lr.file[e.To]
			if a == graphrows.NoNode || b == graphrows.NoNode || a == b || lr.pkg[a] != lr.pkg[b] || !lr.subject[lr.pkg[a]] {
				return -1
			}
			return int(lr.fileIndex[a])
		})
		lr.reachBuilt = true
	}
	seen := map[uint32]bool{from: true}
	work := []uint32{from}
	for len(work) > 0 {
		n := work[len(work)-1]
		work = work[:len(work)-1]
		if n == to {
			return true
		}
		for _, i := range lr.reach.of(int(lr.fileIndex[n])) {
			if m := lr.file[lr.t.Edges[i].To]; !seen[m] {
				seen[m] = true
				work = append(work, m)
			}
		}
	}
	return false
}

// testCode reports whether p's file is test code to the graph rules: a
// file of tests (testFile), or a file of the test build's constructors
// (TESTFILE-FUNCS): with no test or law of its own, it declares a type's
// row-free constructor New<P>, the one the law runner and the generators
// build P with (TypeName.Constructor), and its other declarations are
// functions that no declaration outside the file uses but a test or a
// law, such as a replay helper.
func (lr *layoutRows) testCode(p *Pass) bool {
	if testFile(p.File) {
		return true
	}
	ctors := map[*check.Func]bool{}
	for _, tn := range p.Package.Types {
		if c := tn.Constructor(); c != nil {
			ctors[c] = true
		}
	}
	f := lr.fileOf(p)
	// used is each top-level declaration of the file that code outside
	// it, not a test or a law, uses, by name.
	used := map[string]bool{}
	for _, i := range lr.into.of(int(lr.fileIndex[f])) {
		e := lr.t.Edges[i]
		if lr.file[e.From] == f {
			continue
		}
		if k := graphrows.DeclKind(lr.t.Nodes[lr.top(e.From)].Sub); k != graphrows.Test && k != graphrows.Law {
			used[lr.t.Str(lr.t.Nodes[lr.top(e.To)].Name)] = true
		}
	}
	ctor := false
	for _, d := range p.File.Decls {
		switch d := d.(type) {
		case *syntax.ImportDecl:
		case *syntax.FuncDecl:
			fn, _ := p.Info.Defs[d.Name].(*check.Func)
			switch {
			case fn != nil && ctors[fn]:
				ctor = true
			case used[d.Name.Name]:
				return false
			}
		default:
			return false
		}
	}
	return ctor
}

// top is the top-level declaration that holds node n (n itself when it
// is one).
func (lr *layoutRows) top(n uint32) uint32 {
	for q := lr.t.Nodes[n].Parent; q != graphrows.NoNode && lr.t.Nodes[q].Kind == graphrows.Decl; q = lr.t.Nodes[n].Parent {
		n = q
	}
	return n
}

// declaresOperation reports whether pkg declares an operation type: a
// domain package.
func declaresOperation(pkg *check.Package) bool {
	return slices.ContainsFunc(pkg.Types, check.OperationType)
}

// implementations is the program packages that declare a type
// implementing an operation type another program package declares (a
// std contract, such as a writer, is no domain's) and declare no
// operation type of their own: a domain package that also implements
// another's contract is still a domain package.
func implementations(units []Unit) map[string]bool {
	out := map[string]bool{}
	for _, u := range units {
		if declaresOperation(u.Package) {
			continue
		}
		for _, tn := range u.Package.Types {
			n, ok := tn.Type.(*check.Named)
			if !ok {
				continue
			}
			for _, op := range n.Implements() {
				if op.Obj != nil && op.Obj.Pkg != u.Package && check.ProgramType(op) {
					out[unitPath(u)] = true
				}
			}
		}
	}
	return out
}

// hiddenReexport is the use e, of a let that re-exports a declaration of
// a package the user's package does not import: that package, and the
// declaration; ok false otherwise. A use of a member through a value of
// another package's type is no hidden dependency here: Ergo needs no
// import to read a value's members, and refuses an import nothing names
// ("package p is imported and not used"), so it has no remedy.
func (lr *layoutRows) hiddenReexport(e graphrows.Edge) (pkg, decl uint32, ok bool) {
	t, ok := lr.reexport[e.To]
	if !ok {
		return 0, 0, false
	}
	a, d := lr.pkg[e.From], lr.pkg[t]
	if a == graphrows.NoNode || d == graphrows.NoNode || a == d || lr.imports[[2]uint32{a, d}] {
		return 0, 0, false
	}
	return d, t, true
}

// reexportFix is the fix of a hidden use at off in p's file: the
// selector p.F there replaced by q.G, G the declaration F re-exports, q
// the file's name for its package, which the fix imports when the file
// does not; the import of F's package dropped when nothing else names it.
// nil when the use is no selector of an imported package or q's name is
// taken.
func reexportFix(p *Pass, lr *layoutRows, off syntax.Pos, pkg, decl uint32) *report.Fix {
	var sel *syntax.SelectorExpr
	for _, s := range p.fileIndex().selectors {
		if s.Sel.NamePos == off {
			sel = s
			break
		}
	}
	if sel == nil {
		return nil
	}
	x, ok := sel.X.(*syntax.Ident)
	if !ok {
		return nil
	}
	via, ok := p.Info.Uses[x].(*check.PkgName)
	if !ok || via.Pkg == nil {
		return nil
	}
	path := lr.path(pkg)
	qual, imports := "", []string(nil)
	for _, im := range p.Package.ImportsOf(p.File) {
		if im.Pkg != nil && im.Pkg.Path == path {
			qual = im.Name
		}
	}
	if qual == "" {
		qual = lr.names[path]
		if qual == "" {
			qual = path[strings.LastIndexByte(path, '/')+1:]
		}
		if p.Package.FileBinds(p.File, qual) {
			return nil
		}
		imports = []string{path}
	}
	name := lr.t.Str(lr.t.Nodes[decl].Name)
	return &report.Fix{InsertAt: int(sel.Pos()), Replace: int(sel.End() - sel.Pos()), Text: qual + "." + name, Imports: imports, Unused: []string{via.Pkg.Path}}
}
