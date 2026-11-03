package bindc

import (
	"math/big"
	"path/filepath"
)

// Parse reads a C header of the subset (see the package doc) and returns its
// binding model. Any diagnostic means no model: the error is Diagnostics,
// sorted by position, one per refused construct.
func Parse(file string, src []byte) (*Model, error) {
	d := &diags{}
	toks := lex(file, string(src), d)
	if len(d.list) > 0 {
		return nil, d.sorted()
	}
	pp := runDirectives(toks, d)
	p := &parser{
		d:      d,
		pre:    pp,
		toks:   pp.ctoks,
		types:  map[string]int{},
		tags:   map[string]Pos{},
		names:  map[string]Pos{},
		macros: pp.macros,
	}
	p.checkMacroUses()
	p.parse()
	if len(d.list) > 0 {
		return nil, d.sorted()
	}
	return &Model{
		Header:    filepath.Base(file),
		Guard:     pp.guard,
		CPlusPlus: pp.cplusplus,
		Includes:  append([]string{}, pp.order...),
		Constants: pp.constants,
		Types:     p.decls,
		Functions: p.funcs,
	}, nil
}

// keywords are the C11 keywords and the common compiler extensions. None
// of them can name a declaration; the type parser refuses each with the
// code of its construct.
var keywords = map[string]bool{}

var keywordCode = map[string]Code{
	"int": CodeImplicitWidth, "char": CodeImplicitWidth, "short": CodeImplicitWidth,
	"long": CodeImplicitWidth, "signed": CodeImplicitWidth, "unsigned": CodeImplicitWidth,
	"__int128": CodeImplicitWidth,
	"enum":     CodeEnum,
	"union":    CodeUnion,
	"static":   CodeStorageClass, "inline": CodeStorageClass, "extern": CodeStorageClass,
	"register": CodeStorageClass, "auto": CodeStorageClass, "_Thread_local": CodeStorageClass,
	"_Noreturn": CodeStorageClass, "__inline": CodeStorageClass, "__inline__": CodeStorageClass,
	"thread_local":  CodeStorageClass,
	"__attribute__": CodeAttribute, "__attribute": CodeAttribute, "__declspec": CodeAttribute,
	"_Alignas": CodeAttribute, "alignas": CodeAttribute, "__asm__": CodeAttribute, "__asm": CodeAttribute,
	"asm": CodeAttribute, "__extension__": CodeAttribute, "_Pragma": CodeAttribute,
	"volatile": CodeQualifier, "restrict": CodeQualifier, "__restrict": CodeQualifier,
	"__restrict__": CodeQualifier, "_Atomic": CodeQualifier, "__volatile__": CodeQualifier,
	"__const": CodeQualifier, "_Nonnull": CodeQualifier, "_Nullable": CodeQualifier,
	"_Bool": CodeDeclarator, "_Complex": CodeDeclarator, "_Imaginary": CodeDeclarator,
	"sizeof": CodeDeclarator, "_Alignof": CodeDeclarator, "alignof": CodeDeclarator,
	"_Generic": CodeDeclarator, "_Static_assert": CodeDeclarator, "static_assert": CodeDeclarator,
	"typeof": CodeDeclarator, "__typeof__": CodeDeclarator, "_BitInt": CodeDeclarator,
	"_Float16": CodeDeclarator, "__fp16": CodeDeclarator, "_Decimal32": CodeDeclarator,
	"break": CodeSyntax, "case": CodeSyntax, "continue": CodeSyntax, "default": CodeSyntax,
	"do": CodeSyntax, "else": CodeSyntax, "for": CodeSyntax, "goto": CodeSyntax, "if": CodeSyntax,
	"return": CodeSyntax, "switch": CodeSyntax, "while": CodeSyntax,
	"nullptr": CodeSyntax, "constexpr": CodeSyntax, "true": CodeSyntax, "false": CodeSyntax,
}

// structural keywords the parser reads itself.
var parsedKeywords = []string{"const", "void", "float", "double", "struct", "typedef", "bool"}

var scalarNames = map[string]bool{}

func init() {
	for k := range keywordCode {
		keywords[k] = true
	}
	for _, k := range parsedKeywords {
		keywords[k] = true
	}
	for s := range scalarSize {
		scalarNames[string(s)] = true
	}
}

type parser struct {
	d      *diags
	pre    *pre
	toks   []token
	i      int
	decls  []TypeDecl
	funcs  []Function
	types  map[string]int // typedef name -> index in decls
	tags   map[string]Pos
	names  map[string]Pos // every typedef and function name
	macros map[string]macroDef
	// knr, when set, is the K&R construct the last diagnostic refused;
	// skipTop skips it as one construct.
	knr *knrSkip
}

// knrSkip is a refused K&R construct: open is the index of the `(` of its
// parameter list, and names holds the names of its identifier list.
type knrSkip struct {
	open  int
	names map[string]bool
}

// bail unwinds to the nearest recovery point after a diagnostic.
type bail struct{}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) peekN(n int) token {
	if p.i+n < len(p.toks) {
		return p.toks[p.i+n]
	}
	return p.toks[len(p.toks)-1]
}

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

func (p *parser) fail(t token, c Code, format string, args ...any) {
	p.d.add(t.pos, c, format, args...)
	panic(bail{})
}

func (p *parser) unexpected(t token, want string) {
	if t.kind == tIdent {
		if c, ok := keywordCode[t.text]; ok && c != CodeSyntax {
			p.fail(t, c, "%s: %s", quote(t.text), Codes[c])
		}
	}
	if t.kind == tEOF {
		p.fail(t, CodeSyntax, "unexpected end of file, want %s", want)
	}
	p.fail(t, CodeSyntax, "unexpected %s, want %s", quote(t.text), want)
}

func (p *parser) expect(text string) token {
	t := p.peek()
	if !t.is(text) {
		p.unexpected(t, quote(text))
	}
	return p.next()
}

// try runs f; after a diagnostic it rewinds to start and skips to the end
// of the construct with skip.
func (p *parser) try(f func(), skip func(start int)) {
	start := p.i
	failed := func() (failed bool) {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(bail); !ok {
					panic(r)
				}
				failed = true
			}
		}()
		f()
		return false
	}()
	if failed {
		skip(start)
		p.knr = nil
	}
}

// failKAndR refuses a K&R construct whose parameter list opens at open.
func (p *parser) failKAndR(open int, names map[string]bool, at token, format string, args ...any) {
	p.knr = &knrSkip{open: open, names: names}
	p.fail(at, CodeKAndR, format, args...)
}

// matching returns the index of the bracket that closes the one at i, or
// the index of the end of the file.
func (p *parser) matching(i int) int {
	depth := 0
	for k := i; ; k++ {
		switch t := p.toks[k]; {
		case t.kind == tEOF:
			return k
		case t.is("(") || t.is("[") || t.is("{"):
			depth++
		case t.is(")") || t.is("]") || t.is("}"):
			if depth--; depth == 0 {
				return k
			}
		}
	}
}

// skipKAndR skips a K&R construct as one: its parameter list, then the `;`
// of a declaration, or the declaration list and the body of a definition.
// The declaration right after the parameter list belongs to the construct;
// a later one belongs to it only when it names a parameter of the
// identifier list, so a K&R declarator without a body ends before the next
// declaration of the header.
func (p *parser) skipKAndR(k knrSkip) {
	i := p.matching(k.open)
	if p.toks[i].kind == tEOF {
		p.i = i
		return
	}
	i++
	if p.toks[i].is(";") {
		p.i = i + 1
		return
	}
	for first := true; ; first = false {
		if p.toks[i].is("{") {
			if i = p.matching(i); p.toks[i].kind != tEOF {
				i++
			}
			p.i = i
			return
		}
		end, named := p.declarationEnd(i, k.names)
		switch {
		case p.toks[end].is("{"):
			i = end
		case !p.toks[end].is(";"): // the end of the file or a stray closer
			p.i = end
			return
		case first || named:
			i = end + 1
		default:
			p.i = i
			return
		}
	}
}

// declarationEnd returns the index of the `;` that ends the declaration at
// i, or of the `{`, stray closer or end of file before it, and whether the
// declaration names one of names.
func (p *parser) declarationEnd(i int, names map[string]bool) (int, bool) {
	depth, named := 0, false
	for ; ; i++ {
		t := p.toks[i]
		switch {
		case t.kind == tEOF:
			return i, named
		case t.is("(") || t.is("["):
			depth++
		case t.is(")") || t.is("]"):
			if depth == 0 {
				return i, named
			}
			depth--
		case depth == 0 && (t.is(";") || t.is("{") || t.is("}")):
			return i, named
		case t.kind == tIdent && names[t.text]:
			named = true
		}
	}
}

// skipTo moves to the first token at or after the failure point that ends
// the construct that starts at start. Nesting counts from start, so a
// failure inside braces skips the whole braced group.
func (p *parser) skipTo(start int, end func(t token, depth int, opener token) (stop, consume bool)) {
	failAt := p.i
	depth := 0
	var openers []token
	for k := start; ; k++ {
		t := p.toks[k]
		if t.kind == tEOF {
			p.i = k
			return
		}
		opener := token{}
		if t.is("(") || t.is("[") || t.is("{") {
			prev := token{}
			if k > 0 {
				prev = p.toks[k-1]
			}
			openers = append(openers, prev)
			depth++
		} else if (t.is(")") || t.is("]") || t.is("}")) && depth > 0 {
			depth--
			opener = openers[len(openers)-1]
			openers = openers[:len(openers)-1]
			if k >= failAt {
				if stop, consume := end(t, depth, opener); stop {
					p.i = k
					if consume {
						p.i++
					}
					return
				}
			}
			continue
		}
		if k >= failAt {
			if stop, consume := end(t, depth, opener); stop {
				p.i = k
				if consume {
					p.i++
				}
				return
			}
		}
	}
}

// skipTop ends a top-level declaration at `;`, or at the `}` of a
// function body. A refused K&R construct ends after its body.
func (p *parser) skipTop(start int) {
	if k := p.knr; k != nil {
		p.skipKAndR(*k)
		return
	}
	p.skipTo(start, func(t token, depth int, opener token) (bool, bool) {
		if depth != 0 {
			return false, false
		}
		if t.is(";") {
			return true, true
		}
		return t.is("}") && opener.is(")"), true
	})
}

// skipField ends a field at its `;`, or before the `}` that closes the
// struct.
func (p *parser) skipField(start int) {
	p.skipTo(start, func(t token, depth int, opener token) (bool, bool) {
		if t.is(";") && depth == 0 {
			return true, true
		}
		// An unmatched `}` reaches end with no opener.
		return t.is("}") && opener == (token{}), false
	})
}

// skipParam ends a parameter before the `,` or the unmatched `)` after it.
func (p *parser) skipParam(start int) {
	p.skipTo(start, func(t token, depth int, opener token) (bool, bool) {
		unmatched := t.is(")") && opener == (token{})
		return depth == 0 && (t.is(",") || t.is(";") || unmatched), false
	})
}

// checkMacroUses refuses a macro name used as an identifier: a C compiler
// would expand it. The one accepted use is an array length `[NAME]`.
func (p *parser) checkMacroUses() {
	for k, t := range p.toks {
		if t.kind != tIdent {
			continue
		}
		m, ok := p.macros[t.text]
		if !ok {
			continue
		}
		if m.index >= 0 && k > 0 && p.toks[k-1].is("[") && p.toks[k+1].is("]") {
			if !m.pos.before(t.pos) {
				p.d.add(t.pos, CodeMacroIdentifier, "macro %s is used before its definition", t.text)
			}
			continue
		}
		p.d.add(t.pos, CodeMacroIdentifier, "macro %s is used as an identifier; a C compiler would expand it", t.text)
	}
}

func (p *parser) parse() {
	for p.peek().kind != tEOF {
		p.try(p.topDecl, p.skipTop)
	}
}

func (p *parser) topDecl() {
	t := p.peek()
	switch {
	case t.is("typedef"):
		p.next()
		p.typedefDecl()
	case t.is("struct"):
		p.fail(t, CodeStructTag, "struct declared outside `typedef struct`")
	case t.is(";"):
		p.fail(t, CodeSyntax, "empty declaration")
	case t.is("[") && p.peekN(1).is("["):
		p.fail(t, CodeAttribute, "attribute `[[...]]`")
	default:
		p.prototype()
	}
}

// declare records a typedef or function name.
func (p *parser) declare(t token) {
	if keywords[t.text] || scalarNames[t.text] {
		p.fail(t, CodeDuplicate, "%s names a keyword or a standard type", t.text)
	}
	if prev, ok := p.names[t.text]; ok {
		p.fail(t, CodeDuplicate, "%s is already declared at %s", t.text, prev)
	}
	p.names[t.text] = t.pos
}

func (p *parser) ident(want string) token {
	t := p.peek()
	if t.kind != tIdent || keywords[t.text] {
		p.unexpected(t, want)
	}
	p.checkName(t)
	return p.next()
}

// checkName refuses a declared name that a C compiler would expand as a
// macro of the standard headers or of the compiler.
func (p *parser) checkName(t token) {
	if reservedMacro.MatchString(t.text) {
		p.fail(t, CodeMacroIdentifier, "%s is a reserved name or a standard macro", t.text)
	}
}

func (p *parser) typedefDecl() {
	t := p.peek()
	switch {
	case t.is("struct"):
		p.next()
		tag := token{}
		if p.peek().kind == tIdent && !keywords[p.peek().text] {
			p.checkName(p.peek())
			tag = p.next()
		}
		if p.peek().is("{") {
			p.structDecl(tag)
			return
		}
		if tag.text == "" {
			p.unexpected(p.peek(), "a struct tag or `{`")
		}
		name := p.ident("a typedef name")
		if !p.peek().is(";") {
			p.unexpected(p.peek(), "`;`")
		}
		p.declareTag(tag)
		p.declare(name)
		p.next()
		p.add(TypeDecl{Kind: DeclOpaque, Name: name.text, Tag: tag.text})
		return
	}
	base, constTok := p.typeSpec()
	typ, nptr := p.pointers(base)
	if p.peek().is("(") {
		p.callbackDecl(typ, constTok, nptr)
		return
	}
	if constTok.text != "" {
		p.fail(constTok, CodeQualifier, "const outside `const T *`")
	}
	name := p.ident("a typedef name")
	if nptr > 0 || base.Kind != TypeScalar {
		p.fail(name, CodeDeclarator, "typedef of %s is outside the subset (fixed-width integers, bool, float and double only)", typ.Spell())
	}
	if p.peek().is("[") {
		p.fail(p.peek(), CodeDeclarator, "typedef of an array")
	}
	p.endDecl(name)
	p.add(TypeDecl{Kind: DeclScalar, Name: name.text, Scalar: base.Scalar})
}

func (p *parser) add(d TypeDecl) {
	p.types[d.Name] = len(p.decls)
	p.decls = append(p.decls, d)
}

func (p *parser) declareTag(tag token) {
	if tag.text == "" {
		return
	}
	if prev, ok := p.tags[tag.text]; ok {
		p.fail(tag, CodeDuplicate, "struct tag %s is already declared at %s", tag.text, prev)
	}
	p.tags[tag.text] = tag.pos
}

// endDecl reads the `;` that ends a declaration, refusing an attribute or a
// second declarator before it, and declares name. A duplicate name is
// refused before the `;` is read, so recovery skips to that `;`.
func (p *parser) endDecl(name token) {
	t := p.peek()
	switch {
	case t.is(","):
		p.fail(t, CodeDeclarator, "one declarator per declaration")
	case t.is("[") && p.peekN(1).is("["):
		p.fail(t, CodeAttribute, "attribute `[[...]]`")
	case !t.is(";"):
		p.unexpected(t, "`;`")
	}
	p.declare(name)
	p.next()
}

func (p *parser) structDecl(tag token) {
	open := p.expect("{")
	var fields []Field
	seen := map[string]Pos{}
	before := len(p.d.list)
	for !p.peek().is("}") {
		if p.peek().kind == tEOF {
			p.unexpected(p.peek(), "`}`")
		}
		p.try(func() { fields = append(fields, p.field(seen)...) }, p.skipField)
	}
	p.next()
	if len(fields) == 0 && len(p.d.list) == before {
		p.fail(open, CodeDeclarator, "struct without fields")
	}
	name := p.ident("a typedef name")
	decl := TypeDecl{Kind: DeclStruct, Name: name.text, Tag: tag.text, Fields: fields}
	if !p.layout(&decl) {
		p.fail(name, CodeConstantRange, "struct %s is larger than %d bytes", name.text, maxObject)
	}
	if p.peek().is(";") {
		p.declareTag(tag)
	}
	p.endDecl(name)
	p.add(decl)
}

// field reads one field declaration: a type and one or more declarators,
// as in `uint32_t width, height;`.
func (p *parser) field(seen map[string]Pos) []Field {
	t := p.peek()
	if t.is("struct") {
		if p.peekN(1).is("{") || p.peekN(1).kind == tIdent && p.peekN(2).is("{") {
			p.fail(t, CodeNestedStruct, "struct definition inside a struct")
		}
		p.fail(t, CodeStructTag, "struct named by its tag; use its typedef name")
	}
	if t.is("[") && p.peekN(1).is("[") {
		p.fail(t, CodeAttribute, "attribute `[[...]]`")
	}
	base, constTok := p.typeSpec()
	var fields []Field
	for {
		typ, nptr := p.pointers(base)
		if constTok.text != "" && nptr == 0 {
			p.fail(constTok, CodeQualifier, "const outside `const T *`")
		}
		if p.peek().is("(") {
			p.fail(p.peek(), CodeDeclarator, "function pointer declarator; declare a callback typedef")
		}
		name := p.ident("a field name")
		if p.peek().is("[") {
			typ = p.array(typ)
		}
		switch t := p.peek(); {
		case t.is(":"):
			p.fail(t, CodeBitField, "bit field %s", name.text)
		case t.is("("):
			p.fail(t, CodeDeclarator, "function declarator in a struct")
		case t.is("[") && p.peekN(1).is("["):
			p.fail(t, CodeAttribute, "attribute `[[...]]`")
		}
		p.valueType(typ, name, "field")
		if prev, ok := seen[name.text]; ok {
			p.fail(name, CodeDuplicate, "field %s is already declared at %s", name.text, prev)
		}
		seen[name.text] = name.pos
		fields = append(fields, Field{Name: name.text, Type: typ})
		if !p.peek().is(",") {
			break
		}
		p.next()
	}
	p.expect(";")
	return fields
}

// array reads `[N]` after a field name.
func (p *parser) array(elem Type) Type {
	open := p.expect("[")
	t := p.next()
	if t.is("]") {
		p.fail(open, CodeFlexibleArray, "flexible array member")
	}
	var n *big.Int
	switch {
	case t.kind == tNumber:
		v, _, msg := parseLiteral(t.text)
		if msg != "" {
			p.fail(t, CodeDeclarator, "array length: %s", msg)
		}
		n = v
	case t.kind == tIdent:
		m, ok := p.macros[t.text]
		if !ok || m.index < 0 {
			p.fail(t, CodeDeclarator, "array length %s is not a literal or an earlier constant", t.text)
		}
		n = m.value.v
	default:
		p.fail(t, CodeDeclarator, "array length is a literal or the name of an earlier constant")
	}
	if c := p.peek(); !c.is("]") {
		p.fail(c, CodeDeclarator, "array length is a literal or the name of an earlier constant")
	}
	p.next()
	if n.Sign() <= 0 || n.Cmp(big.NewInt(1<<31)) >= 0 {
		p.fail(t, CodeConstantRange, "array length %s is out of range (1 to 2^31-1)", n)
	}
	if p.peek().is("[") {
		p.fail(p.peek(), CodeDeclarator, "array of arrays")
	}
	e := elem
	return Type{Kind: TypeArray, Elem: &e, Len: n.Int64(), LenExpr: t.text}
}

// valueType refuses a type that has no size: void, an opaque struct by
// value, or an array of those.
func (p *parser) valueType(t Type, at token, what string) {
	for t.Kind == TypeArray {
		t = *t.Elem
	}
	switch {
	case t.Kind == TypeVoid:
		p.fail(at, CodeDeclarator, "%s %s has type void", what, at.text)
	case t.Kind == TypeNamed && p.decls[p.types[t.Name]].Kind == DeclOpaque:
		p.fail(at, CodeDeclarator, "%s %s holds opaque %s by value", what, at.text, t.Name)
	}
}

// typeSpec reads `[const] base` where base is void, a scalar or a typedef
// name. It returns the const token when there is one; the caller allows it
// only before a pointer.
func (p *parser) typeSpec() (Type, token) {
	constTok := token{}
	if p.peek().is("const") {
		constTok = p.next()
	}
	t := p.next()
	var typ Type
	switch {
	case t.kind != tIdent:
		p.i--
		p.unexpected(t, "a type")
	case t.is("const"):
		p.fail(t, CodeQualifier, "const twice")
	case t.is("struct"):
		if p.peek().is("{") || p.peek().kind == tIdent && p.peekN(1).is("{") {
			p.fail(t, CodeNestedStruct, "struct definition in a type")
		}
		p.fail(t, CodeStructTag, "struct named by its tag; use its typedef name")
	case t.is("void"):
		typ = Type{Kind: TypeVoid}
	case t.is("float"):
		typ = Type{Kind: TypeScalar, Scalar: Float32}
	case t.is("double"):
		typ = Type{Kind: TypeScalar, Scalar: Float64}
	case scalarNames[t.text]:
		s := Scalar(t.text)
		if h := scalarHeader[s]; !p.pre.included(h, t.pos) {
			p.fail(t, CodeMissingInclude, "%s needs #include <%s> before it", t.text, h)
		}
		typ = Type{Kind: TypeScalar, Scalar: s}
	case keywordCode[t.text] != "":
		p.i--
		p.unexpected(t, "a type")
	default:
		if _, ok := p.types[t.text]; !ok {
			p.fail(t, CodeUnknownType, "unknown type name %s", t.text)
		}
		typ = Type{Kind: TypeNamed, Name: t.text}
	}
	if q := p.peek(); q.is("const") || keywordCode[q.text] == CodeQualifier {
		p.fail(q, CodeQualifier, "qualifier %s after the type; only `const T *`", q.text)
	}
	if constTok.text != "" {
		typ.Const = true
	}
	return typ, constTok
}

// pointers reads the `*`s after a base type.
func (p *parser) pointers(base Type) (Type, int) {
	t, n := base, 0
	for p.peek().is("*") {
		p.next()
		if q := p.peek(); q.is("const") || keywordCode[q.text] == CodeQualifier {
			p.fail(q, CodeQualifier, "qualifier %s on a pointer; only `const T *`", q.text)
		}
		e := t
		t = Type{Kind: TypePointer, Elem: &e}
		n++
	}
	return t, n
}

func (p *parser) callbackDecl(result Type, constTok token, nptr int) {
	if constTok.text != "" && nptr == 0 {
		p.fail(constTok, CodeQualifier, "const outside `const T *`")
	}
	p.expect("(")
	if !p.peek().is("*") {
		p.fail(p.peek(), CodeDeclarator, "function typedef; write `typedef R (*name)(params);`")
	}
	p.next()
	if p.peek().is("*") || p.peek().is("(") {
		p.fail(p.peek(), CodeDeclarator, "callback typedef is `typedef R (*name)(params);`")
	}
	name := p.ident("a callback name")
	if !p.peek().is(")") {
		p.fail(p.peek(), CodeDeclarator, "callback typedef is `typedef R (*name)(params);`")
	}
	p.next()
	p.resultType(result, name)
	params := p.params(name)
	p.endDecl(name)
	p.add(TypeDecl{Kind: DeclCallback, Name: name.text, Result: &result, Params: params})
}

func (p *parser) resultType(t Type, at token) {
	if t.Kind == TypeNamed && p.decls[p.types[t.Name]].Kind == DeclOpaque {
		p.fail(at, CodeDeclarator, "%s returns opaque %s by value", at.text, t.Name)
	}
}

func (p *parser) prototype() {
	t := p.peek()
	if c, ok := keywordCode[t.text]; ok && t.kind == tIdent && (c == CodeStorageClass || c == CodeAttribute) {
		p.fail(t, c, "%s: %s", quote(t.text), Codes[c])
	}
	base, constTok := p.typeSpec()
	typ, nptr := p.pointers(base)
	if constTok.text != "" && nptr == 0 {
		p.fail(constTok, CodeQualifier, "const outside `const T *`")
	}
	if p.peek().is("(") {
		p.fail(p.peek(), CodeDeclarator, "function pointer declarator; declare a callback typedef")
	}
	name := p.ident("a function name")
	if !p.peek().is("(") {
		if q := p.peek(); q.is(";") || q.is("=") || q.is("[") || q.is(",") {
			p.fail(name, CodeVariable, "variable %s", name.text)
		}
		p.unexpected(p.peek(), "`(`")
	}
	p.resultType(typ, name)
	open := p.i
	params := p.params(name)
	switch q := p.peek(); {
	case q.is("{"):
		p.fail(q, CodeStorageClass, "function definition %s", name.text)
	case q.kind == tIdent && keywordCode[q.text] == CodeAttribute:
		p.fail(q, CodeAttribute, "%s: %s", quote(q.text), Codes[CodeAttribute])
	case q.kind == tIdent && !keywords[q.text]:
		p.failKAndR(open, nil, name, "K&R definition %s", name.text)
	}
	p.endDecl(name)
	p.funcs = append(p.funcs, Function{Name: name.text, Result: typ, Params: params})
}

// params reads a parameter list. owner is the function or callback name.
func (p *parser) params(owner token) []Param {
	open := p.i
	p.expect("(")
	if p.peek().is(")") {
		p.failKAndR(open, nil, owner, "%s has an empty parameter list; write (void)", owner.text)
	}
	if p.peek().is("void") && p.peekN(1).is(")") {
		p.next()
		p.next()
		return nil
	}
	// An identifier list `(a, b)`: a name that is no type, then `,` or `)`.
	if t := p.peek(); t.kind == tIdent && !keywords[t.text] && !scalarNames[t.text] && (p.peekN(1).is(",") || p.peekN(1).is(")")) {
		if _, isType := p.types[t.text]; !isType {
			names := map[string]bool{}
			for k := p.i; p.toks[k].kind == tIdent; k += 2 {
				names[p.toks[k].text] = true
				if !p.toks[k+1].is(",") {
					break
				}
			}
			p.failKAndR(open, names, owner, "%s has an identifier list; write a prototype", owner.text)
		}
	}
	var params []Param
	seen := map[string]Pos{}
	for {
		p.try(func() {
			if t := p.peek(); t.is("...") {
				p.fail(t, CodeVarargs, "variadic parameter list")
			}
			base, constTok := p.typeSpec()
			typ, nptr := p.pointers(base)
			if p.peek().is("(") {
				p.fail(p.peek(), CodeDeclarator, "function pointer parameter; declare a callback typedef")
			}
			name := token{}
			if t := p.peek(); t.kind == tIdent {
				if keywordCode[t.text] == CodeAttribute {
					p.fail(t, CodeAttribute, "%s: %s", quote(t.text), Codes[CodeAttribute])
				}
				name = p.ident("a parameter name")
			}
			at := name
			if at.text == "" {
				at = p.peek()
			}
			if t := p.peek(); t.is("[") {
				// An array parameter `T name[N]` is a pointer to T (C11
				// 6.7.6.3p7); the model keeps its declared length.
				if n := p.peekN(1); n.is("]") || n.is("static") || n.is("*") || keywordCode[n.text] == CodeQualifier || n.is("const") {
					p.fail(t, CodeDeclarator, "array parameter without a constant length")
				}
				arr := p.array(typ)
				p.valueType(arr, at, "parameter")
				typ = Type{Kind: TypePointer, Elem: arr.Elem, Len: arr.Len, LenExpr: arr.LenExpr}
				nptr++
			}
			if constTok.text != "" && nptr == 0 {
				p.fail(constTok, CodeQualifier, "const outside `const T *`")
			}
			if t := p.peek(); !t.is(",") && !t.is(")") {
				p.unexpected(t, "`,` or `)`")
			}
			p.valueType(typ, at, "parameter")
			if name.text != "" {
				if prev, ok := seen[name.text]; ok {
					p.fail(name, CodeDuplicate, "parameter %s is already declared at %s", name.text, prev)
				}
				seen[name.text] = name.pos
			}
			params = append(params, Param{Name: name.text, Type: typ})
		}, p.skipParam)
		if p.peek().is(",") {
			p.next()
			continue
		}
		break
	}
	p.expect(")")
	return params
}
