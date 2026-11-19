package lower

import (
	"sort"

	"ergo.local/prototype/internal/sketch/check"
)

// Pointer-first layouts (NOSCAN1; plan IR-G §3 item 15). Go's collector
// scans a value only up to its last pointer word (abi.Type.PtrBytes), and
// Go keeps a struct's declared field order. An Ergo record promises no
// layout: every field is read and written by its Go name, its literals are
// keyed, and its codecs, text and equality walk the declared fields
// (typeDecl.fields, which this file never reorders). So the Go struct of a
// record lists its fields in an order of the lowering's choosing:
//
//   - the fields that hold a pointer word (a string, a slice, a Map, a
//     pointer, a function, an interface or union, a record that holds one)
//     and the fields whose layout the lowering does not know (a type
//     parameter, a host type), in declared order;
//   - then the pointer-free fields, by alignment from the largest, in
//     declared order among equals, so padding shrinks too.
//
// A record with one string and six numbers is then scanned for two words,
// not eight, in every array of it. The order is a function of the
// declaration alone, so every use of the type agrees on it.
//
// Admission (layoutOrder): a record the lowering emits. No Go code outside
// the program sees its struct: a Go type of import go is Go's own and is
// never emitted, and a library exports no function over an Ergo record in
// v1 (interop.md, Limits 5); the unit that exports one keeps its declared
// order. A test build keeps the declared order (shortcuts.go: the test
// build takes the reference).
//
// The reference is layoutLowering "declared", set by a test or by a build
// with -ldflags=-X=ergo.local/prototype/internal/sketch/lower.layoutLowering=declared.
// TestLayoutsAgree runs generated records under both and against a model.
// Each emitted record's size, scanned prefix and first pointer field under
// the declared order and the chosen order are in the lowering's stats
// (knownStats.Layouts), the layout report.
//
// Prior art: Rust reorders struct fields unless repr(C); GHC puts the
// pointers of every closure first for its collector; OCaml tags blocks
// with no pointers so the collector skips them; the JVM groups reference
// fields. Gleam, ReScript and Elixir lower records to the host's own
// objects (tuples, JavaScript objects, maps) and choose no layout.
var layoutLowering string

// LayoutRow is one record of the layout report: its Go size and scanned
// prefix in bytes (amd64) under the declared and the chosen order, and the
// first field that holds a pointer (empty when none). Known is false when a
// field's layout is not known to the lowering (a type parameter, a host
// type); the sizes then count what is known.
type LayoutRow struct {
	Type                     string
	Size                     int
	ScannedDeclared, Scanned int
	Pointerful               string
	Reordered, Known         bool
}

// fieldLayout is what the lowering knows of a field type's Go layout.
type fieldLayout struct {
	size, align int
	ptrEnd      int // the end of the last pointer word, 0 for none
	known       bool
}

// layoutOrder is the Go field order of the record td: the declared order,
// or pointers first when the shortcut is taken. It is the admission
// function of the shortcut layout-pointers-first (shortcuts.go).
func (l *lowerer) layoutOrder(td *typeDecl) []*check.Field {
	if td.kind != kindRecord || len(td.fields) < 2 {
		return td.fields
	}
	if !l.takes("layout-pointers-first") {
		return td.fields
	}
	return l.pointersFirst(td)
}

// pointersFirst is td's fields, pointer-holding and unknown ones first in
// declared order, then pointer-free ones by alignment, largest first.
func (l *lowerer) pointersFirst(td *typeDecl) []*check.Field {
	var ptrs, scalars []*check.Field
	for _, f := range td.fields {
		fl := l.scanOf(f.Type, 0)
		if !fl.known || fl.ptrEnd > 0 {
			ptrs = append(ptrs, f)
		} else {
			scalars = append(scalars, f)
		}
	}
	sort.SliceStable(scalars, func(i, j int) bool {
		return l.scanOf(scalars[i].Type, 0).align > l.scanOf(scalars[j].Type, 0).align
	})
	return append(ptrs, scalars...)
}

// layoutReport records td's row of the layout report, for the order the
// lowering emits.
func (l *lowerer) layoutReport(td *typeDecl, order []*check.Field) {
	if td.kind != kindRecord {
		return
	}
	declared := l.scanStruct(td.fields, 0)
	chosen := l.scanStruct(order, 0)
	row := LayoutRow{Type: td.tn.Name, Size: chosen.size, ScannedDeclared: declared.ptrEnd, Scanned: chosen.ptrEnd, Known: chosen.known}
	for i, f := range order {
		if i < len(td.fields) && td.fields[i] != f {
			row.Reordered = true
		}
		if fl := l.scanOf(f.Type, 0); row.Pointerful == "" && fl.ptrEnd > 0 {
			row.Pointerful = f.Name
		}
	}
	if row.Reordered {
		l.took("layout-pointers-first")
	}
	l.stats.Layouts = append(l.stats.Layouts, row)
}

// scanStruct is the layout of a struct of fields in the given order, at a
// nesting depth.
func (l *lowerer) scanStruct(fields []*check.Field, depth int) fieldLayout {
	out := fieldLayout{align: 1, known: true}
	off := 0
	for _, f := range fields {
		fl := l.scanOf(f.Type, depth)
		off = roundUp(off, fl.align)
		switch {
		case !fl.known:
			out.known = false
			out.ptrEnd = off + fl.size // the worst case: all of it
		case fl.ptrEnd > 0:
			out.ptrEnd = off + fl.ptrEnd
		}
		off += fl.size
		out.align = max(out.align, fl.align)
	}
	out.size = roundUp(off, out.align)
	return out
}

// scanOf is the Go layout of t on amd64 with its pointer words, as far as
// the lowering knows it (flatunion.go's layoutOf knows no pointer words).
// A record is laid out in its own chosen order (depth bounds a record
// reached through its own fields, which the checker refuses by value).
func (l *lowerer) scanOf(t check.Type, depth int) fieldLayout {
	word := fieldLayout{size: 8, align: 8, ptrEnd: 8, known: true}
	unknown := fieldLayout{size: 8, align: 8, ptrEnd: 8}
	if depth > 16 {
		return unknown
	}
	switch t := t.(type) {
	case *check.Basic:
		switch t.Kind {
		case check.Bool, check.Int8, check.Uint8:
			return fieldLayout{size: 1, align: 1, known: true}
		case check.Int16, check.Uint16:
			return fieldLayout{size: 2, align: 2, known: true}
		case check.Int32, check.Uint32, check.Float32:
			return fieldLayout{size: 4, align: 4, known: true}
		case check.Int, check.Int64, check.Uint, check.Uint64, check.Uintptr, check.Float64, check.Duration:
			return fieldLayout{size: 8, align: 8, known: true}
		case check.Complex64:
			return fieldLayout{size: 8, align: 4, known: true}
		case check.Complex128:
			return fieldLayout{size: 16, align: 8, known: true}
		case check.String:
			return fieldLayout{size: 16, align: 8, ptrEnd: 8, known: true}
		}
		return unknown
	case *check.Pointer, *check.Map, *check.Signature:
		return word
	case *check.Slice:
		return fieldLayout{size: 24, align: 8, ptrEnd: 8, known: true}
	case *check.Array:
		e := l.scanOf(t.Elem, depth+1)
		if !e.known || t.Len < 0 || t.Len > 1<<20 {
			return unknown
		}
		n := int(t.Len)
		out := fieldLayout{size: e.size * n, align: e.align, known: true}
		if e.ptrEnd > 0 && n > 0 {
			out.ptrEnd = e.size*(n-1) + e.ptrEnd
		}
		return out
	case *check.Named:
		if _, ok := check.GoRefElem(t); ok {
			return word
		}
		if t.Obj.Pkg == nil || t.Obj.Pkg.Host() || len(t.TArgs) > 0 || t.Obj.Decl == nil {
			return unknown
		}
		switch t.Underlying().(type) {
		case *check.Struct:
			td := l.typeDecl(t.Obj, t.Obj.Decl.Name)
			if td.kind != kindRecord {
				return unknown
			}
			return l.scanStruct(l.layoutOrder(td), depth+1)
		case *check.Basic, *check.Pointer, *check.Map, *check.Signature, *check.Slice, *check.Array:
			if d := t.Defined(); d != nil {
				return l.scanOf(d, depth+1)
			}
		}
		return unknown
	}
	return unknown
}
