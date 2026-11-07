package lower

import (
	"slices"
	"sort"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The canonical unit (196b). The lowering output is a function of the
// program, not of how a caller or the checker listed it. A Unit lists its
// files and imports in the order the caller found them. Neither that order
// nor the order in which the checker met the files is part of the program.
//
// Two layers keep the output free of them:
//
//  1. The producer (pkg-lower rounds 8 and 9). check.Check meets a
//     package's files in one canonical order (check/canon.go,
//     canonicalFiles): by file name (syntax.File.Name), then by a key of
//     the text with the import lines sorted (fileKey), then by the full
//     text. So all it derives is a function of the sources:
//     its lists, the intern order of its types and so the order of every
//     row's elements, a row value's variants and a catch's subjects, the
//     text of every type, and the order and text of its diagnostics
//     (check.sorted sorts them by the file's canonical rank, then offset,
//     length, code and message; TestCheckDiagnosticOrderIsCanonical). A
//     reader of the check result, by any route, gets the same value for
//     any listing of the files, except Diagnostic.File, which stays the
//     caller's index of the file. TestCheckIsCanonical
//     (packages_r8_test.go) compares a dump of
//     the whole check result, both builds' bytes and the refusals over
//     every order of the files of generated programs.
//  2. The entry. newBuildLowerer converts the Unit once into a canonical
//     value, which orders the sources by path and re-sorts each sourced
//     package's lists by (source, offset), and every later read of a list
//     goes through it. The lowerer holds no Unit, so after the entry no
//     code can reach the caller's order of files or imports. This layer
//     is still needed: a Unit can list the files of one checked package
//     in any order (permuteUnit), which layer 1 does not see.
//
// Round 7 also enumerated each set of the check result (a row's elements,
// a row value's variants, a catch's subjects) through a view sorted by
// check.TypeKey. Layer 1 makes those sets canonical by construction, and
// the key was not total (round 7 M2), so round 8 removed the views and
// the lowering reads the fields again.
//
// What the tests of this package prove about layer 2, and no more:
// TestLoweringReadsCanonicalLists finds each direct read of a list field
// (by its go/types object) outside this file and Validate;
// TestLoweringUnitStaysAtEntries finds each expression of a Unit type
// outside the entry functions; TestLoweringImportsNoReflection finds an
// import of reflect or unsafe. A helper in another package that takes a
// check.Package is not seen by them; layer 1 is what makes its result
// canonical (TestRawListEscapesAreCanonical, TestCheckIsCanonical).
//
// The canonical order of each list kind:
//
//   - source files: the root's, then each import's in package path order,
//     each package's files in path order (sources);
//   - a sourced package's functions, types and tests: by source file in
//     that order, then by offset (declOrder). This is declaration order in
//     each file, which the source fixes. The checker orders the files by
//     syntax.File.Name, then by key (the text with the import lines
//     sorted, fileKey), then by full text (canonicalFiles); the lowering
//     orders them by the Unit's source path, the order go build gives. Both are functions of the sources,
//     so a site that read the checker's list would still emit a function
//     of the program, in the checker's canonical order. The two orders
//     differ when a caller sets no syntax.File.Name or sets one that is
//     not the source path;
//   - a std package's lists: the checker's order. A std package has no
//     source in the unit; the checker reads its embedded files in one
//     fixed order, so that order is a function of the sources too.
//
// Two orders are kept as the checker gives them, as they carry meaning:
//
//   - Info.InitOrder (initOrder). It is the language's initialization
//     order (Go's rule, check/initorder.go): a let after the lets it reads,
//     else the earliest in declaration order. Across files that order is
//     the order in which the checker met the files, their canonical order
//     (check/canon.go), so it is a function of the sources; the lowering
//     must not change which independent let runs first, because a defect
//     in one stops the others (TestInitOrderIsCanonical). It is not a
//     function of the Unit's listing (permuteUnit keeps it).
//   - the order inside one declaration (fields, variants, parameters,
//     witnesses): it is the declaration's own source order.
//
// Laws are never read: no build lowers one (decls.go). Package.Laws is
// guarded with the other lists, so a future read goes through here.
//
// Prior art: go build gives the compiler a package's files sorted by name,
// and cmd/compile sorts what it emits by symbol (types.CompareSyms), never
// by the order of its inputs. Gleam sorts the definitions it imports
// before it emits them; Elixir sorts map keys and definition metadata, and
// Erlang's deterministic option drops input-order metadata.

// canonical is a checked Unit in canonical form. Its fields are unexported
// and canonicalize is its only constructor, which newBuildLowerer calls.
type canonical struct {
	srcs    []srcFile
	sourced map[*check.Package]bool
	nodeSrc map[syntax.Node]int // each node's source file; nil for one file
	lists   map[*check.Package]*declLists
}

// declLists are one sourced package's checker lists in canonical order.
type declLists struct {
	funcs []*check.Func
	types []*check.TypeName
	tests []*check.Test
}

// canonicalize converts u, which Validate admits, to its canonical form.
func canonicalize(u Unit) *canonical {
	c := &canonical{sourced: map[*check.Package]bool{}, lists: map[*check.Package]*declLists{}}
	c.srcs = sources(u)
	for _, s := range c.srcs {
		c.sourced[s.pkg] = true
	}
	if len(c.srcs) > 1 {
		c.nodeSrc = map[syntax.Node]int{}
		for i, s := range c.srcs {
			syntax.Inspect(s.file, func(m syntax.Node) bool {
				if m != nil {
					c.nodeSrc[m] = i
				}
				return true
			})
		}
	}
	// Each list is ordered by its declaration's name: an inline variant's
	// TypeDecl is synthetic (check/shapes.go), and no source holds it.
	for p := range c.sourced {
		d := &declLists{}
		d.funcs = declOrder(c, p.Funcs, func(f *check.Func) syntax.Node {
			if f.Decl == nil {
				return nil
			}
			return f.Decl.Name
		})
		d.types = declOrder(c, p.Types, func(tn *check.TypeName) syntax.Node {
			if tn.Decl == nil {
				return nil
			}
			return tn.Decl.Name
		})
		d.tests = declOrder(c, p.Tests, func(t *check.Test) syntax.Node {
			if t.Decl == nil {
				return nil
			}
			return t.Decl.Name
		})
		c.lists[p] = d
	}
	return c
}

// sources lists u's files in canonical order: the root's, then each
// import's in package path order, each package's files in path order. The
// unit's own order of files and imports is how a caller found them, not
// what the program is, so it decides no part of the output (196b); it
// stays only in each file's unit index, which Refusal.Source reports.
func sources(u Unit) []srcFile {
	var srcs []srcFile
	next := 0
	add := func(u *Unit) {
		start := len(srcs)
		for i, f := range u.Files {
			srcs = append(srcs, srcFile{file: f, path: u.Sources[i].Path, text: u.Sources[i].Text, pkg: u.Package, unit: next})
			next++
		}
		own := srcs[start:]
		sort.SliceStable(own, func(i, j int) bool { return own[i].path < own[j].path })
	}
	add(&u)
	imports := make([]*Unit, len(u.Imports))
	for i := range u.Imports {
		imports[i] = &u.Imports[i]
	}
	order := slices.Clone(imports)
	sort.SliceStable(order, func(i, j int) bool { return order[i].Package.Path < order[j].Package.Path })
	// Unit indexes follow Unit.Imports order, so they are counted first.
	base := map[*Unit]int{}
	for _, imp := range imports {
		base[imp] = next
		next += len(imp.Files)
	}
	for _, imp := range order {
		next = base[imp]
		add(imp)
	}
	return srcs
}

// sourceOf is the index of the source file that holds n. A program of one
// file holds every node it meets there.
func (c *canonical) sourceOf(n syntax.Node) (int, bool) {
	if len(c.srcs) == 1 {
		return 0, true
	}
	if n == nil {
		return 0, false
	}
	i, ok := c.nodeSrc[n]
	return i, ok
}

// sourceIndex is n's source file, or past the last for a node that no
// source holds.
func (c *canonical) sourceIndex(n syntax.Node) int {
	if i, ok := c.sourceOf(n); ok && n != nil {
		return i
	}
	return len(c.srcs)
}

// less orders two nodes of the program's sources by source file, then by
// offset; a node that no source file holds comes last. An offset alone is
// no order: it counts from the start of each file.
func (c *canonical) less(a, b syntax.Node) bool {
	i, j := c.sourceIndex(a), c.sourceIndex(b)
	return i < j || i == j && a != nil && b != nil && a.Pos() < b.Pos()
}

// declOrder is list in canonical order: by the source file and offset of
// each element's declaration name (at), and an element with no
// declaration after them, in the checker's order.
func declOrder[T any](c *canonical, list []T, at func(T) syntax.Node) []T {
	out := slices.Clone(list)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := at(out[i]), at(out[j])
		if a == nil || b == nil {
			return a != nil && b == nil
		}
		return c.less(a, b)
	})
	return out
}

// funcs are p's functions in canonical order.
func (c *canonical) funcs(p *check.Package) []*check.Func {
	if d := c.lists[p]; d != nil {
		return d.funcs
	}
	return p.Funcs
}

// types are p's declared types in canonical order.
func (c *canonical) types(p *check.Package) []*check.TypeName {
	if d := c.lists[p]; d != nil {
		return d.types
	}
	return p.Types
}

// tests are p's tests and properties in canonical order.
func (c *canonical) tests(p *check.Package) []*check.Test {
	if d := c.lists[p]; d != nil {
		return d.tests
	}
	return p.Tests
}

// initOrder is p's package-level initializers in the checker's
// initialization order, which is semantic (see above).
func (c *canonical) initOrder(p *check.Package) []*check.Initializer {
	return p.Info.InitOrder
}
