package lower

import (
	"fmt"
	"slices"
	"sort"
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Sources (196b). A program is the root package's files and the files of
// every user package it imports (Unit.Imports), lowered into one Go
// package. Every source file is one srcFile, the root's first, then each
// import's, in canonical order (canon.go, sources); Refusal.Source counts
// the unit's own order. A user
// package is sourced: its declarations go into the user part of main.go,
// each after the //line directive of its own file, and its calls and
// declarations into the call and declaration tables. A std package has no
// source: its declarations go into the generated part, with no directive.
//
// A syntax.Pos is a byte offset into one file, and the checker's facts are
// keyed by the nodes of one package, so the lowering keeps a current file
// and a current Info (lowerState). Each declaration is lowered within its
// own file (within, enter); a refusal is located in the file of its node.
//
// Initialization (decision Q5, Go's rule since Go 1.21): the imported
// packages' lets run first, each package after the packages it imports,
// else in path order, and each package's lets in its Info.InitOrder; then
// the root package's lets; then the machines' initial values.
//
// Prior art: Go's cmd/compile reads a package of several files into one
// position base per file and orders package initialization by import
// dependency, then by path (Go 1.21). Gleam and ReScript emit one host
// module per source module; Elixir one BEAM module per defmodule. Ergo
// emits one Go package (decision Q5), so the names of all packages share
// its scope, and the naming pass gives each its own (naming.go).

// srcFile is one source file of the program and the package it belongs to.
// unit is its index in the unit's order (Refusal.Source).
type srcFile struct {
	file *syntax.File
	path string
	text string
	pkg  *check.Package
	unit int
}

// lowerState is the lowering's current source file and the Info whose facts
// it reads.
type lowerState struct {
	cur  int
	info *check.Info
}

func (l *lowerer) state() lowerState { return lowerState{l.cur, l.info} }

func (l *lowerer) setCur(i int) {
	l.cur, l.file, l.src = i, l.srcs[i].file, l.srcs[i].path
}

// within makes source i current, and its package's Info. It returns the
// state to restore: defer l.leave(l.within(i)).
func (l *lowerer) within(i int) lowerState {
	s := l.state()
	l.setCur(i)
	l.info = l.srcs[i].pkg.Info
	return s
}

// enter makes the file of the node n current, and its package's Info, when
// n belongs to a sourced package: p's when p is given, any when it is nil.
// A std package's node changes nothing. It returns the state to restore:
// defer l.leave(l.enter(n, p)).
func (l *lowerer) enter(n syntax.Node, p *check.Package) lowerState {
	s := l.state()
	if p != nil && !l.isSourced(p) {
		return s
	}
	if i, ok := l.sourceOf(n); ok && (p == nil || l.srcs[i].pkg == p) {
		l.setCur(i)
		l.info = l.srcs[i].pkg.Info
	} else if p != nil {
		l.info = p.Info
	}
	return s
}

// leave restores the state s. A refusal that passes through it is located
// in the file that was current where it was raised, unless its node
// locates it (guard).
func (l *lowerer) leave(s lowerState) {
	if r := recover(); r != nil {
		if ref, ok := r.(refusal); ok && !ref.located {
			ref.src, ref.located = l.cur, true
			r = ref
		}
		l.cur, l.file, l.src, l.info = s.cur, l.srcs[s.cur].file, l.srcs[s.cur].path, s.info
		panic(r)
	}
	l.cur, l.file, l.src, l.info = s.cur, l.srcs[s.cur].file, l.srcs[s.cur].path, s.info
}

// queue adds f to the pending declarations; it runs within the file of n.
func (l *lowerer) queue(n syntax.Node, f func()) {
	l.pending = append(l.pending, func() {
		defer l.leave(l.enter(n, nil))
		f()
	})
}

// sourceOf is the index of the source file that holds n (canon.go).
func (l *lowerer) sourceOf(n syntax.Node) (int, bool) { return l.canon.sourceOf(n) }

// sourceLess orders two nodes by source file, then by offset (canon.go).
func (l *lowerer) sourceLess(a, b syntax.Node) bool { return l.canon.less(a, b) }

// nodeID is the identity of the node n as a number, given in the order the
// lowering first asks for it, so a key built of it is deterministic. A key
// of node positions is not an identity: offsets count from the start of
// each file, so two files' nodes can share one.
func (l *lowerer) nodeID(n syntax.Node) int {
	if id, ok := l.nodeIDs[n]; ok {
		return id
	}
	if l.nodeIDs == nil {
		l.nodeIDs = map[syntax.Node]int{}
	}
	id := len(l.nodeIDs)
	l.nodeIDs[n] = id
	return id
}

// at is the location of pos in the source file that holds the node n: the
// file's index and path, and pos's line and column there. Every rendered
// location goes through it, so a location is never read against the
// current file of an unrelated declaration (196b); a node that no source
// file holds is a compiler defect.
func (l *lowerer) at(n syntax.Node, pos syntax.Pos) (src int, path string, line, column int) {
	i, ok := l.sourceOf(n)
	if !ok {
		ice(n, "a location of a node that no source file holds")
	}
	line, column = l.srcs[i].file.Position(pos)
	return i, l.srcs[i].path, line, column
}

// stdFile is one embedded file of a std package: the file, and the path a
// location names, the file's place in the repository's std tree
// (std/flag/parse.ergo; the prelude's std/prelude.ergo).
type stdFile struct {
	file *syntax.File
	path string
}

// locate is the location of pos in the file that holds the node n, a
// source of the program (at) or an embedded file of a std package the
// program reaches: its path, and pos's line and column there. A std body
// is lowered with no directive and has no entry in the call tables, so
// only a location a std body writes as text (a match's no-case defect, a
// call's site) goes through it. A node that no file holds, of the program
// or of std, is a compiler defect (G1, batch 11).
func (l *lowerer) locate(n syntax.Node, pos syntax.Pos) (path string, line, column int) {
	if _, ok := l.sourceOf(n); ok {
		_, path, line, column = l.at(n, pos)
		return path, line, column
	}
	if l.stdSrc == nil {
		l.stdSrc = map[syntax.Node]stdFile{}
		for _, p := range l.libraries() {
			if !p.Std() {
				continue
			}
			for _, f := range p.Files() {
				sf := stdFile{file: f, path: "std/" + p.Path + "/" + f.Name}
				if p.Path == std.PreludePath {
					sf.path = "std/" + f.Name
				}
				syntax.Inspect(f, func(m syntax.Node) bool {
					if m != nil {
						l.stdSrc[m] = sf
					}
					return true
				})
			}
		}
	}
	sf, ok := l.stdSrc[n]
	if !ok || n == nil {
		ice(n, "a location of a node that no source file holds")
	}
	line, column = sf.file.Position(pos)
	return sf.path, line, column
}

// eachDecl runs f on each top-level declaration of p's files, in file
// order, each guarded and within its file.
func (l *lowerer) eachDecl(p *check.Package, f func(syntax.Decl)) {
	for i, s := range l.srcs {
		if s.pkg != p {
			continue
		}
		for _, d := range s.file.Decls {
			l.guard(func() {
				defer l.leave(l.within(i))
				f(d)
			})
		}
	}
}

// libraries are the packages other than the root that the program reaches
// (libraryPackages), computed once.
func (l *lowerer) libraries() []*check.Package {
	if !l.libsDone {
		l.libs, l.libsDone = l.libraryPackages(), true
	}
	return l.libs
}

// missingImport is the error of a program that reaches a user package that
// the unit holds no unit of: its lets would not run.
func (l *lowerer) missingImport() error {
	for _, p := range l.libraries() {
		if !p.Std() && !l.isSourced(p) {
			return fmt.Errorf("lower: the program reaches package %q, and the unit lists no unit of it (Unit.Imports)", p.Path)
		}
	}
	return nil
}

// sourcedLibs are the reached user packages other than the root, in
// initialization order: each after every sourced package it imports, else
// in path order (Go 1.21).
func (l *lowerer) sourcedLibs() []*check.Package {
	if l.initPkgs != nil {
		return l.initPkgs
	}
	var todo []*check.Package
	for _, p := range l.libraries() {
		if l.isSourced(p) {
			todo = append(todo, p)
		}
	}
	l.initPkgs = importOrder(todo)
	return l.initPkgs
}

// stdLetPkgs are the reached std packages that declare a top-level let,
// in initialization order (importOrder). A std package imports no user
// package, so each initializes before every sourced package (wf10 D3).
func (l *lowerer) stdLetPkgs() []*check.Package {
	if l.stdInits != nil {
		return *l.stdInits
	}
	var todo []*check.Package
	for _, p := range l.libraries() {
		if !p.Std() || l.isSourced(p) {
			continue
		}
		lets := false
		for _, f := range p.Files() {
			for _, d := range f.Decls {
				if _, ok := d.(*syntax.VarDecl); ok {
					lets = true
				}
			}
		}
		if lets {
			todo = append(todo, p)
		}
	}
	out := importOrder(todo)
	l.stdInits = &out
	return out
}

// stdLet is a top-level let declaration of a std package.
type stdLet struct {
	pkg  *check.Package
	decl *syntax.VarDecl
}

// eachStdLet runs f on each top-level let of the std packages with lets,
// in initialization order, each in its package (inStdPkg): the naming
// pass names them all (wf10 defects D3).
func (l *lowerer) eachStdLet(f func(stdLet)) {
	for _, p := range l.stdLetPkgs() {
		for _, file := range p.Files() {
			for _, d := range file.Decls {
				if d, ok := d.(*syntax.VarDecl); ok {
					l.inStdPkg(p, func() { f(stdLet{p, d}) })
				}
			}
		}
	}
}

// inStdPkg runs f guarded, with p's Info and stdLets set.
func (l *lowerer) inStdPkg(p *check.Package, f func()) {
	l.guard(func() {
		saved, savedStd := l.info, l.stdLets
		l.info, l.stdLets = p.Info, true
		defer func() { l.info, l.stdLets = saved, savedStd }()
		f()
	})
}

// nameStdLet names the variables of the std let s and records s as theirs.
func (l *lowerer) nameStdLet(s stdLet) {
	l.declareName(s.decl)
	if l.stdLetOf == nil {
		l.stdLetOf = map[*check.Var]stdLet{}
	}
	for _, spec := range s.decl.Specs {
		for _, id := range spec.Names {
			if v, ok := l.info.Defs[id].(*check.Var); ok {
				l.stdLetOf[v] = s
			}
		}
		if spec.Pattern != nil {
			for _, v := range patternVars(l.info.Patterns[spec.Pattern], nil) {
				l.stdLetOf[v] = s
			}
		}
	}
}

// reachStdLet notes that a lowered body reads v. A std let is lowered as
// a std function is, where a body of the build reaches it, and not with
// its package (wf10 defects D6): so a let that only std's laws read, such
// as flag's lawText, is neither declared nor initialized in a program
// whose build runs no such law. The first read of each std let queues its
// declaration; initOrder keeps Go's order whatever the order of reads.
func (l *lowerer) reachStdLet(v *check.Var) {
	s, ok := l.stdLetOf[v]
	if !ok || l.stdLetQueued[s.decl] {
		return
	}
	if l.stdLetQueued == nil {
		l.stdLetQueued = map[*syntax.VarDecl]bool{}
	}
	l.stdLetQueued[s.decl] = true
	l.pending = append(l.pending, func() {
		l.inStdPkg(s.pkg, func() {
			l.stdLetInits++
			l.packageLet(s.decl)
		})
	})
}

// importOrder orders todo for initialization: each package after every
// package of todo it imports, directly or through other packages, else in
// path order (Go 1.21).
func importOrder(todo []*check.Package) []*check.Package {
	todo = slices.Clone(todo)
	sort.Slice(todo, func(i, j int) bool { return todo[i].Path < todo[j].Path })
	deps := map[*check.Package][]*check.Package{}
	for _, p := range todo {
		seen := map[*check.Package]bool{p: true}
		var walk func(q *check.Package)
		walk = func(q *check.Package) {
			if q.Info == nil {
				return // a Go package of import go
			}
			for _, o := range q.Info.Uses {
				pn, ok := o.(*check.PkgName)
				if !ok || seen[pn.Pkg] {
					continue
				}
				seen[pn.Pkg] = true
				if slices.Contains(todo, pn.Pkg) {
					deps[p] = append(deps[p], pn.Pkg)
				}
				walk(pn.Pkg)
			}
		}
		walk(p)
	}
	done := map[*check.Package]bool{}
	out := []*check.Package{}
	for len(out) < len(todo) {
		progress := false
		for _, p := range todo {
			if done[p] {
				continue
			}
			ready := true
			for _, d := range deps[p] {
				if !done[d] && slices.Contains(todo, d) {
					ready = false
				}
			}
			if ready {
				done[p] = true
				out = append(out, p)
				progress = true
				break
			}
		}
		if !progress {
			break // an import cycle, which the checker refuses
		}
	}
	return out
}

// sourcedPkgs are the root package, then the sourced libraries.
func (l *lowerer) sourcedPkgs() []*check.Package {
	return append([]*check.Package{l.pkg}, l.sourcedLibs()...)
}

// isSourced reports a package whose declarations carry //line directives.
func (l *lowerer) isSourced(p *check.Package) bool { return l.canon.sourced[p] }

// paramFile qualifies the key of the type parameter tp with its source
// file: positions are offsets into one file, so two files' parameters of
// one name can share one. A parameter of the root's first file, and of a
// std package, has no qualifier, so a program of one file keeps its keys.
func (l *lowerer) paramFile(tp *check.TypeParam) string {
	if len(l.srcs) == 1 {
		return ""
	}
	if l.tpSrc == nil {
		l.tpSrc = map[*check.TypeParam]int{}
		for _, p := range l.sourcedPkgs() {
			for _, tn := range l.canon.types(p) {
				named, ok := tn.Type.(*check.Named)
				if !ok || tn.Decl == nil {
					continue
				}
				if i, ok := l.sourceOf(tn.Decl.Name); ok {
					for _, tp := range named.TParams {
						l.tpSrc[tp] = i
					}
				}
			}
			for _, f := range l.canon.funcs(p) {
				if f.Decl == nil {
					continue
				}
				if i, ok := l.sourceOf(f.Decl.Name); ok {
					for _, tp := range f.Sig.TParams {
						l.tpSrc[tp] = i
					}
				}
			}
		}
	}
	if i := l.tpSrc[tp]; i > 0 {
		return "#" + strconv.Itoa(i)
	}
	return ""
}
