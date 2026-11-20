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
	"path"
	"sort"
	"strings"
	"unicode"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
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
	Helpers  []string // helpers moved beside a test or a type
	Unplaced []string
	// Renamed are the files of laws left holding other declarations, each
	// "old -> new": a file is named by its concept (renameLawsFiles).
	Renamed []string
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
// and Src). It reads only Law.Subjects, Law.FixSubjects, LawHelpers and the
// package's Info, so it works on a package whose only diagnostics are S115.
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
	// after holds, by declaration, what goes right after it, in order: a
	// moved law after its main subject, a helper after its first user.
	after := map[syntax.Decl][]syntax.Decl{}
	moving := map[syntax.Decl]bool{}
	home := map[syntax.Decl]*syntax.File{} // where each moved law goes
	byLaw := map[syntax.Decl]*Move{}
	var moves []*Move
	var order []syntax.Decl // the anchors, in the order they were first used
	put := func(anchor, d syntax.Decl) {
		if _, ok := after[anchor]; !ok {
			order = append(order, anchor)
		}
		after[anchor] = append(after[anchor], d)
		moving[d] = true
	}
	for _, law := range pkg.Laws {
		if law.Owner != nil || law.Machine != nil || law.Decl.Derived != nil || law.Decl.Pair != nil {
			continue
		}
		from := law.File()
		if from == nil {
			continue
		}
		subjects := law.FixSubjects
		if len(subjects) == 0 {
			subjects = law.Subjects
		}
		if len(subjects) == 0 {
			res.Unplaced = append(res.Unplaced, law.Name)
			continue
		}
		stays := false
		for _, s := range subjects {
			stays = stays || s.File == from
		}
		mv := &Move{Law: law.Name, From: from.Name, To: from.Name}
		byLaw[law.Decl] = mv
		moves = append(moves, mv)
		if stays {
			continue
		}
		main := mainSubject(law.Name, subjects)
		put(main.Decl, law.Decl)
		home[law.Decl] = main.File
		mv.To = main.File.Name
	}
	// A helper stays where one of its users stays or arrives, so a second
	// run moves nothing; else it goes beside its first user, where that
	// user ends up.
	ends := func(u syntax.Decl) *syntax.File {
		if f, ok := home[u]; ok {
			return f
		}
		return fileOf[u]
	}
	for _, h := range pkg.LawHelpers {
		dest := ends(h.User)
		stays := false
		for _, u := range h.Users {
			stays = stays || ends(u) == fileOf[h.Decl]
		}
		if dest == nil || stays {
			continue
		}
		put(h.User, h.Decl)
		if mv := byLaw[h.User]; mv != nil {
			mv.Helpers = append(mv.Helpers, declName(h.Decl))
		} else {
			res.Helpers = append(res.Helpers, fmt.Sprintf("%s beside %s in %s", declName(h.Decl), declName(h.User), dest.Name))
		}
	}
	joinLeftoverTests(&res, files, moving, put)
	for _, mv := range moves {
		if mv.From != mv.To || len(mv.Helpers) > 0 {
			res.Moves = append(res.Moves, *mv)
		}
	}
	// Each anchor that stays gets its declarations, each followed by what
	// goes after it, at any depth.
	seq := 0
	for _, anchor := range order {
		if moving[anchor] {
			continue
		}
		to := fileOf[anchor]
		var text strings.Builder
		var emit func(a syntax.Decl)
		emit = func(a syntax.Decl) {
			for _, d := range after[a] {
				src := fileOf[d]
				r := declSpan(src, d)
				edit(src).cuts = append(edit(src).cuts, cutSpan(src.Src, r))
				edit(src).moved[d] = true
				edit(to).arrived = append(edit(to).arrived, d)
				text.WriteString("\n")
				text.WriteString(strings.TrimRight(src.Src[r.from:r.to], "\n"))
				text.WriteString("\n")
				emit(d)
			}
		}
		emit(anchor)
		edit(to).inserts = append(edit(to).inserts, insert{at: lineEnd(to.Src, int(anchor.End())), text: text.String(), seq: seq})
		seq++
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
				return res, fmt.Errorf("%s: the moved text does not format: %v\n%s", f.Name, err, out)
			}
		}
		res.Files[f.Name] = out
	}
	renameLawsFiles(&res, files, edits)
	sort.Strings(res.Deleted)
	return res, nil
}

// joinLeftoverTests moves the tests of each file of laws that the moves
// leave holding only tests into the package's tests.ergo, when it has one
// that keeps a declaration, after that file's last one: the file of laws
// then holds nothing and goes, where renameLawsFiles would keep its laws
// name, tests.ergo being taken. put moves a declaration after an anchor.
func joinLeftoverTests(res *Result, files []*syntax.File, moving map[syntax.Decl]bool, put func(anchor, d syntax.Decl)) {
	for _, f := range files {
		if !lawsFileName(f.Name) {
			continue
		}
		var tests []syntax.Decl
		var names []string
		only := true
		for _, d := range f.Decls {
			if _, imp := d.(*syntax.ImportDecl); imp || moving[d] {
				continue
			}
			t, ok := d.(*syntax.TestDecl)
			if !ok {
				only = false
				break
			}
			tests = append(tests, d)
			names = append(names, t.Name.Name)
		}
		if !only || len(tests) == 0 {
			continue
		}
		var anchor syntax.Decl
		var to *syntax.File
		for _, g := range files {
			if g == f || g.Name != path.Join(path.Dir(f.Name), "tests.ergo") {
				continue
			}
			for _, d := range g.Decls {
				if _, imp := d.(*syntax.ImportDecl); !imp && !moving[d] {
					anchor, to = d, g
				}
			}
		}
		if anchor == nil {
			continue
		}
		for _, t := range tests {
			put(anchor, t)
			anchor = t
		}
		res.Helpers = append(res.Helpers, fmt.Sprintf("tests %s from %s to %s", strings.Join(names, ", "), f.Name, to.Name))
	}
}

// lawsFileName reports a file named for laws: laws.ergo, tree_laws.ergo.
func lawsFileName(name string) bool {
	return strings.Contains(strings.ToLower(path.Base(name)), "law")
}

// renameLawsFiles names each file of laws that is left after the moves,
// holding declarations the laws are about (or that nothing names), by
// its concept: its first machine, else its first exported type, else its
// first type, else its first exported function, else its first named
// declaration, in lower case; a file left with tests alone is a test
// file, tests.ergo. A name another file has keeps the file.
func renameLawsFiles(res *Result, files []*syntax.File, edits map[*syntax.File]*fileEdit) {
	taken := map[string]bool{}
	for _, f := range files {
		taken[f.Name] = true
	}
	gone := map[string]bool{}
	for _, n := range res.Deleted {
		gone[n] = true
	}
	for _, f := range files {
		if !lawsFileName(f.Name) || gone[f.Name] {
			continue
		}
		var left []syntax.Decl
		for _, d := range f.Decls {
			if _, imp := d.(*syntax.ImportDecl); imp {
				continue
			}
			if e := edits[f]; e != nil && e.moved[d] {
				continue
			}
			left = append(left, d)
		}
		concept := ""
		for rank := 0; rank < 6 && concept == ""; rank++ {
			for _, d := range left {
				name, ok := "", false
				switch d := d.(type) {
				case *syntax.TypeDecl:
					_, machine := d.Spec.(*syntax.MachineType)
					name = d.Name.Name
					ok = rank == 0 && machine || rank == 1 && isUpper(name) || rank == 2
				case *syntax.FuncDecl:
					name = d.Name.Name
					ok = rank == 3 && isUpper(name) || rank == 4
				case *syntax.LawDecl:
				case *syntax.TestDecl:
					name, ok = "tests", rank == 5
				default:
					name, ok = declName(d), rank == 4
				}
				if ok && name != "" {
					concept = strings.ToLower(name)
					break
				}
			}
		}
		if concept == "" {
			continue
		}
		to := path.Join(path.Dir(f.Name), concept+".ergo")
		if path.Dir(f.Name) == "." {
			to = concept + ".ergo"
		}
		if taken[to] {
			continue
		}
		taken[to] = true
		text, ok := res.Files[f.Name]
		if !ok {
			text = f.Src
		}
		delete(res.Files, f.Name)
		res.Files[to] = text
		res.Deleted = append(res.Deleted, f.Name)
		res.Renamed = append(res.Renamed, f.Name+" -> "+to)
	}
}

func isUpper(name string) bool { return name != "" && name[0] >= 'A' && name[0] <= 'Z' }

// mainSubject is the subject a law is named for: of the subjects whose
// name occurs in the law's name, the one that occurs first (the longer at
// one place), so SortPermutes goes beside Sort and MaxIsLastSorted beside
// Max; else its first subject.
func mainSubject(name string, subjects []check.LawSubject) check.LawSubject {
	best, at := subjects[0], -1
	for _, s := range subjects {
		i := wordAt(name, s.Name)
		if i < 0 {
			continue
		}
		if at < 0 || i < at || i == at && len(s.Name) > len(best.Name) {
			best, at = s, i
		}
	}
	return best
}

// wordAt is where word first occurs in name as whole words of its mixed
// caps (Sort in SortOrders, not in Sorted), or -1. word starts upper case.
func wordAt(name, word string) int {
	if word == "" || !unicode.IsUpper(rune(word[0])) {
		return -1
	}
	for from := 0; ; {
		i := strings.Index(name[from:], word)
		if i < 0 {
			return -1
		}
		i += from
		if end := i + len(word); end == len(name) || unicode.IsUpper(rune(name[end])) {
			return i
		}
		from = i + 1
	}
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
		for _, s := range usedImports(pkg, d, fileOf[d]) {
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
		for _, s := range usedImports(pkg, d, f) {
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
		for _, s := range usedImports(pkg, d, f) {
			delete(gone, s)
		}
	}
	for _, d := range e.arrived {
		for _, s := range usedImports(pkg, d, fileOf[d]) {
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
		// The decided parser builds one spec per import (#196): the
		// declaration goes, with its doc comment.
		if gone[id.Specs[0]] {
			e.cuts = append(e.cuts, lineSpan(f.Src, declSpan(f, d)))
		}
	}
	return nil
}

// usedImports lists the imports of f whose names d uses, in f's order. A
// use is the left of a dot, `p.X`, whose name resolves to no object of the
// package (a package name in a type, say `world.World`, has no Uses entry)
// or to the import's own name: so an import counts as used whenever d may
// use it, and none is cut that d still needs.
func usedImports(pkg *check.Package, d syntax.Decl, f *syntax.File) []*syntax.ImportSpec {
	names := map[string]bool{}
	syntax.Inspect(d, func(n syntax.Node) bool {
		sel, ok := n.(*syntax.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*syntax.Ident); ok {
			switch pkg.Info.Uses[id].(type) {
			case *check.PkgName, nil:
				if pkg.Info.Defs[id] == nil {
					names[id.Name] = true
				}
			}
		}
		return true
	})
	var out []*syntax.ImportSpec
	for _, decl := range f.Decls {
		if id, ok := decl.(*syntax.ImportDecl); ok {
			for _, s := range id.Specs {
				if names[check.ImportLocalName(s)] {
					out = append(out, s)
				}
			}
		}
	}
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
		// An insert inside a cut (an import added after one that goes)
		// lands where the cut starts.
		for _, c := range merged {
			if c.from < in.at && in.at < c.to {
				in.at = c.from
			}
		}
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

// lineEnd is the offset after the newline that ends the line of at; at
// itself when a newline ends just before it (an import's end may be after
// its newline).
func lineEnd(src string, at int) int {
	if at > len(src) {
		return len(src)
	}
	if at > 0 && src[at-1] == '\n' {
		return at
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
	case *syntax.TypeDecl:
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
	case *syntax.TestDecl:
		return d.Name.Name
	case *syntax.LawDecl:
		return d.Name.Name
	}
	return ""
}
