package syntax

import (
	"unicode"
	"unicode/utf8"
)

// By-name codec pairs (types.md, Codecs 1 and 6; L11 design item 7). A
// file that declares both halves of a pair,
//
//	func ParseX(b B) X fails …
//	func Wire(x X) B
//
// with Wire failing nothing and neither half needing anything, gets the
// law the parser derives after the file's declarations:
//
//	law XRoundTrips(b B) {
//		let x = ParseX(b) catch {
//		case _:
//			return true
//		}
//		try ParseX(x.Wire()) == x
//	}
//
// The dot form names X's own Wire when the package declares several (196m
// rule 3). A ParseX that fails nothing gets the law without catch and try;
// a ParseX with a needs clause derives none, since a law has no provider
// for it.
//
// A value of X is drawn as the codec meets it, through ParseX from a drawn
// B, so the law needs no generator under X's authority, and a ParseX that
// normalises its input (lower case, trimmed) still satisfies it. When the
// file declares X open (openType: a record of exported fields, or a union
// of such records), any value is a valid X, so the law draws x itself and
// reaches every value: law XRoundTrips(x X) { try ParseX(x.Wire()) == x }.
// The law
// is where a hand transformation can break the round trip that a derived
// codec has by construction. Like a refinement's law it is an ordinary
// declaration that Derived marks and the formatter does not print.
//
// Prior art: Haskell's aeson and Rust's serde leave the round trip of a
// hand instance to the user's tests; Effect Schema's transform is a pair of
// functions with no stated law. Ergo states it, and the law runner runs it.

// RoundTripLawName is the name of the law a pair for the type name derives:
// HostRoundTrips for Host.
func RoundTripLawName(name string) string { return name + "RoundTrips" }

// derivePairs appends to f the round-trip law of each pair f declares.
func (p *parser) derivePairs(f *File) {
	parses := map[string]*FuncDecl{}
	types := map[string]*TypeDecl{}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *FuncDecl:
			if d.Derived == nil {
				parses[d.Name.Name] = d
			}
		case *TypeDecl:
			types[d.Name.Name] = d
		}
	}
	var laws []Decl
	for _, d := range f.Decls {
		wire, ok := d.(*FuncDecl)
		if !ok || wire.Name.Name != "Wire" {
			continue
		}
		x, b, ok := wireHalf(wire)
		if !ok {
			continue
		}
		parse := parses[ParseName(x.Name)]
		if parse == nil || !p.parseHalf(parse, x.Name, b) {
			continue
		}
		laws = append(laws, p.pairLaw(wire, x, b, openType(types, x.Name, 0), parseFails(parse)))
	}
	f.Decls = append(f.Decls, laws...)
}

// openType reports a type the file declares that any package may build
// field by field: a record whose fields are all exported, or a union whose
// variants are such records of the file. The law runner draws a value of
// it directly, so its law reaches every value, not only those a drawn B
// parses to. A type built only by its package (an unexported field) or
// declared in another file is drawn through ParseX instead.
func openType(types map[string]*TypeDecl, name string, depth int) bool {
	d := types[name]
	if d == nil || d.TypeParams != nil || d.Where != nil || d.Extern != nil || depth > 8 {
		return false
	}
	switch s := d.Spec.(type) {
	case *StructType:
		return openFields(s.Fields)
	case *InterfaceType:
		if len(s.Union) == 0 {
			return len(s.Methods) == 0 && len(s.Laws) == 0
		}
		for _, v := range s.Union {
			switch v := v.(type) {
			case *Ident:
				if !openType(types, v.Name, depth+1) {
					return false
				}
			case *VariantSpec:
				// An inline variant is a record of the file, as a named
				// one is (types.md Codecs 7).
				if v.TypeArgs != nil || !openFields(v.Fields) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	return false
}

// openFields reports a record's fields that any package may write: each
// named and exported.
func openFields(fields *FieldList) bool {
	if fields == nil {
		return false
	}
	for _, f := range fields.List {
		for _, n := range f.Names {
			if !isExportedName(n.Name) {
				return false
			}
		}
		if len(f.Names) == 0 {
			return false
		}
	}
	return true
}

// isExportedName reports a name that starts with an upper-case letter.
func isExportedName(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

// wireHalf reads Wire(x X) B: the named type X and B's type expression.
func wireHalf(d *FuncDecl) (*Ident, Expr, bool) {
	t := d.Type
	if d.Extern != nil || d.Recv != nil || d.TypeParams != nil || d.Body == nil || t == nil || t.Needs != nil {
		return nil, nil, false
	}
	if t.Params == nil || len(t.Params.List) != 1 || len(t.Params.List[0].Names) > 1 {
		return nil, nil, false
	}
	x, ok := t.Params.List[0].Type.(*Ident)
	if !ok || t.Results == nil || len(t.Results.List) != 1 {
		return nil, nil, false
	}
	b := t.Results.List[0].Type
	if _, isRow := b.(*ErrorRow); isRow || b == nil {
		return nil, nil, false
	}
	return x, b, true
}

// parseHalf reports whether d is ParseX(b B) X: one parameter of B's text
// and a result X.
func (p *parser) parseHalf(d *FuncDecl, x string, b Expr) bool {
	t := d.Type
	if d.Extern != nil || d.Recv != nil || d.TypeParams != nil || t == nil || t.Needs != nil || t.Params == nil || len(t.Params.List) != 1 || len(t.Params.List[0].Names) > 1 {
		return false
	}
	if t.Results == nil || len(t.Results.List) == 0 {
		return false
	}
	if r, ok := t.Results.List[0].Type.(*Ident); !ok || r.Name != x {
		return false
	}
	pb := t.Params.List[0].Type
	return pb != nil && p.src[pb.Pos():pb.End()] == p.src[b.Pos():b.End()]
}

// parseFails reports whether ParseX writes a fails row.
func parseFails(d *FuncDecl) bool {
	for _, r := range d.Type.Results.List {
		if _, ok := r.Type.(*ErrorRow); ok {
			return true
		}
	}
	return false
}

// pairLaw builds the round-trip law of the pair whose Wire is wire: over
// a drawn x of an open type, else over a drawn b through ParseX. It calls
// Wire in the dot form, x.Wire(), which names X's own Wire when the
// package declares several (196m rule 3), and it writes catch and try
// only when ParseX fails.
func (p *parser) pairLaw(wire *FuncDecl, x *Ident, b Expr, open, fails bool) *LawDecl {
	at := wire.Func
	id := func(name string) *Ident { return &Ident{NamePos: at, Name: name} }
	call := func(fun Expr, args ...Expr) *CallExpr {
		return &CallExpr{Fun: fun, Lparen: at, Args: args, Rparen: at}
	}
	parse := ParseName(x.Name)
	var held Expr = &BinaryExpr{X: call(id(parse), call(&SelectorExpr{X: id("x"), Sel: id("Wire")})), OpPos: at, Op: EQL, Y: id("x")}
	if fails {
		held = &TryExpr{Try: at, X: held}
	}
	if open {
		return &LawDecl{
			Law:    at,
			Name:   &Ident{NamePos: wire.Name.NamePos, Name: RoundTripLawName(x.Name)},
			Params: &FieldList{Opening: at, List: []*Field{{Names: []*Ident{id("x")}, Type: id(x.Name), WherePos: NoPos}}, Closing: at},
			Body:   &BlockStmt{Lbrace: at, List: []Stmt{&ExprStmt{X: held}}, Rbrace: at},
			Pair:   wire,
		}
	}
	var drawn Expr = call(id(parse), id("b"))
	if fails {
		drawn = &CatchExpr{X: drawn, Catch: at, Lbrace: at, Arms: []*CatchArm{{
			Case: at, Types: []Expr{id("_")}, Colon: at,
			Body: []Stmt{&ReturnStmt{Return: at, Results: []Expr{id("true")}}},
		}}, Rbrace: at}
	}
	let := &DeclStmt{Decl: &VarDecl{Var: at, Mut: NoPos, Specs: []*VarSpec{{Names: []*Ident{id("x")}, Assign: at, Values: []Expr{drawn}}}}}
	return &LawDecl{
		Law:    at,
		Name:   &Ident{NamePos: wire.Name.NamePos, Name: RoundTripLawName(x.Name)},
		Params: &FieldList{Opening: at, List: []*Field{{Names: []*Ident{id("b")}, Type: p.again(b), WherePos: NoPos}}, Closing: at},
		Body:   &BlockStmt{Lbrace: at, List: []Stmt{let, &ExprStmt{X: held}}, Rbrace: at},
		Pair:   wire,
	}
}

// again parses the type expression t once more from its own tokens, so
// each node stands once in the file.
func (p *parser) again(t Expr) Expr {
	for k, tok := range p.toks {
		if tok.pos == t.Pos() {
			saved := p.i
			p.backtrack(k)
			out := p.parseType(ctxDecl)
			p.backtrack(saved)
			return out
		}
	}
	return t
}
