package syntax

// By-name codec pairs (types.md, Codecs 1, 6 and 7; L11 design item 7).
// A file that declares both halves of a pair,
//
//	func ParseX(b B) X fails …
//	func Wire(x X) B
//
// with Wire failing nothing and neither half needing anything, gets the
// law the parser derives after the file's declarations:
//
//	law XRoundTrips(x X) { try ParseX(x.Wire()) == x }
//
// The append form (GX10, GX11), ParseX(b []byte) X with Wire(x X, into
// []byte) []byte, derives it with an empty buffer: try
// ParseX(x.Wire([]byte{})) == x. Its halves are symmetric in []byte, as
// Go's TextAppender and TextUnmarshaler are.
//
// The dot form names X's own Wire when the package declares several (196m
// rule 3). A ParseX that fails nothing gets the law without try; a ParseX
// with a needs clause derives none, since a law has no provider for it.
//
// The law draws x from X's own generator, the values X's package makes:
// its constructor NewX, or any value of a record or union that every
// package may build. It never draws B, whose values ParseX almost always
// refuses, so a law over them reads as coverage and tests nothing (GX11,
// orchestrator). The checker decides whether X has such a generator, since
// NewX and X may be in other files; a pair whose X has none is a law that
// does not run, and the test build reports it with its reason
// (check/laws.go, pairUngeneratable). The law is where a hand
// transformation can break the round trip that a derived codec has by
// construction. Like a refinement's law it is an ordinary declaration that
// Derived marks and the formatter does not print.
//
// Prior art: Haskell's aeson and Rust's serde leave the round trip of a
// hand instance to the user's tests; Effect Schema's transform is a pair of
// functions with no stated law, and its arbitrary draws the decoded side;
// Gleam's qcheck builds an opaque type through its module's functions.
// Ergo states the law, and the law runner runs it.

// RoundTripLawName is the name of the law a pair for the type name derives:
// HostRoundTrips for Host.
func RoundTripLawName(name string) string { return name + "RoundTrips" }

// derivePairs appends to f the round-trip law of each pair f declares.
func derivePairs(f *File, src string) {
	parses := map[string]*FuncDecl{}
	for _, d := range f.Decls {
		if d, ok := d.(*FuncDecl); ok && d.Derived == nil {
			parses[d.Name.Name] = d
		}
	}
	var laws []Decl
	for _, d := range f.Decls {
		wire, ok := d.(*FuncDecl)
		if !ok || wire.Name.Name != "Wire" {
			continue
		}
		x, b, appends, ok := wireHalf(src, wire)
		if !ok {
			continue
		}
		parse := parses[ParseName(x.Name)]
		if parse == nil || !parseHalf(src, parse, x.Name, b) {
			continue
		}
		laws = append(laws, pairLaw(wire, x, appends, parseFails(parse)))
	}
	f.Decls = append(f.Decls, laws...)
}

// wireHalf reads Wire(x X) B, or the append form Wire(x X, into []byte)
// []byte (appends): the named type X and B's type expression.
func wireHalf(src string, d *FuncDecl) (x *Ident, b Expr, appends, ok bool) {
	t := d.Type
	if d.Extern != nil || d.Recv != nil || d.TypeParams != nil || d.Body == nil || t == nil || t.Needs != nil || t.Params == nil {
		return nil, nil, false, false
	}
	params := t.Params.List
	if len(params) == 2 && len(params[0].Names) == 1 && len(params[1].Names) == 1 {
		appends = true
	} else if len(params) != 1 || len(params[0].Names) > 1 {
		return nil, nil, false, false
	}
	x, ok = params[0].Type.(*Ident)
	if !ok || t.Results == nil || len(t.Results.List) != 1 {
		return nil, nil, false, false
	}
	b = t.Results.List[0].Type
	if _, isRow := b.(*ErrorRow); isRow || b == nil {
		return nil, nil, false, false
	}
	if appends && (!byteSlice(src, params[1].Type) || !byteSlice(src, b)) {
		return nil, nil, false, false
	}
	return x, b, appends, true
}

// byteSlice reports the type expression []byte.
func byteSlice(src string, t Expr) bool {
	a, ok := t.(*ArrayType)
	if !ok || a.Len != "" {
		return false
	}
	e, ok := a.Elt.(*Ident)
	return ok && e.Name == "byte"
}

// parseHalf reports whether d is ParseX(b B) X: one parameter of B's text
// and a result X.
func parseHalf(src string, d *FuncDecl, x string, b Expr) bool {
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
	return pb != nil && src[pb.Pos():pb.End()] == src[b.Pos():b.End()]
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

// pairLaw builds the round-trip law of the pair whose Wire is wire, over a
// drawn x. It calls Wire in the dot form, x.Wire(), which names X's own
// Wire when the package declares several (196m rule 3), with an empty
// buffer for the append form (x.Wire([]byte{})), and it writes try only
// when ParseX fails.
func pairLaw(wire *FuncDecl, x *Ident, appends, fails bool) *LawDecl {
	at := wire.Func
	id := func(name string) *Ident { return &Ident{NamePos: at, Name: name} }
	call := func(fun Expr, args ...Expr) *CallExpr {
		return &CallExpr{Fun: fun, Lparen: at, Args: args, Rparen: at}
	}
	var into []Expr
	if appends {
		into = []Expr{&CompositeLit{Type: &ArrayType{Lbrack: at, Elt: id("byte")}, Lbrace: at, Rbrace: at}}
	}
	var held Expr = &BinaryExpr{X: call(id(ParseName(x.Name)), call(&SelectorExpr{X: id("x"), Sel: id("Wire")}, into...)), OpPos: at, Op: EQL, Y: id("x")}
	if fails {
		held = &TryExpr{Try: at, X: held}
	}
	return &LawDecl{
		Law:    at,
		Name:   &Ident{NamePos: wire.Name.NamePos, Name: RoundTripLawName(x.Name)},
		Params: &FieldList{Opening: at, List: []*Field{{Names: []*Ident{id("x")}, Type: id(x.Name), WherePos: NoPos}}, Closing: at},
		Body:   &BlockStmt{Lbrace: at, List: []Stmt{&ExprStmt{X: held}}, Rbrace: at},
		Pair:   wire,
	}
}
