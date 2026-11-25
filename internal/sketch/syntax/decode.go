package syntax

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"

	linked "ergo.local/prototype/internal/linked/compiler"
)

// The seam of capstone S2d: the tree of a source as compiler/syntax's
// parser builds it (rows, the extra pool, token and comment columns,
// carried across the linked package as syntax.Columns packs them), built
// into this package's nodes for the Go checker. The nodes are the ones
// the Go parser built before S2d, every field: the rows keep what the
// source says (TestDecodeAgreesWithView reads them through the view as
// well), and the decoder adds what the Go parser derived from it
// (an or-pattern case's expansion, a refinement's parse function and law,
// a codec pair's round-trip law, a machine's entries by name).

// MaxNesting bounds the height, in levels, of the returned tree: the
// nesting of expressions, types, statements and blocks in any composition
// (grammar.md, Parser rules). A higher tree is S001 at the first token past
// the limit, so no input exhausts the stack of the parser or of any consumer
// of the AST (the printer, the converter, the checker). go/parser bounds
// nesting at 100 000; 1 000 leaves every consumer a small stack and is far
// above any program written by hand.
const MaxNesting = 1000

// CodeMixedMembers is the code of a type declaration that mixes fields with
// operations or laws (D1.12): the tree cannot hold the mix, so the
// parser reports it with the checker's code for the rule.
const CodeMixedMembers = "S108"

// Parse parses one source file with compiler/syntax's parser (ParseRows).
// It returns the first syntax error, an *Error with code S001 (or S108 for
// a type that mixes fields and operations), and no file; recovery is the language server's.
func Parse(src string) (*File, error) { return ParseRows(src) }

// ParseRows parses src with compiler/syntax's parser and decodes its rows.
// Its result is Parse's, failure included.
func ParseRows(src string) (*File, error) {
	cols, err := linked.Columns(context.Background(), src)
	if err != nil {
		return nil, err
	}
	return decodeColumns(src, cols)
}

// decodeColumns is the file of src's columns, or the parse's failure.
func decodeColumns(src string, cols []uint32) (*File, error) {
	if len(cols) < colHeader || cols[0] != ColumnsVersion && cols[0] != PreviousColumnsVersion {
		return nil, &LayoutError{Linked: layoutOf(cols)}
	}
	if cols[6] != 0 {
		text, err := linked.Failure(context.Background(), src)
		if err != nil {
			return nil, err
		}
		return nil, failureOf(text)
	}
	return Decode(src, cols), nil
}

// ParseFiles is Parse of each source, in the order of srcs (a package's
// files): compiler/syntax parses each file on a task of its own, at most
// GOMAXPROCS at once (syntax.ParseAll, capstone S2e), and the rows are
// decoded here in file order. files[i] and errs[i] are Parse(srcs[i])'s.
func ParseFiles(srcs []string) (files []*File, errs []error) {
	files = make([]*File, len(srcs))
	errs = make([]error, len(srcs))
	if len(srcs) == 0 {
		return files, errs
	}
	all, err := linked.ColumnsAll(context.Background(), srcs, runtime.GOMAXPROCS(0))
	if err != nil {
		for i := range errs {
			errs[i] = err
		}
		return files, errs
	}
	for i, cols := range all {
		files[i], errs[i] = decodeColumns(srcs[i], cols)
	}
	return files, errs
}

// ColumnsVersion is the layout of syntax.Columns this decoder reads, the
// one this tree's compiler/syntax writes (its ColumnsVersion). 3: a law's
// row gains TypeParams, its word 2 (adf427bfe), so it has 9 fixed words. 4:
// a context declaration's and literal's rows gain Fails before Needs
// (CTX-ROWS-1b), so they have 7 and 4. 5: the rows are 4's, and a failed
// parse's text carries its fix as lines after the error's line, which is
// 4's six fields byte for byte (failureOf; VET-CHECK-VAR), so both read
// alike. 6: a test row's body is optional (the harness
// member `test H` of an operation type has none), so its word holds the
// id + 1. 7: a function declaration has two extra fixed words for its
// optional implements token and contract type.
// 8: a function type ends its fixed row with the optional defects token
// (word 7), and kDieStmt (65) is added.
// 9: a law's fixed row ends with the optional quantifier token (word 9),
// and kAlwaysStmt (66) and kSometimesStmt (67) are added.
const ColumnsVersion = 9

// PreviousColumnsVersion is the layout before ColumnsVersion, which this
// decoder still reads: the linked parser writes it until scripts/linked
// regenerates that parser from this tree. A node-layout change is
// bootstrapped in two stages (go run ./scripts/linked -bootstrap): stage 1
// regenerates with the linked parser one layout behind, stage 2 with the
// parser stage 1 wrote.
const PreviousColumnsVersion = 8

// LayoutError is a parse whose rows have a layout this decoder cannot
// read: the linked parser is more than one layout behind or ahead.
type LayoutError struct{ Linked uint32 }

func (e *LayoutError) Error() string {
	return fmt.Sprintf("syntax: the linked parser writes rows of layout %d; this decoder reads %d and %d: regenerate it (go run ./scripts/linked -bootstrap) from a tree one layout apart", e.Linked, PreviousColumnsVersion, ColumnsVersion)
}

func layoutOf(cols []uint32) uint32 {
	if len(cols) == 0 {
		return 0
	}
	return cols[0]
}

// LinkedLayout is the layout of the rows the linked parser writes.
func LinkedLayout() (uint32, error) {
	cols, err := linked.Columns(context.Background(), "")
	if err != nil {
		return 0, err
	}
	return layoutOf(cols), nil
}

// colHeader is the words of the header: version, tokens, rows, extra
// words, comments, comment groups, failed.
const colHeader = 7

// failureOf is the *Error of dump.Failure's text. Its first line is
// "code offset length line column message"; each further line is one edit
// of the error's fix, "fix class offset length "text"", the text Go-quoted
// (compiler/syntax/dump FixLines). Every edit names the same class.
func failureOf(text string) error {
	lines := strings.Split(text, "\n")
	f := strings.SplitN(lines[0], " ", 6)
	if len(f) != 6 {
		return fmt.Errorf("syntax: a failure the decoder cannot read: %q", text)
	}
	n := func(s string) int { v, _ := strconv.Atoi(s); return v }
	e := &Error{Code: f[0], Offset: n(f[1]), Length: n(f[2]), Line: n(f[3]), Column: n(f[4]), Message: f[5]}
	for _, line := range lines[1:] {
		x := strings.SplitN(line, " ", 5)
		bad := len(x) != 5 || x[0] != "fix" || x[1] != "exact" && x[1] != "suggested" || e.Fix != nil && e.Fix.Class != x[1]
		var t string
		if !bad {
			var err error
			t, err = strconv.Unquote(x[4])
			bad = err != nil
		}
		if bad {
			return fmt.Errorf("syntax: a failure's fix the decoder cannot read: %q", text)
		}
		if e.Fix == nil {
			e.Fix = &Fix{Class: x[1]}
		}
		e.Fix.Edits = append(e.Fix.Edits, Edit{Offset: n(x[2]), Length: n(x[3]), Text: t})
	}
	return e
}

// rowDecoder reads the columns of one parse.
type rowDecoder struct {
	src string
	// layout is the rows' layout, ColumnsVersion or PreviousColumnsVersion.
	layout uint32
	// The row columns: kind, token, a and b of each row.
	rowKind, rowTok, rowA, rowB []uint32
	extra                       []uint32
	toks                        []token
	groups                      []*CommentGroup
}

// Decode builds the tree of src from cols, the words syntax.Columns packs
// for a parse that did not fail.
func Decode(src string, cols []uint32) *File {
	ntok, nrows, nextra, ncmt, ngroups := int(cols[1]), int(cols[2]), int(cols[3]), int(cols[4]), int(cols[5])
	d := &rowDecoder{src: src, layout: cols[0]}
	w := colHeader
	d.toks = tokensOfColumns(src, cols[w:w+ntok], cols[w+ntok:w+2*ntok], cols[w+2*ntok:w+3*ntok])
	w += 3 * ntok
	col := func() []uint32 { c := cols[w : w+nrows : w+nrows]; w += nrows; return c }
	d.rowKind, d.rowTok, d.rowA, d.rowB = col(), col(), col(), col()
	d.extra = cols[w : w+nextra]
	w += nextra
	comments := make([]*Comment, ncmt)
	for c := range comments {
		start, end := int(cols[w]), int(cols[w+1])
		text := src[start:end]
		if strings.HasPrefix(text, "//") {
			text = strings.TrimRight(text, "\r")
		} else {
			text = strings.ReplaceAll(text, "\r", "")
		}
		comments[c] = &Comment{Slash: Pos(start), Text: text, EndPos: Pos(end)}
		w += 2
	}
	d.groups = make([]*CommentGroup, ngroups)
	for g := range d.groups {
		first, last := int(cols[w]), int(cols[w+1])
		d.groups[g] = &CommentGroup{List: comments[first : last+1 : last+1]}
		w += 2
	}
	f := d.file(uint32(nrows - 1))
	f.Src = src
	f.lines = lineStarts(src)
	if len(d.groups) > 0 {
		f.Comments = d.groups
	}
	return f
}

// lineStarts is the offsets of src's line starts, as the scanner keeps them.
func lineStarts(src string) []int {
	lines := []int{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			lines = append(lines, i+1)
		}
	}
	return lines
}

func (d *rowDecoder) kind(r uint32) uint8 { return uint8(d.rowKind[r]) }
func (d *rowDecoder) tok(r uint32) uint32 { return d.rowTok[r] }
func (d *rowDecoder) a(r uint32) uint32   { return d.rowA[r] }
func (d *rowDecoder) b(r uint32) uint32   { return d.rowB[r] }

// fixed is word k of the f fixed words that end row r's record.
func (d *rowDecoder) fixed(r uint32, f, k int) uint32 {
	return d.extra[int(d.a(r))+int(d.b(r))-f+k]
}

// item is item j of row r's record; items is their number before f
// fixed words.
func (d *rowDecoder) item(r uint32, j int) uint32 { return d.extra[int(d.a(r))+j] }
func (d *rowDecoder) items(r uint32, f int) int   { return int(d.b(r)) - f }

// pos is the offset of token j; opos of a word holding a token or none
// (0), NoPos for none.
func (d *rowDecoder) pos(j uint32) Pos { return d.toks[j].pos }
func (d *rowDecoder) opos(w uint32) Pos {
	if w == 0 {
		return NoPos
	}
	return d.toks[w-1].pos
}

// doc is the comment group a word holds (index+1, 0 for none).
func (d *rowDecoder) doc(w uint32) *CommentGroup {
	if w == 0 {
		return nil
	}
	return d.groups[w-1]
}

func (d *rowDecoder) text(j uint32) string { return d.toks[j].lit }

// one is the node of row r as T; opt the node a word holds (row+1, 0 for
// none), T's zero for none.
func one[T Node](d *rowDecoder, r uint32) T { return d.node(r).(T) }

func opt[T Node](d *rowDecoder, w uint32) T {
	if w == 0 {
		var zero T
		return zero
	}
	return d.node(w - 1).(T)
}

// list is the n items of row r's record from item j, as T; nil for none.
func list[T Node](d *rowDecoder, r uint32, j, n int) []T {
	if n == 0 {
		return nil
	}
	out := make([]T, n)
	for i := range out {
		out[i] = one[T](d, d.item(r, j+i))
	}
	return out
}

func (d *rowDecoder) ident(r uint32) *Ident {
	j := d.tok(r)
	return &Ident{NamePos: d.pos(j), Name: d.text(j)}
}

func (d *rowDecoder) basicLit(r uint32) *BasicLit {
	t := d.toks[d.tok(r)]
	return &BasicLit{ValuePos: t.pos, Kind: t.tok, Value: t.lit, ValueEnd: t.end}
}

func (d *rowDecoder) file(r uint32) *File {
	f := &File{EOF: d.pos(d.tok(r))}
	n := d.items(r, 0)
	for i := 0; i < n; i++ {
		row := d.item(r, i)
		decl := one[Decl](d, row)
		f.Decls = append(f.Decls, decl)
		if td, ok := decl.(*TypeDecl); ok && td.Where != nil && td.TypeParams == nil {
			f.Decls = append(f.Decls, d.refinement(row, td)...)
		}
	}
	derivePairs(f, d.src)
	return f
}

// refinement is the parse function and law derived from the refined type
// of row r (refine.go): its base type and predicate decoded once more, and
// the predicate's text from its tokens.
func (d *rowDecoder) refinement(r uint32, td *TypeDecl) []Decl {
	spec, where := d.fixed(r, 10, 4), d.fixed(r, 10, 6)
	base := func() Expr { return opt[Expr](d, spec) }
	pred := func() Expr { return opt[Expr](d, where) }
	return deriveRefinement(td, base, pred, tokenText(d.src, d.toks, d.tokenAt(td.Where.Pos()), d.tokenAt(td.Where.End())))
}

// tokenAt is the first token at or after pos.
func (d *rowDecoder) tokenAt(pos Pos) int {
	return sort.Search(len(d.toks), func(j int) bool { return d.toks[j].pos >= pos })
}

func (d *rowDecoder) node(r uint32) Node {
	t, a, b := d.tok(r), d.a(r), d.b(r)
	switch d.kind(r) {
	case kImportDecl:
		return &ImportDecl{Doc: d.doc(b), Import: d.pos(t), Specs: []*ImportSpec{one[*ImportSpec](d, a)}}
	case kImportSpec:
		s := &ImportSpec{NamePos: d.opos(t), Name: opt[*Ident](d, a), Path: one[*BasicLit](d, b)}
		if t != 0 {
			switch d.toks[t-1].tok {
			case GO:
				s.Kind = ImportGo
			case PERIOD:
				s.Kind = ImportDot
			default:
				s.Kind = ImportNamed
			}
		}
		return s
	case kVarDecl:
		return &VarDecl{Doc: d.doc(d.fixed(r, 4, 0)), Extern: opt[*ExternSpec](d, d.fixed(r, 4, 1)), Var: d.pos(t), Mut: d.opos(d.fixed(r, 4, 2)), Specs: []*VarSpec{one[*VarSpec](d, d.fixed(r, 4, 3))}}
	case kVarSpec:
		s := &VarSpec{Names: list[*Ident](d, r, 0, d.items(r, 4)), Pattern: opt[Expr](d, d.fixed(r, 4, 0)), Type: opt[Expr](d, d.fixed(r, 4, 1)), Assign: d.opos(d.fixed(r, 4, 2))}
		if v := opt[Expr](d, d.fixed(r, 4, 3)); v != nil {
			s.Values = []Expr{v}
		}
		return s
	case kTypeDecl:
		return &TypeDecl{
			Doc:           d.doc(d.fixed(r, 10, 0)),
			Extern:        opt[*ExternSpec](d, d.fixed(r, 10, 1)),
			Type:          d.pos(t),
			Name:          one[*Ident](d, d.fixed(r, 10, 2)),
			TypeParams:    opt[*FieldList](d, d.fixed(r, 10, 3)),
			Spec:          opt[Expr](d, d.fixed(r, 10, 4)),
			WherePos:      d.zpos(d.fixed(r, 10, 5)),
			Where:         opt[Expr](d, d.fixed(r, 10, 6)),
			DerivePos:     d.zpos(d.fixed(r, 10, 7)),
			Derive:        opt[Expr](d, d.fixed(r, 10, 8)),
			ImplementsPos: d.zpos(d.fixed(r, 10, 9)),
			Implements:    list[Expr](d, r, 0, d.items(r, 10)),
		}
	case kFuncDecl:
		w := 9
		f := &FuncDecl{
			Doc:           d.doc(d.fixed(r, w, 0)),
			Extern:        opt[*ExternSpec](d, d.fixed(r, w, 1)),
			Func:          d.pos(t),
			Recv:          opt[*FieldList](d, d.fixed(r, w, 2)),
			Name:          one[*Ident](d, d.fixed(r, w, 3)),
			TypeParams:    opt[*FieldList](d, d.fixed(r, w, 4)),
			Type:          one[*FuncType](d, d.fixed(r, w, 5)),
			Body:          opt[*BlockStmt](d, d.fixed(r, w, 6)),
			ImplementsPos: d.zpos(d.fixed(r, w, 7)),
			Implements:    opt[Expr](d, d.fixed(r, w, 8)),
		}
		return f
	case kContextDecl:
		// Layouts 5 and 6 put Fails at word 4 (layout 3, which had no such
		// word, is no longer read).
		return &ContextDecl{Doc: d.doc(d.fixed(r, 7, 0)), Let: d.pos(t), Context: d.pos(d.fixed(r, 7, 1)), Name: one[*Ident](d, d.fixed(r, 7, 2)), Provides: opt[*ProvidesClause](d, d.fixed(r, 7, 3)), Fails: opt[*ErrorRow](d, d.fixed(r, 7, 4)), Needs: opt[*NeedsClause](d, d.fixed(r, 7, 5)), Body: one[*ContextBody](d, d.fixed(r, 7, 6))}
	case kTestDecl:
		x := &TestDecl{Doc: d.doc(d.fixed(r, 4, 0)), Test: d.pos(t), Name: one[*Ident](d, d.fixed(r, 4, 1)), Params: opt[*FieldList](d, d.fixed(r, 4, 2))}
		// The harness member `test H` has no body.
		x.Body = opt[*BlockStmt](d, d.fixed(r, 4, 3))
		return x
	case kLawDecl:
		w, s := 10, 1
		if d.layout == PreviousColumnsVersion {
			w = 9
		}
		l := &LawDecl{Doc: d.doc(d.fixed(r, w, 0)), Law: d.pos(t), Name: one[*Ident](d, d.fixed(r, w, 1)), Params: opt[*FieldList](d, d.fixed(r, w, 2+s)), Body: opt[*BlockStmt](d, d.fixed(r, w, 3+s)), Reach: opt[*LawReach](d, d.fixed(r, w, 4+s))}
		l.TypeParams = opt[*FieldList](d, d.fixed(r, w, 2))
		if w == 10 {
			if q := d.fixed(r, w, 9); q != 0 {
				l.QuantPos = d.pos(q - 1)
				l.Quant = d.text(q - 1)
			}
		}
		switch d.fixed(r, w, 5+s) {
		case 1:
			l.Rewrite = &LawRewrite{When: d.opos(d.fixed(r, w, 6+s)), Eq: d.opos(d.fixed(r, w, 7+s))}
		case 2:
			equation(l, d.opos(d.fixed(r, w, 7+s)))
		}
		return l
	case kLawReach:
		x := &LawReach{Never: d.opos(d.fixed(r, 6, 0)), Word: d.pos(t), Path: d.fixed(r, 6, 1) != 0, Lbrack: d.opos(d.fixed(r, 6, 2)), Targets: list[*Ident](d, r, 0, d.items(r, 6)), Rbrack: d.opos(d.fixed(r, 6, 3)), Pred: opt[*Ident](d, d.fixed(r, 6, 5))}
		if d.fixed(r, 6, 4) != 0 {
			x.Invariant = d.text(t)
		}
		return x
	case kExternSpec:
		return &ExternSpec{Extern: d.pos(t), Go: d.pos(a), Target: one[*BasicLit](d, b)}
	case kContextBody:
		return &ContextBody{Lbrace: d.pos(t), Entries: list[*ContextEntry](d, r, 0, d.items(r, 1)), Rbrace: d.pos(d.fixed(r, 1, 0))}
	case kContextEntry:
		e := &ContextEntry{Doc: d.doc(d.fixed(r, 5, 0)), Replace: d.opos(d.fixed(r, 5, 1)), Key: one[Expr](d, d.fixed(r, 5, 2)), Keys: list[Expr](d, r, 0, d.items(r, 5)), Assign: d.opos(d.fixed(r, 5, 3)), Value: opt[Expr](d, d.fixed(r, 5, 4))}
		switch {
		case e.Replace != NoPos:
			e.Kind = ContextReplace
		case e.Value == nil:
			e.Kind = ContextEmbed
		}
		return e
	case kField:
		f := &Field{
			Doc:      d.doc(d.fixed(r, 11, 0)),
			Names:    list[*Ident](d, r, 0, d.items(r, 11)),
			Type:     opt[Expr](d, d.fixed(r, 11, 1)),
			Key:      opt[*BasicLit](d, d.fixed(r, 11, 2)),
			Dots:     d.zpos(d.fixed(r, 11, 5)),
			Where:    opt[Expr](d, d.fixed(r, 11, 6)),
			WherePos: d.zpos(d.fixed(r, 11, 7)),
			PurePos:  d.zpos(d.fixed(r, 11, 8)),
		}
		if w := d.fixed(r, 11, 3); w != 0 {
			f.Any = d.pos(d.tok(w - 1))
		}
		if w := d.fixed(r, 11, 4); w != 0 {
			f.Lent = d.pos(d.tok(w - 1))
		}
		f.Include = d.fixed(r, 11, 5) != 0
		f.Pure = d.fixed(r, 11, 8) != 0
		if w := d.fixed(r, 11, 9); w != 0 {
			f.Own = &Word{At: d.pos(w - 1)}
		}
		if w := d.fixed(r, 11, 10); w != 0 {
			f.Mut = &Word{At: d.pos(w - 1)}
		}
		return f
	case kFieldList:
		return &FieldList{Opening: d.opos(d.fixed(r, 2, 0)), List: list[*Field](d, r, 0, d.items(r, 2)), Closing: d.opos(d.fixed(r, 2, 1))}
	case kNeedsClause:
		return &NeedsClause{Needs: d.pos(t), Names: list[Expr](d, r, 0, d.items(r, 0))}
	case kTakesClause:
		return &TakesClause{Takes: d.pos(t), Names: list[*Ident](d, r, 0, d.items(r, 0))}
	case kProvidesClause:
		return &ProvidesClause{Provides: d.pos(t), Names: list[Expr](d, r, 0, d.items(r, 0))}
	case kMachineEntry:
		return &MachineEntry{Doc: d.doc(b), NamePos: d.pos(t), Name: d.text(t), Colon: d.pos(t + 1), Value: one[Expr](d, a)}
	case kCatchArm:
		nl := int(d.fixed(r, 2, 0))
		x := &CatchArm{Case: d.pos(t), Colon: d.pos(d.fixed(r, 2, 1)), Body: list[Stmt](d, r, nl, d.items(r, 2)-nl)}
		x.Types, x.Written = expandList(list[Expr](d, r, 0, nl))
		return x
	case kCaseClause:
		nl := int(d.fixed(r, 2, 0))
		x := &CaseClause{Case: d.pos(t), Colon: d.pos(d.fixed(r, 2, 1)), Body: list[Stmt](d, r, nl, d.items(r, 2)-nl)}
		x.List, x.Written = expandList(list[Expr](d, r, 0, nl))
		return x
	case kVariantSpec:
		return &VariantSpec{Name: one[*Ident](d, d.fixed(r, 3, 0)), TypeArgs: opt[*FieldList](d, d.fixed(r, 3, 1)), Fields: one[*FieldList](d, d.fixed(r, 3, 2))}
	case kIdent:
		return d.ident(r)
	case kBasicLit:
		return d.basicLit(r)
	case kCompositeLit:
		return &CompositeLit{Type: opt[Expr](d, d.fixed(r, 4, 0)), Lbrace: d.pos(t), DotDot: d.zpos(d.fixed(r, 4, 1)), Base: opt[Expr](d, d.fixed(r, 4, 2)), Elts: list[Expr](d, r, 0, d.items(r, 4)), Rbrace: d.pos(d.fixed(r, 4, 3))}
	case kKeyValueExpr:
		return &KeyValueExpr{Key: one[Expr](d, a), Colon: d.pos(t), Value: one[Expr](d, b)}
	case kFuncLit:
		return &FuncLit{Type: one[*FuncType](d, a), Body: one[*BlockStmt](d, b)}
	case kParenExpr:
		return &ParenExpr{Lparen: d.pos(t), X: one[Expr](d, a), Rparen: d.pos(b)}
	case kSelectorExpr:
		return &SelectorExpr{X: one[Expr](d, a), Sel: one[*Ident](d, b)}
	case kIndexExpr:
		return &IndexExpr{X: one[Expr](d, d.fixed(r, 2, 0)), Lbrack: d.pos(t), Indices: list[Expr](d, r, 0, d.items(r, 2)), Rbrack: d.pos(d.fixed(r, 2, 1))}
	case kSliceExpr:
		return &SliceExpr{X: one[Expr](d, d.fixed(r, 6, 0)), Lbrack: d.pos(t), Low: opt[Expr](d, d.fixed(r, 6, 1)), High: opt[Expr](d, d.fixed(r, 6, 2)), Max: opt[Expr](d, d.fixed(r, 6, 3)), Slice3: d.fixed(r, 6, 4) != 0, Rbrack: d.pos(d.fixed(r, 6, 5))}
	case kCallExpr:
		return &CallExpr{Fun: one[Expr](d, d.fixed(r, 5, 0)), Lparen: d.opos(d.fixed(r, 5, 1)), Args: list[Expr](d, r, 0, d.items(r, 5)), Rparen: d.opos(d.fixed(r, 5, 2)), Trailing: opt[*BlockStmt](d, d.fixed(r, 5, 3)), TrailingFunc: d.fixed(r, 5, 4) != 0}
	case kStarExpr:
		return &StarExpr{Star: d.pos(t), X: one[Expr](d, a), Mut: d.toks[t].tok == MUT}
	case kUnaryExpr:
		return &UnaryExpr{OpPos: d.pos(t), Op: d.toks[t].tok, X: one[Expr](d, a)}
	case kBinaryExpr:
		return &BinaryExpr{X: one[Expr](d, a), OpPos: d.pos(t), Op: d.toks[t].tok, Y: one[Expr](d, b)}
	case kTryExpr:
		return &TryExpr{Try: d.pos(t), X: one[Expr](d, a)}
	case kCatchExpr:
		return &CatchExpr{X: one[Expr](d, d.fixed(r, 2, 0)), Catch: d.pos(t), Lbrace: d.pos(t + 1), Arms: list[*CatchArm](d, r, 0, d.items(r, 2)), Rbrace: d.pos(d.fixed(r, 2, 1))}
	case kContextLit:
		// Layouts 5 and 6 put Fails at word 1 (layout 3 is no longer read).
		return &ContextLit{Context: d.pos(t), Provides: opt[*ProvidesClause](d, d.fixed(r, 4, 0)), Fails: opt[*ErrorRow](d, d.fixed(r, 4, 1)), Needs: opt[*NeedsClause](d, d.fixed(r, 4, 2)), Body: one[*ContextBody](d, d.fixed(r, 4, 3))}
	case kArrayType:
		x := &ArrayType{Lbrack: d.pos(t), Elt: one[Expr](d, a)}
		if b != 0 {
			x.Len, x.LenPos = d.text(b-1), d.pos(b-1)
		}
		return x
	case kMapType:
		x := &MapType{Map: d.pos(d.tok(d.fixed(r, 2, 0))), Lbrack: d.pos(t), Key: one[Expr](d, d.item(r, 0)), Value: one[Expr](d, d.item(r, 1)), Rbrack: d.pos(d.fixed(r, 2, 1))}
		if d.items(r, 2) == 3 {
			// `Map[K, V, H]`: the Hashing provider (CONSTRAINTS C5).
			x.Hashing = one[Expr](d, d.item(r, 2))
		}
		return x
	case kFuncType:
		w := 8
		x := &FuncType{Escaping: d.opos(d.fixed(r, w, 0)), EntryPos: d.zpos(d.fixed(r, w, 1)), Func: d.opos(d.fixed(r, w, 2)), Params: one[*FieldList](d, d.fixed(r, w, 3)), Results: opt[*FieldList](d, d.fixed(r, w, 4)), Needs: opt[*NeedsClause](d, d.fixed(r, w, 5)), Takes: opt[*TakesClause](d, d.fixed(r, w, 6))}
		x.Entry = d.fixed(r, w, 1) != 0
		x.Defects = d.fixed(r, w, 7) != 0
		x.DefectsPos = d.zpos(d.fixed(r, w, 7))
		if d.items(r, w) == 1 {
			// An operation's own type parameters (GENERIC-OP).
			x.TypeParams = one[*FieldList](d, d.item(r, 0))
		}
		return x
	case kStructType:
		return &StructType{Fields: one[*FieldList](d, a)}
	case kInterfaceType:
		x := &InterfaceType{Lbrace: d.opos(d.fixed(r, 2, 0)), Rbrace: d.opos(d.fixed(r, 2, 1))}
		members := list[Node](d, r, 0, d.items(r, 2))
		if x.Lbrace == NoPos {
			for _, m := range members {
				x.Union = append(x.Union, m.(Expr))
			}
			return x
		}
		for _, m := range members {
			switch m := m.(type) {
			case *Field:
				if m.Include {
					x.Includes = append(x.Includes, m)
				} else {
					x.Methods = append(x.Methods, m)
				}
			case *LawDecl:
				x.Laws = append(x.Laws, m)
			case *TestDecl:
				// `test H`, the harness member (runtime.md, Tests: harnesses).
				x.Harnesses = append(x.Harnesses, &Harness{Doc: m.Doc, Test: m.Test, Name: m.Name})
			case *UnaryExpr:
				// `~T`, a term of the type-set member (CONSTRAINTS C1).
				x.Terms = append(x.Terms, &TypeTerm{Tilde: m.OpPos, Type: m.X})
			case Expr:
				// A bare term `T` of the type-set member.
				x.Terms = append(x.Terms, &TypeTerm{Tilde: NoPos, Type: m})
			}
		}
		return x
	case kErrorRow:
		return &ErrorRow{Error: d.pos(t), Types: list[Expr](d, r, 0, d.items(r, 0))}
	case kMachineType:
		rbrack := d.fixed(r, 4, 2)
		x := &MachineType{Machine: d.pos(t), Lbrack: d.pos(t + 1), State: one[Expr](d, d.fixed(r, 4, 0)), Signal: one[Expr](d, d.fixed(r, 4, 1)), Rbrack: d.pos(rbrack), Lbrace: d.pos(rbrack + 1), Rbrace: d.pos(d.fixed(r, 4, 3))}
		for _, m := range list[Node](d, r, 0, d.items(r, 4)) {
			switch m := m.(type) {
			case *MachineEntry:
				x.Entries = append(x.Entries, m)
				switch m.Name {
				case "initial":
					x.Initial = m.Value
				case "step":
					x.Step = m.Value
				case "work":
					x.Work = m.Value
				case "exit":
					x.Exit = m.Value
				default:
					x.After = m.Value
				}
			case *LawDecl:
				x.Laws = append(x.Laws, m)
			}
		}
		return x
	case kTupleExpr:
		return &TupleExpr{Lparen: d.opos(d.fixed(r, 2, 0)), Elts: list[Expr](d, r, 0, d.items(r, 2)), Rparen: d.opos(d.fixed(r, 2, 1))}
	case kAsPattern:
		return &AsPattern{X: one[Expr](d, a), As: d.pos(t), Name: one[*Ident](d, b)}
	case kOrPattern:
		x := &OrPattern{Alts: list[Expr](d, r, 0, d.items(r, 0))}
		for _, alt := range x.Alts[1:] {
			x.Bars = append(x.Bars, d.toks[d.tokenAt(alt.Pos())-1].pos)
		}
		return x
	case kInterpolatedString:
		x := &InterpolatedString{}
		for i, part := range list[Node](d, r, 0, d.items(r, 0)) {
			if i%2 == 0 {
				x.Texts = append(x.Texts, part.(*BasicLit))
			} else {
				x.Exprs = append(x.Exprs, part.(Expr))
			}
		}
		return x
	case kDeclStmt:
		return &DeclStmt{Decl: one[*VarDecl](d, a)}
	case kExprStmt:
		return &ExprStmt{X: one[Expr](d, a)}
	case kAssignStmt:
		return &AssignStmt{Lhs: []Expr{one[Expr](d, a)}, TokPos: d.pos(t), Tok: d.toks[t].tok, Rhs: []Expr{one[Expr](d, b)}}
	case kReturnStmt:
		x := &ReturnStmt{Return: d.pos(t)}
		if v := opt[Expr](d, a); v != nil {
			x.Results = []Expr{v}
		}
		return x
	case kBranchStmt:
		return &BranchStmt{TokPos: d.pos(t), Tok: d.toks[t].tok}
	case kBlockStmt:
		return &BlockStmt{Lbrace: d.pos(t), List: list[Stmt](d, r, 0, d.items(r, 1)), Rbrace: d.pos(d.fixed(r, 1, 0))}
	case kIfStmt:
		return &IfStmt{If: d.pos(t), Cond: one[Expr](d, d.fixed(r, 4, 0)), Body: one[*BlockStmt](d, d.fixed(r, 4, 1)), ElsePos: d.opos(d.fixed(r, 4, 2)), Else: opt[Stmt](d, d.fixed(r, 4, 3))}
	case kSwitchStmt:
		return &SwitchStmt{Switch: d.pos(t), Tag: one[Expr](d, d.fixed(r, 3, 0)), Lbrace: d.pos(d.fixed(r, 3, 1)), Clauses: list[*CaseClause](d, r, 0, d.items(r, 3)), Rbrace: d.pos(d.fixed(r, 3, 2))}
	case kTypeSwitchStmt:
		x := &TypeSwitchStmt{Switch: d.pos(t), Lbrace: d.pos(d.fixed(r, 3, 1)), Clauses: list[*CaseClause](d, r, 0, d.items(r, 3)), Rbrace: d.pos(d.fixed(r, 3, 2))}
		subject := d.fixed(r, 3, 0)
		if d.kind(subject) == kTupleExpr {
			for _, e := range list[Expr](d, subject, 0, d.items(subject, 2)) {
				x.Subjects = append(x.Subjects, &TypeGuard{X: e})
			}
		} else {
			x.Subjects = []*TypeGuard{{X: one[Expr](d, subject)}}
		}
		return x
	case kForStmt:
		return &ForStmt{For: d.pos(t), Cond: opt[Expr](d, a), Body: one[*BlockStmt](d, b)}
	case kRangeStmt:
		return &RangeStmt{For: d.pos(t), Key: opt[Expr](d, d.fixed(r, 5, 0)), Value: one[Expr](d, d.fixed(r, 5, 1)), Tok: DEFINE, Range: d.pos(d.fixed(r, 5, 2)), X: one[Expr](d, d.fixed(r, 5, 3)), Body: one[*BlockStmt](d, d.fixed(r, 5, 4))}
	case kAssertStmt:
		return &AssertStmt{Assert: d.pos(t), X: one[Expr](d, a)}
	case kSkipStmt:
		reason := d.toks[a]
		return &SkipStmt{Skip: d.pos(t), ReasonPos: reason.pos, Reason: reason.lit, ReasonEnd: reason.end}
	case kDieStmt:
		return &DieStmt{Die: d.pos(t), X: one[Expr](d, a)}
	case kAlwaysStmt:
		return &AlwaysStmt{Always: d.pos(t), X: one[Expr](d, a)}
	case kSometimesStmt:
		return &SometimesStmt{Sometimes: d.pos(t), X: one[Expr](d, a)}
	}
	panic(fmt.Sprintf("syntax: row %d has kind %d, which the decoder does not know", r, d.kind(r)))
}

// zpos is the offset of the token a word holds, 0 for none: a position
// this parser leaves unset.
func (d *rowDecoder) zpos(w uint32) Pos {
	if w == 0 {
		return 0
	}
	return d.toks[w-1].pos
}

// expandList is a case's patterns and, when one holds an or-pattern, the
// patterns as written (parser.expandCase; the Ergo parser reports a case
// that expands too far).
func expandList(list []Expr) ([]Expr, []Expr) {
	for _, x := range list {
		if holdsOr(x) {
			var out []Expr
			for _, x := range list {
				out = append(out, expandPattern(x)...)
			}
			return out, list
		}
	}
	return list, nil
}

// The row kinds of compiler/syntax (tree.ergo).
const (
	kFile               = 1
	kImportDecl         = 2
	kImportSpec         = 3
	kVarDecl            = 4
	kVarSpec            = 5
	kTypeDecl           = 6
	kFuncDecl           = 7
	kContextDecl        = 8
	kTestDecl           = 9
	kLawDecl            = 10
	kLawReach           = 11
	kExternSpec         = 12
	kContextBody        = 13
	kContextEntry       = 14
	kField              = 15
	kFieldList          = 16
	kNeedsClause        = 17
	kTakesClause        = 18
	kProvidesClause     = 19
	kMachineEntry       = 20
	kCatchArm           = 21
	kCaseClause         = 22
	kVariantSpec        = 23
	kIdent              = 24
	kBasicLit           = 25
	kCompositeLit       = 26
	kKeyValueExpr       = 27
	kFuncLit            = 28
	kParenExpr          = 29
	kSelectorExpr       = 30
	kIndexExpr          = 31
	kSliceExpr          = 32
	kCallExpr           = 34
	kStarExpr           = 35
	kUnaryExpr          = 36
	kBinaryExpr         = 37
	kTryExpr            = 38
	kCatchExpr          = 39
	kContextLit         = 40
	kArrayType          = 41
	kMapType            = 42
	kFuncType           = 43
	kStructType         = 44
	kInterfaceType      = 45
	kErrorRow           = 46
	kMachineType        = 47
	kTupleExpr          = 48
	kAsPattern          = 49
	kOrPattern          = 50
	kInterpolatedString = 51
	kDeclStmt           = 52
	kExprStmt           = 53
	kAssignStmt         = 54
	kReturnStmt         = 55
	kBranchStmt         = 56
	kBlockStmt          = 57
	kIfStmt             = 58
	kSwitchStmt         = 59
	kTypeSwitchStmt     = 60
	kForStmt            = 61
	kRangeStmt          = 62
	kAssertStmt         = 63
	kSkipStmt           = 64
	kDieStmt            = 65
	kAlwaysStmt         = 66
	kSometimesStmt      = 67
)

// equation sets a top-level rewrite law's Equation (word 2; LAWFFI LF6a)
// from its row's body, the two blocks of one expression each that
// compiler/syntax checked, and makes its Body the property it states,
// lhs == rhs at the ==, as the Go parser's parseLawEquation did.
func equation(l *LawDecl, eq Pos) {
	lhsB, rhsB := l.Body.List[0].(*BlockStmt), l.Body.List[1].(*BlockStmt)
	lhs, rhs := lhsB.List[0].(*ExprStmt).X, rhsB.List[0].(*ExprStmt).X
	l.Equation = &LawEquation{LhsLbrace: lhsB.Lbrace, LhsRbrace: lhsB.Rbrace, RhsLbrace: rhsB.Lbrace, RhsRbrace: rhsB.Rbrace}
	l.Body = &BlockStmt{Lbrace: lhsB.Lbrace, List: []Stmt{&ExprStmt{X: &BinaryExpr{X: lhs, OpPos: eq, Op: EQL, Y: rhs}}}, Rbrace: rhsB.Rbrace}
}
