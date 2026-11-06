package check

import (
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The canonical input (196b, pkg-lower round 8). The order in which a
// caller lists a package's files is how a driver found them, not what the
// program is. So Check meets the files in one canonical order, a function
// of their contents (canonicalFiles), and never in the caller's order.
// Everything the checker derives follows from the order in which it meets
// the files: the intern ids of the types, the elements of every row
// (Row.Elems, kept in intern id order), the variants of every row value
// (Union.Variants), the subjects of a catch, the text of a type
// (String()), Info.InitOrder, Package.Funcs, Types, Tests and Laws, and the
// order and the text of the diagnostics. With one canonical input, each of
// them is a function of the set of sources, whatever order the caller
// lists the files in. No output needs a canonical view of its own: the
// order is derived from the input, not re-sorted after the fact.
//
// Only the identity of a file stays the caller's. A Diagnostic names its
// file by the caller's index (Diagnostic.File), and every node of the
// result is a node of the caller's *syntax.File values, which Check does
// not copy or change. Check maps each diagnostic from the index of the
// file as met to the caller's index, once, before it sorts them.
//
// The key of a file (fileKey) is its text with its import declarations
// taken out, then its import declarations' texts in sorted order; the
// file's full text breaks a tie. The order of a file's import lines changes
// no declaration, so it does not move the file among the others. Two files
// tie only when their texts are the same, byte for byte. Then the caller's
// order of the two is kept, and it changes nothing: the checker meets the
// same sequence of texts either way. (If they declare a name, each
// declaration in one is a duplicate of one in the other, and the
// diagnostic is in the later one, the same index in both listings.)
//
// Initialization order (Info.InitOrder, initorder.go). A let runs after
// the lets it reads; else the earliest in declaration order runs first,
// and the order of the files is the order the checker meets them. So two
// lets of different files that do not read each other run in the order of
// the canonical keys of their files, not in the caller's order. Go's spec
// lets a build system present files in any order and asks it for a
// reproducible one (go build sorts by name); this order is reproducible
// too. Is the order observable? A package-level let has no capability
// row and no failure row, so it does no I/O and fails with no value; its
// only effect is a defect (an index out of range, a division by zero). A
// program with two independent lets that both defect reports the defect
// of the one that runs first and stops (measured, pkg-lower round 8:
// TestInitOrderIsCanonical in package lower). That is the only route by
// which the order reaches output, and it is now a function of the
// sources.
//
// A std package (the prelude and the embedded library) is checked by its
// importer, not by Check: its importer gives the files sorted by name, a
// fixed order, and its lists stay in that order.
//
// Prior art: go build gives the compiler a package's files sorted by name,
// so go/types and cmd/compile meet them in one order and need not re-sort
// what they derive. Gleam and Elixir sort a module's definitions before
// they emit them; ReScript compiles each file with no shared state.

// canonicalFiles is the order in which Check meets files: the indexes of
// files, sorted by each file's canonical key, then by its text. The sort is
// stable, so two files of the same text keep the caller's order.
func canonicalFiles(files []*syntax.File) []int {
	keys := make([]string, len(files))
	order := make([]int, len(files))
	for i, f := range files {
		keys[i] = fileKey(f)
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		if c := strings.Compare(keys[a], keys[b]); c != 0 {
			return c
		}
		return strings.Compare(files[a].Src, files[b].Src)
	})
	return order
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
