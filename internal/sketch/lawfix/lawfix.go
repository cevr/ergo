// Package lawfix moves each misplaced top-level law or test of a checked
// package into the file of its main subject, with the helpers only laws and
// tests use beside the first law or test that uses them (check/lawplace.go;
// types.md, Laws 15). It is the placement pass of ergo fix: it edits source
// text, so every comment, blank line and spelling outside the moved
// declarations stays.
//
// A move is a cut and a paste of whole declarations, each with its doc
// comment and the rest of its last line: a law or test goes after its main
// subject's declaration (after the declarations already pasted there, in
// source order), and its helpers go after it, in the order it reaches them.
// The imports the moved declarations name are added to the file they go to
// in sorted order when it lacks them (resolving local-name clashes safely),
// and an import of the file they leave that nothing left there names is
// removed. A file left with no declaration is deleted (Result.Deleted). A
// file that was formatted is formatted again.
package lawfix

import (
	"fmt"
	"math"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Move is one law or test moved, with the helpers that went with it.
type Move struct {
	Kind    string
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
	// moved law or test after its main subject, a helper after its first user.
	after := map[syntax.Decl][]syntax.Decl{}
	moving := map[syntax.Decl]bool{}
	home := map[syntax.Decl]*syntax.File{} // where each moved law or test goes
	byUser := map[syntax.Decl]*Move{}
	var moves []*Move
	var order []syntax.Decl // the anchors, in the order they were first used
	put := func(anchor, d syntax.Decl) {
		if _, ok := after[anchor]; !ok {
			order = append(order, anchor)
		}
		after[anchor] = append(after[anchor], d)
		moving[d] = true
	}
	declRank := map[syntax.Decl]int{}
	sortedFiles := slices.Clone(files)
	slices.SortStableFunc(sortedFiles, func(a, b *syntax.File) int {
		return strings.Compare(a.Name, b.Name)
	})
	rank := 0
	for _, f := range sortedFiles {
		for _, d := range f.Decls {
			declRank[d] = rank
			rank++
		}
	}
	type topItem struct {
		kind        string
		name        string
		decl        syntax.Decl
		file        *syntax.File
		subjects    []check.LawSubject
		fixSubjects []check.LawSubject
	}
	var items []topItem
	for _, law := range pkg.Laws {
		if law.Owner != nil || law.Machine != nil || law.Decl.Derived != nil || law.Decl.Pair != nil {
			continue
		}
		items = append(items, topItem{
			kind:        "law",
			name:        law.Name,
			decl:        law.Decl,
			file:        law.File(),
			subjects:    law.Subjects,
			fixSubjects: law.FixSubjects,
		})
	}
	for _, tc := range pkg.Tests {
		items = append(items, topItem{
			kind:        "test",
			name:        tc.Name,
			decl:        tc.Decl,
			file:        tc.File(),
			subjects:    tc.Subjects,
			fixSubjects: tc.FixSubjects,
		})
	}
	slices.SortStableFunc(items, func(a, b topItem) int {
		return declRank[a.decl] - declRank[b.decl]
	})
	for _, item := range items {
		from := item.file
		if from == nil {
			continue
		}
		subjects := item.fixSubjects
		if len(subjects) == 0 {
			subjects = item.subjects
		}
		if len(subjects) == 0 {
			res.Unplaced = append(res.Unplaced, item.kind+" "+item.name)
			continue
		}
		stays := false
		for _, s := range subjects {
			stays = stays || s.File == from
		}
		mv := &Move{Kind: item.kind, Law: item.name, From: from.Name, To: from.Name}
		byUser[item.decl] = mv
		moves = append(moves, mv)
		if stays {
			continue
		}
		main := mainSubject(item.name, subjects)
		put(main.Decl, item.decl)
		home[item.decl] = main.File
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
		if mv := byUser[h.User]; mv != nil {
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
	// Record all cuts and arrivals first so imports and alias rewrites are
	// known before slicing moved declarations.
	for _, anchor := range order {
		if moving[anchor] {
			continue
		}
		to := fileOf[anchor]
		var record func(a syntax.Decl)
		record = func(a syntax.Decl) {
			for _, d := range after[a] {
				src := fileOf[d]
				r := declSpan(src, d)
				edit(src).cuts = append(edit(src).cuts, cutSpan(src.Src, r))
				edit(src).moved[d] = true
				edit(to).arrived = append(edit(to).arrived, d)
				record(d)
			}
		}
		record(anchor)
	}
	deleted := map[*syntax.File]bool{}
	rewrites := map[syntax.Decl]map[string]string{}
	for _, f := range files {
		e := edits[f]
		if e == nil {
			continue
		}
		left := 0
		for _, d := range f.Decls {
			if _, imp := d.(*syntax.ImportDecl); !imp && !e.moved[d] {
				left++
			}
		}
		if left == 0 && len(e.arrived) == 0 {
			deleted[f] = true
			res.Deleted = append(res.Deleted, f.Name)
			continue
		}
		rw, err := imports(pkg, e, fileOf)
		if err != nil {
			return res, err
		}
		for d, m := range rw {
			rewrites[d] = m
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
		at := lineEnd(to.Src, int(anchor.End()))
		var text strings.Builder
		if at == len(to.Src) && at > 0 && to.Src[at-1] != '\n' {
			text.WriteByte('\n')
		}
		var emit func(a syntax.Decl)
		emit = func(a syntax.Decl) {
			for _, d := range after[a] {
				src := fileOf[d]
				r := declSpan(src, d)
				snippet := rewriteDeclImports(pkg, src.Src, r, d, rewrites[d])
				text.WriteString("\n")
				text.WriteString(strings.TrimRight(snippet, "\n"))
				text.WriteString("\n")
				emit(d)
			}
		}
		emit(anchor)
		edit(to).inserts = append(edit(to).inserts, insert{at: at, text: text.String(), seq: seq})
		seq++
	}
	for _, f := range files {
		e := edits[f]
		if e == nil || deleted[f] {
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

func testsFileName(name string) bool {
	return !lawsFileName(name) && check.IsLawOrTestFile(name)
}

func hasTestOrLaw(f *syntax.File) bool {
	for _, d := range f.Decls {
		switch d.(type) {
		case *syntax.TestDecl, *syntax.LawDecl:
			return true
		}
	}
	return false
}

// renameLawsFiles names each file of laws (or a file named for tests that
// holds tests or laws) left after the moves, holding declarations the laws or
// tests are about (or that nothing names), by its concept: its first machine,
// else its first exported type, else its first type, else its first exported
// function, else its first named declaration, in lower case; a file left with
// tests alone is a test file, tests.ergo. A name another file has keeps the
// file.
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
		if gone[f.Name] || !(lawsFileName(f.Name) || testsFileName(f.Name) && hasTestOrLaw(f)) {
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
					if !testsFileName(f.Name) {
						name, ok = "tests", rank == 5
					}
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

// mainSubject is the subject a law or test is named for: of the subjects
// whose name occurs in the declaration's name, the one that occurs first
// (the longer at one place), so SortPermutes goes beside Sort and
// MaxIsLastSorted beside Max; else its first subject.
func mainSubject(name string, subjects []check.LawSubject) check.LawSubject {
	best, at := subjects[0], -1
	for _, s := range subjects {
		i := wordAt(name, subjectWord(s.Name))
		if i < 0 {
			continue
		}
		if at < 0 || i < at || i == at && len(s.Name) > len(best.Name) {
			best, at = s, i
		}
	}
	return best
}

func subjectWord(s string) string {
	if s == "" {
		return ""
	}
	r, n := utf8.DecodeRuneInString(s)
	if unicode.IsUpper(r) {
		return s
	}
	if len(s) > 1 {
		return string(unicode.ToUpper(r)) + s[n:]
	}
	return ""
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
		if end := i + len(word); end == len(name) || unicode.IsUpper(rune(name[end])) || name[end] == '_' || unicode.IsDigit(rune(name[end])) {
			return i
		}
		from = i + 1
	}
}

func formatted(src string) bool {
	s, err := format.Source(src)
	return err == nil && s == src
}

type importKey struct {
	isGo bool
	path string
}

type impEntry struct {
	path string
	decl *syntax.ImportDecl
	pos  int
}

type missingImport struct {
	isGo bool
	path string
	line string
}

// imports adds to e's file in sorted order each import the declarations
// arriving there name and the file lacks (resolving local-name clashes and
// unaliasing aliases that are no longer needed), and cuts each import of
// the file that only the declarations leaving it named. It returns per
// arriving declaration any qualifier renames oldLocal -> newLocal.
func imports(pkg *check.Package, e *fileEdit, fileOf map[syntax.Decl]*syntax.File) (map[syntax.Decl]map[string]string, error) {
	f := e.f
	rewrites := map[syntax.Decl]map[string]string{}
	arrivedByKey := map[importKey]bool{}
	arrivedLocals := map[string]bool{}
	for _, d := range e.arrived {
		for _, s := range usedImports(pkg, d, fileOf[d]) {
			arrivedByKey[importKey{isGo: s.Kind == syntax.ImportGo, path: importPath(s)}] = true
		}
		syntax.Inspect(d, func(n syntax.Node) bool {
			if id, ok := n.(*syntax.Ident); ok && pkg.Info.Defs[id] != nil {
				arrivedLocals[id.Name] = true
			}
			return true
		})
	}
	gone := map[*syntax.ImportSpec]bool{}
	if len(e.moved) > 0 {
		for d := range e.moved {
			for _, s := range usedImports(pkg, d, f) {
				gone[s] = true
			}
		}
		for _, d := range f.Decls {
			if _, ok := d.(*syntax.ImportDecl); ok || e.moved[d] {
				continue
			}
			for _, s := range usedImports(pkg, d, f) {
				delete(gone, s)
			}
		}
	}
	// Compute which imports in f stay and which names clash in f before
	// pruning gone imports (an arriving decl can keep a plain gone import,
	// or a named gone import whose default name clashes in f).
	prelimKeptNames := map[string]bool{}
	for _, d := range f.Decls {
		id, ok := d.(*syntax.ImportDecl)
		if !ok || len(id.Specs) == 0 {
			continue
		}
		s := id.Specs[0]
		if !gone[s] {
			if n := check.ImportLocalName(s); n != "" && n != "_" {
				prelimKeptNames[n] = true
			}
		}
	}
	for s := range gone {
		k := importKey{isGo: s.Kind == syntax.ImportGo, path: importPath(s)}
		if !arrivedByKey[k] {
			continue
		}
		if s.Kind != syntax.ImportNamed {
			delete(gone, s)
			continue
		}
		def := check.ImportDefaultName(s)
		if pkg.DeclaresName(def) || prelimKeptNames[def] || syntax.IsKeyword(def) || !check.IsValidImportName(def) {
			delete(gone, s)
		}
	}

	haveByKey := map[importKey]string{}
	usedLocalNames := map[string]bool{}
	var keptErgo, keptGo []impEntry
	var firstImp, firstNonImp syntax.Decl
	var lastCutEnd int
	keptImports := 0
	for _, d := range f.Decls {
		id, ok := d.(*syntax.ImportDecl)
		if !ok {
			if firstNonImp == nil && !e.moved[d] {
				firstNonImp = d
			}
			continue
		}
		if firstImp == nil {
			firstImp = d
		}
		if len(id.Specs) == 0 {
			continue
		}
		s := id.Specs[0]
		if gone[s] {
			sp := lineSpan(f.Src, declSpan(f, d))
			e.cuts = append(e.cuts, sp)
			if sp.to > lastCutEnd {
				lastCutEnd = sp.to
			}
			continue
		}
		keptImports++
		isGo := s.Kind == syntax.ImportGo
		impPath := importPath(s)
		local := check.ImportLocalName(s)
		haveByKey[importKey{isGo: isGo, path: impPath}] = local
		if local != "" && local != "_" {
			usedLocalNames[local] = true
		}
		entry := impEntry{path: impPath, decl: id, pos: declSpan(f, id).from}
		if isGo {
			keptGo = append(keptGo, entry)
		} else {
			keptErgo = append(keptErgo, entry)
		}
	}

	nameClashes := func(name string) bool {
		if name == "" || name == "_" || syntax.IsKeyword(name) || !check.IsValidImportName(name) {
			return true
		}
		return usedLocalNames[name] || pkg.DeclaresName(name)
	}

	var missing []missingImport
	for _, d := range e.arrived {
		for _, s := range usedImports(pkg, d, fileOf[d]) {
			isGo := s.Kind == syntax.ImportGo
			impPath := importPath(s)
			oldLocal := check.ImportLocalName(s)
			key := importKey{isGo: isGo, path: impPath}
			newLocal, ok := haveByKey[key]
			if !ok {
				def := check.ImportDefaultName(s)
				if !nameClashes(def) {
					newLocal = def
					missing = append(missing, missingImport{isGo: isGo, path: impPath, line: formatImportLine(isGo, "", s)})
				} else {
					alias := ""
					if oldLocal != def && !nameClashes(oldLocal) && !arrivedLocals[oldLocal] {
						alias = oldLocal
					} else {
						base := sanitizeIdent(def)
						for n := 2; ; n++ {
							cand := fmt.Sprintf("%s%d", base, n)
							if !nameClashes(cand) && !arrivedLocals[cand] {
								alias = cand
								break
							}
						}
					}
					newLocal = alias
					missing = append(missing, missingImport{isGo: isGo, path: impPath, line: formatImportLine(isGo, alias, s)})
				}
				haveByKey[key] = newLocal
				usedLocalNames[newLocal] = true
			}
			if newLocal != oldLocal {
				if rewrites[d] == nil {
					rewrites[d] = map[string]string{}
				}
				rewrites[d][oldLocal] = newLocal
			}
		}
	}

	if len(missing) == 0 {
		if keptImports == 0 && len(gone) > 0 && firstNonImp != nil {
			firstAt := declSpan(f, firstNonImp).from
			if lastCutEnd < firstAt && strings.TrimSpace(f.Src[lastCutEnd:firstAt]) == "" {
				e.cuts = append(e.cuts, span{from: lastCutEnd, to: firstAt})
			}
		}
		return rewrites, nil
	}

	slices.SortFunc(missing, func(a, b missingImport) int {
		if a.isGo != b.isGo {
			if !a.isGo {
				return -1
			}
			return 1
		}
		return strings.Compare(a.path, b.path)
	})

	var missingErgo, missingGo []missingImport
	for _, m := range missing {
		if m.isGo {
			missingGo = append(missingGo, m)
		} else {
			missingErgo = append(missingErgo, m)
		}
	}

	seq := -100000
	if len(keptErgo) == 0 && len(keptGo) == 0 {
		var lines []string
		for _, m := range missingErgo {
			lines = append(lines, m.line)
		}
		for _, m := range missingGo {
			lines = append(lines, m.line)
		}
		at := len(f.Src)
		text := strings.Join(lines, "\n") + "\n"
		if len(gone) > 0 && firstImp != nil {
			at = declSpan(f, firstImp).from
			if firstNonImp != nil && lastCutEnd == declSpan(f, firstNonImp).from {
				text += "\n"
			}
		} else if firstNonImp != nil {
			at = declSpan(f, firstNonImp).from
			text += "\n"
		} else if len(f.Decls) > 0 {
			at = declSpan(f, f.Decls[0]).from
			text += "\n"
		}
		e.inserts = append(e.inserts, insert{at: at, text: text, seq: seq})
		return rewrites, nil
	}

	if len(missingErgo) > 0 {
		if len(keptErgo) > 0 {
			lastEnd := lineEnd(f.Src, int(keptErgo[len(keptErgo)-1].decl.End()))
			for _, m := range missingErgo {
				at := lastEnd
				for _, ke := range keptErgo {
					if m.path < ke.path {
						at = ke.pos
						break
					}
				}
				e.inserts = append(e.inserts, insert{at: at, text: m.line + "\n", seq: seq})
				seq++
			}
		} else {
			var lines []string
			for _, m := range missingErgo {
				lines = append(lines, m.line)
			}
			e.inserts = append(e.inserts, insert{at: keptGo[0].pos, text: strings.Join(lines, "\n") + "\n", seq: seq})
			seq++
		}
	}

	if len(missingGo) > 0 {
		if len(keptGo) > 0 {
			lastEnd := lineEnd(f.Src, int(keptGo[len(keptGo)-1].decl.End()))
			for _, m := range missingGo {
				at := lastEnd
				for _, kg := range keptGo {
					if m.path < kg.path {
						at = kg.pos
						break
					}
				}
				e.inserts = append(e.inserts, insert{at: at, text: m.line + "\n", seq: seq})
				seq++
			}
		} else {
			lastEnd := lineEnd(f.Src, int(keptErgo[len(keptErgo)-1].decl.End()))
			var lines []string
			for _, m := range missingGo {
				lines = append(lines, m.line)
			}
			e.inserts = append(e.inserts, insert{at: lastEnd, text: strings.Join(lines, "\n") + "\n", seq: seq})
		}
	}

	return rewrites, nil
}

func importPath(s *syntax.ImportSpec) string {
	p := s.Path.Value
	if s.Path.Kind != syntax.PATH {
		if u, err := strconv.Unquote(p); err == nil {
			p = u
		}
	}
	return p
}

func formatImportLine(isGo bool, alias string, s *syntax.ImportSpec) string {
	var b strings.Builder
	b.WriteString("import ")
	if isGo {
		b.WriteString("go ")
	}
	if alias != "" {
		b.WriteString(alias)
		b.WriteByte(' ')
	}
	b.WriteString(s.Path.Value)
	return b.String()
}

func sanitizeIdent(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r)) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "_" || unicode.IsDigit(rune(out[0])) {
		return "pkg" + out
	}
	return out
}

// rewriteDeclImports slices src[r.from:r.to] and replaces package qualifiers
// in selector expressions according to rewrites.
func rewriteDeclImports(pkg *check.Package, src string, r span, d syntax.Decl, rewrites map[string]string) string {
	text := src[r.from:r.to]
	if len(rewrites) == 0 {
		return text
	}
	type editAt struct {
		from, to int
		name     string
	}
	var eds []editAt
	syntax.Inspect(d, func(n syntax.Node) bool {
		sel, ok := n.(*syntax.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*syntax.Ident)
		if !ok {
			return true
		}
		to, ok := rewrites[id.Name]
		if !ok || pkg.Info.Defs[id] != nil {
			return true
		}
		switch pkg.Info.Uses[id].(type) {
		case *check.PkgName, nil:
			from, end := int(id.Pos())-r.from, int(id.End())-r.from
			if 0 <= from && end <= len(text) {
				eds = append(eds, editAt{from: from, to: end, name: to})
			}
		}
		return true
	})
	if len(eds) == 0 {
		return text
	}
	sort.Slice(eds, func(i, j int) bool { return eds[i].from > eds[j].from })
	last := -1
	for _, ed := range eds {
		if ed.from == last {
			continue
		}
		last = ed.from
		text = text[:ed.from] + ed.name + text[ed.to:]
	}
	return text
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
	if len(merged) > 0 && merged[0].from == 0 {
		insertAtZero := false
		for _, in := range e.inserts {
			if in.at == 0 || (merged[0].from < in.at && in.at < merged[0].to) {
				insertAtZero = true
				break
			}
		}
		if !insertAtZero {
			for merged[0].to < len(src) && src[merged[0].to] == '\n' {
				merged[0].to++
			}
		}
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
