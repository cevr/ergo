// Package graphrows writes the graph of a checked program as dense rows
// (CI-SELECT §12, GRAPH-TABLE): its modules, packages, files,
// declarations and tasks (a test, a law run) as node rows, and the
// relations between them as edge rows, each by a dense id. std codegraph
// decodes these rows, and every tool that asks "what depends on what"
// (the affected selector, the layout lint, a project's own workflow)
// reads the same rows: there is no second analysis.
//
// The rows hold no analysis of their own. They are a projection of what
// the checker and the loader already resolved: the loader's import
// specs, SEM's fact tables (facts.Build: symbols, sites, needs, fails),
// the checker's law subjects and tests, and the digester of the verdict
// store (lower.RunTarget.Inputs, lower.TestInputs), so a task's Input
// rows are exactly the declarations its key covers.
//
// Positions live in side columns (NodeAt, EdgeAt) that no key reads: a
// comment or layout edit changes a position and nothing else (Risk 3).
//
// Prior art: Elixir's mix xref reads edges the compiler's tracer records;
// Nx exports its project graph as data; Bazel's query, cquery and aquery
// read one graph of targets and actions.
package graphrows

import (
	"crypto/sha256"
	"encoding/base64"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/facts"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Version is the rows' format version; a decoder refuses another.
const Version = 1

// NodeKind is a node's kind: a closed set.
type NodeKind uint8

// The node kinds.
const (
	Module NodeKind = iota + 1
	Package
	File
	Decl
	Task
	nodeKindEnd
)

var nodeKindNames = [...]string{"", "module", "package", "file", "decl", "task"}

func (k NodeKind) String() string {
	if k < nodeKindEnd {
		return nodeKindNames[k]
	}
	return "node?"
}

// DeclKind is a declaration node's kind (Node.Sub of a Decl). Variant,
// Field and Operation are members of a type; a member law is a Law under
// its type or machine.
type DeclKind uint8

// The declaration kinds.
const (
	Func DeclKind = iota + 1
	Type
	Let
	Law
	Test
	Machine
	Context
	Operation
	Variant
	Field
	declKindEnd
)

var declKindNames = [...]string{"", "func", "type", "let", "law", "test", "machine", "context", "operation", "variant", "field"}

func (k DeclKind) String() string {
	if k < declKindEnd {
		return declKindNames[k]
	}
	return "decl?"
}

// TaskKind is a task node's kind (Node.Sub of a Task).
type TaskKind uint8

// The task kinds: a test block, and a law run bound to providers, under a
// named context, or a machine's search.
const (
	TestRun TaskKind = iota + 1
	LawRunProvider
	LawRunContext
	LawRunMachine
	taskKindEnd
)

var taskKindNames = [...]string{"", "test", "law-provider", "law-context", "law-machine"}

func (k TaskKind) String() string {
	if k < taskKindEnd {
		return taskKindNames[k]
	}
	return "task?"
}

// PackageGo is Node.Sub of a Package node imported with `import go`.
const PackageGo uint8 = 1

// EdgeKind is an edge's kind: a closed set.
type EdgeKind uint8

// The edge kinds (spec §12.3).
const (
	// Contains: module > package > file > top-level declaration > member.
	Contains EdgeKind = iota + 1
	// Imports: file -> package, at the import's path.
	Imports
	// Uses: declaration -> declaration, at the name; How is the site's
	// kind (facts.SiteKind: call, read, typeuse, construct, ...).
	Uses
	// Needs: function -> capability in its derived needs row.
	Needs
	// Fails: function -> failure of its error row, as derived.
	Fails
	// Provides: declaration -> capability its context binds or provides.
	Provides
	// ReExports: a let -> the declaration its value names, `let F = p.G`.
	ReExports
	// TestOf, LawOf: a test or law -> its subjects.
	TestOf
	LawOf
	// Input: task -> each declaration its key covers.
	Input
	// Requires: module -> module of the workspace it resolves locally (a
	// replace line of its go.mod that lands on the other's folder).
	Requires
	edgeKindEnd
)

var edgeKindNames = [...]string{"", "contains", "imports", "uses", "needs", "fails", "provides", "reexports", "testof", "lawof", "input", "requires"}

func (k EdgeKind) String() string {
	if k < edgeKindEnd {
		return edgeKindNames[k]
	}
	return "edge?"
}

// NoNode is the Parent of a root node and the File of no position.
const NoNode = ^uint32(0)

// Node is one node row. Name and Digest index Table.Strings.
type Node struct {
	Kind NodeKind
	// Sub is the DeclKind of a Decl, the TaskKind of a Task, PackageGo or
	// 0 for a Package.
	Sub uint8
	// Parent is the containing node: a package's module, a file's
	// package, a declaration's file (a member's declaration), a task's
	// package; NoNode for a module and for a package outside the
	// program's modules (std).
	Parent uint32
	// Name is a module's or package's path, a file's module-relative
	// path, a declaration's handle within its package (Name, Type.Member,
	// Name#test), a task's label.
	Name uint32
	// Digest is a top-level declaration's token digest (its tokens: no
	// comment, space or position) and a task's key (the verdict store's
	// digest); "" otherwise.
	Digest uint32
	// Subject is a task's law or test declaration; NoNode otherwise.
	Subject uint32
}

// Edge is one edge row.
type Edge struct {
	Kind EdgeKind
	How  facts.SiteKind
	From uint32
	To   uint32
}

// Pos is a position of the side columns: a file node, a byte offset, and
// its line and column; File is NoNode for none.
type Pos struct {
	File      uint32
	Off       uint32
	Line, Col uint32
}

// Table is the graph of one program.
type Table struct {
	// Strings are the interned names and digests; Strings[0] is "".
	Strings []string
	Nodes   []Node
	// NodeAt is each node's position: a declaration's name, else none.
	NodeAt []Pos
	Edges  []Edge
	// EdgeAt is each edge's position: a use's name, an import's path,
	// else none.
	EdgeAt []Pos
}

// Str is the string at i.
func (t *Table) Str(i uint32) string { return t.Strings[i] }

// SourceFile is one file of a package, by its module-relative name.
type SourceFile struct {
	Name   string
	Syntax *syntax.File
}

// SourcePackage is one package of the program as the loader read it.
type SourcePackage struct {
	Path string
	// Module is the module path that holds the package.
	Module string
	Files  []SourceFile
	// Info is the checker's package, also when it has diagnostics (its
	// rows are then the names the checker resolved); nil when unchecked.
	Info *check.Package
	// Checked is the package when it and its imports check: only a
	// checked package has tasks.
	Checked *check.Package
}

// builder holds the transient indexes of one Build.
type builder struct {
	t       *Table
	strs    map[string]uint32
	pkgs    map[string]uint32 // package path -> node
	mods    map[string]uint32
	handles map[string]uint32 // a declaration's full handle -> node
	decls   map[syntax.Node]uint32
	// named maps (file node, offset, name) of a declaring name to its
	// node: how a symbol finds the declaration that declares it.
	named map[namedKey]uint32
	edges map[Edge]bool // edges with no position, once each
	// files holds each file node's parsed file and its top-level
	// declaration nodes in source order.
	files map[uint32]*fileDecls
}

type fileDecls struct {
	syntax *syntax.File
	decls  []syntax.Decl
	nodes  []uint32
}

type namedKey struct {
	file uint32
	off  uint32
	name string
}

// Build writes the rows of the program pkgs, in import dependency order
// (the loader's, the root last).
func Build(pkgs []SourcePackage) *Table { return BuildWorkspace(nil, pkgs) }

// ModuleRow is a module of a workspace: its name and the modules it
// resolves locally.
type ModuleRow struct {
	Name     string
	Requires []string
}

// BuildWorkspace is Build with a workspace's modules: a module node for
// each of mods first, in their order, also one with no package, and a
// requires edge from a module to each it resolves locally.
func BuildWorkspace(mods []ModuleRow, pkgs []SourcePackage) *Table {
	b := &builder{
		t:       &Table{Strings: []string{""}},
		strs:    map[string]uint32{"": 0},
		pkgs:    map[string]uint32{},
		mods:    map[string]uint32{},
		handles: map[string]uint32{},
		decls:   map[syntax.Node]uint32{},
		named:   map[namedKey]uint32{},
		edges:   map[Edge]bool{},
		files:   map[uint32]*fileDecls{},
	}
	type built struct {
		files []uint32
		table *facts.Table
		path  string
	}
	for _, m := range mods {
		b.module(m.Name)
	}
	for _, m := range mods {
		for _, r := range m.Requires {
			b.edge(Edge{Kind: Requires, From: b.module(m.Name), To: b.module(r)}, Pos{File: NoNode})
		}
	}
	all := make([]built, len(pkgs))
	for i, p := range pkgs {
		parent := NoNode
		if p.Module != "" {
			parent = b.module(p.Module)
		}
		pn := b.packageNode(p.Path, parent, 0)
		files := make([]uint32, len(p.Files))
		for fi, f := range p.Files {
			files[fi] = b.node(Node{Kind: File, Parent: pn, Name: b.str(f.Name)}, Pos{File: NoNode})
			b.edge(Edge{Kind: Contains, From: pn, To: files[fi]}, Pos{File: NoNode})
			if f.Syntax != nil {
				b.declNodes(p, files[fi], f.Syntax)
			}
		}
		all[i] = built{files: files, path: p.Path}
		if p.Info != nil && p.Info.Info != nil {
			var ff []facts.File
			for _, f := range p.Files {
				ff = append(ff, facts.File{Name: f.Name, Syntax: f.Syntax})
			}
			all[i].table, _ = facts.Build(p.Info, ff)
			b.members(all[i].table, p.Path, files)
		}
	}
	for i, p := range pkgs {
		for fi, f := range p.Files {
			if f.Syntax != nil {
				b.imports(all[i].files[fi], f.Syntax)
			}
		}
		if t := all[i].table; t != nil {
			b.rows(t, p.Path, all[i].files, pkgs[i])
		}
		if p.Info != nil {
			b.laws(p.Info, p.Path)
		}
		if p.Checked != nil {
			b.tasks(p.Checked, b.pkgs[p.Path])
		}
	}
	return b.t
}

// module is the node of the module named name, added on first sight.
func (b *builder) module(name string) uint32 {
	m, ok := b.mods[name]
	if !ok {
		m = b.node(Node{Kind: Module, Parent: NoNode, Name: b.str(name)}, Pos{File: NoNode})
		b.mods[name] = m
	}
	return m
}

func (b *builder) str(s string) uint32 {
	if i, ok := b.strs[s]; ok {
		return i
	}
	i := uint32(len(b.t.Strings))
	b.t.Strings = append(b.t.Strings, s)
	b.strs[s] = i
	return i
}

func (b *builder) node(n Node, at Pos) uint32 {
	if n.Kind != Task {
		n.Subject = NoNode
	}
	id := uint32(len(b.t.Nodes))
	b.t.Nodes = append(b.t.Nodes, n)
	b.t.NodeAt = append(b.t.NodeAt, at)
	return id
}

// edge adds e at at; an edge with no position is added once.
func (b *builder) edge(e Edge, at Pos) {
	if at.File == NoNode {
		if b.edges[e] {
			return
		}
		b.edges[e] = true
	}
	b.t.Edges = append(b.t.Edges, e)
	b.t.EdgeAt = append(b.t.EdgeAt, at)
}

// packageNode is the node of the package at path, added under parent on
// first sight; a package first met as an import and later read keeps its
// node and takes its module.
func (b *builder) packageNode(path string, parent uint32, sub uint8) uint32 {
	if n, ok := b.pkgs[path]; ok {
		if parent != NoNode && b.t.Nodes[n].Parent == NoNode {
			b.t.Nodes[n].Parent = parent
			b.edge(Edge{Kind: Contains, From: parent, To: n}, Pos{File: NoNode})
		}
		return n
	}
	n := b.node(Node{Kind: Package, Sub: sub, Parent: parent, Name: b.str(path)}, Pos{File: NoNode})
	b.pkgs[path] = n
	if parent != NoNode {
		b.edge(Edge{Kind: Contains, From: parent, To: n}, Pos{File: NoNode})
	}
	return n
}

func (b *builder) pos(file uint32, f *syntax.File, off syntax.Pos) Pos {
	line, col := f.Position(off)
	return Pos{File: file, Off: uint32(off), Line: uint32(line), Col: uint32(col)}
}

// declNodes adds a node for each top-level declaration of f, an import
// excepted, with its token digest.
func (b *builder) declNodes(p SourcePackage, file uint32, f *syntax.File) {
	fd := &fileDecls{syntax: f}
	b.files[file] = fd
	for _, d := range f.Decls {
		var name *syntax.Ident
		var kind DeclKind
		switch d := d.(type) {
		case *syntax.FuncDecl:
			name, kind = d.Name, Func
		case *syntax.TypeDecl:
			name, kind = d.Name, Type
			if _, ok := d.Spec.(*syntax.MachineType); ok {
				kind = Machine
			}
		case *syntax.VarDecl:
			if len(d.Specs) > 0 && len(d.Specs[0].Names) > 0 {
				name = d.Specs[0].Names[0]
			}
			kind = Let
		case *syntax.ContextDecl:
			name, kind = d.Name, Context
		case *syntax.LawDecl:
			name, kind = d.Name, Law
		case *syntax.TestDecl:
			name, kind = d.Name, Test
		}
		if name == nil {
			continue
		}
		local := name.Name
		switch kind {
		case Test:
			local += "#test"
		case Law:
			local += "#law"
		}
		digest := ""
		if int(d.End()) <= len(f.Src) && d.Pos() >= 0 {
			digest = tokenDigest(f.Src[d.Pos():d.End()])
		}
		n := b.node(Node{Kind: Decl, Sub: uint8(kind), Parent: file, Name: b.str(local), Digest: b.str(digest)}, b.pos(file, f, name.NamePos))
		b.edge(Edge{Kind: Contains, From: file, To: n}, Pos{File: NoNode})
		b.decls[d] = n
		fd.decls, fd.nodes = append(fd.decls, d), append(fd.nodes, n)
		b.named[namedKey{file, uint32(name.NamePos), name.Name}] = n
		if kind == Test {
			b.named[namedKey{file, uint32(name.NamePos), local}] = n
		}
		if _, dup := b.handles[p.Path+"."+local]; !dup {
			b.handles[p.Path+"."+local] = n
		}
		if v, ok := d.(*syntax.VarDecl); ok {
			// Each name a let binds names the let.
			for _, nm := range v.Specs[0].Names[1:] {
				b.named[namedKey{file, uint32(nm.NamePos), nm.Name}] = n
			}
		}
	}
}

// tokenDigest is the digest of a declaration's tokens: kinds and
// literals, no comment, space or position (syntax.TokenText).
func tokenDigest(src string) string {
	h := sha256.Sum256([]byte(syntax.TokenText(src)))
	return "t1:" + base64.RawStdEncoding.EncodeToString(h[:12])
}

// isNode reports whether a symbol of kind k is a node of the graph:
// locals, parameters, type parameters and packages are not.
func isNode(k facts.Kind) bool {
	switch k {
	case facts.Param, facts.TypeParam, facts.Local, facts.Package, 0:
		return false
	}
	return true
}

func declKindOf(k facts.Kind) DeclKind {
	switch k {
	case facts.Func:
		return Func
	case facts.Type:
		return Type
	case facts.Variant:
		return Variant
	case facts.Field:
		return Field
	case facts.Operation:
		return Operation
	case facts.Machine:
		return Machine
	case facts.Law:
		return Law
	case facts.Test:
		return Test
	case facts.Context:
		return Context
	case facts.Let:
		return Let
	}
	return 0
}

// handleOf is a symbol's handle with the root package's checker name
// "main" replaced by its path.
func handleOf(t *facts.Table, path string, s facts.Symbol) string {
	h := s.Handle
	if t.Path == "main" && strings.HasPrefix(h, "main.") {
		h = path + h[len("main"):]
	}
	return h
}

// members maps each symbol t declares to its node: the top-level
// declaration whose name it is, or a member node under the declaration
// that holds it.
func (b *builder) members(t *facts.Table, path string, files []uint32) {
	for _, s := range t.Symbols {
		if !s.Declared || s.Handle == "" || !isNode(s.Kind) {
			continue
		}
		h := handleOf(t, path, s)
		if _, ok := b.handles[h]; ok {
			continue
		}
		file := files[s.File]
		if n, ok := b.named[namedKey{file, s.Off, s.Name}]; ok {
			b.handles[h] = n
			continue
		}
		owner := b.holder(file, s.Off)
		if owner == NoNode {
			continue
		}
		local := strings.TrimPrefix(h, path+".")
		n := b.node(Node{Kind: Decl, Sub: uint8(declKindOf(s.Kind)), Parent: owner, Name: b.str(local)}, b.pos(file, b.files[file].syntax, syntax.Pos(s.Off)))
		b.edge(Edge{Kind: Contains, From: owner, To: n}, Pos{File: NoNode})
		b.handles[h] = n
		b.named[namedKey{file, s.Off, s.Name}] = n
	}
}

// holder is the top-level declaration node of file whose source holds
// off, or NoNode.
func (b *builder) holder(file uint32, off uint32) uint32 {
	fd := b.files[file]
	if fd == nil {
		return NoNode
	}
	for i, d := range fd.decls {
		if uint32(d.Pos()) <= off && off < uint32(d.End()) {
			return fd.nodes[i]
		}
	}
	return NoNode
}

// imports adds an Imports row for each import spec of f.
func (b *builder) imports(file uint32, f *syntax.File) {
	for _, d := range f.Decls {
		id, ok := d.(*syntax.ImportDecl)
		if !ok {
			continue
		}
		for _, s := range id.Specs {
			if s.Path == nil {
				continue
			}
			path := strings.Trim(s.Path.Value, "\"`")
			var sub uint8
			if s.Kind == syntax.ImportGo {
				sub = PackageGo
			}
			pn := b.packageNode(path, NoNode, sub)
			b.edge(Edge{Kind: Imports, From: file, To: pn}, b.pos(file, f, s.Path.ValuePos))
		}
	}
}

// target is the node of a symbol of t: a declared one by its handle, one
// of another package by its handle, added under its package when the
// program does not hold it (a std declaration). NoNode for a symbol that
// is no node, or whose package t does not import (a predeclared name).
func (b *builder) target(t *facts.Table, path string, id uint32, imported []string) uint32 {
	if id == facts.NoSym || int(id) >= len(t.Symbols) {
		return NoNode
	}
	s := t.Symbols[id]
	if s.Handle == "" || !isNode(s.Kind) {
		return NoNode
	}
	h := handleOf(t, path, s)
	if n, ok := b.handles[h]; ok {
		return n
	}
	if s.Declared {
		return NoNode
	}
	pkg := ""
	for _, p := range imported {
		if strings.HasPrefix(h, p+".") && len(p) > len(pkg) {
			pkg = p
		}
	}
	if pkg == "" {
		return NoNode
	}
	pn := b.packageNode(pkg, NoNode, 0)
	n := b.node(Node{Kind: Decl, Sub: uint8(declKindOf(s.Kind)), Parent: pn, Name: b.str(h[len(pkg)+1:])}, Pos{File: NoNode})
	b.handles[h] = n
	return n
}

// rows adds the Uses, Needs, Fails, Provides, ReExports and TestOf rows
// of one package's fact table.
func (b *builder) rows(t *facts.Table, path string, files []uint32, p SourcePackage) {
	var imported []string
	for _, s := range t.Symbols {
		if s.Kind == facts.Package && s.Handle != "" {
			imported = append(imported, s.Handle)
		}
	}
	node := func(id uint32) uint32 { return b.target(t, path, id, imported) }
	syn := make([]*syntax.File, len(p.Files))
	for i, f := range p.Files {
		syn[i] = f.Syntax
	}
	for _, s := range t.Sites {
		if s.Kind == facts.Def || s.Kind == facts.Import || s.Encl == facts.NoSym {
			continue
		}
		from, to := node(s.Encl), node(s.Sym)
		if from == NoNode || to == NoNode {
			continue
		}
		file := files[s.File]
		at := Pos{File: file, Off: s.Off}
		if f := syn[s.File]; f != nil {
			at = b.pos(file, f, syntax.Pos(s.Off))
		}
		b.edge(Edge{Kind: Uses, How: s.Kind, From: from, To: to}, at)
		switch s.Kind {
		case facts.Provides, facts.Binds:
			b.edge(Edge{Kind: Provides, From: from, To: to}, Pos{File: NoNode})
		}
		if b.t.Nodes[from].Sub == uint8(Test) && b.t.Nodes[from].Kind == Decl && from != to {
			// A test's subjects: the declarations of its package it names.
			if top := b.top(to); top != NoNode && b.packageOf(top) == b.pkgs[path] && top != from {
				b.edge(Edge{Kind: TestOf, From: from, To: top}, Pos{File: NoNode})
			}
		}
	}
	for _, e := range t.Needs {
		if from, to := node(e.From), node(e.To); from != NoNode && to != NoNode {
			b.edge(Edge{Kind: Needs, From: from, To: to}, Pos{File: NoNode})
		}
	}
	for _, e := range t.Fails {
		if from, to := node(e.From), node(e.To); from != NoNode && to != NoNode {
			b.edge(Edge{Kind: Fails, From: from, To: to}, Pos{File: NoNode})
		}
	}
	b.reexports(t, syn, node)
}

// reexports adds a ReExports row for each `let X = q.Y` and `let X = Y`
// whose value is one name of a declaration.
func (b *builder) reexports(t *facts.Table, syn []*syntax.File, node func(uint32) uint32) {
	for fi, f := range syn {
		if f == nil {
			continue
		}
		for _, d := range f.Decls {
			v, ok := d.(*syntax.VarDecl)
			if !ok || len(v.Specs) != 1 || len(v.Specs[0].Names) != 1 || len(v.Specs[0].Values) != 1 {
				continue
			}
			var name *syntax.Ident
			switch x := v.Specs[0].Values[0].(type) {
			case *syntax.Ident:
				name = x
			case *syntax.SelectorExpr:
				if _, ok := x.X.(*syntax.Ident); ok {
					name = x.Sel
				}
			}
			if name == nil {
				continue
			}
			from := b.decls[d]
			for _, i := range t.ByPos {
				s := t.Sites[i]
				if int(s.File) == fi && s.Off == uint32(name.NamePos) && s.Kind != facts.Def {
					if to := node(s.Sym); to != NoNode && to != from {
						b.edge(Edge{Kind: ReExports, From: from, To: to}, Pos{File: NoNode})
					}
					break
				}
			}
		}
	}
}

// top is the top-level declaration that holds a declaration node (itself
// for a top-level one), or NoNode.
func (b *builder) top(n uint32) uint32 {
	for n != NoNode && b.t.Nodes[n].Kind == Decl {
		p := b.t.Nodes[n].Parent
		if p == NoNode || b.t.Nodes[p].Kind != Decl {
			return n
		}
		n = p
	}
	return NoNode
}

// packageOf is the package node that holds a node, or NoNode.
func (b *builder) packageOf(n uint32) uint32 {
	for n != NoNode {
		if b.t.Nodes[n].Kind == Package {
			return n
		}
		n = b.t.Nodes[n].Parent
	}
	return NoNode
}

// laws adds the LawOf rows of the laws pkg declares: a top-level law's
// subjects, a member law's type, a machine law's machine.
func (b *builder) laws(pkg *check.Package, path string) {
	for _, l := range pkg.Laws {
		if l.Decl == nil || l.Decl.Name == nil {
			continue
		}
		var from uint32 = NoNode
		if n, ok := b.decls[l.Decl]; ok {
			from = n
		} else {
			owner := l.Owner
			if owner == nil {
				owner = l.Machine
			}
			if owner != nil && owner.Decl != nil {
				// A member law: the node its name declares, in its
				// owner's file.
				if o, ok := b.decls[owner.Decl]; ok {
					if n, ok := b.named[namedKey{b.t.Nodes[o].Parent, uint32(l.Decl.Name.NamePos), l.Decl.Name.Name}]; ok {
						from = n
					}
				}
			}
		}
		if from == NoNode {
			continue
		}
		var subjects []syntax.Decl
		for _, s := range l.Subjects {
			subjects = append(subjects, s.Decl)
		}
		for _, d := range subjects {
			if to, ok := b.decls[d]; ok {
				b.edge(Edge{Kind: LawOf, From: from, To: to}, Pos{File: NoNode})
			}
		}
		for _, owner := range []*check.TypeName{l.Owner, l.Machine} {
			if owner != nil && owner.Decl != nil {
				if to, ok := b.decls[owner.Decl]; ok {
					b.edge(Edge{Kind: LawOf, From: from, To: to}, Pos{File: NoNode})
				}
			}
		}
	}
}

// tasks adds a Task node for each test of pkg and each law run its test
// build makes, with its key and an Input row for each declaration the key
// covers.
func (b *builder) tasks(pkg *check.Package, pn uint32) {
	add := func(kind TaskKind, label string, subject syntax.Node, digest string, inputs []lower.Input) {
		subj := NoNode
		if subject != nil {
			if n, ok := b.decls[subject]; ok {
				subj = n
			}
		}
		n := b.node(Node{Kind: Task, Sub: uint8(kind), Parent: pn, Name: b.str(label), Digest: b.str(digest), Subject: subj}, Pos{File: NoNode})
		for _, in := range inputs {
			if to, ok := b.decls[in.Decl]; ok {
				b.edge(Edge{Kind: Input, From: n, To: to}, Pos{File: NoNode})
			}
		}
	}
	for _, test := range pkg.Tests {
		if test.Decl == nil {
			continue
		}
		digest, inputs := lower.TestInputs(pkg, test)
		add(TestRun, "test "+test.Name, test.Decl, digest, inputs)
	}
	for _, rt := range lower.RunTargets(pkg) {
		kind := LawRunProvider
		switch {
		case rt.Context != nil:
			kind = LawRunContext
		case rt.Machine:
			kind = LawRunMachine
		}
		var subject syntax.Node
		switch {
		case rt.Law.Owner != nil && rt.Law.Owner.Decl != nil:
			subject = rt.Law.Owner.Decl
		case rt.Law.Machine != nil && rt.Law.Machine.Decl != nil:
			subject = rt.Law.Machine.Decl
		case rt.Law.Decl != nil:
			subject = rt.Law.Decl
		}
		digest, inputs := rt.Inputs(pkg)
		add(kind, rt.Label, subject, digest, inputs)
	}
}

// Sorted is the table's edges in a canonical order: kind, from, to, how,
// then position. Build adds them in the walk's order; a reader that
// compares two tables sorts them first.
func (t *Table) Sorted() []int {
	idx := make([]int, len(t.Edges))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(x, y int) int {
		a, c := t.Edges[x], t.Edges[y]
		switch {
		case a.Kind != c.Kind:
			return int(a.Kind) - int(c.Kind)
		case a.From != c.From:
			return cmp32(a.From, c.From)
		case a.To != c.To:
			return cmp32(a.To, c.To)
		case a.How != c.How:
			return int(a.How) - int(c.How)
		}
		return cmp32(t.EdgeAt[x].Off, t.EdgeAt[y].Off)
	})
	return idx
}

func cmp32(a, b uint32) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
