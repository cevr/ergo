package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Digests is what the digest walks of one checked program read of each
// declaration, kept once for every walk that reads it: a declaration's
// token text, the calls its walk makes (digestEdge), each package's lets,
// the members a qualified name reads and each operation type's builder.
// A walk is the same with a memo or without one: the memo keeps only
// answers that depend on the checked packages, which do not change after
// the check, never on a walk's own state (what it has seen), so a run of
// a package's law and test keys (sketchfront's routes, verdict views)
// tokenizes and walks each declaration once instead of once per target
// (PERF-LOWER-BUILD-1: the walks were 15 % of an `ergo test`'s CPU).
//
// A Digests is filled from one program's packages only, and is not safe
// for concurrent use.
type Digests struct {
	text     map[digestKey]string
	edges    map[digestKey][]digestEdge
	lets     map[*check.Var]letDecl
	named    map[*check.Package]map[string]*check.Var
	indexed  map[*check.Package]bool
	members  map[memberKey]memberDecls
	builders map[builderKey]*check.Builder
	// reference makes each digester read through a memo of its own and
	// walk each declaration directly (digester.walkDirect): the digests
	// before the memo, which a test compares the memo's against.
	reference bool
}

// NewDigests is an empty memo.
func NewDigests() *Digests {
	return &Digests{
		text:     map[digestKey]string{},
		edges:    map[digestKey][]digestEdge{},
		lets:     map[*check.Var]letDecl{},
		named:    map[*check.Package]map[string]*check.Var{},
		indexed:  map[*check.Package]bool{},
		members:  map[memberKey]memberDecls{},
		builders: map[builderKey]*check.Builder{},
	}
}

// ReferenceDigests is the memo that keeps nothing: each digest read
// through it walks every declaration directly, as the digests did before
// Digests, the reference a shared memo's digests are compared against
// (TestDigestMemoAgreesWithDirectWalk).
func ReferenceDigests() *Digests {
	m := NewDigests()
	m.reference = true
	return m
}

// digestKey is a declaration node of a package.
type digestKey struct {
	pkg  *check.Package
	node syntax.Node
}

// memberKey is a name a qualified name reads in a package.
type memberKey struct {
	pkg  *check.Package
	name string
}

// memberDecls are the types and functions of a package with one name, in
// the package's order.
type memberDecls struct {
	types []*check.TypeName
	funcs []*check.Func
}

// builderKey is an operation type as a package sees it.
type builderKey struct {
	pkg   *check.Package
	named *check.Named
}

// edgeKind is what a digest walk does at one node.
type edgeKind uint8

const (
	edgeConversion edgeKind = iota // a derived walk adds an implicit conversion
	edgeInstance                   // a derived walk adds a generic call's witnesses
	edgeFunc                       // a call or a name of a function
	edgeType                       // a name of a type
	edgeLet                        // a name of a variable (a package let, or a local)
	edgeMember                     // a qualified name: a member of an imported package
)

// digestEdge is one call a digest walk makes at a node of a declaration,
// in the walk's order: replaying a declaration's edges on a digester is
// walking the declaration.
type digestEdge struct {
	kind edgeKind
	conv check.Conversion
	inst *check.Instance
	fn   *check.Func
	typ  *check.TypeName
	v    *check.Var
	pkg  *check.Package
	name string
}

// declText is the token text of the declaration n of p (syntax.TokenText
// of its source).
func (m *Digests) declText(p *check.Package, n syntax.Node) string {
	k := digestKey{p, n}
	if t, ok := m.text[k]; ok {
		return t
	}
	t := syntax.TokenText(declText(p, n))
	m.text[k] = t
	return t
}

// edgesOf is the calls the digest walk of the node n of p makes, in order:
// at each expression the implicit conversion, those of its elements and
// the instance of a generic call (derived walks only), then the function
// a call reaches, the object an identifier uses, or the member a
// qualified name reads. p.Info is not nil.
func (m *Digests) edgesOf(p *check.Package, n syntax.Node) []digestEdge {
	k := digestKey{p, n}
	if e, ok := m.edges[k]; ok {
		return e
	}
	info := p.Info
	out := []digestEdge{}
	syntax.Inspect(n, func(n syntax.Node) bool {
		if e, ok := n.(syntax.Expr); ok {
			if c, ok := info.Implicit[e]; ok {
				out = append(out, digestEdge{kind: edgeConversion, conv: c})
			}
			for _, c := range info.ImplicitElems[e] {
				out = append(out, digestEdge{kind: edgeConversion, conv: c})
			}
			if in := info.Instances[e]; in != nil {
				out = append(out, digestEdge{kind: edgeInstance, inst: in})
			}
		}
		switch n := n.(type) {
		case *syntax.CallExpr:
			if c := info.Calls[n]; c != nil && c.Func != nil {
				out = append(out, digestEdge{kind: edgeFunc, fn: c.Func})
			}
		case *syntax.Ident:
			switch obj := info.Uses[n].(type) {
			case *check.Func:
				out = append(out, digestEdge{kind: edgeFunc, fn: obj})
			case *check.TypeName:
				out = append(out, digestEdge{kind: edgeType, typ: obj})
			case *check.Var:
				out = append(out, digestEdge{kind: edgeLet, v: obj})
			}
		case *syntax.SelectorExpr:
			// The checker records no use of a qualified name's member.
			if id, ok := n.X.(*syntax.Ident); ok {
				if pn, ok := info.Uses[id].(*check.PkgName); ok {
					out = append(out, digestEdge{kind: edgeMember, pkg: pn.Pkg, name: n.Sel.Name})
				}
			}
		}
		return true
	})
	m.edges[k] = out
	return out
}

// index records the package lets of p: each variable a let of p defines,
// with its declaration, and by its name. p.Info is not nil.
func (m *Digests) index(p *check.Package) {
	if m.indexed[p] {
		return
	}
	m.indexed[p] = true
	for _, f := range p.Files() {
		for _, decl := range f.Decls {
			vd, ok := decl.(*syntax.VarDecl)
			if !ok {
				continue
			}
			syntax.Inspect(vd, func(n syntax.Node) bool {
				if id, ok := n.(*syntax.Ident); ok {
					if v, ok := p.Info.Defs[id].(*check.Var); ok {
						m.lets[v] = letDecl{pkg: p, decl: vd}
						if m.named[p] == nil {
							m.named[p] = map[string]*check.Var{}
						}
						m.named[p][v.Name] = v
					}
				}
				return true
			})
		}
	}
}

// member is the types and the functions of q named name.
func (m *Digests) member(q *check.Package, name string) memberDecls {
	k := memberKey{q, name}
	if md, ok := m.members[k]; ok {
		return md
	}
	var md memberDecls
	for _, t := range q.Types {
		if t.Name == name {
			md.types = append(md.types, t)
		}
	}
	for _, f := range q.Funcs {
		if f.Name == name {
			md.funcs = append(md.funcs, f)
		}
	}
	m.members[k] = md
	return md
}

// builder is the builder of the operation type n in pkg (Package.Builder).
func (m *Digests) builder(pkg *check.Package, n *check.Named) *check.Builder {
	k := builderKey{pkg, n}
	if b, ok := m.builders[k]; ok {
		return b
	}
	b := pkg.Builder(n)
	m.builders[k] = b
	return b
}
