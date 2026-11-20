package lower

// MAP-INT, MI-2: a Map whose key is a Go integer type lowers to the
// runtime's open-addressing table, *v1.IntMap[K, V] (runtime/v1/intmap.go),
// in an IR lowering. The choice is per Map type, for the whole program,
// and it is fixed before emission: one Map type is one Go type everywhere,
// in declarations, signatures, IR bodies and helpers alike.
//
// A Map type is an int map unless something in the lowering cannot take
// the table: a body the IR builder refuses keeps its emitter body, which
// writes Go's map operations; an emitter helper (equality, show, a codec,
// a law) writes them too; the Go boundary sees Go's map. Each of these
// records a conflict, and lowerIRModule lowers again with the conflicted
// types as Go maps, until a lowering records none (intMapRounds). A
// program with no conflict lowers once.
//
// The reference is -ldflags=-X=ergo.local/prototype/internal/sketch/lower.mapLowering=go:
// every Map is Go's map, as before MI-2.

import (
	"path/filepath"
	"runtime"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// mapLowering selects the Map representation of IR lowerings: "int" (the
// default) lowers a Map with an integer key to IntMap, "go" keeps Go's map.
var mapLowering = "int"

// intMapState is an IR lowering's int map choice.
type intMapState struct {
	// off are the Map types (typeKey) an earlier round found a conflict
	// for: Go maps in this round.
	off map[string]bool
	// conflicts are the int map types this round found a conflict for.
	conflicts map[string]bool
	// allGo keeps every Map Go's: the last round (intMapRounds).
	allGo bool
	// why counts this round's conflicts by cause: "body: " and the IR
	// builder's reason, or "helper " and the emitter file that wrote Go's
	// map operations (IRReport.MapConflicts).
	why map[string]int
	// fields are the generic record instances (typeKey) intMapFields has
	// walked this round.
	fields map[string]bool
}

// intKey reports a key type IntMap takes: a Go integer type.
func intKey(t check.Type) bool {
	b, ok := t.(*check.Basic)
	if !ok {
		return false
	}
	switch b.Kind {
	case check.Int, check.Int8, check.Int16, check.Int32, check.Int64,
		check.Uint, check.Uint8, check.Uint16, check.Uint32, check.Uint64, check.Uintptr:
		return true
	}
	return false
}

// intMap reports t as an int map in this lowering. A Map that names a
// type parameter stays Go's map (MI-3 copies generic records).
func (l *lowerer) intMap(t *check.Map) bool {
	if mapLowering == "go" || l.ir == nil || l.ir.census != nil || l.ir.maps.allGo || l.probing {
		return false
	}
	if !intKey(t.Key) || mentionsTypeParam(t) {
		return false
	}
	return !l.ir.maps.off[l.typeKey(t)]
}

// intMapConflict records each int map that t is or holds (through
// slices, arrays, pointers, Maps, tuples and the fields of records) as a
// conflict: the next round lowers it as Go's map. why is the cause the
// report counts; without one, the cause is the emitter file of the caller.
func (l *lowerer) intMapConflict(t check.Type, why ...string) {
	if l.ir == nil {
		return
	}
	_, file, _, _ := runtime.Caller(1)
	cause := func() string {
		if len(why) > 0 {
			return why[0]
		}
		return "helper " + filepath.Base(file)
	}
	seen := map[check.Type]bool{}
	var walk func(t check.Type)
	walk = func(t check.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *check.Map:
			if l.intMap(t) {
				if l.ir.maps.conflicts == nil {
					l.ir.maps.conflicts, l.ir.maps.why = map[string]bool{}, map[string]int{}
				}
				if !l.ir.maps.conflicts[l.typeKey(t)] {
					l.ir.maps.why[cause()]++
				}
				l.ir.maps.conflicts[l.typeKey(t)] = true
			}
			walk(t.Key)
			walk(t.Elem)
		case *check.Slice:
			walk(t.Elem)
		case *check.Array:
			walk(t.Elem)
		case *check.Pointer:
			walk(t.Elem)
		case *check.Tuple:
			for _, e := range t.Types {
				walk(e)
			}
		case *check.Named:
			for _, a := range t.TArgs {
				walk(a)
			}
			if s, ok := t.Underlying().(*check.Struct); ok {
				for _, f := range s.Fields {
					walk(f.Type)
				}
			}
		}
	}
	walk(t)
}

// intMapFields records a conflict for each int map that an instance t of
// a generic record holds through a field whose declared type names a type
// parameter: the record's Go type is declared once, over its parameters,
// so that field is Go's map in every instance (intMap), and an instance's
// Map type must be Go's map too wherever the program uses it (a copy of
// the record, KMEANS-LAW's std/jobs; a read of the field). goType calls it
// for each instance it writes.
func (l *lowerer) intMapFields(t *check.Named) {
	if l.ir == nil || t.Orig == nil || len(t.TArgs) == 0 {
		return
	}
	key := l.typeKey(t)
	if l.ir.maps.fields[key] {
		return
	}
	if l.ir.maps.fields == nil {
		l.ir.maps.fields = map[string]bool{}
	}
	l.ir.maps.fields[key] = true
	inst, ok := t.Underlying().(*check.Struct)
	orig, ok2 := t.Orig.Underlying().(*check.Struct)
	if !ok || !ok2 || len(inst.Fields) != len(orig.Fields) {
		return
	}
	for i, f := range orig.Fields {
		if mentionsTypeParam(f.Type) {
			l.intMapConflict(inst.Fields[i].Type, "generic record field")
		}
	}
}

// intMapBody records a conflict for each int map that a node of decl
// has a value of: decl keeps its emitter body (the IR builder refused
// it), whose Map operations are Go's.
func (l *lowerer) intMapBody(info *check.Info, decl syntax.Node, reason string) {
	if l.ir == nil || info == nil || decl == nil {
		return
	}
	why := "body: " + reason
	syntax.Inspect(decl, func(n syntax.Node) bool {
		if x, ok := n.(syntax.Expr); ok {
			if t, ok := info.Types[x]; ok {
				l.intMapConflict(t, why)
			}
		}
		if id, ok := n.(*syntax.Ident); ok {
			if v, ok := info.Defs[id].(*check.Var); ok {
				l.intMapConflict(v.Type, why)
			}
		}
		return true
	})
}

// intMapRounds is the most lowerings lowerIRModule runs: each round after
// the first turns at least one more Map type into Go's map, and a program
// has few Map types; past it every Map is Go's.
const intMapRounds = 8

// newRound is the int map state of the lowering after this one, and
// whether that lowering is needed: this one found a conflict.
func (s intMapState) newRound() (intMapState, bool) {
	if len(s.conflicts) == 0 {
		return s, false
	}
	off := map[string]bool{}
	for k := range s.off {
		off[k] = true
	}
	for k := range s.conflicts {
		off[k] = true
	}
	return intMapState{off: off}, true
}
