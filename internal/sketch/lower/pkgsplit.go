package lower

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
)

// The package split (compile-speed CS4; docs/tooling.md, "Package
// split", and docs/receipts/compile-speed.md). The lowering writes one Go
// package, main.go, for the whole program. Split places each of its top-level declarations into a Go
// package: one per Ergo package of the program, plus one shared package
// below them all. Go then compiles the packages in parallel, and keeps
// each package's compile in its action cache, so an edit of one Ergo
// package's bodies compiles that Go package again, and its importers only
// when its export data changes (Go's own rule, which also covers a body
// that the Ergo inliner or Go's inliner copies into an importer).
//
// The split works on the declarations and their references, not on Go
// text alone, so the IR path places its functions with the same plan
// (splitPlan): a node is a declaration group (a type and its methods are
// one group, as Go requires), its natural package is the Ergo package of
// its source, and an edge is a reference to another group.
//
// Placement. A group from a source file starts in that file's package;
// a std or generated group starts in the root. Then, while some group u
// references a group v whose package u cannot import, v moves to the
// lower of the two packages: the one the other imports, else the shared
// package. A group only ever moves down, so this ends, and at the end
// every reference points to the group's own package or one it imports:
// the packages have no import cycle, because the Ergo packages have none.
// So a std or generated group lands in the one package that uses it, or
// in the lowest package that all its users see; a source group stays in
// its package unless a lower group needs it (a capability's operation
// type, which the environment of every package names, moves to the
// shared package, and so do the types its methods name).
//
// Names. A name that a reference from another package uses must be
// exported. Split renames exactly the names that cross: a package-scope
// name defined in one package and used in another, and a field or method
// name declared or selected in more than one package. The new name is
// the old one after one X or more, so no two names meet. Every other
// name keeps its spelling, so a %+v of a value that never leaves its
// package prints as before.
//
// Text. Split cuts main.go at declaration ends, keeps each declaration's
// leading comments and //line directives with it (anchorLines has
// anchored every line, so a declaration keeps its source positions in any
// file), and splices the renames and the package qualifiers in. It
// formats nothing again.
//
// The reference is the one-package module (ERGO_LOWER=single in
// sketchfront): TestSplitLoweringAgrees runs both.
//
// Prior art: Gleam, ReScript and Elixir emit one host module per source
// module and so never meet this; OCaml's -pack and Go's own linker work
// per compilation unit. Split keeps Ergo's whole-program lowering (its
// inliner and its flat unions see every package) and splits only what Go
// compiles.

// SplitReport says what Split did.
type SplitReport struct {
	// Packages is the number of Go packages of the module.
	Packages int
	// Reason is why the program stays one Go package, "" when it is split
	// or has one package to split into.
	Reason string
	// Pulled counts the source groups placed below their own package.
	Pulled int
	// Renamed counts the names given an exported spelling.
	Renamed int
}

// splitDir is the folder of the Go packages of the program's Ergo
// packages other than the root, and sharedDir the folder of the shared
// package.
const (
	splitDir  = "pkg"
	sharedDir = "shared"
)

// Split splits the module files of the lowering of u into one Go package
// per Ergo package and the shared package. Files other than main.go stay
// as they are. A program Split cannot split keeps its files, with the
// reason in the report.
func Split(u Unit, files []File) ([]File, SplitReport) {
	i := slices.IndexFunc(files, func(f File) bool { return f.Path == "main.go" })
	if i < 0 {
		return files, SplitReport{Packages: 1, Reason: "no main.go"}
	}
	out, rep := splitMain(u, files[i].Data, programModule)
	if out == nil {
		if rep.Packages == 0 {
			rep.Packages = 1
		}
		return files, rep
	}
	res := slices.Clone(files[:i])
	res = append(res, out...)
	return append(res, files[i+1:]...), rep
}

// splitPkg is one Go package of the split module.
type splitPkg struct {
	ergo  *check.Package // nil for the shared package
	dir   string         // "" for the root
	name  string         // Go package name
	alias string         // its name in an importer
	sees  map[int]bool   // the packages it may import
}

// splitPlan is the placement of declaration groups into packages: the
// part of the split that the source path and the IR path share.
type splitPlan struct {
	pkgs []splitPkg // 0 is the shared package, 1 the root
}

// can reports whether package a may import package b (or is b).
func (p *splitPlan) can(a, b int) bool { return a == b || b == 0 || p.pkgs[a].sees[b] }

// lower is the package that both a and b see: the lower one when one
// imports the other, else the shared package.
func (p *splitPlan) lower(a, b int) int {
	switch {
	case p.can(a, b):
		return b
	case p.can(b, a):
		return a
	}
	return 0
}

// splitGroup is one group of top-level declarations that stay together.
type splitGroup struct {
	decls   []int // indexes into the file's Decls
	natural int   // the package of its source, -1 for none
	pin     bool  // stays in the root: main, init, an embedded asset
	embed   bool
	pkg     int
	refs    []int
}

// place runs the placement fixpoint over groups, and reports the first
// pinned group that would have to leave the root.
func (p *splitPlan) place(groups []splitGroup) string {
	users := make([][]int, len(groups))
	for u := range groups {
		for _, v := range groups[u].refs {
			users[v] = append(users[v], u)
		}
	}
	work := make([]int, 0, len(groups))
	for u := range groups {
		work = append(work, u)
	}
	for len(work) > 0 {
		u := work[len(work)-1]
		work = work[:len(work)-1]
		for _, v := range groups[u].refs {
			if p.can(groups[u].pkg, groups[v].pkg) {
				continue
			}
			if groups[v].pin {
				if groups[v].embed {
					return "an embedded asset is read outside the root package"
				}
				return "a root-only declaration is used outside the root package"
			}
			groups[v].pkg = p.lower(groups[v].pkg, groups[u].pkg)
			work = append(work, v)
		}
	}
	return ""
}

// splitMain splits main.go, the root package at the import path base;
// nil files when it does not split. File paths are relative to the root
// package.
func splitMain(u Unit, src []byte, base string) ([]File, SplitReport) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, parser.ParseComments)
	if err != nil {
		return nil, SplitReport{Reason: "main.go does not parse: " + err.Error()}
	}
	tf := fset.File(f.Pos())
	off := func(p token.Pos) int { return tf.Offset(p) }

	plan, ofSource := splitPackages(u)
	scope := f.Scope

	// Groups: each non-method declaration, then each method joins its
	// receiver's group.
	var groups []splitGroup
	groupOf := map[string]int{} // package-scope name to group
	declGroup := make([]int, len(f.Decls))
	firstDecl := -1
	for i, d := range f.Decls {
		declGroup[i] = -1
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
			continue
		}
		if firstDecl < 0 {
			firstDecl = i
		}
		fd, ok := d.(*ast.FuncDecl)
		if ok && fd.Recv != nil {
			continue
		}
		g := splitGroup{decls: []int{i}, natural: -1}
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Name.Name == "main" || d.Name.Name == "init" {
				g.pin = true
			}
			if d.Name.Name != "init" {
				groupOf[d.Name.Name] = len(groups)
			}
		case *ast.GenDecl:
			if d.Doc != nil && hasEmbed(d.Doc) {
				g.pin, g.embed = true, true
			}
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					groupOf[s.Name.Name] = len(groups)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.Name != "_" {
							groupOf[n.Name] = len(groups)
						}
					}
				}
			}
		}
		declGroup[i] = len(groups)
		groups = append(groups, g)
	}
	if firstDecl < 0 {
		return nil, SplitReport{}
	}
	for i, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil {
			continue
		}
		g, ok := groupOf[recvBase(fd.Recv)]
		if !ok {
			return nil, SplitReport{Reason: "a method's receiver type is not declared in main.go"}
		}
		declGroup[i] = g
		groups[g].decls = append(groups[g].decls, i)
	}

	// Each declaration's segment: from the end of the one before it (the
	// import block for the first) to its own end, comments included.
	segStart := make([]int, len(f.Decls))
	segEnd := make([]int, len(f.Decls))
	prev := 0
	for i, d := range f.Decls {
		if i > 0 {
			prev = off(f.Decls[i-1].End())
		}
		segStart[i], segEnd[i] = prev, off(d.End())
	}
	// A declaration's natural package is the Ergo package of the source
	// file its last //line directive before it names.
	for i := range f.Decls {
		g := declGroup[i]
		if g < 0 || groups[g].natural >= 0 {
			continue
		}
		for _, c := range f.Comments {
			at := off(c.Pos())
			if at < segStart[i] || at >= off(f.Decls[i].Pos()) {
				continue
			}
			for _, line := range c.List {
				if p, ok := strings.CutPrefix(line.Text, "//line "); ok {
					if k, ok := ofSource[directivePath(p)]; ok {
						groups[g].natural = k
					}
				}
			}
		}
	}
	for g := range groups {
		groups[g].pkg = 1
		if groups[g].natural >= 0 && !groups[g].pin {
			groups[g].pkg = groups[g].natural
		}
	}

	// Occurrences: references to package-scope names, and field and method
	// names, by declaration.
	imports := map[string]*ast.ImportSpec{}
	for _, s := range f.Imports {
		imports[importLocal(s)] = s
	}
	type occ struct {
		id    *ast.Ident
		decl  int
		ref   int  // the group referenced, -1 for none
		def   bool // a package-scope name's definition
		field bool // a field or method name
	}
	var occs []occ
	all := map[string]bool{}
	usesImport := make([]map[string]bool, len(f.Decls))
	refSeen := map[[2]int]bool{}
	for i, d := range f.Decls {
		g := declGroup[i]
		if g < 0 {
			continue
		}
		usesImport[i] = map[string]bool{}
		skip := map[*ast.Ident]bool{}
		fieldIDs := map[*ast.Ident]bool{}
		defs := map[*ast.Ident]bool{}
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil {
				fieldIDs[d.Name] = true
			} else if d.Name.Name != "init" {
				defs[d.Name] = true
			} else {
				skip[d.Name] = true
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					defs[s.Name] = true
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.Name == "_" {
							skip[n] = true
						} else {
							defs[n] = true
						}
					}
				}
			}
		}
		var lits func(n ast.Node, elided ast.Expr)
		classify := func(cl *ast.CompositeLit, typ ast.Expr) {
			kind, elem, key := litKind(typ, scope)
			for _, e := range cl.Elts {
				kv, isKV := e.(*ast.KeyValueExpr)
				val := e
				if isKV {
					val = kv.Value
					if id, ok := kv.Key.(*ast.Ident); ok {
						switch kind {
						case litStruct:
							fieldIDs[id] = true
						case litForeign:
							skip[id] = true // a field of another Go package's struct
						}
					}
					if c, ok := kv.Key.(*ast.CompositeLit); ok && c.Type == nil {
						lits(c, key)
					}
				}
				if c, ok := val.(*ast.CompositeLit); ok && c.Type == nil {
					lits(c, elem)
				}
			}
		}
		lits = func(n ast.Node, elided ast.Expr) {
			cl := n.(*ast.CompositeLit)
			typ := cl.Type
			if typ == nil {
				typ = elided
			}
			classify(cl, typ)
		}
		ast.Inspect(d, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				skip[n.Sel] = true
				if x, ok := n.X.(*ast.Ident); ok && x.Obj == nil && scope.Lookup(x.Name) == nil {
					if _, ok := imports[x.Name]; ok {
						usesImport[i][x.Name] = true
						skip[x] = true
						return false
					}
				}
				fieldIDs[n.Sel] = true
			case *ast.StructType:
				for _, fl := range n.Fields.List {
					for _, id := range fl.Names {
						fieldIDs[id] = true
					}
				}
			case *ast.InterfaceType:
				for _, fl := range n.Methods.List {
					for _, id := range fl.Names {
						fieldIDs[id] = true
					}
				}
			case *ast.CompositeLit:
				if n.Type != nil {
					classify(n, n.Type)
				}
			case *ast.LabeledStmt:
				skip[n.Label] = true
			case *ast.BranchStmt:
				if n.Label != nil {
					skip[n.Label] = true
				}
			}
			return true
		})
		ast.Inspect(d, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			all[id.Name] = true
			switch {
			case skip[id] && !fieldIDs[id]:
			case fieldIDs[id]:
				occs = append(occs, occ{id: id, decl: i, ref: -1, field: true})
			case defs[id]:
				occs = append(occs, occ{id: id, decl: i, ref: -1, def: true})
			default:
				obj := scope.Lookup(id.Name)
				if obj == nil || (id.Obj != nil && id.Obj != obj) {
					return true
				}
				v, ok := groupOf[id.Name]
				if !ok {
					return true
				}
				occs = append(occs, occ{id: id, decl: i, ref: v})
				if v != g && !refSeen[[2]int{g, v}] {
					refSeen[[2]int{g, v}] = true
					groups[g].refs = append(groups[g].refs, v)
				}
			}
			return true
		})
	}

	if reason := plan.place(groups); reason != "" {
		return nil, SplitReport{Reason: reason}
	}
	used := map[int]bool{}
	pulled := 0
	for _, g := range groups {
		used[g.pkg] = true
		if g.natural >= 0 && g.pkg != g.natural {
			pulled++
		}
	}
	if len(used) == 1 && used[1] {
		return nil, SplitReport{Packages: 1}
	}

	// Renames: the names that cross packages.
	defPkg := map[string]int{}
	cross := map[string]bool{}
	memberPkgs := map[string]map[int]bool{}
	for _, o := range occs {
		p := groups[declGroup[o.decl]].pkg
		switch {
		case o.def:
			defPkg[o.id.Name] = p
		case o.field:
			if memberPkgs[o.id.Name] == nil {
				memberPkgs[o.id.Name] = map[int]bool{}
			}
			memberPkgs[o.id.Name][p] = true
		}
	}
	for _, o := range occs {
		if o.ref >= 0 && groups[o.ref].pkg != groups[declGroup[o.decl]].pkg {
			cross[o.id.Name] = true
		}
	}
	for name, ps := range memberPkgs {
		if len(ps) > 1 {
			cross[name] = true
		}
	}
	rename := map[string]string{}
	taken := maps.Clone(all)
	for _, name := range slices.Sorted(maps.Keys(cross)) {
		if exported(name) || name == "_" {
			continue
		}
		spell := "X" + name
		for taken[spell] {
			spell = "X" + spell
		}
		taken[spell] = true
		rename[name] = spell
	}

	// Aliases of the program's packages in an importer.
	for k := range plan.pkgs {
		stem := "ergo_" + plan.pkgs[k].name
		a := stem
		for n := 2; taken[a]; n++ {
			a = stem + strconv.Itoa(n)
		}
		taken[a] = true
		plan.pkgs[k].alias = a
	}

	// Splices, by declaration.
	type splice struct {
		at, end int
		text    string
	}
	splices := make([][]splice, len(f.Decls))
	needs := make([]map[int]bool, len(plan.pkgs))
	for k := range needs {
		needs[k] = map[int]bool{}
	}
	for _, o := range occs {
		name := o.id.Name
		if r, ok := rename[name]; ok {
			name = r
		}
		from := groups[declGroup[o.decl]].pkg
		if o.ref >= 0 && groups[o.ref].pkg != from {
			to := groups[o.ref].pkg
			needs[from][to] = true
			name = plan.pkgs[to].alias + "." + name
		}
		if name != o.id.Name {
			at := off(o.id.Pos())
			splices[o.decl] = append(splices[o.decl], splice{at, at + len(o.id.Name), name})
		}
	}

	header := src[:off(f.Package)]
	var outFiles []File
	for k := range plan.pkgs {
		if !used[k] {
			continue
		}
		var body bytes.Buffer
		importsUsed := map[string]bool{}
		for i := range f.Decls {
			g := declGroup[i]
			if g < 0 || groups[g].pkg != k {
				continue
			}
			for name := range usesImport[i] {
				importsUsed[name] = true
			}
			seg := splices[i]
			sort.Slice(seg, func(a, b int) bool { return seg[a].at < seg[b].at })
			at := segStart[i]
			for _, s := range seg {
				body.Write(src[at:s.at])
				body.WriteString(s.text)
				at = s.end
			}
			body.Write(src[at:segEnd[i]])
		}
		if k == 1 {
			// The root keeps what follows the last declaration.
			body.Write(src[segEnd[len(f.Decls)-1]:])
		} else {
			body.WriteString("\n")
		}
		var b bytes.Buffer
		b.Write(header)
		b.WriteString("package " + plan.pkgs[k].name + "\n\n")
		var specs []string
		for _, s := range f.Imports {
			local := importLocal(s)
			if importsUsed[local] || (k == 1 && (local == "_" || local == ".")) {
				specs = append(specs, string(src[off(s.Pos()):off(s.End())]))
			}
		}
		for _, to := range slices.Sorted(maps.Keys(needs[k])) {
			specs = append(specs, plan.pkgs[to].alias+" "+strconv.Quote(base+"/"+plan.pkgs[to].dir))
		}
		if len(specs) > 0 {
			b.WriteString("import (\n\t" + strings.Join(specs, "\n\t") + "\n)\n")
		}
		b.Write(body.Bytes())
		file := "main.go"
		if k != 1 {
			file = plan.pkgs[k].dir + "/" + plan.pkgs[k].name + ".go"
		}
		outFiles = append(outFiles, File{Path: file, Data: b.Bytes()})
	}
	return outFiles, SplitReport{Packages: len(used), Pulled: pulled, Renamed: len(rename)}
}

// splitPackages is the plan's packages for u, and the package of each
// source path its //line directives name.
func splitPackages(u Unit) (*splitPlan, map[string]int) {
	plan := &splitPlan{pkgs: []splitPkg{{dir: sharedDir, name: "ergoshared"}}}
	ofSource := map[string]int{}
	units := []Unit{u}
	imps := slices.Clone(u.Imports)
	sort.SliceStable(imps, func(i, j int) bool { return imps[i].Package.Path < imps[j].Package.Path })
	units = append(units, imps...)
	index := map[*check.Package]int{}
	for k, un := range units {
		p := splitPkg{ergo: un.Package, name: "main", sees: map[int]bool{}}
		if k > 0 {
			p.dir = splitDir + "/" + goPathOf(un.Package.Path)
			p.name = goPackageName(un.Package.Path)
		}
		index[un.Package] = len(plan.pkgs)
		for _, s := range un.Sources {
			ofSource[s.Path] = len(plan.pkgs)
		}
		plan.pkgs = append(plan.pkgs, p)
	}
	// Each package sees the packages it imports, transitively; the root
	// sees every package.
	direct := map[int][]int{}
	for p, k := range index {
		if p.Info == nil {
			continue
		}
		for _, o := range p.Info.Uses {
			if pn, ok := o.(*check.PkgName); ok {
				if j, ok := index[pn.Pkg]; ok && j != k && !slices.Contains(direct[k], j) {
					direct[k] = append(direct[k], j)
				}
			}
		}
	}
	for k := 1; k < len(plan.pkgs); k++ {
		var walk func(j int)
		walk = func(j int) {
			for _, d := range direct[j] {
				if !plan.pkgs[k].sees[d] {
					plan.pkgs[k].sees[d] = true
					walk(d)
				}
			}
		}
		walk(k)
		if k == 1 {
			for j := 2; j < len(plan.pkgs); j++ {
				plan.pkgs[1].sees[j] = true
			}
		}
	}
	return plan, ofSource
}

// directivePath is the source path of a //line directive's text: without
// its line and column, and without the inline tag of an inlined body.
func directivePath(p string) string {
	for range 2 {
		i := strings.LastIndex(p, ":")
		if i < 0 {
			break
		}
		if _, err := strconv.Atoi(p[i+1:]); err != nil {
			break
		}
		p = p[:i]
	}
	if i := strings.Index(p, "#ergo-inline-"); i >= 0 {
		p = p[:i]
	}
	return p
}

// goPathOf is an Ergo package path as a Go import path below splitDir.
func goPathOf(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = goIdentOf(s)
	}
	return path.Join(parts...)
}

// goPackageName is the Go package name of an Ergo package path.
func goPackageName(p string) string { return goIdentOf(path.Base(p)) }

// goIdentOf is s with every byte outside [A-Za-z0-9_] an underscore, and
// a leading digit or a Go keyword prefixed by p.
func goIdentOf(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '_'
		}
	}
	out := string(b)
	if out == "" || out[0] >= '0' && out[0] <= '9' || token.IsKeyword(out) || out == "main" || out == "init" {
		out = "p" + out
	}
	return out
}

// importLocal is the name an import spec gives its package in the file.
func importLocal(s *ast.ImportSpec) string {
	if s.Name != nil {
		return s.Name.Name
	}
	p, _ := strconv.Unquote(s.Path.Value)
	return path.Base(p)
}

// recvBase is the name of a method receiver's base type.
func recvBase(r *ast.FieldList) string {
	if r == nil || len(r.List) == 0 {
		return ""
	}
	t := r.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
		case *ast.IndexExpr:
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		case *ast.ParenExpr:
			t = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

// hasEmbed reports a //go:embed directive in a declaration's comments.
func hasEmbed(c *ast.CommentGroup) bool {
	for _, l := range c.List {
		if strings.HasPrefix(l.Text, "//go:embed") {
			return true
		}
	}
	return false
}

// The kinds of a composite literal: its keys are field names (a struct of
// the program, or a type split cannot see), expressions (a map, a slice
// or an array), or the exported fields of a type of another Go package
// (taken as such: a map of another package with a key that names a
// package-scope entity of the program would not compile, never misrun).
const (
	litStruct = iota
	litExpr
	litForeign
)

// litKind is the kind of a literal of type typ, and the types of its
// elided element and key literals.
func litKind(typ ast.Expr, scope *ast.Scope) (kind int, elem, key ast.Expr) {
	for range 16 {
		switch t := typ.(type) {
		case *ast.MapType:
			return litExpr, t.Value, t.Key
		case *ast.ArrayType:
			return litExpr, t.Elt, nil
		case *ast.StarExpr:
			typ = t.X
			continue
		case *ast.ParenExpr:
			typ = t.X
			continue
		case *ast.IndexExpr:
			typ = t.X
			continue
		case *ast.IndexListExpr:
			typ = t.X
			continue
		case *ast.SelectorExpr:
			return litForeign, nil, nil
		case *ast.Ident:
			obj := scope.Lookup(t.Name)
			if obj == nil {
				return litStruct, nil, nil
			}
			ts, ok := obj.Decl.(*ast.TypeSpec)
			if !ok {
				return litStruct, nil, nil
			}
			typ = ts.Type
			continue
		}
		return litStruct, nil, nil
	}
	return litStruct, nil, nil
}

// exported reports whether a Go name is exported.
func exported(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}
