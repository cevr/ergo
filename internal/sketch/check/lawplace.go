package check

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Law and test placement (types.md, Laws 15; grammar.md rule 11; owner,
// 2026-11-15, 2026-11-26): a top-level law or test states a rule or check
// about declarations of its own package, so it sits beside one of them:
//
//  1. A law's or test's subjects are the declarations of its own package that
//     its parameters or its body name: functions, types and their
//     operations, variants, fields and methods, package lets and contexts. A
//     member or machine law's subject is its type, which declares it, so the
//     rule holds for it by construction and only a top-level law or test is
//     judged. A law the parser derived (a refinement's or a codec pair's) is
//     stated at its declaration and is not judged either.
//  2. A law helper is not a subject: an unexported declaration every one
//     of whose users is a law, a test or another helper. It is a function
//     other than main with no receiver, a package let or a context, or a
//     type with no extern, origin or derive, with the functions dot-called
//     on it; an exported function dot-called on a package type is surface,
//     never a helper. The subjects a helper names are the law's or test's
//     subjects, at any depth, after the ones it names itself. So a helper
//     never pins a law or test to a file of helpers (a laws or tests file).
//  3. A top-level law or test sits in a file that declares one of its
//     subjects; else S115 at its name names each subject's file. A law or
//     test with no subject in its package (it names only the prelude, std or
//     imports) is S115 too: it belongs to the package it is about. A package
//     with another diagnostic gets no S115 (reportLawPlacements).
//
// Law.Subjects, Law.FixSubjects, Test.Subjects, Test.FixSubjects and
// Package.LawHelpers carry the verdict to ergo fix (lawfix), which moves a
// misplaced law or test beside its main subject (the one its name names,
// else the first), and each helper beside the first law or test that uses
// it.
//
// Prior art: Go keeps a package's examples and tests in _test files of the
// package, Rust keeps unit tests in a `mod tests` of the file they test,
// and Zig keeps a `test` block beside the function it tests; none checks
// the placement. Ergo checks it, since a law or test belongs beside the
// code it specifies and verifies (types.md, Laws 12).

// lawPlacementReported turns S115 on in Check. It is off until the
// repository's laws and tests stand with their subjects.
const lawPlacementReported = false

// LawSubject is one subject of a top-level law or test (Law.Subjects,
// Test.Subjects): the name the declaration (or one of its helpers) uses,
// and the declaration and file of it.
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

// File is the source file that declares the test.
func (t *Test) File() *syntax.File {
	if t.file == nil {
		return nil
	}
	return t.file.syntax
}

// ImportDefaultName is the name an unaliased import binds: its last path
// element, or a Go package's name.
func ImportDefaultName(spec *syntax.ImportSpec) string {
	path := spec.Path.Value
	if spec.Path.Kind != syntax.PATH {
		if p, err := strconv.Unquote(path); err == nil {
			path = p
		}
	}
	return importDefault(spec, path)
}

// IsValidImportName reports whether element can be the local name an import binds.
func IsValidImportName(element string) bool {
	return importName(element)
}

// ImportLocalName is the name an import binds: its alias, else its path's
// default name (its last element, or a Go package's name).
func ImportLocalName(spec *syntax.ImportSpec) string {
	if spec.Name != nil {
		return spec.Name.Name
	}
	return ImportDefaultName(spec)
}

// IsLawOrTestFile reports whether name is a file named for laws or tests
// (such as laws.ergo, tree_laws.ergo, tests.ergo, tree_test.ergo, or
// tree_tests.ergo).
func IsLawOrTestFile(name string) bool {
	base := strings.ToLower(path.Base(name))
	if strings.Contains(base, "law") {
		return true
	}
	stem := strings.TrimSuffix(base, ".ergo")
	return stem == "tests" || stem == "test" ||
		strings.HasSuffix(stem, "_tests") || strings.HasSuffix(stem, "_test") ||
		strings.HasPrefix(stem, "tests_") || strings.HasPrefix(stem, "test_")
}

// lawDecls is the package's declaration graph at the level of top-level
// declarations: who declares each object, which declarations each one
// names (refs, in source order) and which name it (users).
type lawDecls struct {
	decl map[Object]syntax.Decl
	top  map[syntax.Node]syntax.Decl
	// dotted holds each function x.f(…) reaches (196m, G-ex-e-2); owner
	// holds, for one whose type is declared in the package, that type's
	// declaration, and methods the reverse. A dotted function is a helper
	// only with its type.
	dotted  map[syntax.Decl]bool
	owner   map[syntax.Decl]syntax.Decl
	methods map[syntax.Decl][]syntax.Decl
	// memberLaws are the member laws of each type or machine declaration.
	memberLaws map[syntax.Decl][]*syntax.LawDecl
	file       map[syntax.Decl]*fileInfo
	refs       map[syntax.Decl][]lawRef
	users      map[syntax.Decl][]syntax.Decl
	order      []syntax.Decl
}

type lawRef struct {
	decl syntax.Decl
	name string
}

func (c *checker) lawDeclGraph() *lawDecls {
	g := &lawDecls{decl: map[Object]syntax.Decl{}, top: map[syntax.Node]syntax.Decl{}, dotted: map[syntax.Decl]bool{}, owner: map[syntax.Decl]syntax.Decl{}, methods: map[syntax.Decl][]syntax.Decl{}, memberLaws: map[syntax.Decl][]*syntax.LawDecl{}, file: map[syntax.Decl]*fileInfo{}, refs: map[syntax.Decl][]lawRef{}, users: map[syntax.Decl][]syntax.Decl{}}
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
		if f.Decl == nil || (f.owner == nil && f.opOwner == nil) {
			continue
		}
		g.dotted[f.Decl] = true
		if f.owner == nil || f.owner.origin().Obj == nil || f.owner.origin().Obj.Decl == nil {
			continue
		}
		if t, ok := g.top[f.owner.origin().Obj.Decl]; ok {
			g.owner[f.Decl] = t
			g.methods[t] = append(g.methods[t], f.Decl)
		}
	}
	var walk func(d syntax.Decl, root syntax.Node)
	walk = func(d syntax.Decl, root syntax.Node) {
		seen := map[syntax.Decl]bool{d: true}
		addObj := func(o Object) {
			if o == nil {
				return
			}
			to, ok := g.declOf(o)
			if !ok || seen[to] {
				return
			}
			seen[to] = true
			g.refs[d] = append(g.refs[d], lawRef{to, o.name()})
			g.users[to] = append(g.users[to], d)
		}
		syntax.Inspect(root, func(n syntax.Node) bool {
			// A name resolves through Uses; a method or operation called
			// through a dot has no Uses entry, so its call names it.
			// Package contexts in with / context embeds carry no Uses
			// entry (Info.Withs and c.pkg.Contexts hold them).
			switch n := n.(type) {
			case *syntax.LawDecl:
				if n != root {
					// A member law of a type or machine uses what it names
					// as a law does, not as its type does.
					g.memberLaws[d] = append(g.memberLaws[d], n)
					walk(n, n)
					return false
				}
			case *syntax.SelectorExpr:
				if id, ok := n.X.(*syntax.Ident); ok && strings.HasPrefix(id.Name, hiddenPrefix) {
					return false
				}
			case *syntax.Ident:
				addObj(c.pkg.Info.Uses[n])
			case *syntax.ContextEntry:
				if n.Kind == syntax.ContextEmbed {
					if id, ok := n.Key.(*syntax.Ident); ok && c.pkg.Info.Uses[id] == nil {
						if w := c.pkg.Contexts[id.Name]; w != nil && !w.Local && w.pkg == c.pkg && w.Decl != nil {
							addObj(w)
						}
					}
				}
			case *syntax.CallExpr:
				if sel, ok := n.Fun.(*syntax.SelectorExpr); ok {
					if id, ok := sel.X.(*syntax.Ident); ok && strings.HasPrefix(id.Name, hiddenPrefix) {
						return true
					}
				}
				if call := c.pkg.Info.Calls[n]; call != nil {
					switch {
					case call.Func != nil:
						addObj(call.Func)
					case call.Op != nil:
						addObj(call.Op)
					}
				}
				for w := c.pkg.Info.Withs[n]; w != nil; w = w.Next {
					for _, wc := range w.Contexts {
						if wc != nil && !wc.Local && wc.pkg == c.pkg && wc.Decl != nil {
							addObj(wc)
						}
					}
				}
			}
			return true
		})
	}
	for _, d := range g.order {
		walk(d, d)
	}
	// A type names its methods: one that moves takes them, and what they
	// name is what it names. They are no users of the type.
	for _, d := range g.order {
		for _, m := range g.methods[d] {
			g.refs[d] = append(g.refs[d], lawRef{m, m.(*syntax.FuncDecl).Name.Name})
		}
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
	case *Context:
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
	// byOwner marks a dotted function admitted only as part of its type,
	// whatever its name (it may provide an operation, which nothing names).
	byOwner := map[syntax.Decl]bool{}
	for _, d := range g.order {
		own := false
		if t := g.owner[d]; t != nil {
			f := d.(*syntax.FuncDecl)
			own = f.Derived == nil && f.Extern == nil
		}
		// An exported dotted function is an operation of the package's
		// surface: never a helper of the rule. ergo fix may still move one
		// out of a file of laws (exported), but never unexports it.
		named := len(g.users[d]) > 0 && helperKind(d, exported) && (exported || !(g.dotted[d] && isExported(declName(d))))
		switch {
		case named:
			h[d] = true
		case own:
			h[d], byOwner[d] = true, true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, d := range g.order {
			if !h[d] {
				continue
			}
			keep := true
			if t := g.owner[d]; byOwner[d] && !h[t] {
				keep = false
			}
			for _, m := range g.methods[d] {
				if !h[m] {
					keep = false
				}
			}
			for _, u := range g.users[d] {
				switch u.(type) {
				case *syntax.LawDecl, *syntax.TestDecl:
					continue
				}
				if !h[u] {
					keep = false
				}
			}
			if !keep {
				delete(h, d)
				changed = true
			}
		}
	}
	return h
}

// declName is the name of a function, type or context declaration, or "".
func declName(d syntax.Decl) string {
	switch d := d.(type) {
	case *syntax.FuncDecl:
		return d.Name.Name
	case *syntax.TypeDecl:
		return d.Name.Name
	case *syntax.ContextDecl:
		return d.Name.Name
	}
	return ""
}

func helperKind(d syntax.Decl, exported bool) bool {
	switch d := d.(type) {
	case *syntax.FuncDecl:
		return d.Recv == nil && d.Derived == nil && d.Extern == nil && d.Name.Name != "main" && (exported || !isExported(d.Name.Name))
	case *syntax.TypeDecl:
		return d.Extern == nil && d.Origin == nil && d.Derive == nil && (exported || !isExported(d.Name.Name))
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

// checkLawPlacement applies rule 3 to each top-level law and test, and
// records their subjects and the helpers that move with them.
func (c *checker) checkLawPlacement() {
	var top []*Law
	for _, law := range c.pkg.Laws {
		if law.Owner == nil && law.Machine == nil && law.Decl.Derived == nil && law.Decl.Pair == nil {
			top = append(top, law)
		}
	}
	if len(top) == 0 && len(c.pkg.Tests) == 0 && len(c.pkg.LawRuns) == 0 {
		return
	}
	g := c.lawDeclGraph()
	helper := g.helpers(false)
	// ergo fix reads more helpers than the rule: in a file of laws or tests
	// (laws.ergo, tree_laws.ergo, tests.ergo, tree_test.ergo), an exported
	// declaration that only laws and tests use was written for them too.
	// ergo fix unexports such a function when no other package names it, and
	// moves each such declaration beside its first user, so no file of laws
	// or tests is left. Elsewhere such a declaration is the package's
	// surface, even when only its tests use it.
	all := g.helpers(true)
	placeFile := map[*fileInfo]bool{}
	for _, fi := range c.files {
		placeFile[fi] = IsLawOrTestFile(fi.syntax.Name)
	}
	fixHelper := map[syntax.Decl]bool{}
	for _, d := range g.order {
		if helper[d] || all[d] && placeFile[g.file[d]] {
			fixHelper[d] = true
		}
		if f, ok := d.(*syntax.FuncDecl); ok && fixHelper[d] && !helper[d] && g.owner[d] == nil && !g.dotted[d] {
			c.pkg.LawExportedHelpers = append(c.pkg.LawExportedHelpers, f)
		}
	}
	// Each helper's first user: the first law or test, in package order,
	// that reaches it through helpers (a member law stands at its type).
	type user struct{ at, decl syntax.Decl }
	var users []user
	for _, d := range g.order {
		switch d.(type) {
		case *syntax.LawDecl, *syntax.TestDecl:
			users = append(users, user{d, d})
		}
		for _, m := range g.memberLaws[d] {
			users = append(users, user{d, m})
		}
	}
	first := map[syntax.Decl]syntax.Decl{}
	reach := map[syntax.Decl][]syntax.Decl{}
	for _, u := range users {
		seen := map[syntax.Decl]bool{}
		var visit func(x syntax.Decl)
		visit = func(x syntax.Decl) {
			for _, r := range g.refs[x] {
				if !fixHelper[r.decl] || seen[r.decl] {
					continue
				}
				seen[r.decl] = true
				if _, ok := first[r.decl]; !ok {
					first[r.decl] = u.at
				}
				if !slices.Contains(reach[r.decl], u.at) {
					reach[r.decl] = append(reach[r.decl], u.at)
				}
				visit(r.decl)
			}
		}
		visit(u.decl)
	}
	// A provider a member law runs against (LawRun.Bindings) that only the
	// laws need, declared in a file of laws or tests, is written for them:
	// it goes beside the operation type that declares the laws, with its
	// functions. Nothing names it, so no law or test reaches it above.
	for _, run := range c.pkg.LawRuns {
		owner := run.Law.Owner
		if owner == nil || owner.Decl == nil {
			continue
		}
		at, ok := g.top[owner.Decl]
		if !ok {
			continue
		}
		for _, b := range run.Bindings {
			for _, p := range b {
				if p.Type == nil || p.Type.origin().Obj == nil {
					continue
				}
				d, ok := g.declOf(p.Type.origin().Obj)
				if !ok || !placeFile[g.file[d]] || !fixHelper[d] {
					continue
				}
				for _, x := range append([]syntax.Decl{d}, g.methods[d]...) {
					if _, ok := first[x]; !ok && fixHelper[x] {
						first[x] = at
						reach[x] = []syntax.Decl{at}
					}
				}
			}
		}
	}
	for _, d := range g.order {
		if u, ok := first[d]; ok {
			c.pkg.LawHelpers = append(c.pkg.LawHelpers, LawHelper{Decl: d, User: u, Users: reach[d]})
		}
	}
	for _, law := range top {
		law.Subjects = g.subjects(law.Decl, helper)
		law.FixSubjects = g.subjects(law.Decl, fixHelper)
	}
	for _, test := range c.pkg.Tests {
		test.Subjects = g.subjects(test.Decl, helper)
		test.FixSubjects = g.subjects(test.Decl, fixHelper)
	}
}

// reportLawPlacements applies rule 3, after every other pass. A package
// with another diagnostic gets no S115: an unresolved name is no subject,
// so where its laws or tests belong is not known yet, and the other
// diagnostic is the one to fix first.
func (c *checker) reportLawPlacements() {
	if !c.placeLaws || len(c.diags) > 0 {
		return
	}
	for _, law := range c.pkg.Laws {
		if law.Owner == nil && law.Machine == nil && law.Decl.Derived == nil && law.Decl.Pair == nil {
			c.reportDeclPlacement("law", law.Name, law.Decl.Name, law.file, law.File(), law.Subjects)
		}
	}
	for _, test := range c.pkg.Tests {
		c.reportDeclPlacement("test", test.Name, test.Decl.Name, test.file, test.File(), test.Subjects)
	}
}

// LawHelper is a declaration ergo fix moves beside the first law or test
// that uses it (Package.LawHelpers): a law helper, or an exported
// declaration of a file of laws that only laws and tests use.
type LawHelper struct {
	Decl syntax.Decl
	// User is the first law or test that reaches it, or the type or
	// machine declaration of the first member law that does.
	User syntax.Decl
	// Users are every law, test or such type that reaches it, User first,
	// in package order. ergo fix leaves a helper where one of them stays
	// or arrives, so a second run moves nothing.
	Users []syntax.Decl
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

func (c *checker) reportDeclPlacement(kind, name string, at *syntax.Ident, fi *fileInfo, file *syntax.File, subjects []LawSubject) {
	saved := c.cur
	c.cur = fi
	defer func() { c.cur = saved }()
	if len(subjects) == 0 {
		c.errorf(at, CodeLawPlace, "%s %s names nothing in package %s; state it in the package it is about", kind, name, c.pkg.Name)
		return
	}
	var named []string
	files := map[*syntax.File]bool{}
	for _, s := range subjects {
		if s.File == file {
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
	c.errorf(at, CodeLawPlace, "%s %s is about %s; state it in %s", kind, name, list, where)
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
