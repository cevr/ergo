package check

import (
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The canonical order of a checked package's lists (196b, pkg-lower round
// 6). The checker meets a package's files in the order its caller lists
// them, and it fills its lists in that order. That order is how a driver
// found the files, not what the program is. So Check puts every list it
// hands out in one canonical order, once, before it returns (canonicalOrder),
// and no reader of a Package can see the order of discovery:
//
//   - the files (Package.files): by their canonical key (fileKey);
//   - Funcs, Types, Tests (properties are tests with parameters) and Laws:
//     by the canonical key of the declaring file, then by the offset of
//     the declaration's name in it. This is source order in each file. An
//     inline variant's name is in the source, after its union's name; a
//     member law's name is inside its type's declaration.
//
// The package path is the same for every element of one Package, so the
// key (package path, file, offset) reduces to (file, offset) here.
//
// A file's key is its text with its import declarations taken out, then
// its import declarations' texts in sorted order. The checker has no file
// path (Check takes syntax files only), so the key is the file's content,
// which is a function of the sources. The order of a file's import lines
// changes no declaration, so it is not part of the key. Two files of one
// package with the same key declare the same names, so each declaration
// in one is a duplicate of one in the other, which is an error; a valid
// program has no tie unless the files declare nothing. A tie keeps the
// caller's order.
//
// These orders are kept, as they carry meaning:
//
//   - Info.InitOrder: the language's initialization order (initorder.go): a
//     let after the lets it reads, else the earliest in declaration order.
//     Inside one file this is a function of the file's text. Across files,
//     two lets that do not read each other run in the order of the files,
//     as Go's spec runs them in the order the files are presented to the
//     compiler (go build presents them sorted by name). This order decides
//     which independent let's defect is reported first, so the checker does
//     not change it here.
//   - the order inside one declaration (fields, variants, parameters, the
//     laws of one operation type in TypeName.Laws, witnesses): it is the
//     declaration's own source text.
//   - a std package (the prelude and the embedded library): its importer
//     gives the files sorted by name, a fixed order, and does not call
//     Check, so its lists stay in that order.
//
// The checker hands out no other list: Info's other facts are maps keyed
// by node, and a caller builds its own list of imported packages (the
// lowering's Unit.Imports, which the lowering orders by package path).
//
// Prior art: go build gives the compiler a package's files sorted by name,
// so go/types' lists are in a canonical order; cmd/compile sorts what it
// emits by symbol. Gleam and Elixir sort their module's definitions before
// they emit them; ReScript compiles each file with no shared state.

// canonicalOrder puts p's lists in their canonical order. Check calls it
// once, at the one place it returns a package.
func canonicalOrder(p *Package) {
	files := slices.Clone(p.files)
	keys := make(map[*fileInfo]string, len(files))
	for _, f := range files {
		keys[f] = fileKey(f.syntax)
	}
	slices.SortStableFunc(files, func(a, b *fileInfo) int { return strings.Compare(keys[a], keys[b]) })
	rank := make(map[*fileInfo]int, len(files))
	for i, f := range files {
		rank[f] = i
	}
	p.files = files
	p.Funcs = declOrder(rank, p.Funcs, func(f *Func) (*fileInfo, syntax.Pos) { return f.file, f.Pos })
	p.Types = declOrder(rank, p.Types, func(tn *TypeName) (*fileInfo, syntax.Pos) { return tn.file, tn.Pos })
	p.Tests = declOrder(rank, p.Tests, func(t *Test) (*fileInfo, syntax.Pos) { return t.file, t.Decl.Name.Pos() })
	p.Laws = declOrder(rank, p.Laws, func(l *Law) (*fileInfo, syntax.Pos) { return l.file, l.Decl.Name.Pos() })
}

// fileKey is f's canonical key: its text with the import declarations
// taken out, then each import declaration's text, sorted.
func fileKey(f *syntax.File) string {
	var text strings.Builder
	var imports []string
	next := 0
	for _, d := range f.Decls {
		if _, ok := d.(*syntax.ImportDecl); !ok {
			break // imports come first (syntax.File.Decls)
		}
		text.WriteString(f.Src[next:d.Pos()])
		imports = append(imports, f.Src[d.Pos():d.End()])
		next = int(d.End())
	}
	text.WriteString(f.Src[next:])
	slices.Sort(imports)
	return text.String() + "\x00" + strings.Join(imports, "\x00")
}

// declOrder is list sorted by the rank of each element's file, then by the
// offset of its name; an element with no file comes last. The sort is
// stable, so equal keys keep the checker's order.
func declOrder[T any](rank map[*fileInfo]int, list []T, at func(T) (*fileInfo, syntax.Pos)) []T {
	if list == nil {
		return nil
	}
	out := slices.Clone(list)
	key := func(x T) (int, syntax.Pos) {
		f, pos := at(x)
		r, ok := rank[f]
		if !ok {
			return len(rank), 0
		}
		return r, pos
	}
	slices.SortStableFunc(out, func(a, b T) int {
		ra, pa := key(a)
		rb, pb := key(b)
		if ra != rb {
			return ra - rb
		}
		return int(pa - pb)
	})
	return out
}
