package check

import (
	"fmt"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The canonical input (196b, pkg-lower rounds 8 and 9). The order in which
// a caller lists a package's files is how a driver found them, not what
// the program is. So Check meets the files in one canonical order, a
// function of their names and contents (canonicalFiles), and never in the
// caller's order.
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
// not copy or change. Check sorts the diagnostics by the file as met (its
// canonical rank), then offset, length, code and message, and only then
// maps each from the index of the file as met to the caller's index, once.
// So the order of the list, too, is a function of the sources
// (TestCheckDiagnosticOrderIsCanonical).
//
// The order is by file name first (syntax.File.Name, the package-relative
// path a caller that read the files from a directory sets), as go build
// presents a package's files sorted by name. So the duplicate of a name
// declared in two files is reported in the file whose name sorts later,
// as Go reports it. Then by the file's key (fileKey): its text with its
// import declarations taken out, then its import declarations' texts in
// sorted order; it orders unnamed files, whose order of import lines does
// not move them. Then by the file's full text.
//
// Two files of one package with the same non-empty name are a caller
// error, an internal compiler error (Check panics): a name is the file's
// identity, and a driver that gives one name twice has lost which file is
// which, so no diagnostic could name the right one. Two unnamed files tie
// only when their texts are the same, byte for byte. An unnamed file has
// no identity but its text and its index, so two such files are
// interchangeable: the checker meets the same sequence of texts in either
// order, and the result is the same up to swapping their two indexes (a
// duplicate is reported in the later one as the caller lists them).
// TestCheckFileKeyIsTotal measures it. A caller that can tell two files
// apart (by a path) names them.
//
// Initialization order (Info.InitOrder, initorder.go). A let runs after
// the lets it reads; else the earliest in declaration order runs first,
// and the order of the files is the order the checker meets them. So two
// lets of different files that do not read each other run in canonical
// file order (by name, as in Go), not in the caller's order. Go's spec
// lets a build system present files in any order and asks it for a
// reproducible one; this order is reproducible. The order is observable:
// a let has no capability row and no failure row, but its initializer can
// call a closure that holds and changes state (a counter made by another
// let), so independent lets can see each other's effects on it, and a
// defect in one stops the rest. Both are measured in package lower
// (TestInitOrderIsCanonical: a shared counter prints "1 2", the first
// let's defect is reported), and both are a function of the sources.
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
// files, sorted by each file's name, then its canonical key, then its
// text. The sort is stable, so two unnamed files of the same text keep the
// caller's order. It panics if two files have the same non-empty name.
func canonicalFiles(files []*syntax.File) []int {
	keys := make([]string, len(files))
	order := make([]int, len(files))
	for i, f := range files {
		keys[i] = fileKey(f)
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		if c := strings.Compare(files[a].Name, files[b].Name); c != 0 {
			return c
		}
		if c := strings.Compare(keys[a], keys[b]); c != 0 {
			return c
		}
		return strings.Compare(files[a].Src, files[b].Src)
	})
	for i := 1; i < len(order); i++ {
		if name := files[order[i]].Name; name != "" && name == files[order[i-1]].Name {
			panic(fmt.Sprintf("check: internal compiler error: two files of one package are named %q (a file's name is its identity)", name))
		}
	}
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
