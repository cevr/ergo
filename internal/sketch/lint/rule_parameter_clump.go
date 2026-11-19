package lint

import (
	"cmp"
	"fmt"
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// parameterClump is the strict rule for data that travels together
// without a name (anti-slop data-clumps; Fowler): a declaration, function
// or record, that shares three or more (name, type) pairs with each of
// two or more other declarations of the program. Two declarations alone
// were mostly a recursive helper and its caller sharing a working
// context (the 2026-11-18 read sample, about 6 of 15 true); a group in
// three places is data the code keeps passing together. The group is a record the domain
// has not named yet, or one it has and these parameters do not use. Out
// of scope: a method that provides an operation and an operation's
// members (the operation fixes the parameters), Go bindings, derived
// declarations, tests and laws, and a function that returns the record
// it shares the pairs with (its constructor). A record some function
// fails with is out too (a failure records the values it is about), as
// are the files of tests (the 2026-11-18 read sample).
var parameterClump = &Rule{
	Name:    "parameter-clump",
	Code:    "L139",
	Doc:     "The same three or more named, typed parameters or fields in three or more declarations are a record the code has not named.",
	Instead: "name the group as a record and pass it, or pass the record that already holds these fields",
	Default: Warning,
	Check:   checkParameterClump,
}

// clumpDecl is one declaration's parameters or fields: its file, name,
// the record type it declares (nil for a function), the record its
// function returns, and its (name, type) pairs.
type clumpDecl struct {
	rel     string
	name    *syntax.Ident
	record  *check.TypeName
	returns map[*check.TypeName]bool
	pairs   map[clumpPair]bool
}

// clumpPair is one parameter or field: its name and its type's key.
type clumpPair struct{ name, typ string }

func checkParameterClump(p *Pass) {
	found := p.Memo(func() any { return parameterClumps(p) }).(map[*syntax.Ident]string)
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			if msg, ok := found[id]; ok {
				p.Report(id.Pos(), id.End(), msg)
			}
		}
		return true
	})
}

// parameterClumps maps each declaration that shares three or more pairs
// with another to its message, naming the first partner by file and
// name.
func parameterClumps(p *Pass) map[*syntax.Ident]string {
	failures := programFailures(p)
	var all []clumpDecl
	for _, u := range p.Program() {
		info := u.Package.Info
		for _, f := range u.Files {
			if f.Syntax == nil || testFile(f.Syntax) {
				continue
			}
			for _, d := range f.Syntax.Decls {
				switch d := d.(type) {
				case *syntax.FuncDecl:
					fn, ok := info.Defs[d.Name].(*check.Func)
					if !ok || fn.Sig == nil || d.Extern != nil || d.Derived != nil || fn.ProvidesOperation() {
						continue
					}
					c := clumpDecl{rel: f.Rel, name: d.Name, returns: map[*check.TypeName]bool{}, pairs: map[clumpPair]bool{}}
					for _, v := range fn.Sig.Params {
						c.pairs[clumpPair{v.Name, typeKey(v.Type)}] = true
					}
					for _, r := range fn.Sig.Results {
						if n, ok := r.(*check.Named); ok {
							c.returns[origin(n).Obj] = true
						}
					}
					all = append(all, c)
				case *syntax.TypeDecl:
					if d.Extern != nil || d.TypeParams != nil || d.Origin != nil {
						continue
					}
					tn, ok := info.Defs[d.Name].(*check.TypeName)
					if !ok || failures[tn] {
						continue
					}
					s, ok := underlying(tn).(*check.Struct)
					if !ok {
						continue
					}
					c := clumpDecl{rel: f.Rel, name: d.Name, record: tn, pairs: map[clumpPair]bool{}}
					for _, fl := range s.Fields {
						c.pairs[clumpPair{fl.Name, typeKey(fl.Type)}] = true
					}
					all = append(all, c)
				}
			}
		}
	}
	slices.SortStableFunc(all, func(a, b clumpDecl) int {
		return cmp.Or(cmp.Compare(a.rel, b.rel), cmp.Compare(a.name.Name, b.name.Name))
	})
	out := map[*syntax.Ident]string{}
	for i, a := range all {
		partners, first := 0, ""
		for j, b := range all {
			if i == j || len(a.pairs) < 3 || len(b.pairs) < 3 || constructs(a, b) || constructs(b, a) {
				continue
			}
			var shared []string
			for k := range a.pairs {
				if b.pairs[k] {
					shared = append(shared, k.name)
				}
			}
			if len(shared) < 3 {
				continue
			}
			slices.Sort(shared)
			where := ""
			if b.rel != a.rel {
				where = " in " + b.rel
			}
			list := shared[0]
			for _, s := range shared[1:] {
				list += ", " + s
			}
			partners++
			if partners == 1 {
				first = a.name.Name + " shares " + list + " with " + b.name.Name + where
			}
		}
		if partners >= 2 {
			out[a.name] = fmt.Sprintf("%s and %d other declarations", first, partners-1)
		}
	}
	return out
}

// constructs reports whether a is a function that returns the record b
// declares.
func constructs(a, b clumpDecl) bool {
	return b.record != nil && a.returns[b.record]
}
