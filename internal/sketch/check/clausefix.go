package check

import (
	"fmt"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Options select checker modes that are off by default. Every check,
// build and test path checks with none: each function declaration is
// checked against its written signature (q137), an unwritten fails or
// needs clause is the empty clause, and a body that exceeds a clause is
// S101 or S102 with the clause to write and a ClauseFix. Literals and
// blocks take their rows from context.
type Options struct {
	// Infer is ergo fix's clause writer (EXPLICIT F9): the checker infers
	// the unwritten rows of unexported functions with a body, as before
	// q137, and records them as the clauses that write them
	// (Package.Clauses). No check, build or test path sets it.
	Infer bool
}

// CheckOptions checks like Check, in the modes opts selects.
func CheckOptions(path string, files []*syntax.File, imp Importer, opts Options) (*Package, []Diagnostic) {
	return check(path, files, imp, opts)
}

// ClauseFix is the edit that writes one clause of a function's signature:
// the text Text inserted at byte offset InsertAt of the file File, and the
// imports (paths) the file must add to spell it. Row is the whole clause,
// written elements first in their written order. Text is empty when an
// element cannot be spelled.
type ClauseFix struct {
	File     int
	Clause   string // "fails" or "needs"
	Function string
	Row      []string
	InsertAt int
	Text     string
	// Open, when not zero, is where "(" goes, with ")" at InsertAt before
	// Text: a lone result of function type would otherwise take the
	// clause as its own row (`func m() (func()) needs C`).
	Open    int
	Imports []string
	// Signature is the function's header with the clause written, as the
	// formatter prints it (the diagnostic's "write:" text).
	Signature string
	// hidden are the prelude type names a type of the package hides that
	// the clause must name (spell): it cannot be written there.
	hidden []string
}

// pendingClause is one site where a function's body exceeds a written
// clause: reported when the body ends, with the whole clause
// (clauseReport).
type pendingClause struct {
	n     syntax.Node
	kind  Kind
	extra *Row // the site's elements outside the written clause
	// what is the subject of a capability site's S102 ("" for
	// describeCharge(n)): a release names its Close (chargeAs).
	what string
}

// pend records that the body of the function declaration being checked
// exceeds its clause of kind at n (what, as for chargeAs). It reports
// false when the target is not a declaration's or Options.Infer holds,
// so the caller reports as before.
func (c *checker) pend(t *target, n syntax.Node, kind Kind, extra *Row, what string) bool {
	if c.opts.Infer || t.decl == nil {
		return false
	}
	if c.silent > 0 {
		return true
	}
	t.pending = append(t.pending, pendingClause{n: n, kind: kind, extra: extra, what: what})
	if kind == KindErrors {
		t.missErrors = t.missErrors.union(c.types, extra)
	} else {
		t.missUses = t.missUses.union(c.types, extra)
	}
	return true
}

// clauseReport reports each pending site of the declaration's body with
// the whole clause to write: every site of one clause prints one text.
func (c *checker) clauseReport(t *target) {
	if len(t.pending) == 0 {
		return
	}
	f := t.decl
	fixes := map[Kind]*ClauseFix{}
	for _, kind := range []Kind{KindErrors, KindUses} {
		missing := t.missErrors
		if kind == KindUses {
			missing = t.missUses
		}
		if !missing.Empty() {
			fixes[kind] = c.clauseFix(f, kind, missing)
		}
	}
	for _, p := range t.pending {
		fix := fixes[p.kind]
		write := "write: " + fix.Signature
		for _, path := range fix.Imports {
			write += fmt.Sprintf(" (import %s to write it)", path)
		}
		attach := true
		if len(fix.hidden) > 0 {
			// No clause the package can write names it (spell): the
			// checker asks for the rename instead, and ergo fix writes
			// nothing.
			slices.Sort(fix.hidden)
			write = fmt.Sprintf("the clause cannot be written here, since this package's type %s hides the prelude's (types.md, Scope 2); rename the package's type", strings.Join(fix.hidden, ", "))
			attach = false
		}
		var msg, code string
		if p.kind == KindErrors {
			code = CodeUndeclared
			// The text before "; write:" is the message every S101 has.
			msg = fmt.Sprintf("%s escapes %s, whose error row %s does not list it; %s", rowText(p.extra), t.name, rowText(t.declErrors), write)
		} else {
			code = CodeMissingCap
			what := p.what
			if what == "" {
				what = describeCharge(p.n)
			}
			msg = fmt.Sprintf("%s requires %s, which is not in the capability row of %s and no with provides it; %s", what, p.extra.String(), t.name, write)
		}
		before := len(c.diags)
		c.errorf(p.n, code, "%s", msg)
		if attach && len(c.diags) > before {
			copied := *fix
			c.diags[len(c.diags)-1].Fix = &copied
		}
	}
}

// inferredClauses lists, after a check with inference, the inferred rows
// of the package's functions as the clauses that write them.
func (c *checker) inferredClauses() []ClauseFix {
	var out []ClauseFix
	for _, f := range c.pkg.Funcs {
		for _, v := range []*RowVar{f.errVar, f.useVar} {
			if v == nil || f.file == nil || f.Decl == nil {
				continue
			}
			row := v.sol.known(c.types)
			if row.Empty() {
				continue
			}
			out = append(out, *c.clauseFix(f, v.Kind, row))
		}
	}
	return out
}

// clauseFix is the edit that adds the elements of missing to f's clause
// of kind: after the written elements, or as a new clause after the
// results (fails) or after the fails clause (needs).
func (c *checker) clauseFix(f *Func, kind Kind, missing *Row) *ClauseFix {
	d := f.Decl
	ft := d.Type
	fix := &ClauseFix{File: f.file.index, Function: f.Name, Clause: "fails"}
	if kind == KindUses {
		fix.Clause = "needs"
	}
	written := writtenRow(f.file.syntax.Src, d, kind)
	var add []string
	spellable := true
	for _, e := range missing.Elems() {
		text, imps, hidden, ok := c.spell(f.file, e)
		if !ok {
			spellable = false
		}
		for _, h := range hidden {
			if !containsString(fix.hidden, h) {
				fix.hidden = append(fix.hidden, h)
			}
		}
		add = append(add, text)
		for _, imp := range imps {
			if !containsString(fix.Imports, imp) {
				fix.Imports = append(fix.Imports, imp)
			}
		}
	}
	// By spelling: the identity order of a row follows the declarations, and
	// the text must not depend on their order (FuzzCheck).
	slices.Sort(add)
	slices.Sort(fix.Imports)
	fix.Row = append(append([]string(nil), written...), add...)
	joined := strings.Join(add, " | ")
	switch {
	case kind == KindErrors && errorRow(ft) != nil && len(errorRow(ft).Types) > 0:
		er := errorRow(ft)
		fix.InsertAt = int(er.Types[len(er.Types)-1].End())
		fix.Text = " | " + joined
	case kind == KindUses && ft.Needs != nil && len(ft.Needs.Names) > 0:
		fix.InsertAt = int(ft.Needs.Names[len(ft.Needs.Names)-1].End())
		fix.Text = " | " + joined
	default:
		fix.InsertAt = int(clauseAt(ft))
		fix.Text = " " + fix.Clause + " " + joined
		if r := ft.Results; r != nil && !r.Opening.IsValid() && len(r.List) == 1 {
			if _, ok := r.List[0].Type.(*syntax.FuncType); ok {
				fix.Open = int(r.List[0].Type.Pos())
			}
		}
	}
	fix.Signature = signatureText(f.file.syntax.Src, d, fix)
	if !spellable {
		fix.Text, fix.Open = "", 0
	}
	return fix
}

// clauseAt is where a new fails or needs clause goes: after the results
// and the fails clause, before needs and takes.
func clauseAt(ft *syntax.FuncType) syntax.Pos {
	at := ft.Params.End()
	if r := ft.Results; r != nil {
		at = max(at, r.End())
		for _, f := range r.List {
			at = max(at, f.Type.End())
		}
	}
	return at
}

func errorRow(ft *syntax.FuncType) *syntax.ErrorRow {
	if ft.Results == nil {
		return nil
	}
	for _, r := range ft.Results.List {
		if er, ok := r.Type.(*syntax.ErrorRow); ok {
			return er
		}
	}
	return nil
}

// writtenRow is the source text of the written elements of a clause.
func writtenRow(src string, d *syntax.FuncDecl, kind Kind) []string {
	var xs []syntax.Expr
	if kind == KindErrors {
		if er := errorRow(d.Type); er != nil {
			xs = er.Types
		}
	} else if d.Type.Needs != nil {
		xs = d.Type.Needs.Names
	}
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = src[x.Pos():x.End()]
	}
	return out
}

// signatureText is the header of d with fix applied, as the formatter
// prints it.
func signatureText(src string, d *syntax.FuncDecl, fix *ClauseFix) string {
	start, end, at := int(d.Func), int(d.Body.Lbrace), fix.InsertAt
	header := src[start:at] + fix.Text + src[at:end]
	if fix.Open > 0 {
		header = src[start:fix.Open] + "(" + src[fix.Open:at] + ")" + fix.Text + src[at:end]
	}
	out, err := format.Source(header + "{}\n")
	if err == nil {
		if h, ok := strings.CutSuffix(strings.TrimSuffix(out, "\n"), " {}"); ok && !strings.Contains(h, "\n") {
			return h
		}
	}
	return strings.Join(strings.Fields(header), " ")
}

// spell is how the file fi writes t: a type of its package or of the
// prelude by name, an imported type through the file's import name. A
// type of a package the file does not import is qualified by the
// package's name and returns its path to import (imps, each once). ok is
// false for a type with no source spelling. It writes every form a
// capability's argument takes (types.md, Capabilities 7: typExpr resolves
// these and no other): a named type and its instance, a slice, an array,
// a map, a tuple, a function type with its rows and takes clause, and an
// error row argument. A record, an operation type or a union is written
// only as a declaration, so it is a named type here.
//
// A prelude or universe type that a type of the package hides (types.md,
// Scope 2; L6, owner q30) has no spelling in the package: its name means
// the package's type there. hidden lists those names, and the clause is
// then not written (clauseReport says to rename the package's type).
func (c *checker) spell(fi *fileInfo, t Type) (text string, imps, hidden []string, ok bool) {
	s := &speller{c: c, fi: fi, ok: true}
	text = s.typ(t)
	return text, s.imps, s.hidden, s.ok
}

// speller spells types for spell, gathering the imports they need and the
// prelude names the package hides.
type speller struct {
	c      *checker
	fi     *fileInfo
	imps   []string
	hidden []string
	ok     bool
}

// hide marks name, a prelude or universe type's, as hidden when a type of
// the package takes it.
func (s *speller) hide(name string) {
	if _, ok := s.c.pkg.Scope.names[name].(*TypeName); ok {
		s.ok = false
		if !containsString(s.hidden, name) {
			s.hidden = append(s.hidden, name)
		}
	}
}

func (s *speller) typ(t Type) string {
	switch t := t.(type) {
	case *TypeParam:
		return t.Name
	case *Named:
		if p := t.Obj.Pkg; p == nil || p != s.c.pkg && !p.qualify {
			s.hide(t.Obj.Name)
		}
		name, imp, ok := s.c.spellName(s.fi, t.Obj)
		s.ok = s.ok && ok
		if imp != "" && !containsString(s.imps, imp) {
			s.imps = append(s.imps, imp)
		}
		if len(t.TArgs) > 0 {
			name += "[" + s.list(t.TArgs) + "]"
		}
		return name
	case *Basic:
		s.hide(t.String())
		return t.String()
	case *Slice:
		return "[]" + s.typ(t.Elem)
	case *Array:
		return fmt.Sprintf("[%d]%s", t.Len, s.typ(t.Elem))
	case *Map:
		s.hide("Map") // the syntax's Map[K, V] (grammar.md, Parser 8)
		return "Map[" + s.typ(t.Key) + ", " + s.typ(t.Elem) + "]"
	case *Pointer:
		return "*" + s.typ(t.Elem) // a borrow, as a function type's parameter
	case *Tuple:
		return "(" + s.list(t.Types) + ")"
	case *RowArg:
		return s.row(t.Row)
	case *Signature:
		return s.sig(t)
	}
	s.ok = false
	return t.String()
}

func (s *speller) list(ts []Type) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = s.typ(t)
	}
	return strings.Join(parts, ", ")
}

// row spells a row's elements as a row is written, A | B, in an order of
// their texts.
func (s *speller) row(r *Row) string {
	var parts []string
	for _, e := range r.Elems() {
		parts = append(parts, s.typ(e))
	}
	slices.Sort(parts)
	return strings.Join(parts, " | ")
}

// sig spells a function type as signature writes it: escaping on a
// parameter of a function type that may escape, the results, the fails
// row and the needs row; then a takes clause by the names of its parameters (a
// written takes names them all). A function type written in source has
// no bare takes and no empty fails.
func (s *speller) sig(t *Signature) string {
	var b strings.Builder
	b.WriteString("func(")
	named := len(t.Takes) > 0
	for i, p := range t.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		if named {
			b.WriteString(p.Name + " ")
		}
		if ps, ok := p.Type.(*Signature); ok && !ps.NonEscaping {
			b.WriteString("escaping ")
		}
		b.WriteString(s.typ(p.Type))
	}
	b.WriteString(")")
	// A written function type has at most one result: several are a
	// tuple, (A, B).
	for _, r := range t.Results {
		b.WriteString(" " + s.typ(r))
	}
	if !t.Errors.Empty() {
		b.WriteString(" fails " + s.row(t.Errors))
	}
	if !t.Uses.Empty() {
		b.WriteString(" " + requirementKeyword + " " + s.row(t.Uses))
	}
	if named {
		names := make([]string, len(t.Takes))
		for i, ix := range t.Takes {
			names[i] = t.Params[ix].Name
		}
		b.WriteString(" takes " + strings.Join(names, ", "))
	}
	return b.String()
}

func (c *checker) spellName(fi *fileInfo, o *TypeName) (string, string, bool) {
	p := o.Pkg
	if p == nil || p == c.pkg || !p.qualify {
		return o.Name, "", true
	}
	if p.Path == "go" {
		return "go." + o.Name, "", true // go.Foreign: the universe's go package
	}
	for _, pn := range fi.imports {
		if pn.Pkg == p && !pn.broken {
			if pn.Name == "." {
				return o.Name, "", true
			}
			return pn.Name + "." + o.Name, "", true
		}
	}
	return p.Name + "." + o.Name, p.Path, true
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
