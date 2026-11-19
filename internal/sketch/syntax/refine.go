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
		return d.Derived != nil || d.Pair != nil
	case *TypeDecl:
		return d.Origin != nil
	}
	return false
}

// tokenText is the source of the tokens i..j on one line, without
// comments: one space between tokens, none inside parentheses or after a
// unary operator, so a comment or a line break never changes the text.
func tokenText(src string, toks []token, i, j int) string {
	var b strings.Builder
	prev := ILLEGAL
	for k := i; k < j; k++ {
		t := toks[k]
		if t.tok == SEMICOLON || t.end <= t.pos {
			continue
		}
		tight := prev == LPAREN || t.tok == RPAREN || prev == NOT || (prev == SUB || prev == ADD) && unaryAt(toks, k-1, i)
		if prev != ILLEGAL && !tight {
			b.WriteByte(' ')
		}
		b.WriteString(src[t.pos:t.end])
		prev = t.tok
	}
	return b.String()
}

// WhereText is the predicate x of the source src as one line, as the
// derived parse function's failure states it (tokenText).
func WhereText(src string, x Expr) string {
	sub := src[x.Pos():x.End()]
	toks := scanTokens(sub)
	return tokenText(sub, toks, 0, len(toks))
}

// unaryAt reports whether the + or - token k, of a predicate starting at
// token i, is a sign: it starts the predicate or follows an operator or (.
func unaryAt(toks []token, k, i int) bool {
	if k == i {
		return true
	}
	before := toks[k-1].tok
	return before == LPAREN || before == NOT || isBinaryOp(before)
}

func isBinaryOp(t Token) bool {
	switch t {
	case LAND, LOR, EQL, NEQ, LSS, LEQ, GTR, GEQ, ADD, SUB, MUL, QUO, REM:
		return true
	}
	return false
}

// ConstructorName is the name of the constructor of the type name, the
// function a generator builds its values with (runtime.md, Tests 6):
// NewHost for Host, newHost for host, unexported as the type is and named
// as ParseName names its parse (GX12b; it was New+name, so Newhost).
func ConstructorName(name string) string {
	r, n := utf8.DecodeRuneInString(name)
	if unicode.IsUpper(r) {
		return "New" + name
	}
	return "new" + string(unicode.ToUpper(r)) + name[n:]
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
