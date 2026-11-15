package check

import (
	"fmt"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Law placement (types.md, Laws 15; grammar.md rule 11; owner, 2026-11-15:
// "laws being in their own file is kind of an anti-pattern. Because
// they're really tied to implementation"). A top-level law states a rule
// about declarations of its own package, so it sits beside one of them:
//
//  1. A law's subjects are the declarations of its own package that its
//     parameters or its body name: functions, types and their operations,
//     variants, fields and methods, package lets and contexts. A member or
//     machine law's subject is its type, which declares it, so the rule
//     holds for it by construction and only a top-level law is judged. A
//     law the parser derived (a refinement's or a codec pair's) is stated
//     at its declaration and is not judged either.
//  2. A law helper is not a subject: a function that is not an operation
//     of a type (no receiver, and no x.f(…) reaches it), a package let or
//     a context, other than main, every one of whose users
//     is a law, a test or another helper. The subjects a helper names are
//     the law's subjects, at any depth, after the ones the law names itself.
//     So a helper never pins a law to a file of helpers (a laws file).
//  3. A top-level law sits in a file that declares one of its subjects;
//     else S115 at its name names each subject's file. A law with no
//     subject in its package (it names only the prelude, std or imports)
//     is S115 too: it belongs to the package it is about.
//
// Law.Subjects and Law.Helpers carry the verdict to ergo fix (lawfix),
// which moves a misplaced law into its first subject's file, and each
// helper only laws use beside the first law that uses it.
//
// Prior art: Go keeps a package's examples and tests in _test files of the
// package, Rust keeps unit tests in a `mod tests` of the file they test,
// and Zig keeps a `test` block beside the function it tests; none checks
// the placement. Ergo checks it, since a law is part of its subject's
// contract (types.md, Laws 12), not a test of it.

// LawSubject is one subject of a top-level law (Law.Subjects): the name the
// law (or one of its helpers) uses, and the declaration and file of it.
type LawSubject struct {
	Name string
	Decl syntax.Decl
	File *syntax.File
}

// File is the source file that declares the law.
func (l *Law) File() *syntax.File {
	if l.file == nil {
		return nil
	}
	return l.file.syntax
}

// lawPlacementReported turns S115 on once the repository's laws sit with
// their subjects (LAWLOC: the rule and ergo fix land first, the move next).
const lawPlacementReported = false

// Spec is the import that binds the name, or nil for one the checker made.
func (p *PkgName) Spec() *syntax.ImportSpec { return p.spec }

// lawDecls is the package's declaration graph at the level of top-level
// declarations: who declares each object, which declarations each one
// names (refs, in source order) and which name it (users).
type lawDecls struct {
	decl map[Object]syntax.Decl
	top  map[syntax.Node]syntax.Decl
	// dotted holds each function x.f(…) reaches (196m, G-ex-e-2): an
	// operation of its type, never a helper.
	dotted map[syntax.Decl]bool
	file   map[syntax.Decl]*fileInfo
	refs   map[syntax.Decl][]lawRef
	users  map[syntax.Decl][]syntax.Decl
	order  []syntax.Decl
}

type lawRef struct {
	decl syntax.Decl
	name string
}

func (c *checker) lawDeclGraph() *lawDecls {
	g := &lawDecls{decl: map[Object]syntax.Decl{}, top: map[syntax.Node]syntax.Decl{}, dotted: map[syntax.Decl]bool{}, file: map[syntax.Decl]*fileInfo{}, refs: map[syntax.Decl][]lawRef{}, users: map[syntax.Decl][]syntax.Decl{}}
	for _, fi := range c.files {
		for _, d := range fi.syntax.Decls {
			if _, ok := d.(*syntax.ImportDecl); ok {
				continue
			}
			g.file[d] = fi
			g.top[d] = d
			g.order = append(g.order, d)
			switch d.(type) {
			case *syntax.LawDecl, *syntax.TestDecl:
				// A law or a test declares nothing another declaration names.
				continue
			}
			syntax.Inspect(d, func(n syntax.Node) bool {
				if id, ok := n.(*syntax.Ident); ok {
					if o := c.pkg.Info.Defs[id]; o != nil {
						if _, seen := g.decl[o]; !seen {
							g.decl[o] = d
						}
					}
				}
				return true
			})
		}
	}
	for _, f := range c.pkg.Funcs {
		if f.Decl != nil && (f.owner != nil || f.opOwner != nil) {
			g.dotted[f.Decl] = true
		}
	}
	for _, d := range g.order {
		seen := map[syntax.Decl]bool{d: true}
		syntax.Inspect(d, func(n syntax.Node) bool {
			// A name resolves through Uses; a method or operation called
			// through a dot has no Uses entry, so its call names it.
			var o Object
			switch n := n.(type) {
			case *syntax.Ident:
				o = c.pkg.Info.Uses[n]
			case *syntax.CallExpr:
				if call := c.pkg.Info.Calls[n]; call != nil {
					switch {
					case call.Func != nil:
						o = call.Func
					case call.Op != nil:
						o = call.Op
					}
				}
			}
			if o == nil {
				return true
			}
			to, ok := g.declOf(o)
			if !ok || seen[to] {
				return true
			}
			seen[to] = true
			g.refs[d] = append(g.refs[d], lawRef{to, o.name()})
			g.users[to] = append(g.users[to], d)
			return true
		})
	}
	return g
}

// declOf is the top-level declaration of o: its declaration node's for a
// function or a type (an instance of a generic one shares its origin's
// node), else the one whose identifier defines o.
func (g *lawDecls) declOf(o Object) (syntax.Decl, bool) {
	var node syntax.Node
	switch o := o.(type) {
	case *Func:
		if o.Decl != nil {
			node = o.Decl
		}
	case *TypeName:
		if o.Decl != nil {
			node = o.Decl
		}
	}
	if node != nil {
		if d, ok := g.top[node]; ok {
			return d, true
		}
	}
	d, ok := g.decl[o]
	return d, ok
}

// helpers is the set of law helpers (rule 2): the greatest set of helper
// candidates each of whose users is a law, a test or a member of the set.
// An exported declaration is part of the package's surface, which other
// packages may name, so it is a candidate only when exported is set (ergo
// fix asks which ones it may unexport).
func (g *lawDecls) helpers(exported bool) map[syntax.Decl]bool {
	h := map[syntax.Decl]bool{}
	for _, d := range g.order {
		if len(g.users[d]) > 0 && helperKind(d, exported) && !g.dotted[d] {
			h[d] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, d := range g.order {
			if !h[d] {
				continue
			}
			for _, u := range g.users[d] {
				switch u.(type) {
				case *syntax.LawDecl, *syntax.TestDecl:
					continue
				}
				if !h[u] {
					delete(h, d)
					changed = true
					break
				}
			}
		}
	}
	return h
}

func helperKind(d syntax.Decl, exported bool) bool {
	switch d := d.(type) {
	case *syntax.FuncDecl:
		return d.Recv == nil && d.Derived == nil && d.Name.Name != "main" && (exported || !isExported(d.Name.Name))
	case *syntax.VarDecl:
		for _, s := range d.Specs {
			for _, n := range s.Names {
				if !exported && isExported(n.Name) {
					return false
				}
			}
		}
		return true
	case *syntax.ContextDecl:
		return exported || !isExported(d.Name.Name)
	}
	return false
}

// checkLawPlacement applies rule 3 to each top-level law, and records its
// subjects and the helpers that move with it.
func (c *checker) checkLawPlacement() {
	var top []*Law
	for _, law := range c.pkg.Laws {
		if law.Owner == nil && law.Machine == nil && law.Decl.Derived == nil && law.Decl.Pair == nil {
			top = append(top, law)
		}
	}
	if len(top) == 0 {
		return
	}
	g := c.lawDeclGraph()
	helper := g.helpers(false)
	for d := range g.helpers(true) {
		if f, ok := d.(*syntax.FuncDecl); ok && !helper[d] {
			c.pkg.LawExportedHelpers = append(c.pkg.LawExportedHelpers, f)
		}
	}
	slices.SortFunc(c.pkg.LawExportedHelpers, func(a, b *syntax.FuncDecl) int { return strings.Compare(a.Name.Name, b.Name.Name) })
	// Each helper's roots: the laws and tests that reach it through
	// helpers, in package order. A helper moves with its first root when
	// every root is a law.
	roots := map[syntax.Decl][]syntax.Decl{}
	for _, d := range g.order {
		switch d.(type) {
		case *syntax.LawDecl, *syntax.TestDecl:
		default:
			continue
		}
		seen := map[syntax.Decl]bool{}
		var visit func(x syntax.Decl)
		visit = func(x syntax.Decl) {
			for _, r := range g.refs[x] {
				if !helper[r.decl] || seen[r.decl] {
					continue
				}
				seen[r.decl] = true
				roots[r.decl] = append(roots[r.decl], d)
				visit(r.decl)
			}
		}
		visit(d)
	}
	for _, law := range top {
		law.Subjects = g.subjects(law.Decl, helper)
		seen := map[syntax.Decl]bool{}
		var visit func(x syntax.Decl)
		visit = func(x syntax.Decl) {
			for _, r := range g.refs[x] {
				if !helper[r.decl] || seen[r.decl] {
					continue
				}
				seen[r.decl] = true
				if rs := roots[r.decl]; len(rs) > 0 && rs[0] == syntax.Decl(law.Decl) && onlyLaws(rs) {
					law.Helpers = append(law.Helpers, r.decl)
				}
				visit(r.decl)
			}
		}
		visit(law.Decl)
		if lawPlacementReported {
			c.reportLawPlacement(law)
		}
	}
}

func onlyLaws(ds []syntax.Decl) bool {
	for _, d := range ds {
		if _, ok := d.(*syntax.LawDecl); !ok {
			return false
		}
	}
	return true
}

// subjects lists d's subjects (rule 1, 2): the declarations d names that
// are not helpers, in source order, then those its helpers name, breadth
// first.
func (g *lawDecls) subjects(d syntax.Decl, helper map[syntax.Decl]bool) []LawSubject {
	var out []LawSubject
	seen := map[syntax.Decl]bool{d: true}
	queue := []syntax.Decl{d}
	for len(queue) > 0 {
		x := queue[0]
		queue = queue[1:]
		for _, r := range g.refs[x] {
			if seen[r.decl] {
				continue
			}
			seen[r.decl] = true
			if helper[r.decl] {
				queue = append(queue, r.decl)
				continue
			}
			out = append(out, LawSubject{Name: r.name, Decl: r.decl, File: g.file[r.decl].syntax})
		}
	}
	return out
}

func (c *checker) reportLawPlacement(law *Law) {
	saved := c.cur
	c.cur = law.file
	defer func() { c.cur = saved }()
	if len(law.Subjects) == 0 {
		c.errorf(law.Decl.Name, CodeLawPlace, "law %s names nothing in package %s; state it in the package it is about", law.Name, c.pkg.Name)
		return
	}
	var named []string
	files := map[*syntax.File]bool{}
	for _, s := range law.Subjects {
		if s.File == law.File() {
			return
		}
		if !files[s.File] {
			files[s.File] = true
			named = append(named, fmt.Sprintf("%s (%s)", s.Name, c.lawFileName(s.File)))
		}
	}
	where := "that file"
	if len(named) > 1 {
		where = "one of those files"
	}
	list := strings.Join(named, ", ")
	if i := strings.LastIndex(list, ", "); i >= 0 {
		list = list[:i] + " and " + list[i+2:]
	}
	c.errorf(law.Decl.Name, CodeLawPlace, "law %s is about %s; state it in %s", law.Name, list, where)
}

func (c *checker) lawFileName(f *syntax.File) string {
	if f.Name != "" {
		return f.Name
	}
	for _, fi := range c.files {
		if fi.syntax == f {
			return fmt.Sprintf("file %d", fi.index+1)
		}
	}
	return "another file"
}
