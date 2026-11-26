package lint

import (
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/syntax"
)

// noComments is the owner's rule (NO-COMMENTS, 2026-11-22: "if the code
// is not self-explanatory, then we need to refactor it"; "Keep doc
// comments"; of package overviews, "Remove them too"): the only comment a
// source keeps is the doc comment of a public declaration. It reads the
// tree alone, so `ergo fix` removes what it reports in a file that does
// not check, std's too.
var noComments = &Rule{
	Name:    "no-comments",
	Code:    "L163",
	Doc:     "A comment that is not the doc comment of a public declaration says what the code should say.",
	Instead: "say it in the code: a name, a function or type that carries it, a refinement or a law that makes it true; ergo fix removes the comment",
	Default: Error,
	Check:   checkNoComments,
	SafeFix: true,
}

func checkNoComments(p *Pass) {
	for _, b := range bannedComments(p.File, p.fileIndex) {
		fix := &report.Fix{InsertAt: b.From, Replace: b.To - b.From, Text: b.Text}
		p.ReportFix(b.Group.Pos(), b.Group.End(), "this comment is not the doc comment of a public declaration", fix)
	}
}

// Banned is one comment group the rule refuses and the edit that removes
// it: the bytes [From, To) of the file become Text.
type Banned struct {
	Group    *syntax.CommentGroup
	From, To int
	Text     string
	// Decl is the handle of the top-level declaration the group sits in
	// or above ("Name", "Name#test", "Name#law"), "" past the last one.
	Decl string
	// Position is where the group stands: trailing (after code on its
	// line), doc (a private declaration's doc comment), header (before
	// the first declaration), body (in a body) or loose.
	Position string
}

// BannedComments is every comment group of f, in source order, that is
// not the doc comment of a public declaration (KeptDocs), with its
// removal.
func BannedComments(f *syntax.File) []Banned {
	return bannedComments(f, func() *fileIndex { return buildFileIndex(f) })
}

// bannedComments is BannedComments reading the file's doc comments and
// body spans from its index, which index builds on the first banned group.
func bannedComments(f *syntax.File, index func() *fileIndex) []Banned {
	kept := KeptDocs(f)
	var out []Banned
	var fi *fileIndex
	for _, g := range f.Comments {
		if kept[g] {
			continue
		}
		if fi == nil {
			fi = index()
		}
		from, to, text := removal(f.Src, int(g.Pos()), int(g.End()))
		out = append(out, Banned{Group: g, From: from, To: to, Text: text, Decl: enclosing(f, g.Pos()), Position: position(f, g, fi)})
	}
	return out
}

func position(f *syntax.File, g *syntax.CommentGroup, fi *fileIndex) string {
	switch {
	case !aloneOnLine(f.Src, int(g.Pos())):
		return "trailing"
	case fi.docComments[g]:
		return "doc"
	case len(f.Decls) == 0 || g.End() <= f.Decls[0].Pos():
		return "header"
	case fi.inBody(g.Pos()):
		return "body"
	}
	return "loose"
}

// exported is Visibility 1: a name whose first character is an upper-case
// letter.
func isExported(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

// KeptDocs is the doc comments of f's public declarations: the Doc the
// parser attaches (an own-line group whose last line is directly above
// the declaration) of a package-level type, function, let (any name) or
// context with an exported name, of a top-level law with an exported
// name, and of an exported member of such a type (a record field, an
// operation, an included operation type, a law of an operation type or a
// machine). A test, an import and anything inside a body is never public.
func KeptDocs(f *syntax.File) map[*syntax.CommentGroup]bool {
	kept := map[*syntax.CommentGroup]bool{}
	keep := func(g *syntax.CommentGroup, public bool) {
		if g != nil && public {
			kept[g] = true
		}
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *syntax.TypeDecl:
			public := isExported(d.Name.Name)
			keep(d.Doc, public)
			if public {
				keepMembers(d, keep)
			}
		case *syntax.FuncDecl:
			keep(d.Doc, isExported(d.Name.Name))
		case *syntax.ContextDecl:
			keep(d.Doc, isExported(d.Name.Name))
		case *syntax.LawDecl:
			keep(d.Doc, isExported(d.Name.Name))
		case *syntax.VarDecl:
			public := false
			for _, s := range d.Specs {
				for _, n := range s.Names {
					public = public || isExported(n.Name)
				}
			}
			keep(d.Doc, public)
		}
	}
	return kept
}

// keepMembers keeps the docs of the exported members of a public type.
func keepMembers(d *syntax.TypeDecl, keep func(*syntax.CommentGroup, bool)) {
	field := func(fl *syntax.Field) {
		keep(fl.Doc, len(fl.Names) == 0 || isExported(fl.Names[0].Name))
	}
	switch s := d.Spec.(type) {
	case *syntax.StructType:
		if s.Fields != nil {
			for _, fl := range s.Fields.List {
				field(fl)
			}
		}
	case *syntax.InterfaceType:
		for _, fl := range s.Includes {
			field(fl)
		}
		for _, fl := range s.Methods {
			field(fl)
		}
		for _, l := range s.Laws {
			keep(l.Doc, isExported(l.Name.Name))
		}
	case *syntax.MachineType:
		for _, l := range s.Laws {
			keep(l.Doc, isExported(l.Name.Name))
		}
	}
}

// enclosing is the handle of the top-level declaration that holds pos or
// that pos documents (the first declaration after it), or "".
func enclosing(f *syntax.File, pos syntax.Pos) string {
	for _, d := range f.Decls {
		if pos < d.Pos() || pos < d.End() {
			return declHandle(d)
		}
	}
	return ""
}

func declHandle(d syntax.Decl) string {
	switch d := d.(type) {
	case *syntax.TypeDecl:
		return d.Name.Name
	case *syntax.FuncDecl:
		return d.Name.Name
	case *syntax.ContextDecl:
		return d.Name.Name
	case *syntax.LawDecl:
		return d.Name.Name + "#law"
	case *syntax.TestDecl:
		return d.Name.Name + "#test"
	case *syntax.VarDecl:
		if len(d.Specs) > 0 && len(d.Specs[0].Names) > 0 {
			return d.Specs[0].Names[0].Name
		}
	}
	return ""
}
