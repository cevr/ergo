package oldsyntax

import (
	"fmt"
	"sort"
	"strconv"
)

// MaxNesting bounds the height, in levels, of the returned tree: the
// nesting of expressions, types, statements and blocks in any composition
// (grammar.md, Parser rule 6). A higher tree is S001 at the first token past
// the limit, so no input exhausts the stack of the
// parser or of any consumer of the AST (the printer, the converter, the
// checker). go/parser bounds nesting at 100 000; 1 000 leaves every
// consumer a small stack and is far above any program written by hand.
const MaxNesting = 1000

// Parse parses one source file of the 2026 edition. It returns the first
// syntax error, an *Error with code S001, and no file; recovery is the language server's.
func Parse(src string) (file *File, err error) {
	s := scan(src)
	p := &parser{s: s, src: src, toks: s.toks}
	p.groupComments()
	p.tok = p.toks[0]
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(bailout); !ok {
				panic(r)
			}
			file, err = nil, p.err
		}
	}()
	file = p.parseFile()
	file.Src = src
	file.lines = s.lines
	file.Comments = p.groups
	return file, nil
}

type bailout struct{}

type parser struct {
	s    *scanner
	src  string
	toks []token
	i    int
	tok  token

	// exprLev is below zero in the header of if, for and switch, where a
	// `{` after a type name or a call opens the body, never a composite
	// literal or a trailing block (Go's rule); it is above zero inside
	// parentheses and brackets.
	exprLev int
	// nest is the number of levels open on the path being parsed: each
	// enter() opens one. peak is the deepest level reached by the subtree
	// being measured (see measure); a chain retains its own height in it,
	// so a composed tree's height is checked, not only the active path.
	nest int
	peak int
	err  *Error

	// guards are the TypeGuards parsed so far; a switch header checks that
	// its own are all subjects.
	guards []*TypeGuard
	// guardOK admits `x.(type)` while a switch header is parsed.
	guardOK bool

	groups     []*CommentGroup
	groupOwn   []bool // the group starts on a line with no token before it
	groupStart []int  // token index of the first token after the group
}

// ---------------------------------------------------------------- tokens and errors

func (p *parser) next() {
	if p.tok.tok != EOF && p.tok.tok != ILLEGAL {
		p.i++
		p.tok = p.toks[p.i]
	}
}

func (p *parser) peek(n int) token {
	if p.i+n < len(p.toks) {
		return p.toks[p.i+n]
	}
	return p.toks[len(p.toks)-1]
}

func (p *parser) errorAt(pos Pos, length int, format string, args ...any) {
	if p.s.err != nil && pos >= Pos(p.s.err.Offset) {
		p.err = p.s.err
	} else {
		p.err = newError(p.src, p.s.lines, pos, length, fmt.Sprintf(format, args...))
	}
	panic(bailout{})
}

// errorf reports an error at the current token.
func (p *parser) errorf(format string, args ...any) {
	p.errorAt(p.tok.pos, int(p.tok.end-p.tok.pos), format, args...)
}

// found describes the current token for a message.
func (p *parser) found() string {
	t := p.tok
	switch t.tok {
	case SEMICOLON:
		if t.lit == "\n" {
			return "newline"
		}
		return `";"`
	case EOF:
		return "end of file"
	case IDENT, INT, DURATION, STRING, RUNE:
		return strconv.Quote(t.lit)
	}
	return strconv.Quote(t.tok.String())
}

func (p *parser) want(tok Token) Pos {
	if p.tok.tok != tok {
		p.errorf("expected %q, found %s", tok.String(), p.found())
	}
	pos := p.tok.pos
	p.next()
	return pos
}

func (p *parser) got(tok Token) bool {
	if p.tok.tok == tok {
		p.next()
		return true
	}
	return false
}

// enter opens one level on the path being parsed; past MaxNesting it is
// S001 at the current token.
func (p *parser) enter() {
	p.nest++
	if p.nest > p.peak {
		p.peak = p.nest
	}
	if p.nest > MaxNesting {
		p.tooDeep()
	}
}

func (p *parser) leave() { p.nest-- }

func (p *parser) tooDeep() {
	p.errorf("nesting deeper than %d levels", MaxNesting)
}

// The binary and postfix loops build a left spine: each operator or link
// wraps the tree built so far, so the tree is taller than the path the
// parser has open. Each loop retains its tree's height h in levels above
// its base o = p.nest, checks o+h+1 before it adds a level, and reports
// o+h through p.peak, where an enclosing loop measures its operand.
//
// measure starts measuring the subtree parsed next from the current level
// and returns the peak to restore; height returns the subtree's height and
// restores the enclosing measurement.
func (p *parser) measure() (saved int) {
	saved, p.peak = p.peak, p.nest
	return saved
}

func (p *parser) height(saved int) int {
	h := p.peak - p.nest
	if saved > p.peak {
		p.peak = saved
	}
	return h
}

// reach records that the tree under the current loop reaches level top.
func (p *parser) reach(top int) {
	if top > p.peak {
		p.peak = top
	}
}

// endStmt consumes the end of a statement, declaration or entry: a
// semicolon, or nothing before closer.
func (p *parser) endStmt(closer Token, what string) {
	switch p.tok.tok {
	case SEMICOLON:
		p.next()
	case closer:
	default:
		p.errorf("expected end of %s, found %s", what, p.found())
	}
}

// ---------------------------------------------------------------- comments

// groupComments forms comment groups as go/parser does: comments with no
// token and no blank line between them. A comment on the line of a token
// before it starts its own (trailing) group.
func (p *parser) groupComments() {
	comments := p.s.comments
	if len(comments) == 0 {
		return
	}
	lines := p.s.lines
	line := func(pos Pos) int { l, _ := lineColumn(lines, int(pos)); return l }
	// lastTokenBefore finds, for each comment, the end of the last real token
	// before it (inserted semicolons are not source tokens).
	ti := 0
	lastEnd := Pos(-1)
	nextToken := func(pos Pos) {
		for ti < len(p.toks) && p.toks[ti].pos < pos {
			t := p.toks[ti]
			if !(t.tok == SEMICOLON && t.lit == "\n") && t.tok != EOF {
				lastEnd = t.end
			}
			ti++
		}
	}
	var current *CommentGroup
	currentOwn := false
	var currentLast *Comment
	currentTokens := -1
	for _, c := range comments {
		nextToken(c.Slash)
		own := lastEnd < 0 || line(lastEnd) < line(c.Slash)
		if current != nil && currentTokens == ti && line(c.Slash) <= line(currentLast.EndPos)+1 && (currentOwn || line(c.Slash) == line(currentLast.EndPos)) {
			current.List = append(current.List, c)
			currentLast = c
			continue
		}
		current = &CommentGroup{List: []*Comment{c}}
		currentOwn = own
		currentLast = c
		currentTokens = ti
		p.groups = append(p.groups, current)
		p.groupOwn = append(p.groupOwn, own)
	}
}

// leadComment returns the comment group that documents a node starting at
// the current token: an own-line group that ends on the line just above it,
// with no token in between.
func (p *parser) leadComment() *CommentGroup {
	pos := p.tok.pos
	i := sort.Search(len(p.groups), func(i int) bool { return p.groups[i].End() > pos }) - 1
	if i < 0 || !p.groupOwn[i] {
		return nil
	}
	g := p.groups[i]
	line := func(pos Pos) int { l, _ := lineColumn(p.s.lines, int(pos)); return l }
	if line(g.End()) != line(pos)-1 {
		return nil
	}
	for j := p.i - 1; j >= 0; j-- {
		t := p.toks[j]
		if t.tok == SEMICOLON && t.lit == "\n" {
			continue
		}
		if t.end > g.Pos() {
			return nil
		}
		break
	}
	return g
}

// ---------------------------------------------------------------- names

// parseIdent parses a name. The removed Go keywords are never names.
func (p *parser) parseIdent() *Ident {
	if p.tok.tok != IDENT {
		p.errorf("expected a name, found %s", p.found())
	}
	if msg, ok := removed[p.tok.lit]; ok {
		p.errorf("%s", msg)
	}
	id := &Ident{NamePos: p.tok.pos, Name: p.tok.lit}
	p.next()
	return id
}

// parseQualifiedName parses `name`, `pkg.name` or `go.name`.
func (p *parser) parseQualifiedName() Expr {
	var x Expr
	if p.tok.tok == GO && p.peek(1).tok == PERIOD {
		x = &Ident{NamePos: p.tok.pos, Name: "go"}
		p.next()
	} else {
		x = p.parseIdent()
	}
	if p.got(PERIOD) {
		x = &SelectorExpr{X: x, Sel: p.parseIdent()}
	}
	return x
}

func (p *parser) parseIdentList() []*Ident {
	list := []*Ident{p.parseIdent()}
	for p.got(COMMA) {
		list = append(list, p.parseIdent())
	}
	return list
}

// ---------------------------------------------------------------- file and declarations

func (p *parser) parseFile() *File {
	f := &File{}
	if p.tok.tok != PACKAGE {
		p.errorf("expected \"package\", found %s", p.found())
	}
	f.Package = p.tok.pos
	p.next()
	f.Name = p.parseIdent()
	p.endDecl()
	for p.tok.tok == IMPORT {
		f.Decls = append(f.Decls, p.parseImportDecl())
		p.endDecl()
	}
	for p.tok.tok != EOF {
		doc := p.leadComment()
		var d Decl
		switch p.tok.tok {
		case TYPE:
			d = p.parseTypeDecl(doc)
		case FUNC:
			d = p.parseFuncDecl(doc)
		case VAR:
			d = p.parseVarDecl(doc)
		case WIRE:
			d = p.parseWireDecl(doc)
		case IMPORT:
			p.errorf("an import must come before every other declaration")
		case IDENT:
			if msg, ok := removed[p.tok.lit]; ok {
				p.errorf("%s", msg)
			}
			fallthrough
		default:
			p.errorf("expected a declaration (type, func, var or wire), found %s", p.found())
		}
		f.Decls = append(f.Decls, d)
		p.endDecl()
	}
	f.EOF = p.tok.pos
	return f
}

func (p *parser) endDecl() {
	switch p.tok.tok {
	case SEMICOLON:
		p.next()
	case EOF:
	default:
		p.errorf("expected end of declaration, found %s", p.found())
	}
}

func (p *parser) parseImportDecl() *ImportDecl {
	d := &ImportDecl{Doc: p.leadComment(), Import: p.want(IMPORT), Lparen: NoPos, Rparen: NoPos}
	if p.tok.tok == LPAREN {
		d.Lparen = p.tok.pos
		p.next()
		for p.tok.tok != RPAREN {
			d.Specs = append(d.Specs, p.parseImportSpec())
			p.endStmt(RPAREN, "import")
		}
		d.Rparen = p.want(RPAREN)
		return d
	}
	d.Specs = []*ImportSpec{p.parseImportSpec()}
	return d
}

func (p *parser) parseImportSpec() *ImportSpec {
	s := &ImportSpec{Doc: p.leadComment(), NamePos: NoPos}
	switch p.tok.tok {
	case PERIOD:
		s.Kind, s.NamePos = ImportDot, p.tok.pos
		p.next()
	case GO:
		s.Kind, s.NamePos = ImportGo, p.tok.pos
		p.next()
	case IDENT:
		s.Kind, s.NamePos = ImportNamed, p.tok.pos
		s.Name = p.parseIdent()
	}
	if p.tok.tok != STRING {
		p.errorf("expected an import path, found %s", p.found())
	}
	s.Path = p.basicLit()
	return s
}

func (p *parser) basicLit() *BasicLit {
	x := &BasicLit{ValuePos: p.tok.pos, Kind: p.tok.tok, Value: p.tok.lit, ValueEnd: p.tok.end}
	p.next()
	return x
}

func (p *parser) parseVarDecl(doc *CommentGroup) *VarDecl {
	d := &VarDecl{Doc: doc, Var: p.want(VAR), Lparen: NoPos, Rparen: NoPos}
	if p.tok.tok == LPAREN {
		d.Lparen = p.tok.pos
		p.next()
		for p.tok.tok != RPAREN {
			d.Specs = append(d.Specs, p.parseVarSpec())
			p.endStmt(RPAREN, "var specification")
		}
		d.Rparen = p.want(RPAREN)
		return d
	}
	d.Specs = []*VarSpec{p.parseVarSpec()}
	return d
}

func (p *parser) parseVarSpec() *VarSpec {
	s := &VarSpec{Doc: p.leadComment(), Names: p.parseIdentList(), Assign: NoPos}
	if p.tok.tok != ASSIGN {
		s.Type = p.parseType(false)
	}
	if p.tok.tok == ASSIGN {
		s.Assign = p.tok.pos
		p.next()
		s.Values = p.parseExprList()
	}
	return s
}

func (p *parser) parseTypeDecl(doc *CommentGroup) *TypeDecl {
	d := &TypeDecl{Doc: doc, Type: p.want(TYPE)}
	d.Name = p.parseIdent()
	if p.tok.tok == LBRACK {
		d.TypeParams = p.parseTypeParams()
	}
	switch p.tok.tok {
	case STRUCT:
		d.Spec = p.parseStructType()
	case INTERFACE:
		d.Spec = p.parseInterfaceType()
	case MACHINE:
		d.Spec = p.parseMachineType()
	case ASSIGN:
		p.errorf("type aliases are not part of the language")
	default:
		p.errorf("a type declaration declares a struct, an interface or a machine, found %s", p.found())
	}
	return d
}

func (p *parser) parseTypeParams() *FieldList {
	if p.peek(1).tok == RBRACK {
		p.errorAt(p.tok.pos, 2, "empty type parameter list")
	}
	list := p.parseParameters(LBRACK, RBRACK, paramType)
	return list
}

func (p *parser) parseStructType() *StructType {
	t := &StructType{Struct: p.want(STRUCT)}
	fields := &FieldList{Opening: p.want(LBRACE)}
	saved := p.exprLev
	p.exprLev = 0
	for p.tok.tok != RBRACE {
		doc := p.leadComment()
		names := p.parseIdentList()
		if p.tok.tok == SEMICOLON || p.tok.tok == RBRACE {
			p.errorf("a struct field needs a name and a type, found %s", p.found())
		}
		fields.List = append(fields.List, &Field{Doc: doc, Names: names, Type: p.parseType(false)})
		p.endStmt(RBRACE, "struct field")
	}
	p.exprLev = saved
	fields.Closing = p.want(RBRACE)
	t.Fields = fields
	return t
}

func (p *parser) parseInterfaceType() *InterfaceType {
	t := &InterfaceType{Interface: p.want(INTERFACE)}
	t.Lbrace = p.want(LBRACE)
	if p.tok.tok == IDENT && p.peek(1).tok == LPAREN {
		// A capability: method signatures.
		for p.tok.tok != RBRACE {
			doc := p.leadComment()
			name := p.parseIdent()
			if p.tok.tok != LPAREN {
				p.errorf("expected a method signature, found %s", p.found())
			}
			sig := p.parseSignature(NoPos, false)
			t.Methods = append(t.Methods, &Field{Doc: doc, Names: []*Ident{name}, Type: sig})
			p.endStmt(RBRACE, "method")
		}
	} else if p.tok.tok != RBRACE {
		// A union: one line of variants.
		t.Union = append(t.Union, p.parseTypeName("a union variant"))
		for p.tok.tok == OR {
			p.next()
			if p.tok.tok == RBRACE || p.tok.tok == SEMICOLON {
				p.errorf("expected a type after \"|\", found %s", p.found())
			}
			t.Union = append(t.Union, p.parseTypeName("a union variant"))
		}
		p.endStmt(RBRACE, "union")
		if p.tok.tok != RBRACE {
			p.errorf("a union is one list of types joined by \"|\"; found %s", p.found())
		}
	}
	t.Rbrace = p.want(RBRACE)
	return t
}

func (p *parser) parseMachineType() *MachineType {
	t := &MachineType{Machine: p.want(MACHINE)}
	t.Lbrack = p.want(LBRACK)
	t.State = p.parseType(true)
	p.want(COMMA)
	t.Signal = p.parseType(true)
	t.Rbrack = p.want(RBRACK)
	t.Lbrace = p.want(LBRACE)
	saved := p.exprLev
	p.exprLev = 0
	for p.tok.tok != RBRACE {
		doc := p.leadComment()
		if p.tok.tok != IDENT || p.tok.lit != "initial" && p.tok.lit != "step" {
			p.errorf("a machine has the entries initial and step, found %s", p.found())
		}
		e := &MachineEntry{Doc: doc, NamePos: p.tok.pos, Name: p.tok.lit}
		if e.Name == "initial" && t.Initial != nil || e.Name == "step" && t.Step != nil {
			p.errorf("duplicate machine entry %s", e.Name)
		}
		p.next()
		e.Value = p.parseExpr()
		if e.Name == "initial" {
			t.Initial = e.Value
		} else {
			t.Step = e.Value
		}
		t.Entries = append(t.Entries, e)
		p.endStmt(RBRACE, "machine entry")
	}
	p.exprLev = saved
	if t.Initial == nil {
		p.errorf("a machine needs an initial entry")
	}
	if t.Step == nil {
		p.errorf("a machine needs a step entry")
	}
	t.Rbrace = p.want(RBRACE)
	return t
}

func (p *parser) parseFuncDecl(doc *CommentGroup) *FuncDecl {
	d := &FuncDecl{Doc: doc, Func: p.want(FUNC)}
	if p.tok.tok == LPAREN {
		d.Recv = p.parseParameters(LPAREN, RPAREN, paramReceiver)
		names := 0
		for _, f := range d.Recv.List {
			names += max(1, len(f.Names))
		}
		if names != 1 {
			p.errorAt(d.Recv.Opening, int(d.Recv.Closing-d.Recv.Opening)+1, "a method has exactly one receiver")
		}
	}
	d.Name = p.parseIdent()
	if p.tok.tok == LBRACK {
		if d.Recv != nil {
			p.errorf("a method cannot have type parameters")
		}
		d.TypeParams = p.parseTypeParams()
	}
	if p.tok.tok != LPAREN {
		p.errorf("expected \"(\", found %s", p.found())
	}
	d.Type = p.parseSignature(NoPos, false)
	if p.tok.tok == LBRACE {
		d.Body = p.parseBlock()
	}
	return d
}

func (p *parser) parseWireDecl(doc *CommentGroup) *WireDecl {
	d := &WireDecl{Doc: doc, Wire: p.want(WIRE)}
	d.Name = p.parseIdent()
	if p.tok.tok == PROVIDES {
		d.Provides = p.parseProvides()
	}
	if p.tok.tok == NEEDS {
		d.Needs = p.parseNeeds(false)
		if p.tok.tok == PROVIDES {
			p.errorf("provides comes before needs in a wire header")
		}
	}
	if p.tok.tok != LBRACE {
		p.errorf("expected \"{\", found %s", p.found())
	}
	d.Body = p.parseWireBody()
	return d
}

func (p *parser) parseWireBody() *WireBody {
	b := &WireBody{Lbrace: p.want(LBRACE)}
	saved := p.exprLev
	p.exprLev = 0
	for p.tok.tok != RBRACE {
		e := &WireEntry{Doc: p.leadComment(), Replace: NoPos, Assign: NoPos}
		if p.tok.tok == IDENT && p.tok.lit == "replace" && (p.peek(1).tok == IDENT || p.peek(1).tok == GO) {
			e.Kind, e.Replace = WireReplace, p.tok.pos
			p.next()
		}
		e.Key = p.parseQualifiedName()
		for p.tok.tok == COMMA {
			p.next()
			e.Keys = append(e.Keys, p.parseQualifiedName())
		}
		if len(e.Keys) > 0 && p.tok.tok != ASSIGN {
			p.errorf("expected \"=\" after the capabilities of a wire entry, found %s", p.found())
		}
		if p.tok.tok == ASSIGN {
			e.Assign = p.tok.pos
			p.next()
			e.Value = p.parseExpr()
		} else if e.Kind == WireReplace {
			p.errorf("expected \"=\", found %s", p.found())
		} else {
			e.Kind = WireEmbed
		}
		b.Entries = append(b.Entries, e)
		p.endStmt(RBRACE, "wire entry")
	}
	p.exprLev = saved
	b.Rbrace = p.want(RBRACE)
	return b
}

// ---------------------------------------------------------------- signatures and parameters

type paramMode int

const (
	paramPlain    paramMode = iota // function parameters
	paramResult                    // a parenthesised result list: error rows allowed
	paramType                      // a type parameter list: names and constraints
	paramReceiver                  // a receiver
)

// parseSignature parses parameters, results and a needs clause. list is true
// for a function type inside a list, where an unparenthesised needs clause
// takes one name (grammar.md D8).
func (p *parser) parseSignature(funcPos Pos, list bool) *FuncType {
	t := &FuncType{Func: funcPos}
	t.Params = p.parseParameters(LPAREN, RPAREN, paramPlain)
	switch {
	case p.tok.tok == LPAREN:
		t.Results = p.parseParameters(LPAREN, RPAREN, paramResult)
	case p.startsType():
		r := p.parseResultType(list)
		t.Results = &FieldList{Opening: NoPos, List: []*Field{{Type: r}}, Closing: NoPos}
	}
	if p.tok.tok == NEEDS {
		t.Needs = p.parseNeeds(list)
	}
	return t
}

// startsType reports whether the current token can start a type.
func (p *parser) startsType() bool {
	switch p.tok.tok {
	case IDENT:
		_, isRemoved := removed[p.tok.lit]
		return !isRemoved
	case LBRACK, MUL, ARROW, CHAN, MAP, FUNC, STRUCT, INTERFACE, LPAREN:
		return true
	case GO:
		return p.peek(1).tok == PERIOD
	}
	return false
}

// parseProvides parses a wire's `provides A, B`.
func (p *parser) parseProvides() *ProvidesClause {
	u := &ProvidesClause{Provides: p.want(PROVIDES)}
	u.Names = append(u.Names, p.parseQualifiedName())
	for p.got(COMMA) {
		u.Names = append(u.Names, p.parseQualifiedName())
	}
	return u
}

func (p *parser) parseNeeds(list bool) *NeedsClause {
	u := &NeedsClause{Needs: p.want(NEEDS), Lparen: NoPos, Rparen: NoPos}
	if p.tok.tok == LPAREN {
		if !list {
			p.errorf("a parenthesised needs list belongs to a function type inside a list")
		}
		u.Lparen = p.tok.pos
		p.next()
		u.Names = append(u.Names, p.parseQualifiedName())
		for p.got(COMMA) {
			if p.tok.tok == RPAREN {
				break
			}
			u.Names = append(u.Names, p.parseQualifiedName())
		}
		u.Rparen = p.want(RPAREN)
		return u
	}
	u.Names = append(u.Names, p.parseQualifiedName())
	if !list {
		for p.got(COMMA) {
			u.Names = append(u.Names, p.parseQualifiedName())
		}
	}
	return u
}

// parseResultType parses one result type, which may be an error row.
func (p *parser) parseResultType(list bool) Expr {
	if p.tok.tok == IDENT && p.tok.lit == "error" && p.peek(1).tok == LBRACK {
		return p.parseErrorRow()
	}
	return p.parseType(list)
}

func (p *parser) parseErrorRow() *ErrorRow {
	r := &ErrorRow{Error: p.tok.pos}
	p.next()
	r.Lbrack = p.want(LBRACK)
	r.Types = append(r.Types, p.parseTypeName("an error row"))
	for p.got(OR) {
		r.Types = append(r.Types, p.parseTypeName("an error row"))
	}
	r.Rbrack = p.want(RBRACK)
	return r
}

type paramEntry struct {
	name *Ident
	typ  Expr
}

// parseParameters parses a parameter, result, receiver or type parameter
// list with Go's grouping: either every entry has a name (`a, b T` groups
// a and b) or none does (`func(a, b) int` is two type-only parameters).
func (p *parser) parseParameters(open, close Token, mode paramMode) *FieldList {
	l := &FieldList{Opening: p.want(open)}
	saved := p.exprLev
	p.exprLev = 1
	var entries []paramEntry
	for p.tok.tok != close {
		entries = append(entries, p.parseParamEntry(close, mode))
		if !p.got(COMMA) {
			break
		}
	}
	p.exprLev = saved
	if p.tok.tok != close {
		p.errorf("expected %q or \",\", found %s", close.String(), p.found())
	}
	l.Closing = p.tok.pos
	p.next()
	named := false
	for _, e := range entries {
		named = named || e.name != nil
	}
	if !named {
		if mode == paramType {
			// Names without constraints: the list only orders the
			// parameters a caller supplies (`type Attempt[T]`;
			// rows-and-constraints §3).
			var names []*Ident
			for _, e := range entries {
				id, ok := e.typ.(*Ident)
				if !ok {
					p.errorAt(e.typ.Pos(), int(e.typ.End()-e.typ.Pos()), "a type parameter is a name with an optional constraint")
				}
				names = append(names, id)
			}
			l.List = []*Field{{Names: names}}
			return l
		}
		for _, e := range entries {
			l.List = append(l.List, &Field{Type: e.typ})
		}
	} else {
		var pending []*Ident
		for _, e := range entries {
			if e.name == nil {
				id, ok := e.typ.(*Ident)
				if !ok {
					p.errorAt(e.typ.Pos(), int(e.typ.End()-e.typ.Pos()), "mixed named and unnamed parameters")
				}
				pending = append(pending, id)
				continue
			}
			l.List = append(l.List, &Field{Names: append(pending, e.name), Type: e.typ})
			pending = nil
		}
		if len(pending) > 0 {
			p.errorAt(pending[len(pending)-1].End(), 1, "missing parameter type after %s", pending[len(pending)-1].Name)
		}
	}
	for i, f := range l.List {
		if v, ok := f.Type.(*Ellipsis); ok && (mode != paramPlain || i != len(l.List)-1 || len(f.Names) > 1) {
			p.errorAt(v.Ellipsis, 3, "only the final parameter can be variadic")
		}
	}
	return l
}

func (p *parser) parseParamEntry(close Token, mode paramMode) paramEntry {
	typeOf := func() Expr {
		if p.tok.tok == ELLIPSIS {
			pos := p.tok.pos
			p.next()
			return &Ellipsis{Ellipsis: pos, Elt: p.parseType(true)}
		}
		if mode == paramResult {
			return p.parseResultType(true)
		}
		return p.parseType(true)
	}
	if p.tok.tok != IDENT {
		return paramEntry{typ: typeOf()}
	}
	if _, isRemoved := removed[p.tok.lit]; isRemoved {
		p.parseIdent()
	}
	// The name `error` before `[` in a result list is an error row.
	if mode == paramResult && p.tok.lit == "error" && p.peek(1).tok == LBRACK {
		return paramEntry{typ: p.parseErrorRow()}
	}
	start := p.i
	id := p.parseIdent()
	switch p.tok.tok {
	case COMMA, close:
		return paramEntry{typ: id}
	case PERIOD:
		p.backtrack(start)
		return paramEntry{typ: p.parseType(true)}
	case LBRACK:
		// `name []T` or `name [N]T`, against the instantiation `T[A]`.
		if next := p.peek(1).tok; next == RBRACK || next == INT {
			return paramEntry{name: id, typ: p.parseType(true)}
		}
		p.backtrack(start)
		t := p.parseType(true)
		if p.startsType() || p.tok.tok == ELLIPSIS {
			p.backtrack(start)
			id = p.parseIdent()
			return paramEntry{name: id, typ: typeOf()}
		}
		return paramEntry{typ: t}
	}
	return paramEntry{name: id, typ: typeOf()}
}

func (p *parser) backtrack(i int) {
	p.i = i
	p.tok = p.toks[i]
}

// ---------------------------------------------------------------- types

// parseType parses a type. list is true inside a comma-separated list, where
// a function type's unparenthesised needs clause takes one name.
func (p *parser) parseType(list bool) Expr {
	p.enter()
	defer p.leave()
	switch p.tok.tok {
	case IDENT, GO:
		if p.tok.tok == IDENT && p.tok.lit == "error" && p.peek(1).tok == LBRACK {
			p.errorf("an error row error[…] appears only in a result list")
		}
		if p.tok.tok == GO && p.peek(1).tok != PERIOD {
			p.errorf("expected a type, found %s", p.found())
		}
		x := p.parseQualifiedName()
		if p.tok.tok == LBRACK {
			x = p.parseTypeArgs(x)
		}
		return x
	case LBRACK:
		lbrack := p.tok.pos
		p.next()
		if p.got(RBRACK) {
			return &ArrayType{Lbrack: lbrack, Elt: p.parseType(list)}
		}
		p.exprLev++
		n := p.parseExpr()
		p.exprLev--
		p.want(RBRACK)
		return &ArrayType{Lbrack: lbrack, Len: n, Elt: p.parseType(list)}
	case MUL:
		star := p.tok.pos
		p.next()
		return &StarExpr{Star: star, X: p.parseType(list)}
	case ARROW:
		begin := p.tok.pos
		p.next()
		if p.tok.tok != CHAN {
			p.errorf("expected \"chan\", found %s", p.found())
		}
		p.next()
		return &ChanType{Begin: begin, Arrow: begin, Dir: ChanRecv, Value: p.parseType(list)}
	case CHAN:
		t := &ChanType{Begin: p.tok.pos, Arrow: NoPos}
		p.next()
		if p.tok.tok == ARROW {
			t.Arrow, t.Dir = p.tok.pos, ChanSend
			p.next()
		}
		t.Value = p.parseType(list)
		return t
	case MAP:
		t := &MapType{Map: p.tok.pos}
		p.next()
		p.want(LBRACK)
		t.Key = p.parseType(false)
		p.want(RBRACK)
		t.Value = p.parseType(list)
		return t
	case FUNC:
		pos := p.tok.pos
		p.next()
		if p.tok.tok != LPAREN {
			p.errorf("expected \"(\", found %s", p.found())
		}
		return p.parseSignature(pos, list)
	case STRUCT:
		return p.parseStructType()
	case INTERFACE:
		return p.parseInterfaceType()
	case LPAREN:
		lparen := p.tok.pos
		p.next()
		x := p.parseType(false)
		return &ParenExpr{Lparen: lparen, X: x, Rparen: p.want(RPAREN)}
	}
	p.errorf("expected a type, found %s", p.found())
	return nil
}

// parseTypeName parses a type name: QualifiedName [ TypeArgs ]. Error rows,
// catch arms and union interfaces name types (grammar.md: QualifiedName,
// identifier); a row variable is a name, and an instantiated generic type
// such as `retry.Exhausted[E]` is a name with type arguments. Any other
// type is S001 at its first token.
func (p *parser) parseTypeName(what string) Expr {
	p.enter()
	defer p.leave()
	switch {
	case p.tok.tok == IDENT && p.tok.lit == "error" && p.peek(1).tok == LBRACK:
		p.errorf("an error row error[…] appears only in a result list")
	case p.tok.tok == IDENT, p.tok.tok == GO && p.peek(1).tok == PERIOD:
	default:
		p.errorf("%s names a type; expected a type name, found %s", what, p.found())
	}
	x := p.parseQualifiedName()
	if p.tok.tok == LBRACK {
		x = p.parseTypeArgs(x)
	}
	return x
}

func (p *parser) parseTypeArgs(x Expr) Expr {
	ix := &IndexExpr{X: x, Lbrack: p.want(LBRACK)}
	if p.tok.tok == RBRACK {
		p.errorf("expected a type argument, found %s", p.found())
	}
	for p.tok.tok != RBRACK {
		ix.Indices = append(ix.Indices, p.parseType(true))
		if !p.got(COMMA) {
			break
		}
	}
	ix.Rbrack = p.want(RBRACK)
	return ix
}
