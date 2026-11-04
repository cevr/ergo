package syntax

import (
	"fmt"
	"sort"
	"strconv"
)

// MaxNesting bounds the height, in levels, of the returned tree: the
// nesting of expressions, types, statements and blocks in any composition
// (grammar.md, Parser rules). A higher tree is S001 at the first token past
// the limit, so no input exhausts the stack of the parser or of any consumer
// of the AST (the printer, the converter, the checker). go/parser bounds
// nesting at 100 000; 1 000 leaves every consumer a small stack and is far
// above any program written by hand.
const MaxNesting = 1000

// CodeMixedMembers is the code of a type declaration that mixes fields with
// operations or laws (#196, D1.12): the tree cannot hold the mix, so the
// parser reports it with the checker's code for the rule.
const CodeMixedMembers = "S108"

// mixedMembers is the message of CodeMixedMembers.
const mixedMembers = "a record holds data; declare the operations in their own type"

// Parse parses one source file. It returns the first syntax error, an
// *Error with code S001 (or S108 for a type that mixes fields and
// operations), and no file; recovery is #38's.
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

	// exprLev is below zero in the header of if, match, for and with, where
	// a `{` after a type name or a call opens the body, never a composite
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

	groups   []*CommentGroup
	groupOwn []bool // the group starts on a line with no token before it
}

// ---------------------------------------------------------------- tokens and errors

// next advances to the next token. It stops at EOF and at the ILLEGAL
// token of a lexical error, so the parse never reads past the error.
func (p *parser) next() {
	if p.tok.tok != EOF && p.tok.tok != ILLEGAL {
		p.i++
		p.tok = p.toks[p.i]
	}
}

// peek returns the token n past the current one, or the stream's final
// EOF past the end: a lookahead stops at EOF, including after a lexical
// error's ILLEGAL token (scanner.fail).
func (p *parser) peek(n int) token {
	if p.i+n < len(p.toks) {
		return p.toks[p.i+n]
	}
	return p.toks[len(p.toks)-1]
}

func (p *parser) backtrack(i int) {
	p.i = i
	p.tok = p.toks[i]
}

func (p *parser) errorAt(pos Pos, length int, format string, args ...any) {
	p.errorCode(CodeSyntax, pos, length, fmt.Sprintf(format, args...))
}

// errorCode reports the first error and stops the parse. A lexical error
// at or before pos wins, since the tokens past it are not the source's.
func (p *parser) errorCode(code string, pos Pos, length int, message string) {
	if p.s.err != nil && pos >= Pos(p.s.err.Offset) {
		p.err = p.s.err
	} else {
		p.err = newError(p.src, p.s.lines, pos, length, message)
		p.err.Code = code
	}
	panic(bailout{})
}

// errorf reports an error at the current token. A Go token that Ergo does
// not use (a reserved word or a removed operator) gets its habit message
// in place of the generic one (#196, D2), wherever the parser meets it.
func (p *parser) errorf(format string, args ...any) {
	if h, ok := tokenHabit(p.tok.tok); ok {
		p.habit(p.tok.pos, int(p.tok.end-p.tok.pos), h)
	}
	p.errorAt(p.tok.pos, int(p.tok.end-p.tok.pos), format, args...)
}

// habit reports the Go habit h at pos.
func (p *parser) habit(pos Pos, length int, h int) {
	p.errorAt(pos, length, "%s", habits[h].Message)
}

// tokenHabit returns the habit a token names on its own: the reserved Go
// keywords and the removed operators.
func tokenHabit(t Token) (int, bool) {
	if h, ok := reservedHabit[t]; ok {
		return h, true
	}
	switch t {
	case DEFINE:
		return habitDefine, true
	case INC, DEC:
		return habitIncDec, true
	case ARROW:
		return habitArrow, true
	case ELLIPSIS:
		return habitVariadic, true
	}
	return 0, false
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
	case IDENT, INT, FLOAT, DURATION, STRING, RUNE, PATH, STRING_HEAD:
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

// atLineEnd reports whether the current token is an inserted semicolon.
func (p *parser) atLineEnd() bool { return p.tok.tok == SEMICOLON && p.tok.lit == "\n" }

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
	p.errorAt(p.tok.pos, int(p.tok.end-p.tok.pos), "nesting deeper than %d levels", MaxNesting)
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

// attempt runs parse and reports whether it succeeded; on failure it
// restores the parser to where it was, so the caller can try another
// reading of the same tokens. A reading that fails on the lexical error
// read the source without fault up to it, so that error is the first in
// the source: attempt stops the parse with it, and no other reading can
// report an earlier error that the source does not have (#226).
func (p *parser) attempt(parse func()) (ok bool) {
	i, nest, peak, lev := p.i, p.nest, p.peak, p.exprLev
	defer func() {
		if r := recover(); r != nil {
			if _, isBailout := r.(bailout); !isBailout || p.err == p.s.err {
				panic(r)
			}
			p.backtrack(i)
			p.nest, p.peak, p.exprLev, p.err = nest, peak, lev, nil
			ok = false
		}
	}()
	parse()
	return true
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

// parseIdent parses a name. A keyword is never a name, and a reserved Go
// keyword gets its habit message.
func (p *parser) parseIdent() *Ident {
	if p.tok.tok != IDENT {
		p.errorf("expected a name, found %s", p.found())
	}
	id := &Ident{NamePos: p.tok.pos, Name: p.tok.lit}
	p.next()
	return id
}

// startsTypeName reports whether the current token starts a type name:
// `name`, `pkg.name` or `go.name`.
func (p *parser) startsTypeName() bool {
	return p.tok.tok == IDENT || p.tok.tok == GO && p.peek(1).tok == PERIOD
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

// ---------------------------------------------------------------- file and declarations

func (p *parser) parseFile() *File {
	f := &File{}
	for p.tok.tok == SEMICOLON {
		p.next()
	}
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
		case EXTERN:
			d = p.parseExternDecl(doc)
		case TEST:
			d = p.parseTestDecl(doc)
		case LAW:
			d = p.parseLawDecl(doc, true)
		case LET:
			d = p.parseLetDecl(doc, true)
		case IMPORT:
			p.errorf("an import must come before every other declaration")
		default:
			p.errorf("expected a declaration (type, func, extern, test, law or let), found %s", p.found())
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

// parseImportDecl parses `import [go] [alias] path`, one per line, the path
// unquoted (#196, D1.6). The scanner lexes the alias and the path as PATH.
func (p *parser) parseImportDecl() *ImportDecl {
	d := &ImportDecl{Doc: p.leadComment(), Import: p.want(IMPORT)}
	switch p.tok.tok {
	case LPAREN:
		p.habit(p.tok.pos, 1, habitImportGroup)
	}
	s := &ImportSpec{Kind: ImportPlain, NamePos: NoPos}
	if p.tok.tok == GO {
		s.Kind, s.NamePos = ImportGo, p.tok.pos
		p.next()
	}
	p.unquotedPath()
	if p.tok.tok != PATH {
		p.errorf("expected an import path, found %s", p.found())
	}
	first := p.tok
	p.next()
	p.unquotedPath()
	if p.tok.tok == PATH {
		if !isIdentifier(first.lit) {
			p.errorAt(first.pos, int(first.end-first.pos), "an import alias is a name, found %q", first.lit)
		}
		s.Name = &Ident{NamePos: first.pos, Name: first.lit}
		if s.Kind == ImportPlain {
			s.Kind, s.NamePos = ImportNamed, first.pos
		}
		first = p.tok
		p.next()
	}
	s.Path = &BasicLit{ValuePos: first.pos, Kind: PATH, Value: first.lit, ValueEnd: first.end}
	d.Specs = []*ImportSpec{s}
	return d
}

// unquotedPath gives a quoted path where an import path may stand, after
// `import`, `import go` or an alias, the habit message.
func (p *parser) unquotedPath() {
	if p.tok.tok == STRING {
		p.habit(p.tok.pos, int(p.tok.end-p.tok.pos), habitImportQuoted)
	}
}

// isIdentifier reports whether s is a name: a letter or `_`, then letters,
// digits and `_`, and not a keyword or reserved word.
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !identRune(r, i == 0) {
			return false
		}
	}
	_, isKeyword := keywords[s]
	return !isKeyword
}

func (p *parser) parseTypeDecl(doc *CommentGroup) *TypeDecl {
	d := &TypeDecl{Doc: doc, Type: p.want(TYPE)}
	d.Name = p.parseIdent()
	if p.tok.tok == LBRACK && p.peek(1).tok != RBRACK {
		d.TypeParams = p.parseTypeParams()
	}
	switch p.tok.tok {
	case LBRACE:
		d.Spec = p.parseMembersType()
	case MACHINE:
		d.Spec = p.parseMachineType()
	case ASSIGN:
		p.next()
		d.Spec = p.parseUnion()
	default:
		d.Spec = p.parseType(ctxDecl)
	}
	return d
}

// parseTypeParams parses `[T ordered]`, `[A, B]` or `[K comparable, V]`
// with Go's grouping: names before a constraint share it, and names at the
// end without one have none.
func (p *parser) parseTypeParams() *FieldList {
	l := &FieldList{Opening: p.want(LBRACK)}
	var pending []*Ident
	for {
		pending = append(pending, p.parseIdent())
		if p.tok.tok != COMMA && p.tok.tok != RBRACK {
			l.List = append(l.List, &Field{Names: pending, Type: p.parseTypeName("a constraint")})
			pending = nil
		}
		if !p.got(COMMA) || p.tok.tok == RBRACK {
			break
		}
	}
	if len(pending) > 0 {
		l.List = append(l.List, &Field{Names: pending})
	}
	if p.tok.tok != RBRACK {
		p.errorf("expected \"]\" or \",\", found %s", p.found())
	}
	l.Closing = p.tok.pos
	p.next()
	return l
}

// memberKind is what a member of a braced type declares.
type memberKind int

const (
	memberField memberKind = iota
	memberOperation
	memberLaw
)

// members is the parsed body of `{ … }` in a type declaration or variant.
type members struct {
	lbrace, rbrace Pos
	fields         []*Field
	ops            []*Field
	laws           []*LawDecl
}

// parseMembers parses `{ Member, … }`. A body that mixes fields with
// operations or laws is S108 at the first member of the second kind.
func (p *parser) parseMembers() *members {
	m := &members{lbrace: p.want(LBRACE)}
	saved := p.exprLev
	p.exprLev = 0
	first := memberKind(-1)
	for p.tok.tok != RBRACE {
		doc := p.leadComment()
		start := p.tok
		var kind memberKind
		switch {
		case p.tok.tok == LAW:
			kind = memberLaw
			m.laws = append(m.laws, p.parseLawDecl(doc, false))
		case p.tok.tok == IDENT && p.peek(1).tok == LBRACK && p.bracketsThenParen():
			p.errorAt(p.peek(1).pos, 1, "an operation has no type parameters; declare them on its type: `type Name[T] { … }`")
		case p.tok.tok == IDENT && p.peek(1).tok == LPAREN:
			kind = memberOperation
			name := p.parseIdent()
			m.ops = append(m.ops, &Field{Doc: doc, Names: []*Ident{name}, Type: p.parseSignature(NoPos, ctxMember)})
		case p.tok.tok == IDENT:
			kind = memberField
			f := p.parseFieldDecl()
			f.Doc = doc
			m.fields = append(m.fields, f)
		default:
			p.errorf("expected a field, an operation or a law, found %s", p.found())
		}
		if first < 0 {
			first = kind
		} else if (first == memberField) != (kind == memberField) {
			p.errorCode(CodeMixedMembers, start.pos, int(start.end-start.pos), mixedMembers)
		}
		if !p.got(COMMA) {
			break
		}
	}
	p.exprLev = saved
	if p.tok.tok != RBRACE {
		p.errorf("expected \",\" or \"}\" after a member, found %s", p.found())
	}
	m.rbrace = p.tok.pos
	p.next()
	return m
}

// refuseSingleton refuses `(x,)` at its comma: a tuple has two or more
// elements in expressions, types and patterns alike, so the one-element
// spelling has no tree of its own (D4).
func (p *parser) refuseSingleton() {
	if p.tok.tok == COMMA && p.peek(1).tok == RPAREN {
		p.errorAt(p.tok.pos, 1, "a tuple has two or more elements; drop the comma")
	}
}

// bracketsThenParen reports whether the brackets that open at the next
// token close before a `(`, as in the operation `Get[T](k string) T`; a
// field type such as `xs [n]int` closes before a type instead. At the
// lexical error it reports false: the field reading parses on to the error
// (#226).
func (p *parser) bracketsThenParen() bool {
	depth := 0
	for i := 1; ; i++ {
		switch p.peek(i).tok {
		case LBRACK:
			depth++
		case RBRACK:
			if depth--; depth == 0 {
				return p.peek(i+1).tok == LPAREN
			}
		case LBRACE, RBRACE, SEMICOLON, ILLEGAL, EOF:
			return false
		}
	}
}

// parseFieldDecl parses `name {, name} Type`. Names group up to the type,
// as Go's struct fields do; a member that is a bare name (Go embedding) is
// S001.
func (p *parser) parseFieldDecl() *Field {
	f := &Field{Names: []*Ident{p.parseIdent()}}
	for p.tok.tok == COMMA && p.peek(1).tok == IDENT && p.groupsName(p.peek(2).tok) {
		p.next()
		f.Names = append(f.Names, p.parseIdent())
	}
	switch p.tok.tok {
	case COMMA, RBRACE, SEMICOLON:
		last := f.Names[len(f.Names)-1]
		p.errorAt(last.Pos(), len(last.Name), "a member names its type: write `%s Type`; Ergo has no embedding", last.Name)
	}
	f.Type = p.parseType(ctxMember)
	return f
}

// groupsName reports whether a name followed by t is one more name of a
// field group: another name, or the group's type.
func (p *parser) groupsName(t Token) bool {
	switch t {
	case COMMA, IDENT, LBRACK, MUL, FUNC, ESCAPING, LPAREN:
		return true
	case GO:
		return true
	}
	return false
}

// parseMembersType parses the braced body of `type T { … }`: a record of
// fields (StructType, also when empty) or an operation type of operations
// and laws (InterfaceType).
func (p *parser) parseMembersType() Expr {
	m := p.parseMembers()
	if len(m.ops) > 0 || len(m.laws) > 0 {
		return &InterfaceType{Lbrace: m.lbrace, Methods: m.ops, Laws: m.laws, Rbrace: m.rbrace}
	}
	return &StructType{Fields: &FieldList{Opening: m.lbrace, List: m.fields, Closing: m.rbrace}}
}

// parseUnion parses `[ "|" ] Variant { "|" Variant }` after `=`. An inserted
// semicolon before `|` does not end the union (#196, D1.5).
func (p *parser) parseUnion() *InterfaceType {
	t := &InterfaceType{Lbrace: NoPos, Rbrace: NoPos}
	if p.tok.tok == LPAREN {
		p.errorf("`=` declares a union of variants; a tuple type is written `type Name (A, B)`")
	}
	p.got(OR)
	for {
		t.Union = append(t.Union, p.parseVariant())
		if p.tok.tok == OR {
			p.next()
			continue
		}
		if p.atLineEnd() && p.peek(1).tok == OR {
			p.next()
			p.next()
			continue
		}
		return t
	}
}

// parseVariant parses `TypeName [TypeArgs] [Members]`. A variant with
// members declares it in place (VariantSpec); its members are fields.
func (p *parser) parseVariant() Expr {
	if !p.startsTypeName() {
		p.errorf("a union variant is a type name, found %s", p.found())
	}
	name := p.parseQualifiedName()
	var x Expr = name
	if p.tok.tok == LBRACK {
		x = p.parseTypeArgs(name)
	}
	if p.tok.tok != LBRACE {
		return x
	}
	id, ok := name.(*Ident)
	if !ok {
		p.errorf("a variant declared in place has a plain name, found %s", p.found())
	}
	v := &VariantSpec{Name: id}
	if ix, ok := x.(*IndexExpr); ok {
		v.TypeArgs = &FieldList{Opening: ix.Lbrack, Closing: ix.Rbrack}
		for _, a := range ix.Indices {
			v.TypeArgs.List = append(v.TypeArgs.List, &Field{Type: a})
		}
	}
	m := p.parseMembers()
	if len(m.ops) > 0 || len(m.laws) > 0 {
		var at Node = m.lawsOrOps()
		p.errorAt(at.Pos(), 1, "a variant holds fields only")
	}
	v.Fields = &FieldList{Opening: m.lbrace, List: m.fields, Closing: m.rbrace}
	return v
}

func (m *members) lawsOrOps() Node {
	if len(m.ops) > 0 {
		return m.ops[0]
	}
	return m.laws[0]
}

// parseMachineType parses `machine[S, E] { initial: x, step: f }`.
func (p *parser) parseMachineType() *MachineType {
	t := &MachineType{Machine: p.want(MACHINE)}
	t.Lbrack = p.want(LBRACK)
	t.State = p.parseType(ctxList)
	p.want(COMMA)
	t.Signal = p.parseType(ctxList)
	t.Rbrack = p.want(RBRACK)
	t.Lbrace = p.want(LBRACE)
	saved := p.exprLev
	p.exprLev = 0
	for p.tok.tok != RBRACE {
		e := &MachineEntry{Doc: p.leadComment(), NamePos: p.tok.pos}
		if p.tok.tok != IDENT || p.tok.lit != "initial" && p.tok.lit != "step" {
			p.errorf("expected initial or step, found %s", p.found())
		}
		e.Name = p.tok.lit
		if e.Name == "initial" && t.Initial != nil || e.Name == "step" && t.Step != nil {
			p.errorf("%s is set twice", e.Name)
		}
		p.next()
		e.Colon = p.want(COLON)
		e.Value = p.parseExpr()
		if e.Name == "initial" {
			t.Initial = e.Value
		} else {
			t.Step = e.Value
		}
		t.Entries = append(t.Entries, e)
		if !p.got(COMMA) {
			break
		}
	}
	p.exprLev = saved
	if p.tok.tok != RBRACE {
		p.errorf("expected \",\" or \"}\" in a machine, found %s", p.found())
	}
	t.Rbrace = p.tok.pos
	p.next()
	if t.Initial == nil || t.Step == nil {
		p.errorAt(t.Machine, len("machine"), "a machine sets initial and step")
	}
	return t
}

func (p *parser) parseFuncDecl(doc *CommentGroup) *FuncDecl {
	d := &FuncDecl{Doc: doc, Func: p.want(FUNC)}
	if p.tok.tok == LPAREN {
		p.habit(p.tok.pos, 1, habitReceiver)
	}
	d.Name = p.parseIdent()
	if p.tok.tok == LBRACK {
		d.TypeParams = p.parseTypeParams()
	}
	if p.tok.tok != LPAREN {
		p.errorf("expected \"(\", found %s", p.found())
	}
	d.Type = p.parseSignature(NoPos, ctxDecl)
	if p.tok.tok != LBRACE {
		p.errorf("expected the function body \"{\", found %s", p.found())
	}
	d.Body = p.parseBlock()
	return d
}

// parseExternDecl parses `extern go path.F func Name [TypeParams] Sig`.
func (p *parser) parseExternDecl(doc *CommentGroup) *FuncDecl {
	x := &ExternSpec{Extern: p.want(EXTERN)}
	x.Go = p.want(GO)
	if p.tok.tok != PATH {
		p.errorf("expected the Go target path.Func, found %s", p.found())
	}
	x.Target = &BasicLit{ValuePos: p.tok.pos, Kind: PATH, Value: p.tok.lit, ValueEnd: p.tok.end}
	if path, name := x.Split(); path == "" || !isIdentifier(name) {
		p.errorf("an extern target is an import path, a dot and a function name, found %s", p.found())
	}
	p.next()
	d := &FuncDecl{Doc: doc, Extern: x, Func: p.want(FUNC)}
	d.Name = p.parseIdent()
	if p.tok.tok == LBRACK {
		d.TypeParams = p.parseTypeParams()
	}
	if p.tok.tok != LPAREN {
		p.errorf("expected \"(\", found %s", p.found())
	}
	d.Type = p.parseSignature(NoPos, ctxDecl)
	return d
}

// parseTestDecl parses `test Name { … }` or `test Name(x T) { … }`.
func (p *parser) parseTestDecl(doc *CommentGroup) *TestDecl {
	d := &TestDecl{Doc: doc, Test: p.want(TEST)}
	d.Name = p.parseIdent()
	if p.tok.tok == LPAREN {
		d.Params = p.parseParameters(paramPlain)
	}
	if p.tok.tok != LBRACE {
		p.errorf("expected \"{\", found %s", p.found())
	}
	d.Body = p.parseBlock()
	return d
}

// parseLawDecl parses `law Name(params) { body }`, top-level (top) or a
// member of an operation type. A top-level law has no type parameters: it
// is stated at concrete types, and a generic law is a member of the
// protocol's operation type (law-top).
func (p *parser) parseLawDecl(doc *CommentGroup, top bool) *LawDecl {
	d := &LawDecl{Doc: doc, Law: p.want(LAW)}
	d.Name = p.parseIdent()
	if top && p.tok.tok == LBRACK {
		p.errorAt(p.tok.pos, 1, "a law has no type parameters; state it at concrete types, or as a member of the protocol's operation type")
	}
	d.Params = p.parseParameters(paramPlain)
	d.Body = p.parseBlock()
	return d
}

// parseLetDecl parses `let [mut] Pattern {, Pattern} [Type] [= e]`. At
// package level, `let Name = context …` is a named context (ContextDecl).
func (p *parser) parseLetDecl(doc *CommentGroup, top bool) Decl {
	d := &VarDecl{Doc: doc, Var: p.want(LET), Mut: NoPos}
	if p.tok.tok == MUT {
		d.Mut = p.tok.pos
		p.next()
	}
	s := &VarSpec{Assign: NoPos}
	var pats []Expr
	for {
		pats = append(pats, p.parseLetPattern())
		if !p.got(COMMA) {
			break
		}
	}
	names := make([]*Ident, 0, len(pats))
	for _, x := range pats {
		if id, ok := x.(*Ident); ok {
			names = append(names, id)
		}
	}
	switch {
	case len(names) == len(pats):
		s.Names = names
	case len(pats) == 1:
		s.Pattern = pats[0]
	default:
		s.Pattern = &TupleExpr{Lparen: NoPos, Elts: pats, Rparen: NoPos}
	}
	switch p.tok.tok {
	case ASSIGN, SEMICOLON, RBRACE, EOF:
	default:
		s.Type = p.parseType(ctxDecl)
	}
	if p.tok.tok == ASSIGN {
		s.Assign = p.tok.pos
		p.next()
		if top && d.Mut == NoPos && len(s.Names) == 1 && s.Type == nil && p.tok.tok == CONTEXT {
			w := p.parseContextLit()
			return &ContextDecl{Doc: doc, Let: d.Var, Context: w.Context, Name: s.Names[0], Provides: w.Provides, Needs: w.Needs, Body: w.Body}
		}
		s.Values = []Expr{p.parseExpr()}
	}
	d.Specs = []*VarSpec{s}
	return d
}

// parseLetPattern parses one pattern of a let. A name followed by a type is
// the name; `name [` starts a type when `]` follows, else type arguments of
// a record pattern.
func (p *parser) parseLetPattern() Expr {
	if p.tok.tok == IDENT && p.peek(1).tok == LBRACK && p.peek(2).tok == RBRACK {
		return p.parseIdent()
	}
	return p.parsePattern()
}

// ---------------------------------------------------------------- signatures and parameters

// typeCtx is where a type stands, which decides what a comma after the
// needs row of a function type means.
type typeCtx int

const (
	ctxDecl   typeCtx = iota // a declaration: a comma after the row is its comma spelling
	ctxList                  // a comma-separated list: a comma ends the element
	ctxMember                // a member of a braced type: a comma ends the member, unless a lone name follows
)

type paramMode int

const (
	paramPlain paramMode = iota // function parameters, named or not
	paramLit                    // a function literal: names, all typed or all untyped
)

// parseSignature parses `Params [Type] [fails …] [needs …]`.
func (p *parser) parseSignature(funcPos Pos, ctx typeCtx) *FuncType {
	t := &FuncType{Escaping: NoPos, Func: funcPos}
	t.Params = p.parseParameters(paramPlain)
	var results []*Field
	if p.startsType() {
		results = append(results, &Field{Type: p.parseResultType(ctx)})
	}
	if p.tok.tok == FAILS {
		results = append(results, &Field{Type: p.parseFails()})
	}
	if results != nil {
		t.Results = &FieldList{Opening: NoPos, List: results, Closing: NoPos}
	}
	if p.tok.tok == NEEDS {
		t.Needs = p.parseNeeds(ctx)
	}
	return t
}

// parseResultType parses the one result type. A tuple ending in `error` is
// Go's `(T, error)` (#196, D1.13).
func (p *parser) parseResultType(ctx typeCtx) Expr {
	t := p.parseType(ctx)
	if tup, ok := t.(*TupleExpr); ok {
		last := tup.Elts[len(tup.Elts)-1]
		if id, ok := last.(*Ident); ok && id.Name == "error" {
			p.habit(last.Pos(), len("error"), habitErrorResult)
		}
	}
	return t
}

// parseFails parses `fails A | B[T]`.
func (p *parser) parseFails() *ErrorRow {
	r := &ErrorRow{Error: p.want(FAILS)}
	r.Types = append(r.Types, p.parseTypeName("fails"))
	for p.got(OR) {
		r.Types = append(r.Types, p.parseTypeName("fails"))
	}
	return r
}

// startsType reports whether the current token can start a type.
func (p *parser) startsType() bool {
	switch p.tok.tok {
	case IDENT, LBRACK, MUL, FUNC, ESCAPING, LPAREN:
		return true
	case GO:
		return p.peek(1).tok == PERIOD
	case MAP, CHAN, STRUCT, INTERFACE, ARROW, ELLIPSIS:
		// Go types: parseType reports their habits.
		return true
	}
	return false
}

// parseProvides parses a context's `provides A | B`. A comma after it is
// never the header going on, so it is the comma spelling of the row.
func (p *parser) parseProvides() *ProvidesClause {
	u := &ProvidesClause{Provides: p.want(PROVIDES)}
	u.Names = p.parseRow("provides")
	if p.tok.tok == COMMA {
		p.rowComma(p.tok.pos, "provides")
	}
	return u
}

// parseNeeds parses `needs A | B`. A comma after the row is its comma
// spelling where it cannot end anything else: in a declaration, and in a
// member when a capability name follows that does not start the next
// member. In a list a comma ends the element (rule 3), and a named
// parameter list reports the comma spelling itself (parseParameters).
func (p *parser) parseNeeds(ctx typeCtx) *NeedsClause {
	u := &NeedsClause{Needs: p.want(NEEDS)}
	u.Names = p.parseRow("needs")
	if p.tok.tok == COMMA && (ctx == ctxDecl || ctx == ctxMember && p.commaContinuesRow()) {
		p.rowComma(p.tok.pos, "needs")
	}
	return u
}

// parseRow parses the names of a capability row, `A | B`. A parenthesised
// row is S001 with the bare form (196n).
func (p *parser) parseRow(what string) []Expr {
	if p.tok.tok == LPAREN {
		p.errorAt(p.tok.pos, 1, "a capability row takes no parentheses: write %s A | B", what)
	}
	names := []Expr{p.parseCapabilityName(what)}
	for p.got(OR) {
		names = append(names, p.parseCapabilityName(what))
	}
	return names
}

// rowComma reports the comma spelling of a capability row at the comma.
func (p *parser) rowComma(pos Pos, what string) {
	p.errorAt(pos, 1, "a capability row separates its names with |: write %s A | B", what)
}

// commaContinuesRow reports whether the comma at the current token would
// continue a member's row: a type name follows that does not start the next
// member (an operation `Name(`, a field `Name Type` or a law).
func (p *parser) commaContinuesRow() bool {
	i := 1
	t := p.peek(i)
	if t.tok != IDENT && !(t.tok == GO && p.peek(i+1).tok == PERIOD) {
		return false
	}
	i++
	if p.peek(i).tok == PERIOD {
		i += 2
	}
	switch p.peek(i).tok {
	case COMMA, RBRACE, SEMICOLON:
		return true
	case LBRACK:
		// `B[T]` is a capability name with type arguments, refused at
		// its `[`; `B []T` is a field of slice type.
		return p.peek(i+1).tok != RBRACK
	}
	return false
}

type paramEntry struct {
	name  *Ident
	typ   Expr
	comma Pos // the comma before the entry, or NoPos for the first
}

// parseParameters parses a parameter list with Go's grouping: either every
// entry has a name (`a, b T` groups a and b) or none does (`func(int64)`).
// A list of bare names is a list of types here; a function literal reads
// it as untyped names (funcLitParams).
func (p *parser) parseParameters(mode paramMode) *FieldList {
	l := &FieldList{Opening: p.want(LPAREN)}
	saved := p.exprLev
	p.exprLev = 1
	var entries []paramEntry
	comma := NoPos
	for p.tok.tok != RPAREN {
		e := p.parseParamEntry()
		e.comma = comma
		entries = append(entries, e)
		if p.tok.tok != COMMA {
			break
		}
		comma = p.tok.pos
		p.next()
	}
	p.exprLev = saved
	if p.tok.tok != RPAREN {
		p.errorf("expected \")\" or \",\", found %s", p.found())
	}
	l.Closing = p.tok.pos
	p.next()
	named := false
	for _, e := range entries {
		named = named || e.name != nil
	}
	if !named {
		for _, e := range entries {
			l.List = append(l.List, &Field{Type: e.typ})
		}
		return l
	}
	var pending []*Ident
	for i, e := range entries {
		if e.name == nil {
			id, ok := e.typ.(*Ident)
			if !ok {
				p.needsComma(entries, i-len(pending))
				p.errorAt(e.typ.Pos(), int(e.typ.End()-e.typ.Pos()), "mixed named and unnamed parameters")
			}
			pending = append(pending, id)
			continue
		}
		l.List = append(l.List, &Field{Names: append(pending, e.name), Type: e.typ})
		pending = nil
	}
	if len(pending) > 0 {
		p.needsComma(entries, len(entries)-len(pending))
		last := pending[len(pending)-1]
		p.errorAt(last.Pos(), len(last.Name), "mixed named and unnamed parameters: %s has no type", last.Name)
	}
	return l
}

// needsComma reports the comma spelling of a needs row in a named parameter
// list, `f func() needs A, B`: the entries from i on have no type, and the
// one before them is a named parameter whose type ends in a needs row.
func (p *parser) needsComma(entries []paramEntry, i int) {
	if i == 0 || entries[i-1].name == nil || !endsInNeeds(entries[i-1].typ) || !isCapabilityName(entries[i].typ) {
		return
	}
	p.rowComma(entries[i].comma, "needs")
}

// endsInNeeds reports whether the type t ends in a needs row.
func endsInNeeds(t Expr) bool {
	switch t := t.(type) {
	case *FuncType:
		if t.Needs != nil {
			return true
		}
		if t.Results != nil && len(t.Results.List) > 0 {
			return endsInNeeds(t.Results.List[len(t.Results.List)-1].Type)
		}
	case *StarExpr:
		return endsInNeeds(t.X)
	case *ArrayType:
		return endsInNeeds(t.Elt)
	}
	return false
}

// isCapabilityName reports whether x has the shape of a capability name:
// `Name`, `pkg.Name` or `go.Name`.
func isCapabilityName(x Expr) bool {
	switch x := x.(type) {
	case *Ident:
		return true
	case *SelectorExpr:
		_, ok := x.X.(*Ident)
		return ok
	}
	return false
}

func (p *parser) parseParamEntry() paramEntry {
	if p.tok.tok != IDENT {
		return paramEntry{typ: p.parseType(ctxList)}
	}
	start := p.i
	id := p.parseIdent()
	switch p.tok.tok {
	case COMMA, RPAREN:
		return paramEntry{typ: id}
	case PERIOD:
		p.backtrack(start)
		return paramEntry{typ: p.parseType(ctxList)}
	case LBRACK:
		// `name []T` or `name [N]T`, against the instantiation `T[A]`.
		if next := p.peek(1).tok; next == RBRACK || next == INT {
			return paramEntry{name: id, typ: p.parseType(ctxList)}
		}
		p.backtrack(start)
		t := p.parseType(ctxList)
		if p.startsType() {
			p.backtrack(start)
			id = p.parseIdent()
			return paramEntry{name: id, typ: p.parseType(ctxList)}
		}
		return paramEntry{typ: t}
	}
	return paramEntry{name: id, typ: p.parseType(ctxList)}
}

// funcLitParams reads the parameters of a function literal: all typed, or
// all untyped names (#214), each untyped name a Field with a nil Type.
func (p *parser) funcLitParams(l *FieldList) {
	for _, f := range l.List {
		if len(f.Names) > 0 {
			return
		}
	}
	for _, f := range l.List {
		id, ok := f.Type.(*Ident)
		if !ok {
			p.errorAt(f.Type.Pos(), int(f.Type.End()-f.Type.Pos()), "a function literal names its parameters: write `func(x)` or `func(x T)`")
		}
		f.Names, f.Type = []*Ident{id}, nil
	}
}

// ---------------------------------------------------------------- types

// parseType parses a type.
func (p *parser) parseType(ctx typeCtx) Expr {
	p.enter()
	defer p.leave()
	switch p.tok.tok {
	case IDENT, GO:
		if p.tok.tok == GO && p.peek(1).tok != PERIOD {
			p.errorf("expected a type, found %s", p.found())
		}
		if p.tok.tok == IDENT && p.tok.lit == "error" && p.peek(1).tok == LBRACK {
			p.errorAt(p.peek(1).pos, 1, "unexpected \"[\" after error")
		}
		x := p.parseQualifiedName()
		if p.tok.tok == LBRACK {
			x = p.parseTypeArgs(x)
		}
		return mapType(x)
	case LBRACK:
		lbrack := p.tok.pos
		p.next()
		// At the lexical error the source after `[` is unknown: parse on,
		// so the error is reported (#226).
		if p.tok.tok != RBRACK && p.tok.tok != ILLEGAL {
			p.habit(lbrack, 1, habitArray)
		}
		p.next()
		return &ArrayType{Lbrack: lbrack, Elt: p.parseType(ctx)}
	case MUL:
		star := p.tok.pos
		p.next()
		return &StarExpr{Star: star, X: p.parseType(ctx)}
	case ESCAPING:
		pos := p.tok.pos
		p.next()
		if p.tok.tok != FUNC {
			p.errorf("expected \"func\" after escaping, found %s", p.found())
		}
		t := p.parseFuncType(ctx)
		t.Escaping = pos
		return t
	case FUNC:
		return p.parseFuncType(ctx)
	case LPAREN:
		lparen := p.tok.pos
		p.next()
		saved := p.exprLev
		p.exprLev = 1
		first := p.parseType(ctxList)
		if p.tok.tok != COMMA {
			p.exprLev = saved
			return &ParenExpr{Lparen: lparen, X: first, Rparen: p.want(RPAREN)}
		}
		p.refuseSingleton()
		t := &TupleExpr{Lparen: lparen, Elts: []Expr{first}}
		for p.got(COMMA) && p.tok.tok != RPAREN {
			t.Elts = append(t.Elts, p.parseType(ctxList))
		}
		p.exprLev = saved
		t.Rparen = p.want(RPAREN)
		return t
	}
	p.errorf("expected a type, found %s", p.found())
	return nil
}

func (p *parser) parseFuncType(ctx typeCtx) *FuncType {
	pos := p.want(FUNC)
	if p.tok.tok != LPAREN {
		p.errorf("expected \"(\", found %s", p.found())
	}
	return p.parseSignature(pos, ctx)
}

// mapType turns `Map[K, V]` into a MapType: Map is a prelude name that no
// declaration can take (#196, eighth round), so the name is the map type.
func mapType(x Expr) Expr {
	ix, ok := x.(*IndexExpr)
	if !ok || len(ix.Indices) != 2 {
		return x
	}
	if id, ok := ix.X.(*Ident); !ok || id.Name != "Map" {
		return x
	}
	return &MapType{Map: ix.X.Pos(), Lbrack: ix.Lbrack, Key: ix.Indices[0], Value: ix.Indices[1], Rbrack: ix.Rbrack}
}

// parseTypeName parses `name`, `pkg.name` or `go.name` with optional type
// arguments: the types of fails, needs, provides, constraints and catch.
func (p *parser) parseTypeName(what string) Expr {
	p.enter()
	defer p.leave()
	if !p.startsTypeName() {
		p.errorf("%s names a type; expected a type name, found %s", what, p.found())
	}
	if p.tok.lit == "error" && p.peek(1).tok == LBRACK {
		p.errorAt(p.peek(1).pos, 1, "unexpected \"[\" after error")
	}
	x := p.parseQualifiedName()
	if p.tok.tok == LBRACK {
		x = p.parseTypeArgs(x)
	}
	return mapType(x)
}

// parseCapabilityName parses the TypeName of a needs, provides or context
// key position: `name`, `pkg.name` or `go.name`, with no type arguments
// (the decided `TypeName = identifier [ "." identifier ]`). Brackets after
// it are S001 at the `[`, so a comma run of names ended by `:` is always
// one context entry's key list (Parser rule 9).
func (p *parser) parseCapabilityName(what string) Expr {
	p.enter()
	defer p.leave()
	if !p.startsTypeName() {
		p.errorf("%s names a type; expected a type name, found %s", what, p.found())
	}
	x := p.parseQualifiedName()
	if p.tok.tok == LBRACK {
		p.errorAt(p.tok.pos, 1, "a capability name takes no type arguments")
	}
	return x
}

func (p *parser) parseTypeArgs(x Expr) Expr {
	ix := &IndexExpr{X: x, Lbrack: p.want(LBRACK)}
	if p.tok.tok == RBRACK {
		p.errorf("expected a type argument, found %s", p.found())
	}
	saved := p.exprLev
	p.exprLev = 1
	for p.tok.tok != RBRACK {
		ix.Indices = append(ix.Indices, p.parseType(ctxList))
		if !p.got(COMMA) {
			break
		}
	}
	p.exprLev = saved
	ix.Rbrack = p.want(RBRACK)
	return ix
}

// ---------------------------------------------------------------- contexts

// parseContextLit parses `context [provides …] [needs …] { entries }`.
func (p *parser) parseContextLit() *ContextLit {
	w := &ContextLit{Context: p.want(CONTEXT)}
	if p.tok.tok == PROVIDES {
		w.Provides = p.parseProvides()
	}
	if p.tok.tok == NEEDS {
		w.Needs = p.parseNeeds(ctxDecl)
	}
	if p.tok.tok != LBRACE {
		p.errorf("expected \"{\" after context, found %s", p.found())
	}
	w.Body = p.parseContextBody()
	return w
}

// parseContextBody parses `{ entry, … }`. An entry binds capabilities to a
// value (`A: x`, `A, B: x`), replaces an embedded binding (`replace A: x`)
// or embeds a context (`W`). A comma run of names ended by `:` is one entry's
// key list, whatever the layout (grammar.md, Parser rule 9).
func (p *parser) parseContextBody() *ContextBody {
	b := &ContextBody{Lbrace: p.want(LBRACE)}
	saved := p.exprLev
	p.exprLev = 0
	for p.tok.tok != RBRACE {
		e := &ContextEntry{Doc: p.leadComment(), Replace: NoPos, Assign: NoPos}
		if p.tok.tok == IDENT && p.tok.lit == "replace" && (p.peek(1).tok == IDENT || p.peek(1).tok == GO) {
			e.Kind, e.Replace = ContextReplace, p.tok.pos
			p.next()
		}
		e.Key = p.parseCapabilityName("a context entry")
		if p.keysContinue() {
			for p.got(COMMA) {
				e.Keys = append(e.Keys, p.parseCapabilityName("a context entry"))
				if p.tok.tok == COLON {
					break
				}
			}
		}
		if p.tok.tok == COLON {
			e.Assign = p.tok.pos
			p.next()
			e.Value = p.parseExpr()
		} else if e.Kind == ContextReplace {
			p.errorf("expected \":\", found %s", p.found())
		} else {
			e.Kind = ContextEmbed
		}
		b.Entries = append(b.Entries, e)
		if !p.got(COMMA) {
			break
		}
	}
	p.exprLev = saved
	if p.tok.tok != RBRACE {
		p.errorf("expected \",\" or \"}\" in a context, found %s", p.found())
	}
	b.Rbrace = p.tok.pos
	p.next()
	return b
}

// keysContinue reports whether the comma at the current token joins more
// capability names to the entry: `, Name {, Name} :`. Layout plays no
// part: a comma run of type names ended by `:` is one entry's key list
// (grammar.md, Parser rule 9), as Go's `case a, b:` is one case.
func (p *parser) keysContinue() bool {
	for i := 0; ; {
		if p.peek(i).tok != COMMA {
			return false
		}
		i++
		if t := p.peek(i); t.tok != IDENT && !(t.tok == GO && p.peek(i+1).tok == PERIOD) {
			return false
		}
		i++
		if p.peek(i).tok == PERIOD {
			i += 2
		}
		if p.peek(i).tok == COLON {
			return true
		}
	}
}
