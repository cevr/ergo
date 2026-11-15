// Package lawfix moves each misplaced top-level law of a checked package
// into the file of its first subject, with the law helpers only laws use
// beside the first law that uses them (check/lawplace.go; types.md, Laws
// 15). It is the law pass of ergo fix: it edits source text, so every
// comment, blank line and spelling outside the moved declarations stays.
//
// A move is a cut and a paste of whole declarations, each with its doc
// comment and the rest of its last line: a law goes after its first
// subject's declaration (after the laws already pasted there, in source
// order), and its helpers go after it, in the order the law reaches them.
// The imports the moved declarations name are added to the file they go
// to when it lacks them, and an import of the file they leave that nothing
// left there names is removed. A file left with no declaration is deleted
// (Result.Deleted). A file that was formatted is formatted again.
package lawfix

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Move is one law moved, with the helpers that went with it.
type Move struct {
	Law     string
	From    string
	To      string
	Helpers []string
}

// Result is the edit of one package: the new text of each changed file
// by its name (syntax.File.Name), the files to delete, the moves and the
// laws no file of the package can hold (no subject in the package).
type Result struct {
	Files    map[string]string
	Deleted  []string
	Moves    []Move
	Unplaced []string
}

type span struct{ from, to int }

type insert struct {
	at   int
	text string
	seq  int
}

type fileEdit struct {
	f       *syntax.File
	cuts    []span
	inserts []insert
	moved   map[syntax.Decl]bool // declarations leaving the file
	arrived []syntax.Decl        // declarations entering it
}

// Plan computes the edit of pkg, whose files are files (each with its Name
// and Src). It reads only Law.Subjects, Law.Helpers and the package's
// Info, so it works on a package whose only diagnostics are S115.
func Plan(pkg *check.Package, files []*syntax.File) (Result, error) {
	res := Result{Files: map[string]string{}}
	edits := map[*syntax.File]*fileEdit{}
	edit := func(f *syntax.File) *fileEdit {
		e := edits[f]
		if e == nil {
			e = &fileEdit{f: f, moved: map[syntax.Decl]bool{}}
			edits[f] = e
		}
		return e
	}
	fileOf := map[syntax.Decl]*syntax.File{}
	for _, f := range files {
		for _, d := range f.Decls {
			fileOf[d] = f
		}
	}
	seq := 0
	// after holds, by anchor declaration, the end of the text pasted after
	// it so far: a second law of one anchor goes after the first.
	pasted := map[syntax.Decl]bool{}
	for _, law := range pkg.Laws {
		if law.Owner != nil || law.Machine != nil || law.Decl.Derived != nil || law.Decl.Pair != nil {
			continue
		}
		from := law.File()
		if from == nil {
			continue
		}
		if len(law.Subjects) == 0 {
			res.Unplaced = append(res.Unplaced, law.Name)
			continue
		}
		to, anchor := law.Subjects[0].File, law.Subjects[0].Decl
		for _, s := range law.Subjects {
			if s.File == from {
				to, anchor = from, nil
				break
			}
		}
		var block []syntax.Decl
		if anchor != nil {
			block = append(block, law.Decl)
		}
		var helpers []string
		for _, h := range law.Helpers {
			if fileOf[h] == to || pasted[h] {
				continue
			}
			pasted[h] = true
			block = append(block, h)
			helpers = append(helpers, declName(h))
		}
		if len(block) == 0 {
			continue
		}
		movesLaw := anchor != nil
		if anchor == nil {
			anchor = law.Decl // its helpers go after it
		}
		at := lineEnd(to.Src, int(anchor.End()))
		var text strings.Builder
		for _, d := range block {
			src := fileOf[d]
			r := declSpan(src, d)
			edit(src).cuts = append(edit(src).cuts, cutSpan(src.Src, r))
			edit(src).moved[d] = true
			edit(to).arrived = append(edit(to).arrived, d)
			text.WriteString("\n")
			text.WriteString(strings.TrimRight(src.Src[r.from:r.to], "\n"))
			text.WriteString("\n")
		}
		edit(to).inserts = append(edit(to).inserts, insert{at: at, text: text.String(), seq: seq})
		seq++
		mv := Move{Law: law.Name, From: from.Name, To: from.Name, Helpers: helpers}
		if movesLaw {
			mv.To = to.Name
		}
		res.Moves = append(res.Moves, mv)
	}
	if len(edits) == 0 {
		return res, nil
	}
	for _, f := range files {
		e := edits[f]
		if e == nil {
			continue
		}
		if err := imports(pkg, e, fileOf); err != nil {
			return res, err
		}
		left := 0
		for _, d := range f.Decls {
			if _, imp := d.(*syntax.ImportDecl); !imp && !e.moved[d] {
				left++
			}
		}
		if left == 0 && len(e.arrived) == 0 {
			res.Deleted = append(res.Deleted, f.Name)
			continue
		}
		out := apply(f.Src, e)
		if formatted(f.Src) {
			if s, err := format.Source(out); err == nil {
				out = s
			} else {
				return res, fmt.Errorf("%s: the moved text does not format: %v", f.Name, err)
			}
		}
		res.Files[f.Name] = out
	}
	sort.Strings(res.Deleted)
	return res, nil
}

func formatted(src string) bool {
	s, err := format.Source(src)
	return err == nil && s == src
}

// imports adds to e's file each import the declarations arriving there
// name and the file lacks, and cuts each import of the file that only the
// declarations leaving it named.
func imports(pkg *check.Package, e *fileEdit, fileOf map[syntax.Decl]*syntax.File) error {
	f := e.f
	have := map[string]bool{}
	for _, d := range f.Decls {
		if id, ok := d.(*syntax.ImportDecl); ok {
			for _, s := range id.Specs {
				have[specText(f.Src, s)] = true
			}
		}
	}
	var add []string
	for _, d := range e.arrived {
		for _, s := range usedImports(pkg, d) {
			t := specText(fileOf[d].Src, s)
			if !have[t] {
				have[t] = true
				add = append(add, "import "+t)
			}
		}
	}
	if len(add) > 0 {
		at := 0
		var last syntax.Decl
		for _, d := range f.Decls {
			if _, ok := d.(*syntax.ImportDecl); ok {
				last = d
			}
		}
		text := strings.Join(add, "\n") + "\n"
		if last != nil {
			at = lineEnd(f.Src, int(last.End()))
		} else {
			if len(f.Decls) > 0 {
				at = declSpan(f, f.Decls[0]).from
			}
			text += "\n"
		}
		e.inserts = append(e.inserts, insert{at: at, text: text, seq: -1})
	}
	if len(e.moved) == 0 {
		return nil
	}
	gone := map[*syntax.ImportSpec]bool{}
	for d := range e.moved {
		for _, s := range usedImports(pkg, d) {
			gone[s] = true
		}
	}
	// An import that a declaration staying or arriving names stays; an
	// arriving one's import is another file's spec, so it is matched by
	// its text.
	kept := map[string]bool{}
	for _, d := range f.Decls {
		if _, ok := d.(*syntax.ImportDecl); ok || e.moved[d] {
			continue
		}
		for _, s := range usedImports(pkg, d) {
			delete(gone, s)
		}
	}
	for _, d := range e.arrived {
		for _, s := range usedImports(pkg, d) {
			kept[specText(fileOf[d].Src, s)] = true
		}
	}
	for s := range gone {
		if kept[specText(f.Src, s)] {
			delete(gone, s)
		}
	}
	for _, d := range f.Decls {
		id, ok := d.(*syntax.ImportDecl)
		if !ok {
			continue
		}
		cut := 0
		for _, s := range id.Specs {
			if gone[s] {
				cut++
			}
		}
		switch {
		case cut == 0:
		case cut == len(id.Specs):
			e.cuts = append(e.cuts, lineSpan(f.Src, declSpan(f, d)))
		default:
			for _, s := range id.Specs {
				if gone[s] {
					e.cuts = append(e.cuts, lineSpan(f.Src, span{int(s.Pos()), int(s.End())}))
				}
			}
		}
	}
	return nil
}

// usedImports lists the imports whose names d uses, in source order.
func usedImports(pkg *check.Package, d syntax.Decl) []*syntax.ImportSpec {
	var out []*syntax.ImportSpec
	seen := map[*syntax.ImportSpec]bool{}
	syntax.Inspect(d, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			if p, ok := pkg.Info.Uses[id].(*check.PkgName); ok {
				if s := p.Spec(); s != nil && !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		}
		return true
	})
	return out
}

func specText(src string, s *syntax.ImportSpec) string {
	return src[int(s.Pos()):int(s.End())]
}

// apply makes the cuts and inserts of e on src, from the end back.
func apply(src string, e *fileEdit) string {
	type op struct {
		at, to int
		text   string
		seq    int
	}
	var ops []op
	// Cuts may touch (a law and the helper after it): merge them.
	cuts := append([]span(nil), e.cuts...)
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].from < cuts[j].from })
	var merged []span
	for _, c := range cuts {
		if n := len(merged); n > 0 && c.from <= merged[n-1].to {
			merged[n-1].to = max(merged[n-1].to, c.to)
			continue
		}
		merged = append(merged, c)
	}
	for _, c := range merged {
		ops = append(ops, op{at: c.from, to: c.to, seq: math.MaxInt})
	}
	for _, in := range e.inserts {
		ops = append(ops, op{at: in.at, to: in.at, text: in.text, seq: in.seq})
	}
	// From the end back; at one offset, a cut first, then a later insert
	// before an earlier one, so the earlier one ends up first.
	sort.SliceStable(ops, func(i, j int) bool {
		if ops[i].at != ops[j].at {
			return ops[i].at > ops[j].at
		}
		return ops[i].seq > ops[j].seq
	})
	for _, o := range ops {
		src = src[:o.at] + o.text + src[o.to:]
	}
	return src
}

// declSpan is d's text: from the start of its doc comment's line to the
// end of its last line, newline included.
func declSpan(f *syntax.File, d syntax.Decl) span {
	from := int(d.Pos())
	if doc := declDoc(d); doc != nil && int(doc.Pos()) < from {
		from = int(doc.Pos())
	}
	return lineSpan(f.Src, span{from, int(d.End())})
}

// lineSpan widens r to whole lines.
func lineSpan(src string, r span) span {
	from := strings.LastIndexByte(src[:r.from], '\n') + 1
	return span{from, lineEnd(src, r.to)}
}

// cutSpan is r with one blank line around it, so the cut leaves one blank
// line between its neighbours.
func cutSpan(src string, r span) span {
	if r.from >= 2 && src[r.from-1] == '\n' && src[r.from-2] == '\n' {
		r.from--
	} else if r.to < len(src) && src[r.to] == '\n' {
		r.to++
	}
	return r
}

// lineEnd is the offset after the newline that ends the line of at.
func lineEnd(src string, at int) int {
	if at > len(src) {
		return len(src)
	}
	i := strings.IndexByte(src[at:], '\n')
	if i < 0 {
		return len(src)
	}
	return at + i + 1
}

func declDoc(d syntax.Decl) *syntax.CommentGroup {
	switch d := d.(type) {
	case *syntax.FuncDecl:
		return d.Doc
	case *syntax.LawDecl:
		return d.Doc
	case *syntax.VarDecl:
		return d.Doc
	case *syntax.ContextDecl:
		return d.Doc
	case *syntax.TypeDecl:
		return d.Doc
	case *syntax.TestDecl:
		return d.Doc
	case *syntax.ImportDecl:
		return d.Doc
	}
	return nil
}

func declName(d syntax.Decl) string {
	switch d := d.(type) {
	case *syntax.FuncDecl:
		return d.Name.Name
	case *syntax.ContextDecl:
		return d.Name.Name
	case *syntax.VarDecl:
		var names []string
		for _, s := range d.Specs {
			for _, n := range s.Names {
				names = append(names, n.Name)
			}
		}
		if len(names) > 0 {
			return strings.Join(names, ", ")
		}
	}
	return fmt.Sprintf("%T", d)
}
