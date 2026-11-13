package lower

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// foreignDecls writes, into each package's extra text, the declarations
// of the names its IR bodies use from other packages: text spells a part
// of a declaration as a package spells it, qualified a name of another
// package. It reports why it cannot, "" when it can.
func (s *irSplit) foreignDecls(defs splitDefs, taken map[string]bool, text func(k, decl int, n ast.Node) string, qualified func(k int, name string) string, extra []bytes.Buffer) string {
	for _, k := range slices.Sorted(maps.Keys(s.uses)) {
		for _, name := range slices.Sorted(maps.Keys(s.uses[k])) {
			decl := defs.decl[name]
			switch n := defs.node[name].(type) {
			case *ast.TypeSpec:
				if n.TypeParams != nil {
					return "an IR body uses the generic type " + name + " of another package"
				}
				extra[k].WriteString("\ntype " + name + " = " + qualified(k, name) + "\n")
			case *ast.FuncDecl:
				if n.Type.TypeParams != nil {
					return "an IR body calls the generic function " + name + " of another package"
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
				extra[k].WriteString("\nfunc " + name + "(" + strings.Join(params, ", ") + ")" + results + " {\n\t" + ret + qualified(k, name) + "(" + strings.Join(args, ", ") + ")\n}\n")
			default:
				return "an IR body uses the variable " + name + " of another package"
			}
		}
	}
	return ""
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
// renamed as the split renames, in the package's folder. The root always
// has one, so a module built through IR names its root's file.
func (s *irSplit) files(pkgOf func(i int) int, rename map[string]string, plan *splitPlan) ([]File, int, error) {
	byPkg := map[int][]irFunc{1: nil}
	for i := range s.ir.Funcs {
		// A copy: the split may still fall back to the one-package IR.
		data, err := json.Marshal(&s.ir.Funcs[i])
		if err != nil {
			return nil, 0, err
		}
		var fn irFunc
		if err := json.Unmarshal(data, &fn); err != nil {
			return nil, 0, err
		}
		p := pkgOf(i)
		renamed := func(n *string) {
			if r, ok := rename[*n]; ok {
				*n = r
			}
		}
		own := func(n *string) {
			// A name of another package keeps its spelling: its alias or
			// forwarding function in p has it. A name of no declaration
			// of main.go (int, error) is no package-scope name.
			if _, declared := s.groupOf[*n]; declared && !s.uses[p][*n] {
				renamed(n)
			}
		}
		recv, method, isMethod := strings.Cut(fn.Name, ".")
		renamed(&recv)
		fn.Name = recv
		if isMethod {
			renamed(&method)
			fn.Name += "." + method
		}
		fn.walk(own, renamed)
		byPkg[p] = append(byPkg[p], fn)
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
// function, the named part of a type) and member on every field and
// method name; each may respell it.
func (fn *irFunc) walk(name, member func(*string)) {
	typ := func(t *string) {
		if *t == "" {
			return
		}
		rest := strings.TrimLeft(*t, "[]*")
		prefix := (*t)[:len(*t)-len(rest)]
		name(&rest)
		*t = prefix + rest
	}
	var expr func(x *irExpr)
	var stmts func(list []*irStmt)
	expr = func(x *irExpr) {
		if x == nil {
			return
		}
		switch x.K {
		case "call":
			name(&x.Name)
		case "mcall", "field":
			member(&x.Name)
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
	}
	stmts = func(list []*irStmt) {
		for _, s := range list {
			expr(s.X)
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
