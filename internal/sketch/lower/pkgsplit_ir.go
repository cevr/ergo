package lower

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"maps"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The IR path's package split (compile-speed CS10; docs/tooling.md,
// "Package split"). LowerIR leaves each function the IR builder lowers
// without a Go body, and the patched compile reads its body from an IR
// file. The split places these functions as it places any other, counting
// the names an IR body uses as its function's references, and writes each
// Go package's IR bodies to an IR file of its own, ergoir.json in the
// package's folder: one IR file and one compile per package, so an edit
// of one Ergo package's bodies changes one IR file.
//
// The patched compile resolves an IR body's names in its own package's
// scope (toolchain/ir: typ, call). A name an IR body uses that the split
// places in another package is declared again in the body's package,
// under the name the IR spells: a type as an alias of the other package's
// type, a function as a function that calls the other package's (Go
// inlines it). A field or method name an IR body selects counts as a
// member use in its package, so the split's renames reach the IR too.
//
// Prior art: Gleam and ReScript write one host module per source module
// and qualify every cross-module reference in the host's own syntax; here
// the IR has no qualified names, so the importing package declares what
// it uses, as a Go file would import it.

// irSplit is the IR of a module the split places.
type irSplit struct {
	ir *irFile
	// owner is each function's group, names the package-scope names its
	// body uses (its receiver's type included), members the field and
	// method names.
	owner   []int
	names   [][]string
	members [][]string
	// uses are, by package, the names of other packages its IR bodies use.
	uses map[int]map[string]bool
	// groupOf is the group of each package-scope name of main.go.
	groupOf map[string]int
	// qual are, by package, the generic names of other packages its IR
	// bodies use, each spelled PATH.Name (foreignDecls).
	qual map[int]map[string]string
	// getters are, by package, the forwarding functions its IR bodies
	// read another package's let with no value through (foreignDecls),
	// by the let's name.
	getters map[int]map[string]string
	// fresh, when set (LowerGoPackages), spells each declaration
	// foreignDecls writes anew, and local holds, by package, those
	// spellings by the name each declares; unset, a declaration has the
	// name it declares, which the one Go file keeps unique.
	fresh func(name string) string
	local map[int]map[string]string
}

// localName is the name package k declares name of another package by.
func (s *irSplit) localName(k int, name string) string {
	if n, ok := s.local[k][name]; ok {
		return n
	}
	if s.fresh == nil {
		return name
	}
	n := s.fresh(name)
	if s.local == nil {
		s.local = map[int]map[string]string{}
	}
	if s.local[k] == nil {
		s.local[k] = map[string]string{}
	}
	s.local[k][name] = n
	return n
}

// newIRSplit reads the references of ir's bodies; groupOf is the group
// of each package-scope name of main.go.
func newIRSplit(ir *irFile, groupOf map[string]int) (*irSplit, string) {
	s := &irSplit{ir: ir, uses: map[int]map[string]bool{}, groupOf: groupOf}
	for i := range ir.Funcs {
		fn := &ir.Funcs[i]
		recv, method, isMethod := strings.Cut(fn.Name, ".")
		g, ok := groupOf[recv]
		if !ok {
			return nil, "the IR function " + fn.Name + " is not declared in main.go"
		}
		names, members := map[string]bool{}, map[string]bool{}
		if isMethod {
			members[method] = true
		}
		fn.walk(func(n *string) {
			if _, ok := groupOf[*n]; ok {
				names[*n] = true
			}
		}, func(m *string) { members[*m] = true })
		s.owner = append(s.owner, g)
		s.names = append(s.names, slices.Sorted(maps.Keys(names)))
		s.members = append(s.members, slices.Sorted(maps.Keys(members)))
	}
	return s, ""
}

// foreign records that package p's IR bodies use name, which another
// package declares.
func (s *irSplit) foreign(p int, name string) {
	if s.uses[p] == nil {
		s.uses[p] = map[string]bool{}
	}
	s.uses[p][name] = true
}

// forwardedNames are the package-scope names (keys of groupOf) that
// foreignDecls spells in the declaration it writes for n in another
// package: a plain function's parameter and result types, a let's type
// (its getter's result). A generic function or type, and a type alias,
// spell none.
func forwardedNames(n ast.Node, groupOf map[string]int) []string {
	var types []ast.Expr
	switch n := n.(type) {
	case *ast.FuncDecl:
		if n.Type.TypeParams == nil {
			for _, list := range []*ast.FieldList{n.Type.Params, n.Type.Results} {
				if list != nil {
					for _, f := range list.List {
						types = append(types, f.Type) // not its names
					}
				}
			}
		}
	case *ast.ValueSpec:
		if len(n.Values) == 0 && n.Type != nil {
			types = append(types, n.Type)
		}
	}
	var out []string
	for _, x := range types {
		ast.Inspect(x, func(m ast.Node) bool {
			switch m := m.(type) {
			case *ast.SelectorExpr:
				return false // another Go package's name
			case *ast.Ident: // a nested field's name too: one more export, never a missing one
				if _, ok := groupOf[m.Name]; ok {
					out = append(out, m.Name)
				}
			}
			return true
		})
	}
	return out
}

// splitDefs are main.go's package-scope declarations: each name's node
// (*ast.FuncDecl, *ast.TypeSpec or *ast.ValueSpec) and the index of its
// declaration.
type splitDefs struct {
	node map[string]ast.Node
	decl map[string]int
}

func (d splitDefs) add(name string, n ast.Node, i int) {
	d.node[name], d.decl[name] = n, i
}

// forwardFile is the //line name of a forwarder foreignDecls writes: a
// name that is no source file, so a defect's report skips the frame.
const forwardFile = "ergoforward.go"

// foreignDecls writes, into each package's extra text, the declarations
// of the names its IR bodies use from other packages: text spells a part
// of a declaration as a package spells it, qualified a name of another
// package. A generic name gets no declaration: Go has no generic alias of
// a function, so the IR spells it PATH.Name (goPath), as it spells a Go
// package's names, and the patched compile resolves it in the package's
// imports (Q5, option b). It reports why it cannot, "" when it can.
func (s *irSplit) foreignDecls(defs splitDefs, taken map[string]bool, text func(k, decl int, n ast.Node) string, qualified, goPath func(k int, name string) string, extra []bytes.Buffer) string {
	s.qual = map[int]map[string]string{}
	s.getters = nil
	generic := func(k int, name string) {
		if s.qual[k] == nil {
			s.qual[k] = map[string]string{}
		}
		s.qual[k][name] = goPath(k, name)
	}
	for _, k := range slices.Sorted(maps.Keys(s.uses)) {
		for _, name := range slices.Sorted(maps.Keys(s.uses[k])) {
			decl := defs.decl[name]
			switch n := defs.node[name].(type) {
			case *ast.TypeSpec:
				if n.TypeParams != nil {
					generic(k, name)
					continue
				}
				extra[k].WriteString("\ntype " + s.localName(k, name) + " = " + qualified(k, name) + "\n")
			case *ast.FuncDecl:
				if n.Type.TypeParams != nil {
					generic(k, name)
					continue
				}
				var params, args []string
				for _, field := range n.Type.Params.List {
					typ := text(k, decl, field.Type)
					count := max(1, len(field.Names))
					for range count {
						arg := freshName(taken, "ergoArg")
						params = append(params, arg+" "+typ)
						if _, variadic := field.Type.(*ast.Ellipsis); variadic {
							arg += "..."
						}
						args = append(args, arg)
					}
				}
				results, ret := "", ""
				if n.Type.Results != nil {
					results, ret = " "+text(k, decl, n.Type.Results), "return "
				}
				// The forwarder is no source declaration: its own //line
				// name, which no source map holds, keeps it from the
				// directive above it, and a defect's report skips its
				// frame (test-speed FT6: the harness calls the root
				// through forwarders).
				extra[k].WriteString("\n//line " + forwardFile + ":1\nfunc " + s.localName(k, name) + "(" + strings.Join(params, ", ") + ")" + results + " {\n\t" + ret + qualified(k, name) + "(" + strings.Join(args, ", ") + ")\n}\n")
			case *ast.ValueSpec:
				// A package let of another package: Ergo has no mutable
				// package state (mutPackageLet), and Go initializes an
				// imported package first, so a copy reads what the
				// variable holds (the reader's "global" looks a name up in
				// the package's own scope only). A variable declared with
				// no value is set by ergoInit, which main runs inside the
				// root task (a let whose value takes statements), after
				// every package's Go initialization: a copy holds its zero
				// value (Q13: address-routes's people.All read as an empty list,
				// "person 3: not found"). The IR body reads it through a
				// function that returns its address (files), when the body
				// runs: *get() is the variable itself, a place whose
				// address the body may take (TAB-IR's &tabFK[i]; the
				// 20e3b4fbe fallout, where get() returned a value).
				if len(n.Values) == 0 {
					if n.Type == nil {
						return "an IR body uses the variable " + name + " of another package, declared with no type"
					}
					get := freshName(taken, "ergoGet"+strings.ToUpper(name[:1])+name[1:])
					extra[k].WriteString("\nfunc " + get + "() *" + text(k, decl, n.Type) + " { return &" + qualified(k, name) + " }\n")
					if s.getters == nil {
						s.getters = map[int]map[string]string{}
					}
					if s.getters[k] == nil {
						s.getters[k] = map[string]string{}
					}
					s.getters[k][name] = get
					continue
				}
				extra[k].WriteString("\nvar " + s.localName(k, name) + " = " + qualified(k, name) + "\n")
			default:
				return "an IR body uses the variable " + name + " of another package"
			}
		}
	}
	return ""
}

// importPaths are, by package, the Go import paths (keys of local) that
// its IR bodies' qualified names spell.
func (s *irSplit) importPaths(pkgOf func(i int) int, local map[string]string) map[int]map[string]bool {
	out := map[int]map[string]bool{}
	for i := range s.ir.Funcs {
		p := pkgOf(i)
		see := func(n *string) {
			// A qualified name is path.Name; a package call ("pcall")
			// spells its package's path alone.
			q := strings.TrimLeft(*n, "[]*")
			if _, ok := local[q]; !ok {
				at := strings.LastIndex(q, ".")
				if at <= 0 {
					return
				}
				if q = q[:at]; local[q] == "" {
					return
				}
			}
			if out[p] == nil {
				out[p] = map[string]bool{}
			}
			out[p][q] = true
		}
		s.ir.Funcs[i].walk(see, func(*string) {})
	}
	return out
}

// freshName is a name with prefix that taken lacks, then taken.
func freshName(taken map[string]bool, prefix string) string {
	for n := 0; ; n++ {
		name := prefix + strconv.Itoa(n)
		if !taken[name] {
			taken[name] = true
			return name
		}
	}
}

// files are the IR files of the split module: each package's bodies,
// renamed as the split renames, in the package's folder: nameOf spells a
// package-scope name in its own package, member a field or method name.
// The root always has one, so a module built through IR names its root's
// file; a split of Go packages writes no root (LowerGoPackages).
func (s *irSplit) files(pkgOf func(i int) int, nameOf, member func(string) string, plan *splitPlan) ([]File, int, error) {
	byPkg := map[int][]irFunc{}
	if plan.pkgs[1].name != goPackagesRoot {
		byPkg[1] = nil
	}
	for i := range s.ir.Funcs {
		// Renamed in place: the split no longer falls back to the
		// one-package IR once it writes the IR files, and an error here
		// fails the lowering (lowerIRSplit). A JSON round trip of each
		// function copied it before GP-SPLIT-EDIT, half of the split's
		// own work.
		fn := &s.ir.Funcs[i]
		p := pkgOf(i)
		renamed := func(n *string) { *n = member(*n) }
		own := func(n *string) {
			// A name of another package is spelled as its alias or
			// forwarding function in p is declared (localName). A
			// generic one is spelled PATH.Name. A name of no declaration
			// of main.go (int, error) is no package-scope name.
			if q, ok := s.qual[p][*n]; ok {
				*n = q
			} else if _, declared := s.groupOf[*n]; declared && s.uses[p][*n] {
				*n = s.localName(p, *n)
			} else if declared {
				*n = nameOf(*n)
			}
		}
		recv, method, isMethod := strings.Cut(fn.Name, ".")
		fn.Name = nameOf(recv)
		if isMethod {
			renamed(&method)
			fn.Name += "." + method
		}
		fn.walk(own, renamed)
		if err := pcalls(fn, s.qual[p]); err != nil {
			return nil, 0, err // after the rename: the lowering fails (SplitReport.err)
		}
		if get := s.getters[p]; len(get) > 0 {
			fn.exprs(func(x *irExpr) {
				if g, ok := get[x.Name]; ok && x.K == "global" {
					// *g(): the variable itself, a place, as the global
					// is (&tabFK[i], a *T method's receiver), never a copy.
					x.K, x.Name, x.X = "deref", "", &irExpr{K: "call", Pos: x.Pos, Name: g}
				}
			})
		}
		byPkg[p] = append(byPkg[p], *fn)
	}
	var out []File
	withIR := 0
	for _, p := range slices.Sorted(maps.Keys(byPkg)) {
		funcs := byPkg[p]
		if len(funcs) > 0 {
			withIR++
		}
		sort.Slice(funcs, func(a, b int) bool { return funcs[a].Name < funcs[b].Name })
		data, err := json.Marshal(&irFile{V: s.ir.V, Files: s.ir.Files, Funcs: funcs})
		if err != nil {
			return nil, 0, err
		}
		out = append(out, File{Path: path.Join(plan.pkgs[p].dir, IRFileName), Data: data})
	}
	return out, withIR, nil
}

// pcalls makes each call of a generic function of another package, which
// files spelled PATH.Name (qual's values), the IR's call of a function
// of an imported package: pcall, its path in T, its type arguments in V.
// A use of one as a value has no such form.
func pcalls(fn *irFunc, qual map[string]string) error {
	if len(qual) == 0 {
		return nil
	}
	foreign := map[string]bool{}
	for _, q := range qual {
		foreign[q] = true
	}
	var err error
	fn.exprs(func(x *irExpr) {
		if !foreign[x.Name] {
			return
		}
		switch x.K {
		case "call":
			at := strings.LastIndex(x.Name, ".")
			x.K, x.V, x.T, x.Name = "pcall", x.T, x.Name[:at], x.Name[at+1:]
		case "global":
			if err == nil {
				err = fmt.Errorf("an IR body uses the generic function %s of another package as a value", x.Name)
			}
		}
	})
	return err
}

// exprs calls f on every expression of fn's body, outer first.
func (fn *irFunc) exprs(f func(*irExpr)) {
	var expr func(x *irExpr)
	var stmts func(list []*irStmt)
	expr = func(x *irExpr) {
		if x == nil {
			return
		}
		f(x)
		expr(x.X)
		expr(x.Y)
		for _, a := range x.Args {
			expr(a)
		}
		for _, e := range x.Elems {
			expr(e)
		}
		stmts(x.Body)
	}
	stmts = func(list []*irStmt) {
		for _, s := range list {
			expr(s.X)
			expr(s.Y)
			stmts(s.Then)
			stmts(s.Else)
			stmts(s.Body)
			for _, c := range s.Cases {
				stmts(c.Body)
			}
		}
	}
	stmts(fn.Body)
}

// irHash is the digest of a module's IR files, in path order.
func irHash(files []File) string {
	h := sha256.New()
	for _, f := range files {
		if path.Base(f.Path) == IRFileName {
			h.Write([]byte(f.Path + "\x00"))
			h.Write(f.Data)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// walk calls name on every package-scope name fn's body spells (a called
// function, a package variable, the named part of a type) and member on
// every field and method name; each may respell it.
func (fn *irFunc) walk(name, member func(*string)) {
	// typ respells each package-scope name of a type spelling: a generic
	// instance's own name and its type arguments (Decision[Phase, string],
	// MSB2 x CS10), and a slice's, a pointer's, a map's or a function's
	// element (map[int]Placed, func(int) Option[ended]: Q13, the I-FLIP
	// form of 59b5a95fa).
	typ := func(t *string) {
		s := *t
		var out strings.Builder
		for i := 0; i < len(s); {
			j := i
			// An import path (a pcall's package, a runtime type's
			// qualifier) is one name: its slashes do not split it.
			for j < len(s) && (isSpellingName(s[j]) || s[j] == '/') {
				j++
			}
			if j == i {
				out.WriteByte(s[i])
				i++
				continue
			}
			// A dotted name (path.Name, a path alone) is another
			// package's: the respellers keep it, and importPaths reads it.
			n := s[i:j]
			name(&n)
			out.WriteString(n)
			i = j
		}
		*t = out.String()
	}
	var expr func(x *irExpr)
	var stmts func(list []*irStmt)
	expr = func(x *irExpr) {
		if x == nil {
			return
		}
		switch x.K {
		case "call", "global":
			name(&x.Name)
		case "mcall", "field":
			member(&x.Name)
		case "pcall":
			// A generic function's instance of another package: V lists
			// its type arguments (slices.Clone[[]Ref[node[E]], …]).
			typ(&x.V)
		}
		typ(&x.T)
		for i := range x.Keys {
			member(&x.Keys[i])
		}
		expr(x.X)
		expr(x.Y)
		for _, a := range x.Args {
			expr(a)
		}
		for _, e := range x.Elems {
			expr(e)
		}
		stmts(x.Body)
	}
	stmts = func(list []*irStmt) {
		for _, s := range list {
			expr(s.X)
			expr(s.Y)
			stmts(s.Then)
			stmts(s.Else)
			stmts(s.Body)
			for _, c := range s.Cases {
				for i := range c.Types {
					typ(&c.Types[i])
				}
				stmts(c.Body)
			}
		}
	}
	for i := range fn.Locals {
		typ(&fn.Locals[i].Type)
	}
	stmts(fn.Body)
}
