package check

import (
	"fmt"
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
// Every slice of the check result is of one of three kinds (round 7). The
// rule is stated per kind of value, not per list:
//
//	(a) a sequence: its order is the program's, written in the source or
//	    fixed by a rule of the language. Kept as it is.
//	(b) a set: its order means nothing. The checker keeps such a set in
//	    the order of its intern ids (intern.go), which follow the order in
//	    which it met the types, and so the order of the files. A reader
//	    whose result can depend on the order enumerates the set only
//	    through its canonical view below, sorted by TypeKey.
//	(c) a list sorted by the checker by a canonical key. Kept as it is.
//
//	slice                                   kind  why
//	Package.Funcs, Types, Tests, Laws       (c)   canonicalOrder (above)
//	Info.InitOrder                          (a)   initialization rule (above)
//	Signature.TParams, Params, Results      (a)   the declaration's text
//	Struct.Fields, Interface.Methods        (a)   the declaration's text
//	Named.TArgs, Instance.TypeArgs          (a)   the type parameters' order
//	Instance.Witnesses, Witness.Ops         (a)   parameters, constraint ops
//	Tuple.Types, Info.ImplicitElems values  (a)   the elements' positions
//	TypeName.Laws, Context.Entries          (a)   the declaration's text
//	Pattern.Fields, Elems; Loop.Bindings    (a)   the pattern's text
//	Union.Variants of a declared union      (a)   the declaration's text
//	Match.Subjects of a match               (a)   the operands' positions
//	With.Contexts                           (a)   the operands' positions
//	With.Discharged, Entries; Entry.Caps,   (c)   by capability name (capKey)
//	  Conversions
//	Context.Overrides                       (c)   context name, then the
//	                                              source order of entries
//	With.Acquired                           (c)   dependencies, then the
//	                                              context name and source
//	                                              order of its entries
//	Row elements (Row.Elems): every         (b)   CanonElems
//	  Signature.Errors and Uses row
//	Union.Variants of a row value (an       (b)   CanonVariants
//	  anonymous union: case err, ConvRow)
//	Match.Subjects of a catch               (b)   CanonCatchSubjects
//	ContextEntry.Uses                       (b)   CanonEntryUses
//
// A caller builds its own list of imported packages (the lowering's
// Unit.Imports, which the lowering orders by package path). Row.String
// prints a row sorted by its package-qualified text (types.go), so a
// diagnostic that prints a row does not follow the intern order either.
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

// TypeKey is t's canonical key: a total text of t's identity, a function
// of the sources. A declared type is its package path, name and the
// offset of its name (two declarations of one name in one package are an
// error); an instance adds its type arguments' keys; every other type is
// spelled from its parts' keys, and a set inside it (a row, the variants
// of a row value) in canonical order. It is the order of every canonical
// set view below. Unlike typeString, which is for printing, two distinct
// declarations never share a key.
func TypeKey(t Type) string {
	var b strings.Builder
	writeKey(&b, t)
	return b.String()
}

func writeKey(b *strings.Builder, t Type) {
	list := func(open, sep, close string, n int, at func(int)) {
		b.WriteString(open)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(sep)
			}
			at(i)
		}
		b.WriteString(close)
	}
	switch t := t.(type) {
	case nil:
		b.WriteString("<nil>")
	case *Basic:
		b.WriteString(t.Name)
	case *Named:
		o := t.origin().Obj
		if o.Pkg != nil {
			b.WriteString(o.Pkg.Path)
		}
		fmt.Fprintf(b, ".%s@%d", o.Name, o.Pos)
		if len(t.TArgs) > 0 {
			list("[", ", ", "]", len(t.TArgs), func(i int) { writeKey(b, t.TArgs[i]) })
		}
	case *TypeParam:
		fmt.Fprintf(b, "$%s@%d", t.Name, t.Pos)
	case *Slice:
		b.WriteString("[]")
		writeKey(b, t.Elem)
	case *Map:
		b.WriteString("map[")
		writeKey(b, t.Key)
		b.WriteString("]")
		writeKey(b, t.Elem)
	case *Pointer:
		b.WriteString("*")
		writeKey(b, t.Elem)
	case *Tuple:
		list("(", ", ", ")", len(t.Types), func(i int) { writeKey(b, t.Types[i]) })
	case *Signature:
		list("func(", ", ", ")", len(t.Params), func(i int) { writeKey(b, t.Params[i].Type) })
		list(" (", ", ", ")", len(t.Results), func(i int) { writeKey(b, t.Results[i]) })
		writeRowKey(b, " fails ", t.Errors)
		writeRowKey(b, " needs ", t.Uses)
	case *Struct:
		list("struct{", "; ", "}", len(t.Fields), func(i int) {
			b.WriteString(t.Fields[i].Name + " ")
			writeKey(b, t.Fields[i].Type)
		})
	case *Union:
		vs := CanonVariants(t)
		list("union{", " | ", "}", len(vs), func(i int) { writeKey(b, vs[i]) })
	case *Interface:
		b.WriteString("interface/" + t.Set.String())
		list("{", "; ", "}", len(t.Methods), func(i int) {
			b.WriteString(t.Methods[i].Name)
			writeKey(b, t.Methods[i].Sig)
		})
	case *MachineType:
		b.WriteString("machine[")
		writeKey(b, t.State)
		b.WriteString(", ")
		writeKey(b, t.Signal)
		b.WriteString("]")
	case *RowVar:
		v := t.root()
		fmt.Fprintf(b, "?row/%d ", v.Kind)
		if v.Fn != nil {
			if v.Fn.Pkg != nil {
				b.WriteString(v.Fn.Pkg.Path)
			}
			fmt.Fprintf(b, ".%s@%d", v.Fn.Name, v.Fn.Pos)
		}
	case *RowArg:
		writeRowKey(b, "row", t.Row)
	default:
		b.WriteString("?" + t.String())
	}
}

// writeRowKey writes the key of the row r, its elements in canonical order.
func writeRowKey(b *strings.Builder, head string, r *Row) {
	if r.Empty() {
		return
	}
	es := CanonElems(r)
	b.WriteString(head + "{")
	for i, e := range es {
		if i > 0 {
			b.WriteString(" | ")
		}
		writeKey(b, e)
	}
	b.WriteString("}")
}

// CanonSet is the set ts in canonical order: sorted by TypeKey, a new
// slice. Two elements of one key are one type; the sort is stable.
func CanonSet(ts []Type) []Type {
	if len(ts) == 0 {
		return nil
	}
	keys := make(map[Type]string, len(ts))
	for _, t := range ts {
		keys[t] = TypeKey(t)
	}
	out := slices.Clone(ts)
	slices.SortStableFunc(out, func(a, b Type) int { return strings.Compare(keys[a], keys[b]) })
	return out
}

// CanonElems is the row r's elements in canonical order (kind b). Row.Elems
// stays in intern id order, which the row's set operations and its dedup
// need; this view is the one outward enumeration.
func CanonElems(r *Row) []Type { return CanonSet(r.Elems()) }

// CanonVariants is u's variants in canonical order. Use it for a row value
// (an anonymous union, kind b), whose variants are a row's elements. A
// declared union's Variants (the Underlying of a Named) are in the
// declaration's order (kind a), which this view does not keep.
func CanonVariants(u *Union) []Type { return CanonSet(u.Variants) }

// CanonCatchSubjects is a catch's subjects (its row's elements) in
// canonical order. A match's subjects are its operands, in their order:
// read m.Subjects for a match.
func CanonCatchSubjects(m *Match) []Type { return CanonSet(m.Subjects) }

// CanonEntryUses is the capabilities an entry's provider uses while it is
// built, a row's elements, in canonical order.
func CanonEntryUses(e *ContextEntry) []Type { return CanonSet(e.Uses) }
