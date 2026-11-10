package syntax

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Refinements (types.md, Refinements). `type Port int where 1 <= it && it
// <= 65535` declares a defined type and, after it in File.Decls, the two
// declarations the parser derives from it:
//
//	func ParsePort(it int) Port fails Unsatisfied {
//		if 1 <= it && it <= 65535 { Port(it) } else { Unsatisfied{Type: "Port", Where: "1 <= it && it <= 65535"} }
//	}
//	law PortWhere(v Port) {
//		let it = int(v)
//		1 <= it && it <= 65535 && try ParsePort(it) == v
//	}
//
// They are ordinary declarations, so the checker, the lowering and the law
// runner treat them as hand-written ones; Derived marks them, and the
// formatter does not print them. Their nodes are parsed again from the
// type's own tokens, so each node stands once in the file and a position
// in them is a position of the source.

// IsDerived reports a declaration the parser derived from a refinement.
func IsDerived(d Decl) bool {
	switch d := d.(type) {
	case *FuncDecl:
		return d.Derived != nil
	case *LawDecl:
		return d.Derived != nil
	}
	return false
}

// atWhere reports the contextual word `where`, which is no keyword: a
// name `where` stays a name everywhere else.
func (p *parser) atWhere() bool { return p.tok.tok == IDENT && p.tok.lit == "where" }

// parseRefinement parses `where P` after the type of d, which starts at the
// token typeStart, and derives the parse function and the law into
// p.derived. A generic type derives nothing: the checker refuses its where.
func (p *parser) parseRefinement(d *TypeDecl, typeStart int) {
	d.WherePos = p.tok.pos
	p.next()
	predStart := p.i
	d.Where = p.parseExpr()
	if d.TypeParams != nil {
		return
	}
	again := func(at int, parse func() Expr) Expr {
		saved := p.i
		p.backtrack(at)
		x := parse()
		p.backtrack(saved)
		return x
	}
	base := func() Expr { return again(typeStart, func() Expr { return p.parseType(ctxDecl) }) }
	pred := func() Expr { return again(predStart, p.parseExpr) }
	text := strings.Join(strings.Fields(p.src[d.Where.Pos():d.Where.End()]), " ")
	p.derived = deriveRefinement(d, base, pred, text)
}

// ParseName is the name of the parse function a refinement of the type
// name derives: ParsePort for Port, parsePort for port.
func ParseName(name string) string {
	r, n := utf8.DecodeRuneInString(name)
	if unicode.IsUpper(r) {
		return "Parse" + name
	}
	return "parse" + string(unicode.ToUpper(r)) + name[n:]
}

// WhereLawName is the name of the law a refinement of the type name
// derives: PortWhere for Port.
func WhereLawName(name string) string { return name + "Where" }

// deriveRefinement builds the parse function and the law of the refined
// type d (see above). base and pred parse a fresh copy of the base type and
// of the predicate; text is the predicate's source on one line.
func deriveRefinement(d *TypeDecl, base, pred func() Expr, text string) []Decl {
	at := d.WherePos
	id := func(name string) *Ident { return &Ident{NamePos: at, Name: name} }
	self := func() *Ident { return &Ident{NamePos: d.Name.NamePos, Name: d.Name.Name} }
	str := func(s string) *BasicLit {
		return &BasicLit{ValuePos: at, Kind: STRING, Value: strconv.Quote(s), ValueEnd: at}
	}
	if strings.ContainsAny(text, "\\\"") {
		text = "the where of " + d.Name.Name
	}
	block := func(list ...Stmt) *BlockStmt { return &BlockStmt{Lbrace: at, List: list, Rbrace: at} }
	params := func(name string, t Expr) *FieldList {
		return &FieldList{Opening: at, List: []*Field{{Names: []*Ident{id(name)}, Type: t, WherePos: NoPos}}, Closing: at}
	}
	call := func(fun Expr, args ...Expr) *CallExpr {
		return &CallExpr{Fun: fun, Lparen: at, Args: args, Rparen: at}
	}
	parseName := ParseName(d.Name.Name)
	failure := &CompositeLit{Type: id("Unsatisfied"), Lbrace: at, DotDot: NoPos, Elts: []Expr{
		&KeyValueExpr{Key: id("Type"), Colon: at, Value: str(d.Name.Name)},
		&KeyValueExpr{Key: id("Where"), Colon: at, Value: str(text)},
	}, Rbrace: at}
	fn := &FuncDecl{
		Func: at,
		Name: &Ident{NamePos: d.Name.NamePos, Name: parseName},
		Type: &FuncType{Escaping: NoPos, Func: NoPos, Params: params("it", base()), Results: &FieldList{Opening: NoPos, List: []*Field{
			{Type: self(), WherePos: NoPos},
			{Type: &ErrorRow{Error: at, Types: []Expr{id("Unsatisfied")}}, WherePos: NoPos},
		}, Closing: NoPos}},
		Body: block(&IfStmt{
			If:      at,
			Cond:    pred(),
			Body:    block(&ExprStmt{X: call(self(), id("it"))}),
			ElsePos: at,
			Else:    block(&ExprStmt{X: failure}),
		}),
		Derived: d,
	}
	// The law reads v as its base: a conversion when the base has a name,
	// else an assignment (types.md, Conversions 2: a base with no name has
	// none to call).
	spec := &VarSpec{Names: []*Ident{id("it")}, Assign: at}
	switch b := base(); b.(type) {
	case *Ident, *SelectorExpr:
		spec.Values = []Expr{call(b, id("v"))}
	default:
		spec.Type, spec.Values = b, []Expr{id("v")}
	}
	held := &BinaryExpr{X: &ParenExpr{Lparen: at, X: pred(), Rparen: at}, OpPos: at, Op: LAND, Y: &TryExpr{Try: at, X: &BinaryExpr{
		X: call(id(parseName), id("it")), OpPos: at, Op: EQL, Y: id("v"),
	}}}
	law := &LawDecl{
		Law:     at,
		Name:    &Ident{NamePos: d.Name.NamePos, Name: WhereLawName(d.Name.Name)},
		Params:  params("v", self()),
		Body:    block(&DeclStmt{Decl: &VarDecl{Var: at, Mut: NoPos, Specs: []*VarSpec{spec}}}, &ExprStmt{X: held}),
		Derived: d,
	}
	return []Decl{fn, law}
}
