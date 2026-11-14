package check

import (
	"encoding/binary"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// An object path names an object of a checked package by the steps that
// reach it from the package's declarations, as Go's objectpath does
// (compile-speed/cs5b.md §3): a reference from one user package's export
// segment to an object of another is written as a path and resolved in
// whichever check of that package is current, so a cached importer
// survives a fresh check of its import.
//
// A path starts at a name of the package scope or of its context
// namespace, then steps through fields (by index in the Go struct),
// slice elements (by index) and map entries with a string or integer key
// (by key). Pointers and interfaces are followed without a step. The empty
// path is the package itself. Paths are valid only for the compiler that
// wrote them (the export is keyed by the compiler's identity).
//
// Decided: the walk reads the model by reflection, not by an exportgen
// walker, by simplicity (one walker and one resolver read the same field
// list, so they cannot disagree); reverse by generating both from
// exportgen's type graph when CS5b-3 measures the walk's cost.
type objPath string

// pathStep kinds.
const (
	stepScope   = 's' // a package scope name (root)
	stepContext = 'c' // a context name (root)
	stepField   = 'f' // a struct field, by index
	stepIndex   = 'i' // a slice or array element, by index
	stepString  = 'k' // a map entry, by string key
	stepInt     = 'n' // a map entry, by integer key
)

// A walk takes each struct's fields in declaration order, so an object is
// named through the field that holds it before any cache of it (a
// Struct's Fields before its index, a Named's methods before mlist), and
// it skips a map keyed by an object, whose keys have no name a path may
// hold.

// stopType reports the pointer types a walk does not enter: syntax nodes,
// checkers and their tables, importers, and other packages.
func stopType(t reflect.Type) bool {
	el := t.Elem()
	if el.PkgPath() == "ergo.local/prototype/internal/sketch/syntax" || el.PkgPath() == "sync" {
		return true
	}
	if el.PkgPath() != "ergo.local/prototype/internal/sketch/check" {
		return true
	}
	switch el.Name() {
	case "checker", "typeTable", "stdImporter", "goImp", "Package", "Info":
		return true
	}
	return false
}

// objectPaths are the paths of every object reached from pkg's
// declarations, by object, each object's first path in a fixed walk order
// (scope names, then contexts, sorted; fields in declaration order; map
// entries by key). Compute it right after pkg's check, before an importer
// fills lazy state in it (census.md §1).
func objectPaths(pkg *Package) map[any]objPath {
	out := map[any]objPath{pkg: ""}
	w := &pathWalker{out: out}
	if pkg.Scope != nil {
		for _, name := range sortedKeys(pkg.Scope.names) {
			w.walk(reflect.ValueOf(pkg.Scope.names[name]), appendStep(nil, stepScope, name))
		}
	}
	for _, name := range sortedKeys(pkg.Contexts) {
		w.walk(reflect.ValueOf(pkg.Contexts[name]), appendStep(nil, stepContext, name))
	}
	return out
}

func appendStep(p []byte, kind byte, s string) []byte {
	p = append(p, kind)
	p = binary.AppendUvarint(p, uint64(len(s)))
	return append(p, s...)
}

func appendNum(p []byte, kind byte, n int) []byte {
	return appendStep(p, kind, strconv.Itoa(n))
}

type pathWalker struct {
	out map[any]objPath
}

// object returns the pointer v as an any a caller may hold (a pointer read
// through an unexported field is read-only to reflect).
func object(v reflect.Value) any {
	return reflect.NewAt(v.Type().Elem(), v.UnsafePointer()).Interface()
}

func (w *pathWalker) walk(v reflect.Value, path []byte) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() || stopType(v.Type()) {
			return
		}
		x := object(v)
		if _, seen := w.out[x]; seen {
			return
		}
		w.out[x] = objPath(path)
		w.walk(v.Elem(), path)
	case reflect.Interface:
		if !v.IsNil() {
			w.walk(v.Elem(), path)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			w.walk(v.Field(i), appendNum(path, stepField, i))
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			w.walk(v.Index(i), appendNum(path, stepIndex, i))
		}
	case reflect.Map:
		switch v.Type().Key().Kind() {
		case reflect.String:
			keys := v.MapKeys()
			sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
			for _, k := range keys {
				w.walk(v.MapIndex(k), appendStep(path, stepString, k.String()))
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			keys := v.MapKeys()
			sort.Slice(keys, func(i, j int) bool { return keys[i].Int() < keys[j].Int() })
			for _, k := range keys {
				w.walk(v.MapIndex(k), appendNum(path, stepInt, int(k.Int())))
			}
		}
		// A map keyed by an object is skipped: its keys have no name a
		// path may hold.
	}
}

// ResolvePath returns the object at path (ObjectPath) in pkg, or false
// when the path names nothing there.
func ResolvePath(pkg *Package, path string) (any, bool) { return resolvePath(pkg, objPath(path)) }

// PathString renders a path for messages.
func PathString(path string) string { return objPath(path).String() }

func resolvePath(pkg *Package, path objPath) (any, bool) {
	if path == "" {
		return pkg, true
	}
	p := []byte(path)
	kind, s, p, ok := nextStep(p)
	if !ok {
		return nil, false
	}
	var v reflect.Value
	switch kind {
	case stepScope:
		if pkg.Scope == nil {
			return nil, false
		}
		o, found := pkg.Scope.names[s]
		if !found {
			return nil, false
		}
		v = reflect.ValueOf(o)
	case stepContext:
		c, found := pkg.Contexts[s]
		if !found {
			return nil, false
		}
		v = reflect.ValueOf(c)
	default:
		return nil, false
	}
	for len(p) > 0 {
		kind, s, p, ok = nextStep(p)
		if !ok {
			return nil, false
		}
		v = deref(v)
		if !v.IsValid() {
			return nil, false
		}
		switch kind {
		case stepField:
			i, err := strconv.Atoi(s)
			if err != nil || v.Kind() != reflect.Struct || i < 0 || i >= v.NumField() {
				return nil, false
			}
			v = v.Field(i)
		case stepIndex:
			i, err := strconv.Atoi(s)
			if err != nil || (v.Kind() != reflect.Slice && v.Kind() != reflect.Array) || i < 0 || i >= v.Len() {
				return nil, false
			}
			v = v.Index(i)
		case stepString:
			if v.Kind() != reflect.Map || v.Type().Key().Kind() != reflect.String {
				return nil, false
			}
			v = v.MapIndex(reflect.ValueOf(s).Convert(v.Type().Key()))
		case stepInt:
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

func nextStep(p []byte) (kind byte, s string, rest []byte, ok bool) {
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

// String renders a path for messages: s:Name.f3.i0.k:key.
func (p objPath) String() string {
	b := []byte(p)
	var parts []string
	for len(b) > 0 {
		kind, s, rest, ok := nextStep(b)
		if !ok {
			parts = append(parts, "?")
			break
		}
		parts = append(parts, string(kind)+s)
		b = rest
	}
	return strings.Join(parts, ".")
}
