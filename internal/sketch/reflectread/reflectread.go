// Package reflectread reads the checker's model by reflection for the
// export (compile-speed CS1b, CS5b): the rank that orders a map's entries
// in a snapshot, and the object paths that name an object of a checked
// package.
//
// It is the checker's only use of reflect. The provenance analyzer
// (tools/provenance) bans reflect in the checker, because reflect can build
// a syntax node out of the analyzer's sight, and admits this package on its
// reviewed import allow-list instead (internal/sketch/check/testdata/
// provenance-manifest.txt): it only reads. It calls no reflect function or
// method that makes or writes a value (New, Zero, Make*, Append, Copy,
// Set*); reflect.NewAt only re-reads a pointer it was given, at the same
// address and of the same type, so a caller may hold it as an interface.
// It imports no ergo.local package, so it names no syntax node type.
package reflectread

import (
	"encoding/binary"
	"math"
	"reflect"
	"sort"
	"strconv"
)

// Comparable reports whether x's dynamic type may be a map key.
func Comparable(x any) bool { return reflect.TypeOf(x).Comparable() }

// Lookup returns a tag and an id for a pointer the encoder already knows,
// or false.
type Lookup func(x any) (tag byte, id uint64, ok bool)

// Rank appends the rank of *p, p a pointer, to b. depth is how many
// pointers below *p a rank may follow to an object known has no id for.
func Rank(b []byte, p any, depth int, known Lookup) []byte {
	return rank(b, reflect.ValueOf(p).Elem(), depth, known)
}

func rank(b []byte, v reflect.Value, depth int, known Lookup) []byte {
	switch v.Kind() {
	case reflect.Invalid:
		return append(b, 0)
	case reflect.Interface:
		if v.IsNil() {
			return append(b, 0)
		}
		return rank(b, v.Elem(), depth, known)
	case reflect.Pointer:
		if v.IsNil() {
			return append(b, 0)
		}
		if tag, id, ok := known(object(v)); ok {
			return binary.BigEndian.AppendUint64(append(b, tag), id)
		}
		b = rankString(append(b, 4), v.Type().String())
		if depth == 0 {
			return b
		}
		return rank(b, v.Elem(), depth-1, known)
	case reflect.Struct:
		b = append(b, 5)
		for i := range v.NumField() {
			b = rank(b, v.Field(i), depth, known)
		}
		return b
	case reflect.Slice:
		if v.IsNil() {
			return append(b, 0)
		}
		b = binary.BigEndian.AppendUint64(append(b, 6), uint64(v.Len()))
		for i := range v.Len() {
			b = rank(b, v.Index(i), depth, known)
		}
		return b
	case reflect.Array:
		b = append(b, 6)
		for i := range v.Len() {
			b = rank(b, v.Index(i), depth, known)
		}
		return b
	case reflect.String:
		return rankString(append(b, 7), v.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return binary.BigEndian.AppendUint64(append(b, 8), uint64(v.Int())^(1<<63))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return binary.BigEndian.AppendUint64(append(b, 8), v.Uint())
	case reflect.Bool:
		if v.Bool() {
			return append(b, 8, 1)
		}
		return append(b, 8, 0)
	case reflect.Float32, reflect.Float64:
		return binary.BigEndian.AppendUint64(append(b, 8), math.Float64bits(v.Float()))
	case reflect.Map:
		// A map has no order to rank its entries by: its length alone.
		return binary.BigEndian.AppendUint64(append(b, 9), uint64(v.Len()))
	default:
		// A function or a channel.
		return append(b, 9)
	}
}

func rankString(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint64(b, uint64(len(s)))
	return append(b, s...)
}

// object returns the pointer v as an any a caller may hold (a pointer read
// through an unexported field is read-only to reflect).
func object(v reflect.Value) any {
	return reflect.NewAt(v.Type().Elem(), v.UnsafePointer()).Interface()
}

// Path step kinds (check's objpath.go).
const (
	StepScope   = 's' // a package scope name (root)
	StepContext = 'c' // a context name (root)
	StepField   = 'f' // a struct field, by index
	StepIndex   = 'i' // a slice or array element, by index
	StepString  = 'k' // a map entry, by string key
	StepInt     = 'n' // a map entry, by integer key
)

// AppendStep appends a step of kind with text s to p.
func AppendStep(p []byte, kind byte, s string) []byte {
	p = append(p, kind)
	p = binary.AppendUvarint(p, uint64(len(s)))
	return append(p, s...)
}

func appendNum(p []byte, kind byte, n int) []byte {
	return AppendStep(p, kind, strconv.Itoa(n))
}

// NextStep reads the first step of p.
func NextStep(p []byte) (kind byte, s string, rest []byte, ok bool) {
	if len(p) < 2 {
		return 0, "", nil, false
	}
	kind = p[0]
	n, k := binary.Uvarint(p[1:])
	if k <= 0 || uint64(len(p)-1-k) < n {
		return 0, "", nil, false
	}
	start := 1 + k
	return kind, string(p[start : start+int(n)]), p[start+int(n):], true
}

// Stop reports whether a walk enters no pointer to the named type of
// package pkgPath.
type Stop func(pkgPath, name string) bool

// Walk calls first with each pointer reached from root, and the path that
// reaches it, in a fixed order: fields in declaration order, elements by
// index, map entries with a string or integer key by key (a map keyed
// otherwise is skipped: its keys have no name a path may hold). first
// reports whether the pointer was reached before; the walk goes below a
// pointer only the first time.
func Walk(root any, path []byte, stop Stop, first func(x any, path []byte) (seen bool)) {
	w := walker{stop: stop, first: first}
	w.walk(reflect.ValueOf(root), path)
}

type walker struct {
	stop  Stop
	first func(any, []byte) bool
}

func (w *walker) walk(v reflect.Value, path []byte) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() || w.stop(v.Type().Elem().PkgPath(), v.Type().Elem().Name()) {
			return
		}
		if w.first(object(v), path) {
			return
		}
		w.walk(v.Elem(), path)
	case reflect.Interface:
		if !v.IsNil() {
			w.walk(v.Elem(), path)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			w.walk(v.Field(i), appendNum(path, StepField, i))
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			w.walk(v.Index(i), appendNum(path, StepIndex, i))
		}
	case reflect.Map:
		switch v.Type().Key().Kind() {
		case reflect.String:
			keys := v.MapKeys()
			sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
			for _, k := range keys {
				w.walk(v.MapIndex(k), AppendStep(path, StepString, k.String()))
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			keys := v.MapKeys()
			sort.Slice(keys, func(i, j int) bool { return keys[i].Int() < keys[j].Int() })
			for _, k := range keys {
				w.walk(v.MapIndex(k), appendNum(path, StepInt, int(k.Int())))
			}
		}
	}
}

// Resolve returns the pointer the steps of path (no root step) reach from
// root, or false when they reach nothing there.
func Resolve(root any, p []byte) (any, bool) {
	v := reflect.ValueOf(root)
	for len(p) > 0 {
		kind, s, rest, ok := NextStep(p)
		if !ok {
			return nil, false
		}
		p = rest
		v = deref(v)
		if !v.IsValid() {
			return nil, false
		}
		switch kind {
		case StepField:
			i, err := strconv.Atoi(s)
			if err != nil || v.Kind() != reflect.Struct || i < 0 || i >= v.NumField() {
				return nil, false
			}
			v = v.Field(i)
		case StepIndex:
			i, err := strconv.Atoi(s)
			if err != nil || (v.Kind() != reflect.Slice && v.Kind() != reflect.Array) || i < 0 || i >= v.Len() {
				return nil, false
			}
			v = v.Index(i)
		case StepString:
			if v.Kind() != reflect.Map || v.Type().Key().Kind() != reflect.String {
				return nil, false
			}
			v = v.MapIndex(reflect.ValueOf(s).Convert(v.Type().Key()))
		case StepInt:
			n, err := strconv.Atoi(s)
			if err != nil || v.Kind() != reflect.Map {
				return nil, false
			}
			k := v.Type().Key()
			switch k.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			default:
				return nil, false
			}
			v = v.MapIndex(reflect.ValueOf(int64(n)).Convert(k))
		default:
			return nil, false
		}
	}
	for v.IsValid() && v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, false
		}
		v = v.Elem()
	}
	if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() {
		return nil, false
	}
	return object(v), true
}

// deref follows pointers and interfaces to the value a step reads.
func deref(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}
